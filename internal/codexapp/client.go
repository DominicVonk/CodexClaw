package codexapp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/DominicVonk/CodexClaw/internal/config"
)

type Gateway struct {
	cfg       config.CodexConfig
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	nextID    atomic.Int64
	writeMu   sync.Mutex
	sendMu    sync.Mutex
	pendingMu sync.Mutex
	pending   map[int64]chan rpcMessage
	events    chan rpcMessage
	closed    chan struct{}
}

type TurnResult struct {
	Text       string
	TokenUsage TokenUsage
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
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
}

type InputPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	URL  string `json:"url,omitempty"`
	Path string `json:"path,omitempty"`
	Name string `json:"name,omitempty"`
}

type rpcMessage struct {
	ID     *int64          `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func Start(ctx context.Context, cfg config.CodexConfig) (*Gateway, error) {
	cmd := exec.CommandContext(ctx, cfg.Command, cfg.Args...)
	cmd.Dir = cfg.CWD

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	g := &Gateway{
		cfg:     cfg,
		cmd:     cmd,
		stdin:   stdin,
		stdout:  stdout,
		pending: make(map[int64]chan rpcMessage),
		events:  make(chan rpcMessage, 256),
		closed:  make(chan struct{}),
	}

	go g.readLoop()
	go func() {
		_ = cmd.Wait()
		close(g.closed)
	}()

	if err := g.initialize(ctx); err != nil {
		g.Close()
		return nil, err
	}
	return g, nil
}

func (g *Gateway) Close() error {
	_ = g.stdin.Close()
	if g.cmd != nil && g.cmd.Process != nil {
		_ = g.cmd.Process.Kill()
	}
	return nil
}

func (g *Gateway) initialize(ctx context.Context) error {
	params := map[string]any{
		"clientInfo": map[string]string{
			"name":    g.cfg.ClientName,
			"title":   g.cfg.ClientTitle,
			"version": g.cfg.ClientVersion,
		},
		"capabilities": map[string]any{
			"experimentalApi": true,
		},
	}
	if _, err := g.call(ctx, "initialize", params); err != nil {
		return err
	}
	return g.notify(ctx, "initialized", nil)
}

func (g *Gateway) ResumeThread(ctx context.Context, threadID string) error {
	if strings.TrimSpace(threadID) == "" {
		return errors.New("thread id is required")
	}
	_, err := g.call(ctx, "thread/resume", map[string]any{
		"threadId": threadID,
		"cwd":      g.cfg.CWD,
	})
	return err
}

func (g *Gateway) CompactThread(ctx context.Context, threadID string) error {
	if strings.TrimSpace(threadID) == "" {
		return errors.New("thread id is required")
	}
	_, err := g.call(ctx, "thread/compact/start", map[string]any{
		"threadId": threadID,
	})
	return err
}

func (g *Gateway) ListSkills(ctx context.Context) ([]Skill, error) {
	raw, err := g.call(ctx, "skills/list", map[string]any{
		"cwds": []string{g.cfg.CWD},
	})
	if err != nil {
		return nil, err
	}
	return extractSkills(raw), nil
}

func (g *Gateway) StartThread(ctx context.Context) (string, error) {
	params := map[string]any{
		"cwd": g.cfg.CWD,
	}
	if g.cfg.PermissionProfile != "" {
		params["permissions"] = g.cfg.PermissionProfile
	}
	raw, err := g.call(ctx, "thread/start", params)
	if err != nil {
		return "", err
	}

	var decoded struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return "", err
	}
	if decoded.Thread.ID == "" {
		return "", errors.New("thread/start response did not include thread.id")
	}
	return decoded.Thread.ID, nil
}

func (g *Gateway) Send(ctx context.Context, threadID string, input []InputPart, effort string, progress ProgressFunc) (TurnResult, error) {
	g.sendMu.Lock()
	defer g.sendMu.Unlock()

	if len(input) == 0 {
		return TurnResult{}, errors.New("turn input is required")
	}
	params := map[string]any{
		"threadId": threadID,
		"input":    input,
		"cwd":      g.cfg.CWD,
	}
	if g.cfg.Model != "" {
		params["model"] = g.cfg.Model
	}
	if effort != "" {
		params["effort"] = effort
	} else if g.cfg.Effort != "" {
		params["effort"] = g.cfg.Effort
	}
	if g.cfg.ApprovalPolicy != "" {
		params["approvalPolicy"] = g.cfg.ApprovalPolicy
	}
	if g.cfg.PermissionProfile != "" {
		params["permissions"] = g.cfg.PermissionProfile
	}

	raw, err := g.call(ctx, "turn/start", params)
	if err != nil {
		return TurnResult{}, err
	}
	turnID := turnIDFromStart(raw)
	var builder strings.Builder
	completedText := ""
	tokenUsage := TokenUsage{}

	for {
		select {
		case <-ctx.Done():
			return TurnResult{}, ctx.Err()
		case msg, ok := <-g.events:
			if !ok {
				return TurnResult{}, errors.New("codex app-server event stream closed")
			}
			if msg.Method == "" {
				continue
			}
			if !eventMatchesTurn(msg.Params, threadID, turnID) {
				continue
			}
			switch msg.Method {
			case "item/started":
				if event, ok := extractToolEvent("started", msg.Params); ok && progress != nil {
					progress(event)
				}
			case "item/agentMessage/delta":
				builder.WriteString(extractString(msg.Params, "delta"))
			case "item/completed":
				if text := extractCompletedAgentText(msg.Params); text != "" {
					completedText = text
				}
				if event, ok := extractToolEvent("completed", msg.Params); ok && progress != nil {
					progress(event)
				}
			case "thread/tokenUsage/updated":
				if usage := extractTokenUsage(msg.Params); usage.TotalTokens > 0 {
					tokenUsage = usage
				}
			case "turn/completed":
				if usage := extractTokenUsage(msg.Params); usage.TotalTokens > 0 {
					tokenUsage = usage
				}
				text := strings.TrimSpace(builder.String())
				if text == "" {
					text = strings.TrimSpace(completedText)
				}
				return TurnResult{Text: text, TokenUsage: tokenUsage}, nil
			}
		}
	}
}

func (g *Gateway) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := g.nextID.Add(1)
	ch := make(chan rpcMessage, 1)
	g.pendingMu.Lock()
	g.pending[id] = ch
	g.pendingMu.Unlock()
	defer func() {
		g.pendingMu.Lock()
		delete(g.pending, id)
		g.pendingMu.Unlock()
	}()

	if err := g.write(rpcMessageWithParams(id, method, params)); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-g.closed:
		return nil, errors.New("codex app-server exited")
	case msg := <-ch:
		if msg.Error != nil {
			return nil, fmt.Errorf("codex app-server %s failed: %s", method, msg.Error.Message)
		}
		return msg.Result, nil
	}
}

func (g *Gateway) notify(ctx context.Context, method string, params any) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return g.write(rpcNotificationWithParams(method, params))
	}
}

func (g *Gateway) write(msg any) error {
	line, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	g.writeMu.Lock()
	defer g.writeMu.Unlock()
	if _, err := g.stdin.Write(append(line, '\n')); err != nil {
		return err
	}
	return nil
}

func (g *Gateway) readLoop() {
	defer close(g.events)
	scanner := bufio.NewScanner(g.stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var msg rpcMessage
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		if msg.ID != nil {
			g.pendingMu.Lock()
			ch := g.pending[*msg.ID]
			g.pendingMu.Unlock()
			if ch != nil {
				ch <- msg
				continue
			}
		}
		select {
		case g.events <- msg:
		default:
		}
	}
}

func rpcMessageWithParams(id int64, method string, params any) any {
	if params == nil {
		return map[string]any{"id": id, "method": method}
	}
	return map[string]any{"id": id, "method": method, "params": params}
}

func rpcNotificationWithParams(method string, params any) any {
	if params == nil {
		return map[string]any{"method": method}
	}
	return map[string]any{"method": method, "params": params}
}

func turnIDFromStart(raw json.RawMessage) string {
	var decoded struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	_ = json.Unmarshal(raw, &decoded)
	return decoded.Turn.ID
}

func eventMatchesTurn(raw json.RawMessage, threadID string, turnID string) bool {
	if threadID == "" {
		return true
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return true
	}
	if v, ok := decoded["threadId"].(string); ok && v != threadID {
		return false
	}
	if turnID != "" {
		if v, ok := decoded["turnId"].(string); ok && v != turnID {
			return false
		}
	}
	return true
}

func extractSkills(raw json.RawMessage) []Skill {
	var data any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil
	}
	var skills []Skill
	walkSkills(data, &skills)
	return skills
}

func walkSkills(value any, skills *[]Skill) {
	switch typed := value.(type) {
	case map[string]any:
		name, _ := typed["name"].(string)
		path, _ := typed["path"].(string)
		if name != "" && path != "" {
			*skills = append(*skills, Skill{Name: name, Path: path})
		}
		for _, child := range typed {
			walkSkills(child, skills)
		}
	case []any:
		for _, child := range typed {
			walkSkills(child, skills)
		}
	}
}

func extractToolEvent(phase string, raw json.RawMessage) (ToolEvent, bool) {
	var decoded struct {
		Item map[string]any `json:"item"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded.Item == nil {
		return ToolEvent{}, false
	}
	itemType, _ := decoded.Item["type"].(string)
	if !isToolItem(itemType) {
		return ToolEvent{}, false
	}
	event := ToolEvent{Phase: phase, Type: itemType, Status: stringValue(decoded.Item, "status")}
	switch itemType {
	case "commandExecution":
		event.Label = stringValue(decoded.Item, "command")
		event.Details = stringValue(decoded.Item, "cwd")
		if code := intValue(decoded.Item, "exitCode"); code != 0 || hasKey(decoded.Item, "exitCode") {
			event.Details = strings.TrimSpace(fmt.Sprintf("%s exit=%d", event.Details, code))
		}
	case "fileChange":
		event.Label = "file changes"
		event.Details = changesSummary(decoded.Item["changes"])
	case "mcpToolCall":
		event.Label = strings.TrimSpace(stringValue(decoded.Item, "server") + "/" + stringValue(decoded.Item, "tool"))
	case "collabToolCall":
		event.Label = stringValue(decoded.Item, "tool")
	case "webSearch":
		event.Label = stringValue(decoded.Item, "query")
	case "imageView":
		event.Label = stringValue(decoded.Item, "path")
	case "contextCompaction", "compacted":
		event.Label = "conversation compaction"
	case "dynamicToolCall":
		event.Label = stringValue(decoded.Item, "tool")
	}
	if event.Label == "" {
		event.Label = itemType
	}
	return event, true
}

func isToolItem(itemType string) bool {
	switch itemType {
	case "commandExecution", "fileChange", "mcpToolCall", "collabToolCall", "webSearch", "imageView", "contextCompaction", "compacted", "dynamicToolCall":
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

func stringValue(values map[string]any, key string) string {
	if value, ok := values[key].(string); ok {
		return value
	}
	return ""
}

func intValue(values map[string]any, key string) int64 {
	if value, ok := values[key]; ok {
		switch typed := value.(type) {
		case float64:
			return int64(typed)
		case int64:
			return typed
		case int:
			return int64(typed)
		}
	}
	return 0
}

func hasKey(values map[string]any, key string) bool {
	_, ok := values[key]
	return ok
}

func extractTokenUsage(raw json.RawMessage) TokenUsage {
	var data any
	if err := json.Unmarshal(raw, &data); err != nil {
		return TokenUsage{}
	}
	return tokenUsageFromAny(data)
}

func tokenUsageFromAny(value any) TokenUsage {
	switch typed := value.(type) {
	case map[string]any:
		usage := TokenUsage{
			InputTokens:  firstInt(typed, "inputTokens", "input_tokens", "promptTokens", "prompt_tokens"),
			OutputTokens: firstInt(typed, "outputTokens", "output_tokens", "completionTokens", "completion_tokens"),
			TotalTokens:  firstInt(typed, "totalTokens", "total_tokens"),
		}
		if usage.TotalTokens == 0 && (usage.InputTokens > 0 || usage.OutputTokens > 0) {
			usage.TotalTokens = usage.InputTokens + usage.OutputTokens
		}
		if usage.TotalTokens > 0 {
			return usage
		}
		for _, child := range typed {
			if usage := tokenUsageFromAny(child); usage.TotalTokens > 0 {
				return usage
			}
		}
	case []any:
		for _, child := range typed {
			if usage := tokenUsageFromAny(child); usage.TotalTokens > 0 {
				return usage
			}
		}
	}
	return TokenUsage{}
}

func firstInt(values map[string]any, keys ...string) int64 {
	for _, key := range keys {
		if value, ok := values[key]; ok {
			switch typed := value.(type) {
			case float64:
				return int64(typed)
			case int64:
				return typed
			case int:
				return int64(typed)
			case json.Number:
				parsed, _ := typed.Int64()
				return parsed
			}
		}
	}
	return 0
}

func extractCompletedAgentText(raw json.RawMessage) string {
	var decoded struct {
		Item struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"item"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return ""
	}
	if decoded.Item.Type != "agentMessage" {
		return ""
	}
	return decoded.Item.Text
}

func extractString(raw json.RawMessage, key string) string {
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return ""
	}
	if v, ok := decoded[key].(string); ok {
		return v
	}
	return ""
}
