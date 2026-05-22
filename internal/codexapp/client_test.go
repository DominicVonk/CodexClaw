package codexapp

import (
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
