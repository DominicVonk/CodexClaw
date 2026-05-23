package router

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DominicVonk/CodexClaw/internal/codexapp"
	"github.com/DominicVonk/CodexClaw/internal/config"
	"github.com/DominicVonk/CodexClaw/internal/session"
)

func TestCodexInputDoesNotInjectMemoryWithoutMemorySkill(t *testing.T) {
	rt := &Router{}
	parts, err := rt.codexInput(context.Background(), "hello", nil, []session.Memory{
		{ID: 1, Content: "secret preference"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 {
		t.Fatalf("expected only text input, got %d parts", len(parts))
	}
	if strings.Contains(parts[0].Text, "secret preference") {
		t.Fatalf("memory should be opt-in, got text:\n%s", parts[0].Text)
	}
}

func TestCodexInputInjectsRelevantMemoryAutomatically(t *testing.T) {
	rt := &Router{}
	parts, err := rt.codexInput(context.Background(), "How do telegram threads work?", nil, []session.Memory{
		{ID: 1, Content: "Telegram uses forum topic threads."},
		{ID: 2, Content: "WhatsApp requires QR auth."},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 {
		t.Fatalf("expected only text input, got %d parts", len(parts))
	}
	if !strings.Contains(parts[0].Text, "Telegram uses forum topic threads.") {
		t.Fatalf("expected relevant memory, got text:\n%s", parts[0].Text)
	}
	if strings.Contains(parts[0].Text, "WhatsApp requires QR auth.") {
		t.Fatalf("expected irrelevant memory to stay out, got text:\n%s", parts[0].Text)
	}
}

func TestSkillNamesCanonicalizeBuiltInAliases(t *testing.T) {
	got := skillNames("Use $Memories, $skill-dictionary, $browser and $skill-creator please")
	want := []string{"memory", "skills", "agent-browser", "skill-creator"}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}
}

func TestCodexInputAutoInjectsAgentBrowserForURLs(t *testing.T) {
	rt := &Router{cfg: config.Config{AgentBrowser: config.AgentBrowserConfig{Enabled: true, AutoInject: true, Command: "agent-browser", Session: "codexclaw", MaxOutput: 12000}}}
	parts, err := rt.codexInput(context.Background(), "Open https://example.com and take a screenshot", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 {
		t.Fatalf("expected text plus agent-browser guidance, got %d parts", len(parts))
	}
	if !strings.Contains(parts[1].Text, "Agent browser skill") || !strings.Contains(parts[1].Text, "snapshot -i") {
		t.Fatalf("expected agent-browser guidance, got:\n%s", parts[1].Text)
	}
}

func TestMemorySkillTextIncludesIDsAndCommands(t *testing.T) {
	text := memorySkillText([]session.Memory{
		{ID: 7, Content: "Prefer concise answers."},
	}, 1)
	for _, want := range []string{
		"7: Prefer concise answers.",
		"/remember <text>",
		"/forget <id|all>",
		"/memory",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected memory skill text to contain %q, got:\n%s", want, text)
		}
	}
}

func TestSelectMemoriesPrefersRelevantSubset(t *testing.T) {
	memories := []session.Memory{
		{ID: 1, Content: "Use terse replies."},
		{ID: 2, Content: "Deploy production with pm2."},
		{ID: 3, Content: "Telegram uses forum topic threads."},
		{ID: 4, Content: "WhatsApp requires QR auth."},
		{ID: 5, Content: "Use sqlite without cgo."},
		{ID: 6, Content: "The preferred model is gpt-5.3-codex."},
	}
	selected := selectMemories("Use $memory for telegram thread behavior", memories)
	if len(selected) != 1 || selected[0].ID != 3 {
		t.Fatalf("expected only telegram memory, got %#v", selected)
	}
}

func TestSelectMemoriesAllKeepsEveryMemory(t *testing.T) {
	memories := []session.Memory{
		{ID: 1, Content: "one"},
		{ID: 2, Content: "two"},
		{ID: 3, Content: "three"},
		{ID: 4, Content: "four"},
		{ID: 5, Content: "five"},
		{ID: 6, Content: "six"},
	}
	selected := selectMemories("$memory all", memories)
	if len(selected) != len(memories) {
		t.Fatalf("expected all memories, got %d", len(selected))
	}
}

func TestSkillDictionaryIncludesBuiltInsAndAppSkillErrors(t *testing.T) {
	text := skillDictionaryText(nil, errors.New("offline"))
	for _, want := range []string{
		"$memory",
		"$skill-creator",
		"$agent-browser",
		"Codex skills unavailable: offline",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected dictionary to contain %q, got:\n%s", want, text)
		}
	}
}

func TestSkillDictionaryIncludesAppServerSkills(t *testing.T) {
	text := skillDictionaryText([]codexapp.Skill{{Name: "example", Path: "/tmp/example"}}, nil)
	for _, want := range []string{"$memory", "$example"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected dictionary to contain %q, got:\n%s", want, text)
		}
	}
	if strings.Contains(text, "/tmp/example") {
		t.Fatalf("dictionary should not include Codex skill paths, got:\n%s", text)
	}
}

func TestFormatToolEventCommandIncludesDetails(t *testing.T) {
	text := formatToolEvent(codexapp.ToolEvent{
		Phase:   "completed",
		Type:    "command_execution",
		Label:   "go test ./...",
		Context: "running Go tests",
		Status:  "completed",
		Details: "Exit: 0\nDuration: 1.2s\nOutput preview:\nok",
	})
	for _, want := range []string{"Command finished", "Status: success", "Context: running Go tests", "Command:\ngo test ./...", "Exit: 0", "Duration: 1.2s", "Output preview:\nok"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected formatted tool event to contain %q, got:\n%s", want, text)
		}
	}
}

func TestFormatToolEventStartedCommandIncludesCWD(t *testing.T) {
	text := formatToolEvent(codexapp.ToolEvent{
		Phase:   "started",
		Type:    "command_execution",
		Label:   "rg token",
		Context: "searching repository text",
		Details: "Directory: /repo",
	})
	for _, want := range []string{"Running command", "Context: searching repository text", "Command:\nrg token", "Directory: /repo"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected formatted tool event to contain %q, got:\n%s", want, text)
		}
	}
}

func TestFormatToolEventShowsGenericCommandItemsWithContext(t *testing.T) {
	text := formatToolEvent(codexapp.ToolEvent{
		Phase:  "started",
		Type:   "commandExecution",
		Label:  "commandExecution",
		Status: "in_progress",
	})
	if strings.Contains(text, "commandExecution") {
		t.Fatalf("expected generic commandExecution label to be hidden, got:\n%s", text)
	}
	for _, want := range []string{"Running command", "Context: Codex is running a shell command"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected formatted generic command event to contain %q, got:\n%s", want, text)
		}
	}
}

func TestMergeTokenUsagePrefersLastTurnOverCumulativeTotals(t *testing.T) {
	active := session.Session{InputTokens: 10, OutputTokens: 2, TotalTokens: 12}
	merged := mergeTokenUsage(active, codexapp.TurnResult{
		TokenUsage:    codexapp.TokenUsage{InputTokens: 100, OutputTokens: 20, TotalTokens: 120, Cumulative: true},
		LastTurnUsage: codexapp.TokenUsage{InputTokens: 40, OutputTokens: 5, TotalTokens: 45},
	}, false)
	if merged.TotalTokens != 45 || merged.InputTokens != 40 || merged.OutputTokens != 5 {
		t.Fatalf("expected last-turn context to replace stored totals, got %#v", merged)
	}
	if merged.LastTotalTokens != 45 || merged.LastInputTokens != 40 || merged.LastOutputTokens != 5 {
		t.Fatalf("expected last turn usage to be stored, got %#v", merged)
	}
}

func TestMergeTokenUsageFallsBackToTokenUsageWhenLastTurnMissing(t *testing.T) {
	active := session.Session{InputTokens: 10, OutputTokens: 2, TotalTokens: 12}
	merged := mergeTokenUsage(active, codexapp.TurnResult{
		TokenUsage: codexapp.TokenUsage{InputTokens: 100, OutputTokens: 20, TotalTokens: 120, Cumulative: true},
	}, false)
	if merged.TotalTokens != 120 || merged.InputTokens != 100 || merged.OutputTokens != 20 {
		t.Fatalf("expected token usage fallback to replace stored totals, got %#v", merged)
	}
}

func TestMergeTokenUsageReplacesTotalsInPersistentContext(t *testing.T) {
	active := session.Session{InputTokens: 10, OutputTokens: 2, TotalTokens: 12}
	merged := mergeTokenUsage(active, codexapp.TurnResult{
		TokenUsage:    codexapp.TokenUsage{InputTokens: 40, OutputTokens: 5, TotalTokens: 45},
		LastTurnUsage: codexapp.TokenUsage{InputTokens: 40, OutputTokens: 5, TotalTokens: 45},
	}, false)
	if merged.TotalTokens != 45 || merged.InputTokens != 40 || merged.OutputTokens != 5 {
		t.Fatalf("expected persistent context usage to replace stored totals, got %#v", merged)
	}
}

func TestMergeTokenUsageReplacesTotalsInMinimalContext(t *testing.T) {
	active := session.Session{InputTokens: 10, OutputTokens: 2, TotalTokens: 12}
	merged := mergeTokenUsage(active, codexapp.TurnResult{
		TokenUsage:    codexapp.TokenUsage{InputTokens: 40, OutputTokens: 5, TotalTokens: 45},
		LastTurnUsage: codexapp.TokenUsage{InputTokens: 40, OutputTokens: 5, TotalTokens: 45},
	}, true)
	if merged.TotalTokens != 45 || merged.InputTokens != 40 || merged.OutputTokens != 5 {
		t.Fatalf("expected minimal context usage to replace stored totals, got %#v", merged)
	}
}

func TestMergeTokenUsageReplacesMinimalContextThreadTotals(t *testing.T) {
	active := session.Session{InputTokens: 10, OutputTokens: 2, TotalTokens: 12}
	result := codexapp.TurnResult{
		TokenUsage:    codexapp.TokenUsage{InputTokens: 40, OutputTokens: 5, TotalTokens: 45, Cumulative: true},
		LastTurnUsage: codexapp.TokenUsage{InputTokens: 40, OutputTokens: 5, TotalTokens: 45},
	}
	result.TokenUsage.Cumulative = false
	merged := mergeTokenUsage(active, result, true)
	if merged.TotalTokens != 45 || merged.InputTokens != 40 || merged.OutputTokens != 5 {
		t.Fatalf("expected minimal-context thread total to replace stored totals, got %#v", merged)
	}
}

func TestWhatsAppAllowlistMatchesDeviceSuffixVariants(t *testing.T) {
	allowlist := buildAllowlist([]string{"whatsapp:4473462384593"})
	identity := normalizeIdentity(codexappIdentity("4473462384593:7@s.whatsapp.net"))
	for _, key := range identity.AllowKeys {
		if _, ok := allowlist[key]; ok {
			return
		}
	}
	t.Fatalf("expected allowlist to match sender variants; allowlist=%v keys=%v", allowlist, identity.AllowKeys)
}

func TestWhatsAppAllowlistEntryExpandsJIDVariants(t *testing.T) {
	allowlist := buildAllowlist([]string{"whatsapp:4473462384593:7@s.whatsapp.net"})
	for _, want := range []string{"whatsapp:4473462384593:7@s.whatsapp.net", "whatsapp:4473462384593:7", "whatsapp:4473462384593", "whatsapp:4473462384593@s.whatsapp.net"} {
		if _, ok := allowlist[want]; !ok {
			t.Fatalf("expected expanded allowlist key %q in %v", want, allowlist)
		}
	}
}

func codexappIdentity(sender string) Identity {
	return Identity{
		Source:    "whatsapp",
		ChatID:    "chat",
		SenderID:  sender,
		SessionID: "chat",
		AllowKeys: []string{"whatsapp:" + sender},
	}
}
