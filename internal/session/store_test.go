package session

import (
	"context"
	"testing"
)

func TestStoreCreateListSwitchAndReopen(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/sessions.db"

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Create(ctx, "telegram:1:2", "work", "thr_1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Create(ctx, "telegram:1:2", "work", "thr_2")
	if err != nil {
		t.Fatal(err)
	}
	if second.Name == first.Name {
		t.Fatalf("expected duplicate name to be uniquified, got %q", second.Name)
	}

	active, ok, err := store.Active(ctx, "telegram:1:2")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || active.ID != second.ID {
		t.Fatalf("expected second session active, got ok=%v id=%d", ok, active.ID)
	}
	if err := store.SetActive(ctx, "telegram:1:2", first.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	active, ok, err = reopened.Active(ctx, "telegram:1:2")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || active.ThreadID != "thr_1" {
		t.Fatalf("expected persisted active thread thr_1, got ok=%v thread=%q", ok, active.ThreadID)
	}
	if err := reopened.UpdateModel(ctx, active.ID, "gpt-5.4"); err != nil {
		t.Fatal(err)
	}
	active, ok, err = reopened.Active(ctx, "telegram:1:2")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || active.Model != "gpt-5.4" {
		t.Fatalf("expected active model gpt-5.4, got ok=%v model=%q", ok, active.Model)
	}
	if err := reopened.UpdateThreadID(ctx, active.ID, "thr_updated"); err != nil {
		t.Fatal(err)
	}
	active, ok, err = reopened.Active(ctx, "telegram:1:2")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || active.ThreadID != "thr_updated" {
		t.Fatalf("expected updated thread id, got ok=%v thread=%q", ok, active.ThreadID)
	}
	if err := reopened.UpdateTokenUsage(ctx, active.ID, 100, 20, 120, 40, 5, 45); err != nil {
		t.Fatal(err)
	}
	active, ok, err = reopened.Active(ctx, "telegram:1:2")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || active.TotalTokens != 120 || active.LastTotalTokens != 45 {
		t.Fatalf("expected persisted token usage, got ok=%v total=%d last=%d", ok, active.TotalTokens, active.LastTotalTokens)
	}

	found, err := reopened.Find(ctx, "telegram:1:2", "wor")
	if err == nil && found.ID != active.ID {
		t.Fatalf("expected ambiguous prefix or active session, got id=%d", found.ID)
	}
	found, err = reopened.Find(ctx, "telegram:1:2", first.Name)
	if err != nil {
		t.Fatal(err)
	}
	if found.ID != first.ID {
		t.Fatalf("expected first session by exact name, got %d", found.ID)
	}
}

func TestStoreMemoryGraphLinks(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir() + "/sessions.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	first, err := store.AddMemory(ctx, "telegram:1", "Use Dutch replies.")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.AddMemory(ctx, "telegram:1", "Dutch voice uses nl-NL-ColetteNeural.")
	if err != nil {
		t.Fatal(err)
	}
	link, err := store.AddMemoryLink(ctx, "telegram:1", first.ID, "voice preference", second.ID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if link.Relation != "voice-preference" || link.Weight != 3 {
		t.Fatalf("unexpected link: %#v", link)
	}
	links, err := store.ListMemoryLinks(ctx, "telegram:1")
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].ID != link.ID {
		t.Fatalf("expected one link, got %#v", links)
	}
	deleted, err := store.DeleteMemory(ctx, "telegram:1", first.ID)
	if err != nil || !deleted {
		t.Fatalf("expected memory delete, deleted=%v err=%v", deleted, err)
	}
	links, err = store.ListMemoryLinks(ctx, "telegram:1")
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 0 {
		t.Fatalf("expected cascaded link delete, got %#v", links)
	}
}
