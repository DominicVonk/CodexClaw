//go:build !windows

package codexapp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	sdk "github.com/bazelment/yoloswe/agent-cli-wrapper/codex"
	"gopkg.in/yaml.v3"

	"github.com/DominicVonk/CodexClaw/internal/config"
)

const (
	originatorEnv = "CODEX_INTERNAL_ORIGINATOR_OVERRIDE"
	originator    = "codexclaw_go_sdk"
)

var ErrCompactUnsupported = errors.New("codex app-server backend does not support explicit compaction")

type Gateway struct {
	cfg     config.CodexConfig
	client  *sdk.Client
	threads map[string]*sdk.Thread
	tools   map[string]ToolEvent
	sendMu  sync.Mutex
	mu      sync.Mutex
}

type TurnResult struct {
	Text          string
	ThreadID      string
	TokenUsage    TokenUsage
	LastTurnUsage TokenUsage
}

type ProgressFunc func(ToolEvent)

type ToolEvent struct {
	Phase   string
	Type    string
	Label   string
	Context string
	Status  string
	Details string
}

type Skill struct {
	Name string
	Path string
}

type TokenUsage struct {
	InputTokens           int64
	CachedInputTokens     int64
	OutputTokens          int64
	ReasoningOutputTokens int64
	TotalTokens           int64
	Cumulative            bool
}

type InputPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	URL  string `json:"url,omitempty"`
	Path string `json:"path,omitempty"`
	Name string `json:"name,omitempty"`
}

func Start(ctx context.Context, cfg config.CodexConfig) (*Gateway, error) {
	if strings.TrimSpace(cfg.Command) == "" {
		return nil, errors.New("codex command is required")
	}
	if cfg.CWD == "" {
		cfg.CWD = "."
	}
	client := sdk.NewClient(clientOptions(cfg)...)
	if err := client.Start(ctx); err != nil {
		return nil, err
	}
	return &Gateway{
		cfg:     cfg,
		client:  client,
		threads: make(map[string]*sdk.Thread),
		tools:   make(map[string]ToolEvent),
	}, nil
}

func (g *Gateway) Close() error {
	if g.client == nil {
		return nil
	}
	return g.client.Stop()
}

func (g *Gateway) ResumeThread(ctx context.Context, threadID string) error {
	if strings.TrimSpace(threadID) == "" || strings.HasPrefix(threadID, "new-") {
		return nil
	}
	_, err := g.thread(ctx, threadID, "")
	return err
}

func (g *Gateway) CompactThread(ctx context.Context, threadID string) error {
	return ErrCompactUnsupported
}

func (g *Gateway) ListSkills(ctx context.Context) ([]Skill, error) {
	return scanSkills(g.cfg.CWD)
}

func (g *Gateway) StartThread(ctx context.Context) (string, error) {
	thread, err := g.createThread(ctx, "")
	if err != nil {
		return "", err
	}
	return thread.ID(), nil
}

func (g *Gateway) Send(ctx context.Context, threadID string, input []InputPart, model string, effort string, progress ProgressFunc) (TurnResult, error) {
	g.sendMu.Lock()
	defer g.sendMu.Unlock()

	sdkInput := normalizeInput(input)
	if len(sdkInput) == 0 {
		return TurnResult{}, errors.New("turn input is required")
	}

	freshThread := strings.TrimSpace(threadID) == "" || strings.HasPrefix(strings.TrimSpace(threadID), "new-")
	thread, err := g.thread(ctx, threadID, model)
	if err != nil {
		return TurnResult{}, err
	}
	opts := g.turnOptions(model, effort)
	turnID, err := thread.SendInput(ctx, sdkInput, opts...)
	if err != nil {
		return TurnResult{}, err
	}

	result := TurnResult{ThreadID: thread.ID()}
	for {
		select {
		case event, ok := <-g.client.Events():
			if !ok {
				return TurnResult{}, errors.New("codex event stream closed")
			}
			g.handleEvent(event, thread.ID(), progress, &result)
			if completed, err := turnCompleted(event, thread.ID(), turnID); completed || err != nil {
				if err != nil {
					return TurnResult{}, err
				}
				return g.finishTurn(ctx, thread.ID(), threadSessionPath(thread), thread.GetFullText(), progress, result, freshThread)
			}
			if err := eventError(event, thread.ID(), turnID); err != nil {
				return TurnResult{}, err
			}
			if result.TokenUsage.TotalTokens == 0 {
				result.TokenUsage = result.LastTurnUsage
			}
		case <-ctx.Done():
			return TurnResult{}, ctx.Err()
		}
	}
}

func (g *Gateway) finishTurn(ctx context.Context, threadID string, sessionPath string, fallbackText string, progress ProgressFunc, result TurnResult, freshThread bool) (TurnResult, error) {
	result.Text = strings.TrimSpace(firstNonEmpty(result.Text, fallbackText))
	if result.LastTurnUsage.TotalTokens == 0 && result.TokenUsage.TotalTokens == 0 {
		if fillUsageFromSessionLog(sessionPath, &result) {
			return finalizeUsage(result, freshThread), nil
		}
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		for {
			select {
			case event, ok := <-g.client.Events():
				if !ok {
					return TurnResult{}, errors.New("codex event stream closed")
				}
				g.handleEvent(event, threadID, progress, &result)
				if result.LastTurnUsage.TotalTokens > 0 || result.TokenUsage.TotalTokens > 0 {
					result.Text = strings.TrimSpace(firstNonEmpty(result.Text, fallbackText))
					return finalizeUsage(result, freshThread), nil
				}
			case <-timer.C:
				fillUsageFromSessionLog(sessionPath, &result)
				return finalizeUsage(result, freshThread), nil
			case <-ctx.Done():
				return TurnResult{}, ctx.Err()
			}
		}
	}
	return finalizeUsage(result, freshThread), nil
}

func finalizeUsage(result TurnResult, freshThread bool) TurnResult {
	if result.TokenUsage.TotalTokens == 0 {
		result.TokenUsage = result.LastTurnUsage
	}
	if freshThread && result.LastTurnUsage.TotalTokens > 0 {
		result.TokenUsage = result.LastTurnUsage
		result.TokenUsage.Cumulative = false
	}
	return result
}

func threadSessionPath(thread *sdk.Thread) string {
	info := thread.Info()
	if info == nil {
		return ""
	}
	return info.Path
}

func (g *Gateway) thread(ctx context.Context, threadID string, model string) (*sdk.Thread, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" || strings.HasPrefix(threadID, "new-") {
		return g.createThread(ctx, model)
	}

	g.mu.Lock()
	thread := g.threads[threadID]
	g.mu.Unlock()
	if thread != nil {
		return thread, nil
	}

	thread, err := g.client.ResumeThread(ctx, threadID, g.threadOptions(model)...)
	if err != nil {
		return nil, err
	}
	if err := thread.WaitReady(ctx); err != nil {
		return nil, err
	}
	g.storeThread(thread)
	return thread, nil
}

func (g *Gateway) createThread(ctx context.Context, model string) (*sdk.Thread, error) {
	thread, err := g.client.CreateThread(ctx, g.threadOptions(model)...)
	if err != nil {
		return nil, err
	}
	if err := thread.WaitReady(ctx); err != nil {
		return nil, err
	}
	g.storeThread(thread)
	return thread, nil
}

func (g *Gateway) storeThread(thread *sdk.Thread) {
	g.mu.Lock()
	g.threads[thread.ID()] = thread
	g.mu.Unlock()
}

func clientOptions(cfg config.CodexConfig) []sdk.ClientOption {
	opts := []sdk.ClientOption{
		sdk.WithCodexPath(cfg.Command),
		sdk.WithClientName(firstNonEmpty(cfg.ClientName, "codexclaw")),
		sdk.WithClientVersion(firstNonEmpty(cfg.ClientVersion, "0.1.0")),
		sdk.WithEnv(map[string]string{originatorEnv: originator}),
		sdk.WithStderrHandler(func(data []byte) {
			if text := strings.TrimSpace(string(data)); text != "" {
				log.Printf("codex app-server: %s", text)
			}
		}),
	}
	if len(cfg.Args) > 0 {
		opts = append(opts, sdk.WithAppServerArgs(cfg.Args...))
	}
	return opts
}

func (g *Gateway) threadOptions(model string) []sdk.ThreadOption {
	var opts []sdk.ThreadOption
	if effectiveModel := firstNonEmpty(model, g.cfg.Model); effectiveModel != "" {
		opts = append(opts, sdk.WithModel(effectiveModel))
	}
	if policy := approvalPolicy(g.cfg.ApprovalPolicy); policy != "" {
		opts = append(opts, sdk.WithApprovalPolicy(policy))
	}
	if sandbox := sandboxMode(g.cfg.PermissionProfile); sandbox != "" {
		opts = append(opts, sdk.WithSandbox(sandbox))
	}
	return opts
}

func (g *Gateway) turnOptions(model string, effort string) []sdk.TurnOption {
	var opts []sdk.TurnOption
	if effectiveModel := firstNonEmpty(model, g.cfg.Model); effectiveModel != "" {
		opts = append(opts, sdk.WithTurnModel(effectiveModel))
	}
	if effectiveEffort := firstNonEmpty(effort, g.cfg.Effort); effectiveEffort != "" {
		opts = append(opts, sdk.WithEffort(effectiveEffort))
	}
	if policy := approvalPolicy(g.cfg.ApprovalPolicy); policy != "" {
		opts = append(opts, sdk.WithTurnApprovalPolicy(policy))
	}
	return opts
}

func (g *Gateway) handleEvent(event sdk.Event, threadID string, progress ProgressFunc, result *TurnResult) {
	switch typed := event.(type) {
	case sdk.CommandStartEvent:
		if typed.ThreadID == threadID && progress != nil {
			label := commandText(typed.ParsedCmd, typed.Command)
			event := ToolEvent{Phase: "started", Type: "command_execution", Label: label, Context: commandContext(label), Status: "in_progress", Details: cwdDetails(typed.CWD)}
			g.storeTool(typed.CallID, event)
			progress(event)
		}
	case sdk.CommandEndEvent:
		if typed.ThreadID == threadID && progress != nil {
			started := g.popTool(typed.CallID)
			label := firstNonEmpty(started.Label, "shell command")
			progress(ToolEvent{Phase: "completed", Type: "command_execution", Label: label, Context: firstNonEmpty(started.Context, commandContext(label)), Status: statusFromExit(typed.ExitCode), Details: commandDetails(typed)})
		}
	case sdk.ItemStartedEvent:
		if typed.ThreadID == threadID && progress != nil && isToolItem(typed.ItemType) {
			progress(ToolEvent{Phase: "started", Type: typed.ItemType, Label: typed.ItemType, Status: "in_progress"})
		}
	case sdk.ItemCompletedEvent:
		if typed.ThreadID == threadID && typed.Text != "" {
			result.Text = typed.Text
		}
		if typed.ThreadID == threadID && progress != nil && isToolItem(typed.ItemType) {
			progress(ToolEvent{Phase: "completed", Type: typed.ItemType, Label: typed.ItemType, Status: "completed"})
		}
	case sdk.TokenUsageEvent:
		if typed.ThreadID == threadID {
			if typed.TotalUsage != nil {
				result.TokenUsage = usageFromToken(typed.TotalUsage, true)
			}
			if typed.LastUsage != nil {
				result.LastTurnUsage = usageFromToken(typed.LastUsage, false)
			}
		}
	case sdk.TurnCompletedEvent:
		if typed.ThreadID == threadID {
			if typed.FullText != "" {
				result.Text = typed.FullText
			}
			if result.LastTurnUsage.TotalTokens == 0 {
				result.LastTurnUsage = usageFromTurn(typed.Usage, false)
			}
		}
	}
}

func (g *Gateway) storeTool(callID string, event ToolEvent) {
	if strings.TrimSpace(callID) == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.tools == nil {
		g.tools = make(map[string]ToolEvent)
	}
	g.tools[callID] = event
}

func (g *Gateway) popTool(callID string) ToolEvent {
	if strings.TrimSpace(callID) == "" {
		return ToolEvent{}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	event := g.tools[callID]
	delete(g.tools, callID)
	return event
}

func turnCompleted(event sdk.Event, threadID string, turnID string) (bool, error) {
	typed, ok := event.(sdk.TurnCompletedEvent)
	if !ok || typed.ThreadID != threadID || typed.TurnID != turnID {
		return false, nil
	}
	return true, typed.Error
}

func eventError(event sdk.Event, threadID string, turnID string) error {
	typed, ok := event.(sdk.ErrorEvent)
	if !ok || typed.ThreadID != threadID {
		return nil
	}
	if typed.TurnID != "" && typed.TurnID != turnID {
		return nil
	}
	return typed.Error
}

func normalizeInput(input []InputPart) []sdk.UserInput {
	var prompt []string
	for _, item := range input {
		switch strings.ToLower(item.Type) {
		case "text":
			if strings.TrimSpace(item.Text) != "" {
				prompt = append(prompt, item.Text)
			}
		case "localimage", "local_image":
			if item.Path != "" {
				prompt = append(prompt, "Attached local image: "+item.Path)
			}
		case "skill":
			if item.Name != "" {
				prompt = append(prompt, "Use Codex skill $"+item.Name+" if relevant.")
			}
		}
	}
	text := strings.TrimSpace(strings.Join(prompt, "\n\n"))
	if text == "" {
		return nil
	}
	return []sdk.UserInput{{Type: "text", Text: text}}
}

func usageFromToken(usage *sdk.TokenUsage, cumulative bool) TokenUsage {
	if usage == nil {
		return TokenUsage{}
	}
	return TokenUsage{
		InputTokens:           usage.InputTokens,
		CachedInputTokens:     usage.CachedInputTokens,
		OutputTokens:          usage.OutputTokens,
		ReasoningOutputTokens: usage.ReasoningOutputTokens,
		TotalTokens:           totalTokens(usage.InputTokens, usage.OutputTokens, usage.TotalTokens),
		Cumulative:            cumulative,
	}
}

func usageFromTurn(usage sdk.TurnUsage, cumulative bool) TokenUsage {
	return TokenUsage{
		InputTokens:           usage.InputTokens,
		CachedInputTokens:     usage.CachedInputTokens,
		OutputTokens:          usage.OutputTokens,
		ReasoningOutputTokens: usage.ReasoningOutputTokens,
		TotalTokens:           totalTokens(usage.InputTokens, usage.OutputTokens, usage.TotalTokens),
		Cumulative:            cumulative,
	}
}

type sessionLogEvent struct {
	Type    string                 `json:"type"`
	Payload sessionTokenLogPayload `json:"payload"`
}

type sessionTokenLogPayload struct {
	Type string              `json:"type"`
	Info sessionTokenLogInfo `json:"info"`
}

type sessionTokenLogInfo struct {
	TotalTokenUsage sessionTokenUsage `json:"total_token_usage"`
	LastTokenUsage  sessionTokenUsage `json:"last_token_usage"`
}

type sessionTokenUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
	TotalTokens           int64 `json:"total_tokens"`
}

func fillUsageFromSessionLog(path string, result *TurnResult) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()

	var found bool
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, `"token_count"`) {
			continue
		}
		var event sessionLogEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if event.Type != "event_msg" || event.Payload.Type != "token_count" {
			continue
		}
		if usage := usageFromSessionLog(event.Payload.Info.TotalTokenUsage, true); usage.TotalTokens > 0 {
			result.TokenUsage = usage
			found = true
		}
		if usage := usageFromSessionLog(event.Payload.Info.LastTokenUsage, false); usage.TotalTokens > 0 {
			result.LastTurnUsage = usage
			found = true
		}
	}
	return found
}

func usageFromSessionLog(usage sessionTokenUsage, cumulative bool) TokenUsage {
	return TokenUsage{
		InputTokens:           usage.InputTokens,
		CachedInputTokens:     usage.CachedInputTokens,
		OutputTokens:          usage.OutputTokens,
		ReasoningOutputTokens: usage.ReasoningOutputTokens,
		TotalTokens:           totalTokens(usage.InputTokens, usage.OutputTokens, usage.TotalTokens),
		Cumulative:            cumulative,
	}
}

func totalTokens(input int64, output int64, total int64) int64 {
	if total > 0 {
		return total
	}
	return input + output
}

func scanSkills(cwd string) ([]Skill, error) {
	roots := skillRoots(cwd)
	seen := map[string]struct{}{}
	var skills []Skill
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry == nil {
				return nil
			}
			if entry.IsDir() {
				if entry.Name() == "node_modules" || entry.Name() == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.Name() != "SKILL.md" {
				return nil
			}
			skill, err := readSkill(path)
			if err != nil || skill.Name == "" {
				return nil
			}
			if _, ok := seen[skill.Name]; ok {
				return nil
			}
			seen[skill.Name] = struct{}{}
			skills = append(skills, skill)
			return nil
		})
	}
	return skills, nil
}

func skillRoots(cwd string) []string {
	var roots []string
	if cwd != "" {
		roots = append(roots, filepath.Join(cwd, ".codex", "skills"))
	}
	if codexHome := os.Getenv("CODEX_HOME"); codexHome != "" {
		roots = append(roots, filepath.Join(codexHome, "skills"))
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		roots = append(roots, filepath.Join(home, ".codex", "skills"))
	}
	return roots
}

func readSkill(path string) (Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}
	text := string(data)
	if !strings.HasPrefix(text, "---") {
		return Skill{Name: filepath.Base(filepath.Dir(path)), Path: path}, nil
	}
	rest := strings.TrimPrefix(text, "---")
	head, _, ok := strings.Cut(rest, "---")
	if !ok {
		return Skill{Name: filepath.Base(filepath.Dir(path)), Path: path}, nil
	}
	var meta struct {
		Name string `yaml:"name"`
	}
	if err := yaml.Unmarshal([]byte(head), &meta); err != nil {
		return Skill{}, err
	}
	return Skill{Name: strings.TrimSpace(meta.Name), Path: path}, nil
}

func sandboxMode(permissionProfile string) string {
	switch strings.TrimSpace(permissionProfile) {
	case ":workspace", "workspace", "workspace-write":
		return "workspace-write"
	case ":read-only", "read-only":
		return "read-only"
	case ":danger-full-access", "danger-full-access":
		return "danger-full-access"
	default:
		return ""
	}
}

func approvalPolicy(policy string) sdk.ApprovalPolicy {
	switch strings.TrimSpace(policy) {
	case "never":
		return sdk.ApprovalPolicyNever
	case "on-request":
		return sdk.ApprovalPolicyOnRequest
	case "on-failure":
		return sdk.ApprovalPolicyOnFailure
	case "untrusted":
		return sdk.ApprovalPolicyUntrusted
	default:
		return ""
	}
}

func commandText(parsed string, command []string) string {
	if strings.TrimSpace(parsed) != "" {
		parsed = strings.TrimSpace(parsed)
		if !isGenericCommandItem(parsed) {
			return parsed
		}
	}
	return strings.TrimSpace(strings.Join(command, " "))
}

func cwdDetails(cwd string) string {
	if strings.TrimSpace(cwd) == "" {
		return ""
	}
	return "Directory: " + cwd
}

func commandDetails(event sdk.CommandEndEvent) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("Exit: %d", event.ExitCode))
	if event.DurationMs > 0 {
		parts = append(parts, fmt.Sprintf("Duration: %s", time.Duration(event.DurationMs)*time.Millisecond))
	}
	if stdout := outputPreview("Output preview", event.Stdout); stdout != "" {
		parts = append(parts, stdout)
	}
	if stderr := outputPreview("Error output preview", event.Stderr); stderr != "" {
		parts = append(parts, stderr)
	}
	return strings.Join(parts, "\n")
}

func outputPreview(label string, text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	const limit = 1000
	if len(text) > limit {
		text = text[:limit] + "\n... (truncated)"
	}
	return label + ":\n" + text
}

func statusFromExit(code int) string {
	if code == 0 {
		return "completed"
	}
	return "failed"
}

func isToolItem(itemType string) bool {
	switch itemType {
	case "command_execution", "commandExecution", "file_change", "fileChange", "mcp_tool_call", "mcpToolCall", "web_search", "webSearch", "todo_list":
		return true
	default:
		return false
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
