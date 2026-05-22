//go:build windows

package codexapp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/DominicVonk/CodexClaw/internal/config"
)

const (
	originatorEnv = "CODEX_INTERNAL_ORIGINATOR_OVERRIDE"
	originator    = "codexclaw_go_sdk"
)

var ErrCompactUnsupported = errors.New("codex exec backend does not support explicit compaction")

type Gateway struct {
	cfg    config.CodexConfig
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

type execEvent struct {
	Type     string          `json:"type"`
	ThreadID string          `json:"thread_id"`
	Item     json.RawMessage `json:"item"`
	Usage    execUsage       `json:"usage"`
	Payload  execPayload     `json:"payload"`
	Error    *execError      `json:"error"`
	Message  string          `json:"message"`
}

type execUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
	TotalTokens           int64 `json:"total_tokens"`
}

type execPayload struct {
	Type string        `json:"type"`
	Info execTokenInfo `json:"info"`
}

type execTokenInfo struct {
	TotalTokenUsage execUsage `json:"total_token_usage"`
	LastTokenUsage  execUsage `json:"last_token_usage"`
}

type execError struct {
	Message string `json:"message"`
}

func Start(ctx context.Context, cfg config.CodexConfig) (*Gateway, error) {
	if strings.TrimSpace(cfg.Command) == "" {
		return nil, errors.New("codex command is required")
	}
	if cfg.CWD == "" {
		cfg.CWD = "."
	}
	return &Gateway{cfg: cfg}, nil
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

	prompt, images := normalizeInput(input)
	if strings.TrimSpace(prompt) == "" && len(images) == 0 {
		return TurnResult{}, errors.New("turn input is required")
	}

	args := g.execArgs(threadID, model, effort, images)
	cmd := exec.CommandContext(ctx, g.cfg.Command, args...)
	cmd.Dir = g.cfg.CWD
	cmd.Env = codexEnv(os.Environ())

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return TurnResult{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return TurnResult{}, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return TurnResult{}, err
	}
	go func() {
		_, _ = io.WriteString(stdin, prompt)
		_ = stdin.Close()
	}()

	result, scanErr := readEvents(ctx, stdout, threadID, progress)
	waitErr := cmd.Wait()
	if scanErr != nil {
		return TurnResult{}, scanErr
	}
	if waitErr != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return TurnResult{}, fmt.Errorf("codex exec failed: %w: %s", waitErr, detail)
		}
		return TurnResult{}, fmt.Errorf("codex exec failed: %w", waitErr)
	}
	if result.ThreadID == "" {
		result.ThreadID = threadID
	}
	return result, nil
}

func (g *Gateway) execArgs(threadID string, model string, effort string, images []string) []string {
	args := []string{"exec", "--json"}
	for _, override := range g.configOverrides(model, effort) {
		args = append(args, "--config", override)
	}
	if effectiveModel := firstNonEmpty(model, g.cfg.Model); effectiveModel != "" {
		args = append(args, "--model", effectiveModel)
	}
	if sandbox := sandboxMode(g.cfg.PermissionProfile); sandbox != "" {
		args = append(args, "--sandbox", sandbox)
	}
	if g.cfg.CWD != "" {
		args = append(args, "--cd", g.cfg.CWD)
	}
	if strings.HasPrefix(threadID, "new-") || strings.TrimSpace(threadID) == "" {
		for _, image := range images {
			args = append(args, "--image", image)
		}
		return args
	}
	args = append(args, "resume", threadID)
	for _, image := range images {
		args = append(args, "--image", image)
	}
	return args
}

func (g *Gateway) configOverrides(model string, effort string) []string {
	var out []string
	if effectiveEffort := firstNonEmpty(effort, g.cfg.Effort); effectiveEffort != "" {
		out = append(out, fmt.Sprintf("model_reasoning_effort=%q", effectiveEffort))
	}
	if g.cfg.ApprovalPolicy != "" {
		out = append(out, fmt.Sprintf("approval_policy=%q", g.cfg.ApprovalPolicy))
	}
	return out
}

func readEvents(ctx context.Context, stdout io.Reader, fallbackThreadID string, progress ProgressFunc) (TurnResult, error) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	result := TurnResult{ThreadID: fallbackThreadID}
	var completedText string
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return TurnResult{}, ctx.Err()
		default:
		}
		var event execEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return TurnResult{}, fmt.Errorf("parse codex exec event: %w", err)
		}
		switch event.Type {
		case "thread.started":
			if event.ThreadID != "" {
				result.ThreadID = event.ThreadID
			}
		case "item.started":
			if tool, ok := execToolEvent("started", event.Item); ok && progress != nil {
				progress(tool)
			}
		case "item.updated":
			continue
		case "item.completed":
			if text := agentMessageText(event.Item); text != "" {
				completedText = text
			}
			if tool, ok := execToolEvent("completed", event.Item); ok && progress != nil {
				progress(tool)
			}
		case "turn.completed":
			turnUsage := usageFromExec(event.Usage, false)
			if result.LastTurnUsage.TotalTokens == 0 {
				result.LastTurnUsage = turnUsage
			}
			if result.TokenUsage.TotalTokens == 0 {
				result.TokenUsage = turnUsage
			}
		case "token_count":
			applyTokenCount(&result, event.Payload.Info)
		case "event_msg":
			if event.Payload.Type == "token_count" {
				applyTokenCount(&result, event.Payload.Info)
			}
		case "turn.failed":
			if event.Error != nil && event.Error.Message != "" {
				return TurnResult{}, errors.New(event.Error.Message)
			}
			return TurnResult{}, errors.New("codex turn failed")
		case "error":
			if event.Message != "" {
				return TurnResult{}, errors.New(event.Message)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return TurnResult{}, err
	}
	result.Text = strings.TrimSpace(completedText)
	return result, nil
}

func normalizeInput(input []InputPart) (string, []string) {
	var prompt []string
	var images []string
	for _, item := range input {
		switch strings.ToLower(item.Type) {
		case "text":
			if strings.TrimSpace(item.Text) != "" {
				prompt = append(prompt, item.Text)
			}
		case "localimage", "local_image":
			if item.Path != "" {
				images = append(images, item.Path)
			}
		case "skill":
			if item.Name != "" {
				prompt = append(prompt, "Use Codex skill $"+item.Name+" if relevant.")
			}
		}
	}
	return strings.Join(prompt, "\n\n"), images
}

func execToolEvent(phase string, raw json.RawMessage) (ToolEvent, bool) {
	if len(raw) == 0 {
		return ToolEvent{}, false
	}
	var item map[string]any
	if err := json.Unmarshal(raw, &item); err != nil {
		return ToolEvent{}, false
	}
	itemType := stringValue(item, "type")
	if !isToolItem(itemType) {
		return ToolEvent{}, false
	}
	event := ToolEvent{Phase: phase, Type: itemType, Status: stringValue(item, "status")}
	switch itemType {
	case "command_execution", "commandExecution":
		event.Label = stringValue(item, "command")
		if code, ok := optionalInt(item, "exit_code", "exitCode"); ok {
			event.Details = fmt.Sprintf("exit=%d", code)
		}
	case "file_change", "fileChange":
		event.Label = "file changes"
		event.Details = changesSummary(item["changes"])
	case "mcp_tool_call", "mcpToolCall":
		event.Label = strings.TrimSpace(firstNonEmpty(stringValue(item, "server"), "") + "/" + firstNonEmpty(stringValue(item, "tool"), ""))
	case "web_search", "webSearch":
		event.Label = stringValue(item, "query")
	case "todo_list":
		event.Label = "todo list"
	}
	if event.Label == "" {
		event.Label = itemType
	}
	return event, true
}

func agentMessageText(raw json.RawMessage) string {
	var item struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return ""
	}
	if item.Type == "agent_message" || item.Type == "agentMessage" {
		return item.Text
	}
	return ""
}

func applyTokenCount(result *TurnResult, info execTokenInfo) {
	if total := usageFromExec(info.TotalTokenUsage, true); total.TotalTokens > 0 {
		result.TokenUsage = total
	}
	if last := usageFromExec(info.LastTokenUsage, false); last.TotalTokens > 0 {
		result.LastTurnUsage = last
	}
}

func usageFromExec(usage execUsage, cumulative bool) TokenUsage {
	input := usage.InputTokens
	output := usage.OutputTokens
	total := usage.TotalTokens
	if total == 0 {
		total = input + output
	}
	return TokenUsage{
		InputTokens:           input,
		CachedInputTokens:     usage.CachedInputTokens,
		OutputTokens:          output,
		ReasoningOutputTokens: usage.ReasoningOutputTokens,
		TotalTokens:           total,
		Cumulative:            cumulative,
	}
}

func isToolItem(itemType string) bool {
	switch itemType {
	case "command_execution", "commandExecution", "file_change", "fileChange", "mcp_tool_call", "mcpToolCall", "web_search", "webSearch", "todo_list":
		return true
	default:
		return false
	}
}

func changesSummary(value any) string {
	changes, ok := value.([]any)
	if !ok || len(changes) == 0 {
		return ""
	}
	paths := make([]string, 0, len(changes))
	for _, change := range changes {
		if item, ok := change.(map[string]any); ok {
			if path := stringValue(item, "path"); path != "" {
				paths = append(paths, path)
			}
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

func codexEnv(base []string) []string {
	hasOriginator := false
	for _, item := range base {
		if strings.HasPrefix(item, originatorEnv+"=") {
			hasOriginator = true
			break
		}
	}
	if !hasOriginator {
		base = append(base, originatorEnv+"="+originator)
	}
	return base
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

func stringValue(values map[string]any, key string) string {
	if value, ok := values[key].(string); ok {
		return value
	}
	return ""
}

func optionalInt(values map[string]any, keys ...string) (int64, bool) {
	for _, key := range keys {
		if value, ok := values[key]; ok {
			switch typed := value.(type) {
			case float64:
				return int64(typed), true
			case int64:
				return typed, true
			case int:
				return int64(typed), true
			}
		}
	}
	return 0, false
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
