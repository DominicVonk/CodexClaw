package codexapp

import (
	"context"
	"strings"
	"testing"

	"github.com/DominicVonk/CodexClaw/internal/config"
)

func TestExecArgsStartAndResume(t *testing.T) {
	g := &Gateway{cfg: config.CodexConfig{
		Command:           "codex",
		CWD:               "/repo",
		Model:             "gpt-5.3-codex",
		Effort:            "high",
		ApprovalPolicy:    "never",
		PermissionProfile: ":workspace",
	}}

	startArgs := strings.Join(g.execArgs("new-abc", "", "", []string{"a.png"}), " ")
	for _, want := range []string{"exec --json", "--model gpt-5.3-codex", "--sandbox workspace-write", "--cd /repo", "--image a.png"} {
		if !strings.Contains(startArgs, want) {
			t.Fatalf("expected start args to contain %q, got %q", want, startArgs)
		}
	}
	if strings.Contains(startArgs, "resume") {
		t.Fatalf("new thread args should not include resume: %q", startArgs)
	}

	resumeArgs := strings.Join(g.execArgs("thread-1", "gpt-5.4", "low", nil), " ")
	for _, want := range []string{"exec --json", "--model gpt-5.4", "model_reasoning_effort=\"low\"", "resume thread-1"} {
		if !strings.Contains(resumeArgs, want) {
			t.Fatalf("expected resume args to contain %q, got %q", want, resumeArgs)
		}
	}
}

func TestReadEvents(t *testing.T) {
	stream := strings.NewReader(strings.Join([]string{
		`{"type":"thread.started","thread_id":"thread-1"}`,
		`{"type":"item.started","item":{"id":"cmd","type":"command_execution","command":"go test ./...","status":"in_progress","aggregated_output":""}}`,
		`{"type":"item.completed","item":{"id":"msg","type":"agent_message","text":"done"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":2,"output_tokens":5,"reasoning_output_tokens":1}}`,
	}, "\n"))
	var tools []ToolEvent
	result, err := readEvents(context.Background(), stream, "new-1", func(event ToolEvent) {
		tools = append(tools, event)
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ThreadID != "thread-1" {
		t.Fatalf("expected thread id thread-1, got %q", result.ThreadID)
	}
	if result.Text != "done" {
		t.Fatalf("expected final response done, got %q", result.Text)
	}
	if result.TokenUsage.TotalTokens != 15 {
		t.Fatalf("expected 15 total tokens, got %d", result.TokenUsage.TotalTokens)
	}
	if result.TokenUsage.Cumulative {
		t.Fatalf("turn.completed usage should be treated as last-turn usage")
	}
	if result.LastTurnUsage.TotalTokens != 15 {
		t.Fatalf("expected 15 last-turn tokens, got %d", result.LastTurnUsage.TotalTokens)
	}
	if len(tools) != 1 || tools[0].Label != "go test ./..." {
		t.Fatalf("expected command tool event, got %#v", tools)
	}
}

func TestReadEventsTokenCountUsesCodexTotals(t *testing.T) {
	stream := strings.NewReader(strings.Join([]string{
		`{"type":"thread.started","thread_id":"thread-1"}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":30,"output_tokens":20,"reasoning_output_tokens":4,"total_tokens":120},"last_token_usage":{"input_tokens":40,"cached_input_tokens":10,"output_tokens":5,"reasoning_output_tokens":1,"total_tokens":45}}}}`,
		`{"type":"item.completed","item":{"id":"msg","type":"agent_message","text":"done"}}`,
	}, "\n"))
	result, err := readEvents(context.Background(), stream, "new-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.TokenUsage.Cumulative || result.TokenUsage.TotalTokens != 120 {
		t.Fatalf("expected cumulative total 120, got %#v", result.TokenUsage)
	}
	if result.LastTurnUsage.TotalTokens != 45 {
		t.Fatalf("expected last-turn total 45, got %#v", result.LastTurnUsage)
	}
	if result.TokenUsage.CachedInputTokens != 30 {
		t.Fatalf("expected cached input tokens to be preserved, got %d", result.TokenUsage.CachedInputTokens)
	}
}

func TestNormalizeInput(t *testing.T) {
	prompt, images := normalizeInput([]InputPart{
		{Type: "text", Text: "hello"},
		{Type: "localImage", Path: "a.png"},
		{Type: "skill", Name: "memory"},
	})
	if !strings.Contains(prompt, "hello") || !strings.Contains(prompt, "$memory") {
		t.Fatalf("unexpected prompt %q", prompt)
	}
	if len(images) != 1 || images[0] != "a.png" {
		t.Fatalf("unexpected images %#v", images)
	}
}
