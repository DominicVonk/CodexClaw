//go:build !windows

package codexapp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFillUsageFromSessionLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	data := []byte(`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":30,"output_tokens":20,"reasoning_output_tokens":4,"total_tokens":120},"last_token_usage":{"input_tokens":40,"cached_input_tokens":10,"output_tokens":5,"reasoning_output_tokens":1,"total_tokens":45}}}}` + "\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var result TurnResult
	if !fillUsageFromSessionLog(path, &result) {
		t.Fatal("expected usage to be found")
	}
	if !result.TokenUsage.Cumulative || result.TokenUsage.TotalTokens != 120 || result.TokenUsage.CachedInputTokens != 30 {
		t.Fatalf("unexpected total usage %#v", result.TokenUsage)
	}
	if result.LastTurnUsage.Cumulative || result.LastTurnUsage.TotalTokens != 45 || result.LastTurnUsage.InputTokens != 40 {
		t.Fatalf("unexpected last usage %#v", result.LastTurnUsage)
	}
}
