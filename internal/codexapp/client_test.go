package codexapp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/bazelment/yoloswe/agent-cli-wrapper/codex"
)

func TestNormalizeInput(t *testing.T) {
	input := normalizeInput([]InputPart{
		{Type: "text", Text: "hello"},
		{Type: "localImage", Path: "a.png"},
		{Type: "skill", Name: "memory"},
	})
	if len(input) != 1 || input[0].Type != "text" {
		t.Fatalf("unexpected input %#v", input)
	}
	for _, want := range []string{"hello", "Attached local image: a.png", "$memory"} {
		if !strings.Contains(input[0].Text, want) {
			t.Fatalf("expected prompt to contain %q, got %q", want, input[0].Text)
		}
	}
}

func TestUsageFromTokenPreservesCumulativeFlag(t *testing.T) {
	usage := usageFromToken(&sdk.TokenUsage{
		InputTokens:           10,
		CachedInputTokens:     4,
		OutputTokens:          5,
		ReasoningOutputTokens: 2,
		TotalTokens:           15,
	}, true)
	if !usage.Cumulative || usage.TotalTokens != 15 || usage.CachedInputTokens != 4 {
		t.Fatalf("unexpected usage %#v", usage)
	}
}

func TestUsageFromTurnFallsBackToInputPlusOutput(t *testing.T) {
	usage := usageFromTurn(sdk.TurnUsage{InputTokens: 10, OutputTokens: 5}, false)
	if usage.Cumulative || usage.TotalTokens != 15 {
		t.Fatalf("unexpected usage %#v", usage)
	}
}

func TestFinalizeUsageMarksFreshThreadAsNonCumulative(t *testing.T) {
	result := finalizeUsage(TurnResult{
		TokenUsage:    TokenUsage{InputTokens: 100, OutputTokens: 20, TotalTokens: 120, Cumulative: true},
		LastTurnUsage: TokenUsage{InputTokens: 40, OutputTokens: 5, TotalTokens: 45},
	}, true)
	if result.TokenUsage.Cumulative || result.TokenUsage.TotalTokens != 45 {
		t.Fatalf("expected fresh thread usage to use non-cumulative last turn, got %#v", result.TokenUsage)
	}
}

func TestCommandContextDescribesCommonCommands(t *testing.T) {
	tests := map[string]string{
		"which wacli":        "checking command availability or version",
		"rg token":           "searching repository text",
		"go test ./...":      "running Go tests",
		"git status --short": "checking repository state",
	}
	for command, want := range tests {
		if got := commandContext(command); got != want {
			t.Fatalf("commandContext(%q) = %q, want %q", command, got, want)
		}
	}
}

func TestSkillRootsIncludeSkillsShAgentDirectory(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("home directory unavailable")
	}
	roots := skillRoots("")
	want := filepath.Join(home, ".agents", "skills")
	for _, root := range roots {
		if root == want {
			return
		}
	}
	t.Fatalf("expected skill roots to include %q, got %#v", want, roots)
}
