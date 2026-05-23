package codexapp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/DominicVonk/CodexClaw/internal/config"
	"github.com/DominicVonk/CodexClaw/pkg/codexsdk"
)

var ErrCompactUnsupported = errors.New("codex exec backend does not support explicit compaction")

type Gateway struct {
	cfg    config.CodexConfig
	client *codexsdk.Codex
	sendMu sync.Mutex
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
	return &Gateway{
		cfg: cfg,
		client: codexsdk.New(codexsdk.Options{
			CodexPathOverride: cfg.Command,
		}),
	}, nil
}

func (g *Gateway) Close() error {
	return nil
}

func (g *Gateway) ResumeThread(ctx context.Context, threadID string) error {
	return nil
}

func (g *Gateway) CompactThread(ctx context.Context, threadID string) error {
	return ErrCompactUnsupported
}

func (g *Gateway) ListSkills(ctx context.Context) ([]Skill, error) {
	return scanSkills(g.cfg.CWD)
}

func (g *Gateway) StartThread(ctx context.Context) (string, error) {
	id, err := randomID()
	if err != nil {
		return "", err
	}
	return "new-" + id, nil
}

func (g *Gateway) Send(ctx context.Context, threadID string, input []InputPart, model string, effort string, progress ProgressFunc) (TurnResult, error) {
	g.sendMu.Lock()
	defer g.sendMu.Unlock()

	sdkInput := normalizeInput(input)
	if len(sdkInput) == 0 {
		return TurnResult{}, errors.New("turn input is required")
	}
	thread := g.thread(threadID, model, effort)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	events, errs := thread.RunStreamed(runCtx, sdkInput, codexsdk.TurnOptions{})

	result := TurnResult{ThreadID: firstNonEmpty(thread.ID(), threadID)}
	for event := range events {
		if event.ThreadID != "" {
			result.ThreadID = event.ThreadID
		}
		if event.Type == "thread.started" && event.ThreadID != "" {
			result.ThreadID = event.ThreadID
		}
		if err := eventFailure(event); err != nil {
			cancel()
			<-errs
			return TurnResult{}, err
		}
		handleEvent(event, progress, &result)
	}
	if err := <-errs; err != nil {
		return TurnResult{}, err
	}
	if result.ThreadID == "" || strings.HasPrefix(result.ThreadID, "new-") {
		result.ThreadID = firstNonEmpty(thread.ID(), threadID)
	}
	if result.TokenUsage.TotalTokens == 0 {
		result.TokenUsage = result.LastTurnUsage
	}
	return result, nil
}

func (g *Gateway) thread(threadID string, model string, effort string) *codexsdk.Thread {
	options := g.threadOptions(model, effort)
	if strings.TrimSpace(threadID) == "" || strings.HasPrefix(threadID, "new-") {
		return g.client.StartThread(options)
	}
	return g.client.ResumeThread(threadID, options)
}

func (g *Gateway) threadOptions(model string, effort string) codexsdk.ThreadOptions {
	return codexsdk.ThreadOptions{
		Model:                firstNonEmpty(model, g.cfg.Model),
		WorkingDirectory:     g.cfg.CWD,
		ModelReasoningEffort: modelReasoningEffort(firstNonEmpty(effort, g.cfg.Effort)),
		ApprovalPolicy:       approvalPolicy(g.cfg.ApprovalPolicy),
		SandboxMode:          sandboxMode(g.cfg.PermissionProfile),
	}
}

func eventFailure(event codexsdk.ThreadEvent) error {
	switch event.Type {
	case "turn.failed":
		if event.Error != nil && event.Error.Message != "" {
			return errors.New(event.Error.Message)
		}
		return errors.New("codex turn failed")
	case "error":
		if event.Message != "" {
			return errors.New(event.Message)
		}
	}
	return nil
}

func handleEvent(event codexsdk.ThreadEvent, progress ProgressFunc, result *TurnResult) {
	switch event.Type {
	case "item.started":
		if tool, ok := toolEvent("started", event.Item); ok && progress != nil {
			progress(tool)
		}
	case "item.updated":
		if tool, ok := toolEvent("updated", event.Item); ok && progress != nil {
			progress(tool)
		}
	case "item.completed":
		if event.Item.Type == "agent_message" && event.Item.Text != "" {
			result.Text = event.Item.Text
		}
		if tool, ok := toolEvent("completed", event.Item); ok && progress != nil {
			progress(tool)
		}
	case "turn.completed":
		usage := usageFromSDK(event.Usage, false)
		result.LastTurnUsage = usage
		result.TokenUsage = usage
	case "turn.failed":
		if event.Error != nil && event.Error.Message != "" {
			result.Text = event.Error.Message
		}
	case "error":
		if event.Message != "" {
			result.Text = event.Message
		}
	}
}

func normalizeInput(input []InputPart) []codexsdk.UserInput {
	var parts []codexsdk.UserInput
	var prompt []string
	for _, item := range input {
		switch strings.ToLower(item.Type) {
		case "text":
			if strings.TrimSpace(item.Text) != "" {
				prompt = append(prompt, item.Text)
			}
		case "localimage", "local_image":
			if item.Path != "" {
				parts = append(parts, codexsdk.LocalImageInput(item.Path))
			}
		case "skill":
			if item.Name != "" {
				prompt = append(prompt, "Use Codex skill $"+item.Name+" if relevant.")
			}
		}
	}
	text := strings.TrimSpace(strings.Join(prompt, "\n\n"))
	if text != "" {
		parts = append([]codexsdk.UserInput{codexsdk.TextInput(text)}, parts...)
	}
	return parts
}

func usageFromSDK(usage codexsdk.Usage, cumulative bool) TokenUsage {
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

func toolEvent(phase string, item codexsdk.ThreadItem) (ToolEvent, bool) {
	if !isToolItem(item.Type) {
		return ToolEvent{}, false
	}
	event := ToolEvent{Phase: phase, Type: item.Type, Status: item.Status}
	switch item.Type {
	case "command_execution", "commandExecution":
		event.Label = item.Command
		if item.ExitCode != nil {
			event.Details = fmt.Sprintf("exit=%d", *item.ExitCode)
			event.Status = statusFromExit(*item.ExitCode)
		}
	case "file_change", "fileChange":
		event.Label = "file changes"
		event.Details = changesSummary(item.Changes)
	case "mcp_tool_call", "mcpToolCall":
		event.Label = strings.Trim(strings.TrimSpace(item.Server)+"/"+strings.TrimSpace(item.Tool), "/")
	case "web_search", "webSearch":
		event.Label = item.Query
	case "todo_list":
		event.Label = "todo list"
	}
	if event.Label == "" {
		event.Label = item.Type
	}
	return event, true
}

func changesSummary(changes []codexsdk.FileUpdateChange) string {
	if len(changes) == 0 {
		return ""
	}
	paths := make([]string, 0, len(changes))
	for _, change := range changes {
		if change.Path != "" {
			paths = append(paths, change.Path)
		}
	}
	return strings.Join(paths, ", ")
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

func sandboxMode(permissionProfile string) codexsdk.SandboxMode {
	switch strings.TrimSpace(permissionProfile) {
	case ":workspace", "workspace", "workspace-write":
		return codexsdk.SandboxModeWorkspaceWrite
	case ":read-only", "read-only":
		return codexsdk.SandboxModeReadOnly
	case ":danger-full-access", "danger-full-access":
		return codexsdk.SandboxModeDangerFullAccess
	default:
		return ""
	}
}

func approvalPolicy(policy string) codexsdk.ApprovalMode {
	switch strings.TrimSpace(policy) {
	case "never":
		return codexsdk.ApprovalModeNever
	case "on-request":
		return codexsdk.ApprovalModeOnRequest
	case "on-failure":
		return codexsdk.ApprovalModeOnFailure
	case "untrusted":
		return codexsdk.ApprovalModeUntrusted
	default:
		return ""
	}
}

func modelReasoningEffort(effort string) codexsdk.ModelReasoningEffort {
	switch strings.TrimSpace(effort) {
	case "minimal":
		return codexsdk.ModelReasoningEffortMinimal
	case "low":
		return codexsdk.ModelReasoningEffortLow
	case "medium":
		return codexsdk.ModelReasoningEffortMedium
	case "high":
		return codexsdk.ModelReasoningEffortHigh
	case "xhigh":
		return codexsdk.ModelReasoningEffortXHigh
	default:
		return ""
	}
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

func randomID() (string, error) {
	var data [8]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}
