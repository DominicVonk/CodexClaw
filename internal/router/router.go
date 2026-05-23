package router

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"sync"

	"github.com/DominicVonk/CodexClaw/internal/codexapp"
	"github.com/DominicVonk/CodexClaw/internal/config"
	"github.com/DominicVonk/CodexClaw/internal/media"
	"github.com/DominicVonk/CodexClaw/internal/session"
	"github.com/DominicVonk/CodexClaw/internal/speech"
)

type OutgoingMessage struct {
	Text       string
	Attachment *media.Attachment
}

type ReplyFunc func(context.Context, OutgoingMessage) error

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
	speech    speech.Service
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
		speech:    speech.New(cfg.Speech, cfg.Media),
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
		log.Printf("message ignored by allowlist source=%s sender=%s keys=%s", identity.Source, redactedID(identity.SenderID), redactedKeys(identity.AllowKeys))
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

	active, err := r.activeSession(ctx, scopeKey)
	if err != nil {
		_ = replyText(ctx, reply, "Codex session startup failed: "+err.Error())
		return err
	}

	memories, err := r.sessions.ListMemories(ctx, scopeKey)
	if err != nil {
		_ = replyText(ctx, reply, "Could not load memory: "+err.Error())
		return err
	}
	input, err := r.codexInput(ctx, text, message.Attachments, memories)
	if err != nil {
		_ = replyText(ctx, reply, "Could not prepare Codex input: "+err.Error())
		return err
	}
	var progress codexapp.ProgressFunc
	if r.cfg.Router.ShowToolUsage {
		progress = func(event codexapp.ToolEvent) {
			if text := formatToolEvent(event); text != "" {
				_ = replyText(ctx, reply, text)
			}
		}
	}
	threadID := active.ThreadID
	if r.cfg.Sessions.MinimalContext() {
		threadID = ""
	}
	result, err := r.gateway.Send(ctx, threadID, input, active.Model, active.ReasoningEffort, progress)
	if err != nil {
		_ = replyText(ctx, reply, "Codex turn failed: "+err.Error())
		return err
	}
	if result.ThreadID != "" && result.ThreadID != active.ThreadID {
		active.ThreadID = result.ThreadID
		_ = r.sessions.UpdateThreadID(ctx, active.ID, result.ThreadID)
		r.markLoaded(result.ThreadID)
	}
	if result.TokenUsage.TotalTokens > 0 {
		active = mergeTokenUsage(active, result, r.cfg.Sessions.MinimalContext())
		_ = r.sessions.UpdateTokenUsage(ctx, active.ID, active.InputTokens, active.OutputTokens, active.TotalTokens, active.LastInputTokens, active.LastOutputTokens, active.LastTotalTokens)
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
		if err := replyText(ctx, reply, chunk); err != nil {
			return fmt.Errorf("send reply: %w", err)
		}
	}
	if r.shouldSynthesizeReply(text, message.Attachments) {
		audio, err := r.speech.Synthesize(ctx, answer)
		if err != nil {
			_ = replyText(ctx, reply, "Text-to-speech failed: "+err.Error())
			return nil
		}
		if err := reply(ctx, OutgoingMessage{Attachment: audio}); err != nil {
			return fmt.Errorf("send audio reply: %w", err)
		}
	}
	return nil
}

func replyText(ctx context.Context, reply ReplyFunc, text string) error {
	return reply(ctx, OutgoingMessage{Text: text})
}

func (r *Router) codexInput(ctx context.Context, text string, attachments []media.Attachment, memories []session.Memory) ([]codexapp.InputPart, error) {
	text = strings.TrimSpace(text)
	userText := text
	if automatic := automaticMemories(userText, memories); len(automatic) > 0 {
		var builder strings.Builder
		builder.WriteString(memoryContextText(automatic, false))
		if text != "" {
			builder.WriteString("\nUser message:\n")
			builder.WriteString(text)
		}
		text = strings.TrimSpace(builder.String())
	}
	audioText, localAttachments := r.prepareAttachments(ctx, attachments)
	if text == "" && len(localAttachments) > 0 && strings.TrimSpace(audioText) == "" {
		text = "Please inspect the attached file(s)."
	}
	if audioText != "" {
		if text != "" {
			text += "\n\n" + audioText
		} else {
			text = audioText
		}
	}
	if len(localAttachments) > 0 {
		var builder strings.Builder
		builder.WriteString(text)
		builder.WriteString("\n\nAttached local files:")
		for _, attachment := range localAttachments {
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
	for _, attachment := range localAttachments {
		if attachment.Kind == "image" {
			parts = append(parts, codexapp.InputPart{Type: "localImage", Path: attachment.Path})
		}
	}
	parts = append(parts, r.skillInputs(ctx, userText, memories)...)
	return parts, nil
}

func (r *Router) prepareAttachments(ctx context.Context, attachments []media.Attachment) (string, []media.Attachment) {
	var audio strings.Builder
	localAttachments := make([]media.Attachment, 0, len(attachments))
	for _, attachment := range attachments {
		if attachment.Kind != "audio" {
			localAttachments = append(localAttachments, attachment)
			continue
		}
		if !r.speech.STTEnabled() {
			audio.WriteString("Audio message received, but speech-to-text is not configured. Use $stt after configuring speech.stt.command to transcribe audio automatically.\n")
			localAttachments = append(localAttachments, attachment)
			continue
		}
		transcript, err := r.speech.Transcribe(ctx, attachment)
		if err != nil {
			audio.WriteString("Audio message received, but speech-to-text failed: " + err.Error() + "\n")
			localAttachments = append(localAttachments, attachment)
			continue
		}
		audio.WriteString("User voice transcript:\n")
		audio.WriteString(transcript)
		audio.WriteString("\n\nRespond to the spoken message. Do not inspect the audio file unless the user explicitly asks for audio metadata.\n")
	}
	return strings.TrimSpace(audio.String()), localAttachments
}

func (r *Router) skillInputs(ctx context.Context, text string, memories []session.Memory) []codexapp.InputPart {
	names := skillNames(text)
	if r.cfg.AgentBrowser.Enabled && r.cfg.AgentBrowser.AutoInject && shouldAutoUseAgentBrowser(text) {
		names = appendMissingSkill(names, "agent-browser")
	}
	if len(names) == 0 {
		return nil
	}
	needsAppSkills := false
	for _, name := range names {
		if !builtInSkill(name) || name == "skills" {
			needsAppSkills = true
			break
		}
	}
	var skills []codexapp.Skill
	var skillsErr error
	if needsAppSkills {
		skills, skillsErr = r.gateway.ListSkills(ctx)
	}
	byName := skillsByName(skills)
	parts := make([]codexapp.InputPart, 0, len(names))
	for _, name := range names {
		switch name {
		case "skills":
			parts = append(parts, codexapp.InputPart{Type: "text", Text: skillDictionaryText(skills, skillsErr)})
			continue
		case "memory":
			parts = append(parts, codexapp.InputPart{Type: "text", Text: memorySkillText(selectMemories(text, memories), len(memories))})
			continue
		case "skill-creator":
			parts = append(parts, codexapp.InputPart{Type: "text", Text: skillCreatorText()})
			continue
		case "agent-browser":
			parts = append(parts, codexapp.InputPart{Type: "text", Text: agentBrowserSkillText(r.cfg.AgentBrowser)})
			continue
		case "stt":
			parts = append(parts, codexapp.InputPart{Type: "text", Text: sttSkillText(r.cfg.Speech)})
			continue
		case "tts":
			parts = append(parts, codexapp.InputPart{Type: "text", Text: ttsSkillText(r.cfg.Speech)})
			continue
		}
		if skill, ok := byName[name]; ok {
			parts = append(parts, codexapp.InputPart{Type: "skill", Name: skill.Name, Path: skill.Path})
		}
	}
	return parts
}

func skillsByName(skills []codexapp.Skill) map[string]codexapp.Skill {
	byName := make(map[string]codexapp.Skill, len(skills))
	for _, skill := range skills {
		byName[strings.ToLower(skill.Name)] = skill
	}
	return byName
}

func skillDictionaryText(skills []codexapp.Skill, skillsErr error) string {
	var builder strings.Builder
	builder.WriteString("Skill dictionary for this CodexClaw chat. Use these by referencing $skill-name in a message when the task matches the skill. Available skills:\n")
	for _, skill := range builtInSkills() {
		builder.WriteString("- $")
		builder.WriteString(skill.name)
		builder.WriteString(": ")
		builder.WriteString(skill.description)
		builder.WriteString("\n")
	}
	if skillsErr != nil {
		builder.WriteString("- Codex skills unavailable: ")
		builder.WriteString(skillsErr.Error())
		builder.WriteString("\n")
		return strings.TrimSpace(builder.String())
	}
	if len(skills) == 0 {
		builder.WriteString("- No Codex skills found.\n")
		return strings.TrimSpace(builder.String())
	}
	for _, skill := range skills {
		builder.WriteString("- $")
		builder.WriteString(skill.Name)
		builder.WriteString("\n")
	}
	return strings.TrimSpace(builder.String())
}

type builtInSkillInfo struct {
	name        string
	description string
}

func builtInSkills() []builtInSkillInfo {
	return []builtInSkillInfo{
		{name: "skills", description: "inject this dictionary of built-in and Codex skills"},
		{name: "memory", description: "inject saved persistent memories for this chat with memory-management guidance"},
		{name: "memories", description: "alias for $memory"},
		{name: "skill-creator", description: "inject concise guidance for creating or updating Codex skills"},
		{name: "agent-browser", description: "inject compact agent-browser workflow for browser automation with refs and snapshots"},
		{name: "browser", description: "alias for $agent-browser"},
		{name: "stt", description: "transcribe attached voice/audio before the turn when speech-to-text is configured"},
		{name: "speech-to-text", description: "alias for $stt"},
		{name: "tts", description: "send the final answer back as an audio message when text-to-speech is configured"},
		{name: "text-to-speech", description: "alias for $tts"},
	}
}

func builtInSkill(name string) bool {
	switch name {
	case "skills", "memory", "skill-creator", "agent-browser", "stt", "tts":
		return true
	default:
		return false
	}
}

func memoryContextText(memories []session.Memory, includeIDs bool) string {
	var builder strings.Builder
	builder.WriteString("Persistent memory for this chat:\n")
	for _, memory := range memories {
		if includeIDs {
			builder.WriteString(fmt.Sprintf("- %d: %s\n", memory.ID, memory.Content))
		} else {
			builder.WriteString(fmt.Sprintf("- %s\n", memory.Content))
		}
	}
	return strings.TrimSpace(builder.String())
}

func memorySkillText(memories []session.Memory, total int) string {
	if len(memories) == 0 {
		return "Memory: none saved. Use /remember <text> to save durable chat context."
	}
	suffix := "\nCommands: /remember <text>, /memory, /forget <id|all>."
	if total > len(memories) {
		suffix = fmt.Sprintf("\nShowing %d of %d relevant memories. Use $memory all for every memory.\nCommands: /remember <text>, /memory, /forget <id|all>.", len(memories), total)
	}
	return memoryContextText(memories, true) + suffix
}

func skillCreatorText() string {
	return "Skill creator: create or update a concise Codex skill folder with SKILL.md frontmatter (name, description), a short workflow, and optional scripts/references/assets only when needed. Avoid extra README/changelog docs."
}

func agentBrowserSkillText(cfg config.AgentBrowserConfig) string {
	command := strings.TrimSpace(cfg.Command)
	if command == "" {
		command = "agent-browser"
	}
	session := strings.TrimSpace(cfg.Session)
	if session == "" {
		session = "codexclaw"
	}
	maxOutput := cfg.MaxOutput
	if maxOutput <= 0 {
		maxOutput = 12000
	}
	return fmt.Sprintf("Agent browser skill: use `%s` for browser automation when the task needs a real browser, page interaction, screenshots, login state, or DOM/page inspection. Keep output compact and prefer snapshots over full HTML.\nWorkflow:\n1. Open or connect: `%s --session %s --max-output %d open <url>`.\n2. Inspect interactives: `%s --session %s snapshot -i --compact`.\n3. Act by refs from the latest snapshot: `%s --session %s click @e1`, `fill @e2 \"text\"`, `press Enter`, `scroll down`.\n4. Re-snapshot after navigation or UI changes; use `get text <selector|@ref>`, `screenshot <path>`, `console`, and `errors` only when useful.\n5. Close with `%s --session %s close` when the browser session is no longer needed.\nInstall/repair: `mise run browser:install`, `mise run browser:doctor`, or `npx --yes agent-browser install`. For full upstream guidance run `%s skills get core --full`.", command, command, session, maxOutput, command, session, command, session, command, session, command)
}

func sttSkillText(cfg config.SpeechConfig) string {
	status := "not configured"
	if cfg.STT.Enabled && strings.TrimSpace(cfg.STT.Command) != "" {
		status = "configured"
	}
	return "Speech-to-text skill: attached Telegram voice/audio and WhatsApp audio messages are saved as local audio files. If STT is configured, CodexClaw transcribes them before sending this turn to Codex; use the transcript as the user's spoken input and mention uncertainty only when the transcript is unclear. STT status: " + status + "."
}

func ttsSkillText(cfg config.SpeechConfig) string {
	status := "not configured"
	if cfg.TTS.Enabled && strings.TrimSpace(cfg.TTS.Command) != "" {
		status = "configured"
	}
	return "Text-to-speech skill: answer normally in text. Because $tts was requested, CodexClaw will synthesize the final answer into an audio message after the turn when TTS is configured. Keep the answer concise and spoken-friendly. TTS status: " + status + "."
}

func shouldAutoUseAgentBrowser(text string) bool {
	lower := strings.ToLower(text)
	if strings.Contains(lower, "http://") || strings.Contains(lower, "https://") {
		return true
	}
	triggers := []string{
		"agent-browser",
		"browser",
		"web page",
		"webpage",
		"website",
		"open the site",
		"visit the site",
		"navigate to",
		"click ",
		"fill the form",
		"login",
		"log in",
		"screenshot",
		"inspect the page",
		"scrape",
		"crawl",
	}
	for _, trigger := range triggers {
		if strings.Contains(lower, trigger) {
			return true
		}
	}
	return false
}

func appendMissingSkill(names []string, name string) []string {
	for _, existing := range names {
		if existing == name {
			return names
		}
	}
	return append(names, name)
}

func selectMemories(text string, memories []session.Memory) []session.Memory {
	if len(memories) <= 5 || wantsAllMemory(text) {
		return memories
	}
	queryTerms := significantTerms(text)
	if len(queryTerms) == 0 {
		return memories[:3]
	}
	type scoredMemory struct {
		memory session.Memory
		score  int
		index  int
	}
	scored := make([]scoredMemory, 0, len(memories))
	for i, memory := range memories {
		score := memoryScore(queryTerms, memory.Content)
		if score > 0 {
			scored = append(scored, scoredMemory{memory: memory, score: score, index: i})
		}
	}
	if len(scored) == 0 {
		return memories[:3]
	}
	for i := 1; i < len(scored); i++ {
		for j := i; j > 0 && betterMemory(scored[j], scored[j-1]); j-- {
			scored[j], scored[j-1] = scored[j-1], scored[j]
		}
	}
	limit := min(5, len(scored))
	selected := make([]session.Memory, 0, limit)
	for _, item := range scored[:limit] {
		selected = append(selected, item.memory)
	}
	return selected
}

func automaticMemories(text string, memories []session.Memory) []session.Memory {
	if len(memories) == 0 || wantsAllMemory(text) {
		return nil
	}
	queryTerms := significantTerms(text)
	if len(queryTerms) == 0 {
		return nil
	}
	type scoredMemory struct {
		memory session.Memory
		score  int
		index  int
	}
	scored := make([]scoredMemory, 0, len(memories))
	for i, memory := range memories {
		score := memoryScore(queryTerms, memory.Content)
		if score > 0 {
			scored = append(scored, scoredMemory{memory: memory, score: score, index: i})
		}
	}
	if len(scored) == 0 {
		return nil
	}
	for i := 1; i < len(scored); i++ {
		for j := i; j > 0 && betterMemory(scored[j], scored[j-1]); j-- {
			scored[j], scored[j-1] = scored[j-1], scored[j]
		}
	}
	limit := min(3, len(scored))
	selected := make([]session.Memory, 0, limit)
	for _, item := range scored[:limit] {
		selected = append(selected, item.memory)
	}
	return selected
}

func betterMemory(a, b struct {
	memory session.Memory
	score  int
	index  int
}) bool {
	if a.score != b.score {
		return a.score > b.score
	}
	return a.index < b.index
}

func wantsAllMemory(text string) bool {
	fields := strings.Fields(strings.ToLower(text))
	for i, field := range fields {
		field = strings.Trim(field, " .,;:!?()[]{}<>\"'")
		if canonicalSkillName(strings.TrimPrefix(field, "$")) != "memory" {
			continue
		}
		if i+1 < len(fields) && strings.Trim(fields[i+1], " .,;:!?()[]{}<>\"'") == "all" {
			return true
		}
	}
	return false
}

func memoryScore(queryTerms map[string]struct{}, memory string) int {
	score := 0
	for _, term := range strings.FieldsFunc(strings.ToLower(memory), termSeparator) {
		if _, ok := queryTerms[term]; ok {
			score++
		}
	}
	return score
}

func significantTerms(text string) map[string]struct{} {
	terms := map[string]struct{}{}
	for _, term := range strings.FieldsFunc(strings.ToLower(text), termSeparator) {
		term = strings.TrimPrefix(term, "$")
		if len(term) < 3 || stopWord(term) {
			continue
		}
		terms[term] = struct{}{}
	}
	return terms
}

func termSeparator(r rune) bool {
	return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '$')
}

func stopWord(term string) bool {
	switch term {
	case "the", "and", "for", "with", "that", "this", "you", "your", "are", "was", "were", "use", "using", "memory", "memories", "please":
		return true
	default:
		return false
	}
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
		name = canonicalSkillName(name)
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names
}

func canonicalSkillName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	switch name {
	case "memories":
		return "memory"
	case "skill-dictionary":
		return "skills"
	case "browser":
		return "agent-browser"
	case "speech-to-text", "transcribe":
		return "stt"
	case "text-to-speech", "speak":
		return "tts"
	default:
		return name
	}
}

func wantsTTS(text string) bool {
	for _, name := range skillNames(text) {
		if name == "tts" {
			return true
		}
	}
	return false
}

func (r *Router) shouldSynthesizeReply(text string, attachments []media.Attachment) bool {
	if wantsTTS(text) {
		return true
	}
	if !r.cfg.Speech.TTS.AutoForAudio {
		return false
	}
	for _, attachment := range attachments {
		if attachment.Kind == "audio" {
			return true
		}
	}
	return false
}

func formatToolEvent(event codexapp.ToolEvent) string {
	label := strings.TrimSpace(event.Label)
	if label == "" {
		label = humanToolName(event.Type)
	}
	genericLabel := isUninformativeToolLabel(event.Type, label)
	contextText := strings.TrimSpace(event.Context)
	if contextText == "" {
		contextText = defaultToolContext(event.Type, label)
	}
	switch event.Phase {
	case "started":
		if isCommandTool(event.Type) {
			text := "Running command"
			if contextText != "" {
				text += "\nContext: " + contextText
			}
			if label != "" && !genericLabel {
				text += "\nCommand:\n" + label
			}
			if event.Details != "" {
				text += "\n" + event.Details
			}
			return text
		}
		text := toolStartedText(event.Type, label)
		if contextText != "" {
			text += "\nContext: " + contextText
		}
		if event.Details != "" {
			text += "\n" + event.Details
		}
		return text
	case "completed":
		prefix := "Finished " + humanToolName(event.Type)
		if isCommandTool(event.Type) {
			if event.Status == "failed" {
				prefix = "Command failed"
			} else {
				prefix = "Command finished"
			}
		}
		if event.Status != "" {
			prefix += "\nStatus: " + humanToolStatus(event.Status)
		}
		if contextText != "" {
			prefix += "\nContext: " + contextText
		}
		if label != "" && !genericLabel {
			if isCommandTool(event.Type) {
				prefix += "\nCommand:\n" + label
			} else {
				prefix += ":\n" + label
			}
		}
		if event.Details != "" {
			prefix += "\n" + event.Details
		}
		return prefix
	default:
		text := humanToolName(event.Type) + ": " + label
		if event.Details != "" {
			text += "\n" + event.Details
		}
		return text
	}
}

func toolStartedText(toolType string, label string) string {
	switch toolType {
	case "web_search", "webSearch":
		if !isUninformativeToolLabel(toolType, label) {
			return "Searching the web:\n" + label
		}
		return "Searching the web"
	case "file_change", "fileChange":
		return "Updating files"
	case "mcp_tool_call", "mcpToolCall":
		if !isUninformativeToolLabel(toolType, label) {
			return "Calling MCP tool:\n" + label
		}
		return "Calling MCP tool"
	case "todo_list":
		return "Updating todo list"
	default:
		if isUninformativeToolLabel(toolType, label) {
			return "Using " + humanToolName(toolType)
		}
		return "Using " + humanToolName(toolType) + ":\n" + label
	}
}

func humanToolStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "success", "succeeded":
		return "success"
	case "failed", "error":
		return "failed"
	case "in_progress", "running":
		return "running"
	default:
		return status
	}
}

func defaultToolContext(toolType string, label string) string {
	switch toolType {
	case "command_execution", "commandExecution":
		if isUninformativeToolLabel(toolType, label) {
			return "Codex is running a shell command; command details were not provided yet"
		}
		return "running a shell command for this request"
	case "web_search", "webSearch":
		return "looking up external information"
	case "file_change", "fileChange":
		return "applying file changes"
	case "mcp_tool_call", "mcpToolCall":
		if !isUninformativeToolLabel(toolType, label) {
			return "calling " + label
		}
		return "calling a connected tool"
	case "todo_list":
		return "updating the task plan"
	default:
		return ""
	}
}

func isCommandTool(toolType string) bool {
	switch toolType {
	case "command_execution", "commandExecution":
		return true
	default:
		return false
	}
}

func isUninformativeToolLabel(toolType string, label string) bool {
	toolType = strings.TrimSpace(toolType)
	label = strings.TrimSpace(label)
	if label == "" {
		return true
	}
	normalizedLabel := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(label, "_", ""), " ", ""))
	normalizedType := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(toolType, "_", ""), " ", ""))
	normalizedHuman := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(humanToolName(toolType), "_", ""), " ", ""))
	return normalizedLabel == normalizedType || normalizedLabel == normalizedHuman
}

func humanToolName(toolType string) string {
	switch toolType {
	case "command_execution", "commandExecution":
		return "shell command"
	case "file_change", "fileChange":
		return "file change"
	case "mcp_tool_call", "mcpToolCall":
		return "MCP tool"
	case "web_search", "webSearch":
		return "web search"
	case "todo_list":
		return "todo list"
	default:
		toolType = strings.ReplaceAll(toolType, "_", "")
		if toolType == "" {
			return "tool"
		}
		return toolType
	}
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
			_ = replyText(ctx, reply, "Could not save memory: "+err.Error())
			return true, err
		}
		return true, replyText(ctx, reply, fmt.Sprintf("Remembered %d: %s", memory.ID, memory.Content))
	case "/memory":
		return true, r.replyMemories(ctx, scopeKey, reply)
	case "/forget":
		return true, r.forgetMemory(ctx, scopeKey, arg, reply)
	case "/skills":
		return true, r.replySkills(ctx, reply)
	case "/browser":
		return true, replyText(ctx, reply, r.browserStatusText(ctx))
	case "/speech":
		return true, replyText(ctx, reply, r.speechStatusText())
	case "/new":
		session, err := r.createSession(ctx, scopeKey, arg)
		if err != nil {
			_ = replyText(ctx, reply, "Could not create session: "+err.Error())
			return true, err
		}
		return true, replyText(ctx, reply, fmt.Sprintf("Started session %d: %s", session.ID, session.Name))
	case "/status":
		active, err := r.activeSession(ctx, scopeKey)
		if err != nil {
			_ = replyText(ctx, reply, "Could not read status: "+err.Error())
			return true, err
		}
		return true, replyText(ctx, reply, statusText(active, r.cfg))
	case "/reasoning":
		active, err := r.activeSession(ctx, scopeKey)
		if err != nil {
			_ = replyText(ctx, reply, "Could not read session: "+err.Error())
			return true, err
		}
		effort, global := parseReasoningArg(arg)
		if effort == "" {
			return true, replyText(ctx, reply, reasoningText(active))
		}
		if !validReasoningEffort(effort) {
			return true, replyText(ctx, reply, "Usage: /reasoning <low|medium|high|xhigh|default> [--global]")
		}
		if global {
			if err := r.updateGlobalReasoning(effort); err != nil {
				_ = replyText(ctx, reply, "Could not update global reasoning: "+err.Error())
				return true, err
			}
			if effort == "default" {
				r.cfg.Codex.Effort = ""
				return true, replyText(ctx, reply, "Global reasoning reset to Codex default.")
			}
			r.cfg.Codex.Effort = effort
			return true, replyText(ctx, reply, "Global reasoning set to "+effort+".")
		}
		if effort == "default" {
			effort = ""
		}
		if err := r.sessions.UpdateReasoning(ctx, active.ID, effort); err != nil {
			_ = replyText(ctx, reply, "Could not update reasoning: "+err.Error())
			return true, err
		}
		if effort == "" {
			return true, replyText(ctx, reply, "Reasoning reset to config default.")
		}
		return true, replyText(ctx, reply, "Reasoning set to "+effort+".")
	case "/model":
		active, err := r.activeSession(ctx, scopeKey)
		if err != nil {
			_ = replyText(ctx, reply, "Could not read session: "+err.Error())
			return true, err
		}
		model, global := parseModelArg(arg)
		if model == "" {
			return true, replyText(ctx, reply, modelText(active, r.cfg))
		}
		if global {
			if err := r.updateGlobalModel(model); err != nil {
				_ = replyText(ctx, reply, "Could not update global model: "+err.Error())
				return true, err
			}
			if model == "default" {
				r.cfg.Codex.Model = ""
				return true, replyText(ctx, reply, "Global model reset to Codex default.")
			}
			r.cfg.Codex.Model = model
			return true, replyText(ctx, reply, "Global model set to "+model+".")
		}
		if model == "default" {
			model = ""
		}
		if err := r.sessions.UpdateModel(ctx, active.ID, model); err != nil {
			_ = replyText(ctx, reply, "Could not update model: "+err.Error())
			return true, err
		}
		if model == "" {
			return true, replyText(ctx, reply, "Model reset to config default.")
		}
		return true, replyText(ctx, reply, "Model set to "+model+".")
	case "/session":
		if strings.TrimSpace(arg) == "" {
			return true, r.replySessionList(ctx, scopeKey, reply)
		}
		session, err := r.sessions.Find(ctx, scopeKey, arg)
		if err != nil {
			_ = replyText(ctx, reply, "Could not switch session: "+err.Error())
			return true, err
		}
		if !r.cfg.Sessions.MinimalContext() {
			if err := r.ensureThreadLoaded(ctx, session.ThreadID); err != nil {
				_ = replyText(ctx, reply, "Could not resume session: "+err.Error())
				return true, err
			}
		}
		if err := r.sessions.SetActive(ctx, scopeKey, session.ID); err != nil {
			_ = replyText(ctx, reply, "Could not switch session: "+err.Error())
			return true, err
		}
		return true, replyText(ctx, reply, fmt.Sprintf("Switched to session %d: %s", session.ID, session.Name))
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
	if !r.cfg.Sessions.MinimalContext() {
		if err := r.ensureThreadLoaded(ctx, active.ThreadID); err != nil {
			return session.Session{}, err
		}
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
		_ = replyText(ctx, reply, "Could not list sessions: "+err.Error())
		return err
	}
	if len(sessions) == 0 {
		return replyText(ctx, reply, "No sessions yet. Use /new [name] to create one.")
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
	return replyText(ctx, reply, strings.TrimSpace(builder.String()))
}

func (r *Router) replyMemories(ctx context.Context, scopeKey string, reply ReplyFunc) error {
	memories, err := r.sessions.ListMemories(ctx, scopeKey)
	if err != nil {
		_ = replyText(ctx, reply, "Could not list memory: "+err.Error())
		return err
	}
	if len(memories) == 0 {
		return replyText(ctx, reply, "No memory stored. Use /remember <text> to save one.")
	}
	var builder strings.Builder
	builder.WriteString("Memory:\n")
	for _, memory := range memories {
		builder.WriteString(fmt.Sprintf("%d %s\n", memory.ID, memory.Content))
	}
	builder.WriteString("Use /forget <id> or /forget all.")
	return replyText(ctx, reply, strings.TrimSpace(builder.String()))
}

func (r *Router) forgetMemory(ctx context.Context, scopeKey string, arg string, reply ReplyFunc) error {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return replyText(ctx, reply, "Usage: /forget <id|all>")
	}
	if strings.EqualFold(arg, "all") {
		if err := r.sessions.ClearMemories(ctx, scopeKey); err != nil {
			_ = replyText(ctx, reply, "Could not clear memory: "+err.Error())
			return err
		}
		return replyText(ctx, reply, "Memory cleared.")
	}
	id, err := strconv.ParseInt(arg, 10, 64)
	if err != nil {
		return replyText(ctx, reply, "Usage: /forget <id|all>")
	}
	deleted, err := r.sessions.DeleteMemory(ctx, scopeKey, id)
	if err != nil {
		_ = replyText(ctx, reply, "Could not forget memory: "+err.Error())
		return err
	}
	if !deleted {
		return replyText(ctx, reply, "Memory not found.")
	}
	return replyText(ctx, reply, "Memory deleted.")
}

func (r *Router) replySkills(ctx context.Context, reply ReplyFunc) error {
	skills, err := r.gateway.ListSkills(ctx)
	var builder strings.Builder
	builder.WriteString("Skills:\n")
	for _, skill := range builtInSkills() {
		builder.WriteString("$")
		builder.WriteString(skill.name)
		builder.WriteString(" - ")
		builder.WriteString(skill.description)
		builder.WriteString("\n")
	}
	if err != nil {
		builder.WriteString("Codex skills unavailable: ")
		builder.WriteString(err.Error())
		builder.WriteString("\n")
		return replyText(ctx, reply, strings.TrimSpace(builder.String()))
	}
	if len(skills) == 0 {
		builder.WriteString("No Codex skills found.\n")
		return replyText(ctx, reply, strings.TrimSpace(builder.String()))
	}
	for _, skill := range skills {
		builder.WriteString("$" + skill.Name)
		if skill.Path != "" {
			builder.WriteString(" - " + skill.Path)
		}
		builder.WriteString("\n")
	}
	return replyText(ctx, reply, strings.TrimSpace(builder.String()))
}

func (r *Router) browserStatusText(ctx context.Context) string {
	cfg := r.cfg.AgentBrowser
	command := strings.TrimSpace(cfg.Command)
	if command == "" {
		command = "agent-browser"
	}
	status := "disabled"
	if cfg.Enabled {
		status = "enabled"
	}
	autoInject := "off"
	if cfg.AutoInject {
		autoInject = "on"
	}
	session := strings.TrimSpace(cfg.Session)
	if session == "" {
		session = "codexclaw"
	}
	var builder strings.Builder
	builder.WriteString("Agent browser: " + status + "\n")
	builder.WriteString("Command: " + command + "\n")
	builder.WriteString("Auto-inject: " + autoInject + "\n")
	builder.WriteString("Session: " + session + "\n")
	if path, err := exec.LookPath(command); err == nil {
		builder.WriteString("Installed: yes (" + path + ")\n")
		if version := agentBrowserVersion(ctx, command); version != "" {
			builder.WriteString("Version: " + version + "\n")
		}
	} else {
		builder.WriteString("Installed: not found on PATH\n")
	}
	builder.WriteString("Use $agent-browser or $browser to force browser automation guidance. Install/repair with `mise run browser:install` and `mise run browser:doctor`.")
	return strings.TrimSpace(builder.String())
}

func (r *Router) speechStatusText() string {
	stt := "disabled"
	if r.speech.STTEnabled() {
		stt = "enabled"
	}
	tts := "disabled"
	if r.speech.TTSEnabled() {
		tts = "enabled"
	}
	return fmt.Sprintf("Speech\nSTT: %s\nTTS: %s\nTimeout: %s\nUse $stt with voice/audio input and $tts when you want the final answer sent back as audio.", stt, tts, r.cfg.Speech.Timeout())
}

func agentBrowserVersion(ctx context.Context, command string) string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, command, "--version").CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func (r *Router) autoCompact(ctx context.Context, active session.Session, reply ReplyFunc) error {
	if !r.cfg.Sessions.AutoCompact || r.cfg.Sessions.AutoCompactAfterTokens <= 0 || active.TotalTokens <= 0 {
		return nil
	}
	if active.TotalTokens-active.LastCompactedTotalTokens < r.cfg.Sessions.AutoCompactAfterTokens {
		return nil
	}
	if err := r.gateway.CompactThread(ctx, active.ThreadID); err != nil {
		if errors.Is(err, codexapp.ErrCompactUnsupported) {
			_ = r.sessions.MarkCompacted(ctx, active.ID, active.TotalTokens)
			_ = replyText(ctx, reply, "Auto-compaction skipped: the Codex app-server backend does not expose explicit compaction.")
			return nil
		}
		_ = replyText(ctx, reply, "Auto-compaction failed: "+err.Error())
		return err
	}
	_ = r.sessions.MarkCompacted(ctx, active.ID, active.TotalTokens)
	_ = replyText(ctx, reply, fmt.Sprintf("Auto-compaction started at %d tokens.", active.TotalTokens))
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
	contextMode := cfg.Sessions.ContextMode
	if contextMode == "" {
		contextMode = "minimal"
	}
	model := active.Model
	if model == "" {
		model = "default"
		if cfg.Codex.Model != "" {
			model = cfg.Codex.Model + " (default)"
		}
	}
	lastTurn := "unknown"
	if active.LastTotalTokens > 0 {
		lastTurn = fmt.Sprintf("%d total (%d input, %d output)", active.LastTotalTokens, active.LastInputTokens, active.LastOutputTokens)
	}
	tokenLabel := "Current context"
	return fmt.Sprintf("Session %d: %s\nThread: %s\nContext: %s\nModel: %s\nReasoning: %s\n%s: %d total (%d input, %d output)\nLast turn: %s\nAuto-compaction: %s", active.ID, active.Name, active.ThreadID, contextMode, model, reasoning, tokenLabel, active.TotalTokens, active.InputTokens, active.OutputTokens, lastTurn, compact)
}

func mergeTokenUsage(active session.Session, result codexapp.TurnResult, minimalContext bool) session.Session {
	last := result.LastTurnUsage
	if last.TotalTokens == 0 {
		last = result.TokenUsage
	}
	active.LastInputTokens = last.InputTokens
	active.LastOutputTokens = last.OutputTokens
	active.LastTotalTokens = last.TotalTokens

	active.InputTokens = last.InputTokens
	active.OutputTokens = last.OutputTokens
	active.TotalTokens = last.TotalTokens
	return active
}

func parseReasoningArg(arg string) (string, bool) {
	value, global := parseScopedValueArg(arg)
	return strings.ToLower(value), global
}

func parseModelArg(arg string) (string, bool) {
	value, global := parseScopedValueArg(arg)
	if strings.EqualFold(value, "default") {
		value = "default"
	}
	return value, global
}

func parseScopedValueArg(arg string) (string, bool) {
	fields := strings.Fields(arg)
	global := false
	var value string
	for _, field := range fields {
		switch normalizedFlag(field) {
		case "--global", "-global":
			global = true
		default:
			if value == "" {
				value = strings.TrimSpace(field)
			}
		}
	}
	return value, global
}

func normalizedFlag(field string) string {
	field = strings.ToLower(strings.TrimSpace(field))
	field = strings.TrimPrefix(field, "—")
	field = strings.TrimPrefix(field, "–")
	if strings.HasPrefix(field, "global") {
		return "--global"
	}
	return field
}

func (r *Router) updateGlobalReasoning(effort string) error {
	return r.updateGlobalCodexString("effort", effort)
}

func (r *Router) updateGlobalModel(model string) error {
	return r.updateGlobalCodexString("model", model)
}

func (r *Router) updateGlobalCodexString(key string, value string) error {
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
	if value == "default" {
		delete(codex, key)
	} else {
		codex[key] = value
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

func modelText(active session.Session, cfg config.Config) string {
	if active.Model == "" {
		if cfg.Codex.Model != "" {
			return "Model: " + cfg.Codex.Model + " (default). Use /model <model-name> to switch."
		}
		return "Model: default. Use /model <model-name> to switch."
	}
	return "Model: " + active.Model + ". Use /model default to reset."
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
	if cmd != "/new" && cmd != "/session" && cmd != "/status" && cmd != "/reasoning" && cmd != "/model" && cmd != "/remember" && cmd != "/memory" && cmd != "/forget" && cmd != "/skills" && cmd != "/browser" {
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
		if entry == "" {
			continue
		}
		for _, variant := range allowlistEntryVariants(entry) {
			out[variant] = struct{}{}
		}
	}
	return out
}

func allowlistEntryVariants(entry string) []string {
	source, id, ok := strings.Cut(strings.TrimSpace(entry), ":")
	if !ok {
		return []string{entry}
	}
	source = strings.ToLower(strings.TrimSpace(source))
	id = strings.TrimSpace(id)
	if source != "whatsapp" {
		return []string{source + ":" + id}
	}
	var keys []string
	for _, variant := range whatsappIDVariants(id) {
		keys = append(keys, source+":"+variant)
	}
	return dedupe(keys)
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
	if strings.EqualFold(identity.Source, "whatsapp") {
		for _, key := range append([]string{senderKey}, identity.AllowKeys...) {
			source, id, ok := strings.Cut(key, ":")
			if !ok || !strings.EqualFold(source, "whatsapp") {
				continue
			}
			for _, variant := range whatsappIDVariants(id) {
				keys = append(keys, "whatsapp:"+variant)
			}
		}
	}
	identity.AllowKeys = dedupe(keys)
	return identity
}

func whatsappIDVariants(id string) []string {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		return nil
	}
	var variants []string
	add := func(value string) {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			variants = append(variants, value)
		}
	}
	add(id)
	user, server, hasServer := strings.Cut(id, "@")
	if hasServer {
		add(user)
		if base := whatsappBaseUser(user); base != user {
			add(base)
			add(base + "@" + server)
		}
		return dedupe(variants)
	}
	if base := whatsappBaseUser(id); base != id {
		add(base)
		add(base + "@s.whatsapp.net")
	}
	return dedupe(variants)
}

func whatsappBaseUser(user string) string {
	if before, _, ok := strings.Cut(user, ":"); ok && before != "" {
		return before
	}
	return user
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

func redactedKeys(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		source, id, ok := strings.Cut(key, ":")
		if !ok {
			out = append(out, redactedID(key))
			continue
		}
		out = append(out, source+":"+redactedID(id))
	}
	return strings.Join(out, ",")
}

func redactedID(id string) string {
	if len(id) <= 4 {
		return id
	}
	return "..." + id[len(id)-4:]
}
