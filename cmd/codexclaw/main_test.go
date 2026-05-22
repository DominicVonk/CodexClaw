package main

import "testing"

func TestVersionDefault(t *testing.T) {
	if version != "0.0.0-alpha.1" {
		t.Fatalf("expected default version 0.0.0-alpha.1, got %q", version)
	}
}

func TestRunVersion(t *testing.T) {
	if err := run([]string{"codexclaw", "version"}); err != nil {
		t.Fatal(err)
	}
}
