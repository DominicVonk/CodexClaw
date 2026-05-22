package router

import (
	"errors"
	"strings"
	"testing"

	"github.com/DominicVonk/CodexClaw/internal/codexapp"
	"github.com/DominicVonk/CodexClaw/internal/session"
)

func TestSkillNamesCanonicalizeBuiltInAliases(t *testing.T) {
	got := skillNames("Use $Memories, $skill-dictionary and $skill-creator please")
	want := []string{"memory", "skills", "skill-creator"}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}
}

func TestMemorySkillTextIncludesIDsAndCommands(t *testing.T) {
	text := memorySkillText([]session.Memory{
		{ID: 7, Content: "Prefer concise answers."},
	})
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

func TestSkillDictionaryIncludesBuiltInsAndAppSkillErrors(t *testing.T) {
	text := skillDictionaryText(nil, errors.New("offline"))
	for _, want := range []string{
		"$memory",
		"$skill-creator",
		"App-server skills unavailable: offline",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected dictionary to contain %q, got:\n%s", want, text)
		}
	}
}

func TestSkillDictionaryIncludesAppServerSkills(t *testing.T) {
	text := skillDictionaryText([]codexapp.Skill{{Name: "example", Path: "/tmp/example"}}, nil)
	for _, want := range []string{"$memory", "$example: /tmp/example"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected dictionary to contain %q, got:\n%s", want, text)
		}
	}
}
