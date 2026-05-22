package router

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
	"sync"

	"github.com/DominicVonk/CodexClaw/internal/codexapp"
	"github.com/DominicVonk/CodexClaw/internal/config"
	"github.com/DominicVonk/CodexClaw/internal/media"
	"github.com/DominicVonk/CodexClaw/internal/session"
)

type ReplyFunc func(context.Context, string) error

type Message struct {
	Text        string
	Attachments []media.Attachment
}

type Identity struct {
	Source    string
	ChatID    string
	SenderID  string
	SessionID string
	AllowKeys []string
}

type Router struct {
	gateway   *codexapp.Gateway
	cfg       config.Config
	sessions  *session.Store
	allowlist map[string]struct{}
	mu        sync.Mutex
	loaded    map[string]struct{}
	locks     map[string]*sync.Mutex
}

func New(gateway *codexapp.Gateway, cfg config.Config) (*Router, error) {
	sessions, err := session.Open(cfg.Sessions.SQLitePath)
	if err != nil {
		return nil, err
	}
	return &Router{
		gateway:   gateway,
		cfg:       cfg,
		sessions:  sessions,
		allowlist: buildAllowlist(cfg.Allowlist.Entries),
		loaded:    make(map[string]struct{}),
		locks:     make(map[string]*sync.Mutex),
	}, nil
}

func (r *Router) Close() error {
	return r.sessions.Close()
}

func (r *Router) Handle(ctx context.Context, identity Identity, text string, reply ReplyFunc) error {
	return r.HandleMessage(ctx, identity, Message{Text: text}, reply)
}

func (r *Router) HandleMessage(ctx context.Context, identity Identity, message Message, reply ReplyFunc) error {
	text := strings.TrimSpace(message.Text)
	if text == "" && len(message.Attachments) == 0 {
		return nil
	}
	identity = normalizeIdentity(identity)
	if !r.allowed(identity) {
		return nil
	}

	scopeKey := identity.Source + ":" + identity.SessionID
	lock := r.lockFor(scopeKey)
	lock.Lock()
	defer lock.Unlock()

	if len(message.Attachments) == 0 {
		if handled, err := r.handleCommand(ctx, scopeKey, text, reply); handled || err != nil {
			return err
		}
	}

	if r.cfg.Router.ProgressMessage != "" {
		_ = reply(ctx, r.cfg.Router.ProgressMessage)
	}

	active, err := r.activeSession(ctx, scopeKey)
	if err != nil {
		_ = reply(ctx, "Codex session startup failed: "+err.Error())
		return err
	}

	memories, err := r.sessions.ListMemories(ctx, scopeKey)
	if err != nil {
		_ = reply(ctx, "Could not load memory: "+err.Error())
		return err
	}
	input, err := r.codexInput(ctx, text, message.Attachments, memories)
	if err != nil {
		_ = reply(ctx, "Could not prepare Codex input: "+err.Error())
		return err
	}
	var progress codexapp.ProgressFunc
	if r.cfg.Router.ShowToolUsage {
		progress = func(event codexapp.ToolEvent) {
			_ = reply(ctx, formatToolEvent(event))
		}
	}
	result, err := r.gateway.Send(ctx, active.ThreadID, input, active.ReasoningEffort, progress)
	if err != nil {
		_ = reply(ctx, "Codex turn failed: "+err.Error())
		return err
	}
	if result.TokenUsage.TotalTokens > 0 {
		active.InputTokens = result.TokenUsage.InputTokens
		active.OutputTokens = result.TokenUsage.OutputTokens
		active.TotalTokens = result.TokenUsage.TotalTokens
		_ = r.sessions.UpdateTokenUsage(ctx, active.ID, active.InputTokens, active.OutputTokens, active.TotalTokens)
	} else {
		_ = r.sessions.Touch(ctx, active.ID)
	}
	if err := r.autoCompact(ctx, active, reply); err != nil {
		return err
	}

	answer := strings.TrimSpace(result.Text)
	if answer == "" {
		answer = "(Codex completed without a final text response.)"
	}
	for _, chunk := range split(answer, r.cfg.Router.MaxReplyChars) {
		if err := reply(ctx, chunk); err != nil {
			return fmt.Errorf("send reply: %w", err)
		}
	}
	return nil
}

func (r *Router) codexInput(ctx context.Context, text string, attachments []media.Attachment, memories []session.Memory) ([]codexapp.InputPart, error) {
	text = strings.TrimSpace(text)
	if len(memories) > 0 {
		var memoryText strings.Builder
		memoryText.WriteString("Persistent memory for this chat:\n")
		for _, memory := range memories {
			memoryText.WriteString(fmt.Sprintf("- %s\n", memory.Content))
		}
		if text != "" {
			memoryText.WriteString("\nUser message:\n")
			memoryText.WriteString(text)
		}
		text = strings.TrimSpace(memoryText.String())
	}
	if text == "" && len(attachments) > 0 {
		text = "Please inspect the attached file(s)."
	}
	if len(attachments) > 0 {
		var builder strings.Builder
		builder.WriteString(text)
		builder.WriteString("\n\nAttached local files:")
		for _, attachment := range attachments {
			builder.WriteString(fmt.Sprintf("\n- %s: %s", attachment.Kind, attachment.Path))
			if attachment.Name != "" {
				builder.WriteString(" (" + attachment.Name + ")")
			}
			if attachment.MIME != "" {
				builder.WriteString(" [" + attachment.MIME + "]")
			}
		}
		text = builder.String()
	}
	parts := []codexapp.InputPart{{Type: "text", Text: text}}
	for _, attachment := range attachments {
		if attachment.Kind == "image" {
			parts = append(parts, codexapp.InputPart{Type: "localImage", Path: attachment.Path})
		}
	}
	parts = append(parts, r.skillInputs(ctx, text)...)
	return parts, nil
}

func (r *Router) skillInputs(ctx context.Context, text string) []codexapp.InputPart {
	names := skillNames(text)
	if len(names) == 0 {
		return nil
	}
	skills, err := r.gateway.ListSkills(ctx)
	if err != nil {
		return nil
	}
	byName := make(map[string]codexapp.Skill, len(skills))
	for _, skill := range skills {
		byName[skill.Name] = skill
	}
	parts := make([]codexapp.InputPart, 0, len(names))
	for _, name := range names {
		if name == "skills" || name == "skill-dictionary" {
			parts = append(parts, codexapp.InputPart{Type: "text", Text: skillDictionaryText(skills)})
			continue
		}
		if skill, ok := byName[name]; ok {
			parts = append(parts, codexapp.InputPart{Type: "skill", Name: skill.Name, Path: skill.Path})
		}
	}
	return parts
}

func skillDictionaryText(skills []codexapp.Skill) string {
	if len(skills) == 0 {
		return "Skill dictionary: no Codex skills are currently available."
	}
	var builder strings.Builder
	builder.WriteString("Skill dictionary for this CodexClaw chat. Use these by referencing $skill-name in a message when the task matches the skill. Available skills:\n")
	for _, skill := range skills {
		builder.WriteString("- $")
		builder.WriteString(skill.Name)
		if skill.Path != "" {
			builder.WriteString(": ")
			builder.WriteString(skill.Path)
		}
		builder.WriteString("\n")
	}
	return strings.TrimSpace(builder.String())
}

func skillNames(text string) []string {
	fields := strings.Fields(text)
	seen := map[string]struct{}{}
	var names []string
	for _, field := range fields {
		field = strings.Trim(field, " .,;:!?()[]{}<>\"'")
		if !strings.HasPrefix(field, "$") || len(field) < 2 {
			continue
		}
		name := strings.TrimPrefix(field, "$")
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names
}

func formatToolEvent(event codexapp.ToolEvent) string {
	verb := "Using"
	if event.Phase == "completed" {
		verb = "Finished"
	}
	label := event.Label
	if label == "" {
		label = event.Type
	}
	text := fmt.Sprintf("%s tool: %s", verb, label)
	if event.Status != "" && event.Phase == "completed" {
		text += " (" + event.Status + ")"
	}
	if event.Details != "" {
		text += "\n" + event.Details
	}
	return text
}

func (r *Router) handleCommand(ctx context.Context, scopeKey string, text string, reply ReplyFunc) (bool, error) {
	cmd, arg, ok := parseCommand(text)
	if !ok {
		return false, nil
	}
	switch cmd {
	case "/remember":
		memory, err := r.sessions.AddMemory(ctx, scopeKey, arg)
		if err != nil {
			_ = reply(ctx, "Could not save memory: "+err.Error())
			return true, err
		}
		return true, reply(ctx, fmt.Sprintf("Remembered %d: %s", memory.ID, memory.Content))
	case "/memory":
		return true, r.replyMemories(ctx, scopeKey, reply)
	case "/forget":
		return true, r.forgetMemory(ctx, scopeKey, arg, reply)
	case "/skills":
		return true, r.replySkills(ctx, reply)
	case "/new":
		session, err := r.createSession(ctx, scopeKey, arg)
		if err != nil {
			_ = reply(ctx, "Could not create session: "+err.Error())
			return true, err
		}
		return true, reply(ctx, fmt.Sprintf("Started session %d: %s", session.ID, session.Name))
	case "/status":
		active, err := r.activeSession(ctx, scopeKey)
		if err != nil {
			_ = reply(ctx, "Could not read status: "+err.Error())
			return true, err
		}
		return true, reply(ctx, statusText(active, r.cfg))
	case "/reasoning":
		active, err := r.activeSession(ctx, scopeKey)
		if err != nil {
			_ = reply(ctx, "Could not read session: "+err.Error())
			return true, err
		}
		effort, global := parseReasoningArg(arg)
		if effort == "" {
			return true, reply(ctx, reasoningText(active))
		}
		if !validReasoningEffort(effort) {
			return true, reply(ctx, "Usage: /reasoning <low|medium|high|xhigh|default> [--global]")
		}
		if global {
			if err := r.updateGlobalReasoning(effort); err != nil {
				_ = reply(ctx, "Could not update global reasoning: "+err.Error())
				return true, err
			}
			if effort == "default" {
				r.cfg.Codex.Effort = ""
				return true, reply(ctx, "Global reasoning reset to Codex default.")
			}
			r.cfg.Codex.Effort = effort
			return true, reply(ctx, "Global reasoning set to "+effort+".")
		}
		if effort == "default" {
			effort = ""
		}
		if err := r.sessions.UpdateReasoning(ctx, active.ID, effort); err != nil {
			_ = reply(ctx, "Could not update reasoning: "+err.Error())
			return true, err
		}
		if effort == "" {
			return true, reply(ctx, "Reasoning reset to config default.")
		}
		return true, reply(ctx, "Reasoning set to "+effort+".")
	case "/session":
		if strings.TrimSpace(arg) == "" {
			return true, r.replySessionList(ctx, scopeKey, reply)
		}
		session, err := r.sessions.Find(ctx, scopeKey, arg)
		if err != nil {
			_ = reply(ctx, "Could not switch session: "+err.Error())
			return true, err
		}
		if err := r.ensureThreadLoaded(ctx, session.ThreadID); err != nil {
			_ = reply(ctx, "Could not resume session: "+err.Error())
			return true, err
		}
		if err := r.sessions.SetActive(ctx, scopeKey, session.ID); err != nil {
			_ = reply(ctx, "Could not switch session: "+err.Error())
			return true, err
		}
		return true, reply(ctx, fmt.Sprintf("Switched to session %d: %s", session.ID, session.Name))
	default:
		return false, nil
	}
}

func (r *Router) activeSession(ctx context.Context, scopeKey string) (session.Session, error) {
	active, ok, err := r.sessions.Active(ctx, scopeKey)
	if err != nil {
		return session.Session{}, err
	}
	if !ok {
		return r.createSession(ctx, scopeKey, "default")
	}
	if err := r.ensureThreadLoaded(ctx, active.ThreadID); err != nil {
		return session.Session{}, err
	}
	return active, nil
}

func (r *Router) createSession(ctx context.Context, scopeKey string, name string) (session.Session, error) {
	threadID, err := r.gateway.StartThread(ctx)
	if err != nil {
		return session.Session{}, err
	}
	created, err := r.sessions.Create(ctx, scopeKey, name, threadID)
	if err != nil {
		return session.Session{}, err
	}
	r.markLoaded(threadID)
	return created, nil
}

func (r *Router) ensureThreadLoaded(ctx context.Context, threadID string) error {
	r.mu.Lock()
	_, ok := r.loaded[threadID]
	r.mu.Unlock()
	if ok {
		return nil
	}
	if err := r.gateway.ResumeThread(ctx, threadID); err != nil {
		return err
	}
	r.markLoaded(threadID)
	return nil
}

func (r *Router) markLoaded(threadID string) {
	r.mu.Lock()
	r.loaded[threadID] = struct{}{}
	r.mu.Unlock()
}

func (r *Router) replySessionList(ctx context.Context, scopeKey string, reply ReplyFunc) error {
	sessions, activeID, err := r.sessions.List(ctx, scopeKey)
	if err != nil {
		_ = reply(ctx, "Could not list sessions: "+err.Error())
		return err
	}
	if len(sessions) == 0 {
		return reply(ctx, "No sessions yet. Use /new [name] to create one.")
	}
	var builder strings.Builder
	builder.WriteString("Sessions:\n")
	for _, session := range sessions {
		prefix := "  "
		if session.ID == activeID {
			prefix = "* "
		}
		builder.WriteString(fmt.Sprintf("%s%d %s\n", prefix, session.ID, session.Name))
	}
	builder.WriteString("Use /session <id|name> to switch.")
	return reply(ctx, strings.TrimSpace(builder.String()))
}

func (r *Router) replyMemories(ctx context.Context, scopeKey string, reply ReplyFunc) error {
	memories, err := r.sessions.ListMemories(ctx, scopeKey)
	if err != nil {
		_ = reply(ctx, "Could not list memory: "+err.Error())
		return err
	}
	if len(memories) == 0 {
		return reply(ctx, "No memory stored. Use /remember <text> to save one.")
	}
	var builder strings.Builder
	builder.WriteString("Memory:\n")
	for _, memory := range memories {
		builder.WriteString(fmt.Sprintf("%d %s\n", memory.ID, memory.Content))
	}
	builder.WriteString("Use /forget <id> or /forget all.")
	return reply(ctx, strings.TrimSpace(builder.String()))
}

func (r *Router) forgetMemory(ctx context.Context, scopeKey string, arg string, reply ReplyFunc) error {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return reply(ctx, "Usage: /forget <id|all>")
	}
	if strings.EqualFold(arg, "all") {
		if err := r.sessions.ClearMemories(ctx, scopeKey); err != nil {
			_ = reply(ctx, "Could not clear memory: "+err.Error())
			return err
		}
		return reply(ctx, "Memory cleared.")
	}
	id, err := strconv.ParseInt(arg, 10, 64)
	if err != nil {
		return reply(ctx, "Usage: /forget <id|all>")
	}
	deleted, err := r.sessions.DeleteMemory(ctx, scopeKey, id)
	if err != nil {
		_ = reply(ctx, "Could not forget memory: "+err.Error())
		return err
	}
	if !deleted {
		return reply(ctx, "Memory not found.")
	}
	return reply(ctx, "Memory deleted.")
}

func (r *Router) replySkills(ctx context.Context, reply ReplyFunc) error {
	skills, err := r.gateway.ListSkills(ctx)
	if err != nil {
		_ = reply(ctx, "Could not list skills: "+err.Error())
		return err
	}
	var builder strings.Builder
	builder.WriteString("Skills:\n")
	builder.WriteString("$skills - inject a dictionary of available skills into the next Codex turn\n")
	builder.WriteString("$skill-dictionary - alias for $skills\n")
	if len(skills) == 0 {
		builder.WriteString("No app-server skills found.\n")
		return reply(ctx, strings.TrimSpace(builder.String()))
	}
	for _, skill := range skills {
		builder.WriteString("$" + skill.Name)
		if skill.Path != "" {
			builder.WriteString(" - " + skill.Path)
		}
		builder.WriteString("\n")
	}
	return reply(ctx, strings.TrimSpace(builder.String()))
}

func (r *Router) autoCompact(ctx context.Context, active session.Session, reply ReplyFunc) error {
	if !r.cfg.Sessions.AutoCompact || r.cfg.Sessions.AutoCompactAfterTokens <= 0 || active.TotalTokens <= 0 {
		return nil
	}
	if active.TotalTokens-active.LastCompactedTotalTokens < r.cfg.Sessions.AutoCompactAfterTokens {
		return nil
	}
	if err := r.gateway.CompactThread(ctx, active.ThreadID); err != nil {
		_ = reply(ctx, "Auto-compaction failed: "+err.Error())
		return err
	}
	_ = r.sessions.MarkCompacted(ctx, active.ID, active.TotalTokens)
	_ = reply(ctx, fmt.Sprintf("Auto-compaction started at %d tokens.", active.TotalTokens))
	return nil
}

func statusText(active session.Session, cfg config.Config) string {
	reasoning := active.ReasoningEffort
	if reasoning == "" {
		reasoning = "default"
		if cfg.Codex.Effort != "" {
			reasoning = cfg.Codex.Effort + " (default)"
		}
	}
	compact := "off"
	if cfg.Sessions.AutoCompact {
		compact = fmt.Sprintf("on at %d tokens", cfg.Sessions.AutoCompactAfterTokens)
	}
	return fmt.Sprintf("Session %d: %s\nThread: %s\nReasoning: %s\nTokens: %d total (%d input, %d output)\nAuto-compaction: %s", active.ID, active.Name, active.ThreadID, reasoning, active.TotalTokens, active.InputTokens, active.OutputTokens, compact)
}

func parseReasoningArg(arg string) (string, bool) {
	fields := strings.Fields(arg)
	global := false
	var effort string
	for _, field := range fields {
		switch strings.ToLower(strings.TrimSpace(field)) {
		case "--global", "-global":
			global = true
		default:
			if effort == "" {
				effort = strings.ToLower(field)
			}
		}
	}
	return effort, global
}

func (r *Router) updateGlobalReasoning(effort string) error {
	if r.cfg.SourcePath == "" {
		return fmt.Errorf("config source path is unknown")
	}
	data, err := os.ReadFile(r.cfg.SourcePath)
	if err != nil {
		return err
	}
	var root map[string]any
	if err := yaml.Unmarshal(data, &root); err != nil {
		return err
	}
	codex, ok := root["codex"].(map[string]any)
	if !ok {
		codex = map[string]any{}
		root["codex"] = codex
	}
	if effort == "default" {
		delete(codex, "effort")
	} else {
		codex["effort"] = effort
	}
	updated, err := yaml.Marshal(root)
	if err != nil {
		return err
	}
	return os.WriteFile(r.cfg.SourcePath, updated, 0o600)
}

func reasoningText(active session.Session) string {
	if active.ReasoningEffort == "" {
		return "Reasoning: default. Use /reasoning <low|medium|high|xhigh> to switch."
	}
	return "Reasoning: " + active.ReasoningEffort + ". Use /reasoning default to reset."
}

func validReasoningEffort(effort string) bool {
	switch effort {
	case "low", "medium", "high", "xhigh", "default":
		return true
	default:
		return false
	}
}

func parseCommand(text string) (string, string, bool) {
	fields := strings.Fields(text)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return "", "", false
	}
	cmd := fields[0]
	if i := strings.Index(cmd, "@"); i >= 0 {
		cmd = cmd[:i]
	}
	if cmd != "/new" && cmd != "/session" && cmd != "/status" && cmd != "/reasoning" && cmd != "/remember" && cmd != "/memory" && cmd != "/forget" && cmd != "/skills" {
		return "", "", false
	}
	arg := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
	return cmd, arg, true
}

func (r *Router) lockFor(key string) *sync.Mutex {
	r.mu.Lock()
	defer r.mu.Unlock()
	lock := r.locks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		r.locks[key] = lock
	}
	return lock
}

func (r *Router) allowed(identity Identity) bool {
	if !r.cfg.Allowlist.Enabled {
		return true
	}
	for _, key := range identity.AllowKeys {
		if _, ok := r.allowlist[key]; ok {
			return true
		}
	}
	return false
}

func buildAllowlist(entries []string) map[string]struct{} {
	out := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry != "" {
			out[entry] = struct{}{}
		}
	}
	return out
}

func normalizeIdentity(identity Identity) Identity {
	identity.Source = strings.TrimSpace(identity.Source)
	identity.ChatID = strings.TrimSpace(identity.ChatID)
	identity.SenderID = strings.TrimSpace(identity.SenderID)
	identity.SessionID = strings.TrimSpace(identity.SessionID)
	if identity.SessionID == "" {
		identity.SessionID = identity.ChatID
	}
	senderKey := identity.Source + ":" + identity.SenderID
	keys := []string{senderKey}
	keys = append(keys, identity.AllowKeys...)
	identity.AllowKeys = dedupe(keys)
	return identity
}

func dedupe(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func split(s string, max int) []string {
	if max <= 0 || len(s) <= max {
		return []string{s}
	}
	var out []string
	for len(s) > max {
		cut := strings.LastIndex(s[:max], "\n")
		if cut < max/2 {
			cut = strings.LastIndex(s[:max], " ")
		}
		if cut < max/2 {
			cut = max
		}
		out = append(out, strings.TrimSpace(s[:cut]))
		s = strings.TrimSpace(s[cut:])
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}
