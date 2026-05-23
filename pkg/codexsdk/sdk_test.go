package codexsdk

import (
	"reflect"
	"testing"
)

func TestCommandArgsMatchesTypeScriptSDKShape(t *testing.T) {
	network := true
	webSearch := false
	exec := newExec(Options{
		BaseURL: "https://example.test",
		Config: ConfigObject{
			"show_raw_agent_reasoning": true,
			"sandbox_workspace_write":  ConfigObject{"network_access": true},
		},
	})
	args, err := exec.CommandArgs(ExecArgs{
		BaseURL:               "https://example.test",
		ThreadID:              "thread-1",
		Images:                []string{"a.png", "b.jpg"},
		Model:                 "gpt-5.5",
		SandboxMode:           SandboxModeWorkspaceWrite,
		WorkingDirectory:      "/repo",
		AdditionalDirectories: []string{"/extra"},
		SkipGitRepoCheck:      true,
		OutputSchemaFile:      "/tmp/schema.json",
		ModelReasoningEffort:  ModelReasoningEffortLow,
		NetworkAccessEnabled:  &network,
		WebSearchEnabled:      &webSearch,
		ApprovalPolicy:        ApprovalModeNever,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"exec", "--experimental-json",
		"--config", "sandbox_workspace_write.network_access=true",
		"--config", "show_raw_agent_reasoning=true",
		"--config", `openai_base_url="https://example.test"`,
		"--model", "gpt-5.5",
		"--sandbox", "workspace-write",
		"--cd", "/repo",
		"--add-dir", "/extra",
		"--skip-git-repo-check",
		"--output-schema", "/tmp/schema.json",
		"--config", `model_reasoning_effort="low"`,
		"--config", "sandbox_workspace_write.network_access=true",
		"--config", `web_search="disabled"`,
		"--config", `approval_policy="never"`,
		"resume", "thread-1",
		"--image", "a.png",
		"--image", "b.jpg",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args mismatch\nwant %#v\n got %#v", want, args)
	}
}

func TestSerializeConfigOverrides(t *testing.T) {
	overrides, err := serializeConfigOverrides(ConfigObject{
		"arr": []any{"x", 1, true},
		"nested": ConfigObject{
			"child-key": "value",
			"empty":     ConfigObject{},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`arr=["x", 1, true]`,
		`nested.child-key="value"`,
		`nested.empty={}`,
	}
	if !reflect.DeepEqual(overrides, want) {
		t.Fatalf("overrides mismatch\nwant %#v\n got %#v", want, overrides)
	}
}

func TestParseEventsAndUsage(t *testing.T) {
	event, err := ParseEvent([]byte(`{"type":"item.completed","item":{"id":"1","type":"agent_message","text":"done"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if event.Type != "item.completed" || event.Item.Type != "agent_message" || event.Item.Text != "done" {
		t.Fatalf("unexpected event %#v", event)
	}
	completed, err := ParseEvent([]byte(`{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":4,"output_tokens":5,"reasoning_output_tokens":2}}`))
	if err != nil {
		t.Fatal(err)
	}
	if completed.Usage.Total() != 15 || completed.Usage.CachedInputTokens != 4 {
		t.Fatalf("unexpected usage %#v", completed.Usage)
	}
}
