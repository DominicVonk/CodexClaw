package codexapp

import (
	"strings"
	"testing"
)

func TestNormalizeInput(t *testing.T) {
	input := normalizeInput([]InputPart{
		{Type: "text", Text: "hello"},
		{Type: "localImage", Path: "a.png"},
		{Type: "skill", Name: "memory"},
	})
	if len(input) != 2 || input[0].Type != "text" || input[1].Type != "local_image" {
		t.Fatalf("unexpected input %#v", input)
	}
	for _, want := range []string{"hello", "$memory"} {
		if !strings.Contains(input[0].Text, want) {
			t.Fatalf("expected prompt to contain %q, got %q", want, input[0].Text)
		}
	}
	if input[1].Path != "a.png" {
		t.Fatalf("expected image path to be preserved, got %#v", input[1])
	}
}

func TestUsageFromSDKPreservesCumulativeFlag(t *testing.T) {
	usage := TokenUsage{
		InputTokens:           10,
		CachedInputTokens:     4,
		OutputTokens:          5,
		ReasoningOutputTokens: 2,
		TotalTokens:           15,
		Cumulative:            true,
	}
	if !usage.Cumulative || usage.TotalTokens != 15 || usage.CachedInputTokens != 4 {
		t.Fatalf("unexpected usage %#v", usage)
	}
}

func TestTotalTokensFallsBackToInputPlusOutput(t *testing.T) {
	usage := TokenUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: totalTokens(10, 5, 0)}
	if usage.Cumulative || usage.TotalTokens != 15 {
		t.Fatalf("unexpected usage %#v", usage)
	}
}
