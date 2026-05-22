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
