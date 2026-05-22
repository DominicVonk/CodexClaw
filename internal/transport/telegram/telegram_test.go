package telegram

import "testing"

func TestTelegramCommandsAreValidForBotMenu(t *testing.T) {
	commands := telegramCommands()
	if len(commands) == 0 {
		t.Fatal("expected telegram commands")
	}
	seen := map[string]struct{}{}
	for _, command := range commands {
		if command.Command == "" {
			t.Fatal("command name is empty")
		}
		if command.Command[0] == '/' {
			t.Fatalf("telegram command %q must not include a slash", command.Command)
		}
		if len(command.Command) > 32 {
			t.Fatalf("telegram command %q is longer than 32 chars", command.Command)
		}
		if command.Description == "" {
			t.Fatalf("telegram command %q has empty description", command.Command)
		}
		if len(command.Description) > 256 {
			t.Fatalf("telegram command %q description is longer than 256 chars", command.Command)
		}
		if _, ok := seen[command.Command]; ok {
			t.Fatalf("duplicate telegram command %q", command.Command)
		}
		seen[command.Command] = struct{}{}
	}
	for _, required := range []string{"new", "session", "status", "model", "reasoning", "skills", "remember", "memory", "forget"} {
		if _, ok := seen[required]; !ok {
			t.Fatalf("missing telegram command %q", required)
		}
	}
}
