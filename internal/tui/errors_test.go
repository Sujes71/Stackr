package tui

import (
	"fmt"
	"testing"
)

func TestErrorRegistry_AddSingle(t *testing.T) {
	r := NewErrorRegistry()
	r.Add("add_syncenv", "connection refused")

	if r.Len() != 1 {
		t.Fatalf("expected Len()=1, got %d", r.Len())
	}

	all := r.All()
	if len(all) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(all))
	}

	e := all[0]
	if e.ID != 1 {
		t.Errorf("expected ID=1, got %d", e.ID)
	}
	if e.Context != "add_syncenv" {
		t.Errorf("expected Context=%q, got %q", "add_syncenv", e.Context)
	}
	if e.Message != "connection refused" {
		t.Errorf("expected Message=%q, got %q", "connection refused", e.Message)
	}
	if e.Timestamp.IsZero() {
		t.Error("expected non-zero Timestamp")
	}
}

func TestErrorRegistry_FIFOEviction(t *testing.T) {
	r := NewErrorRegistry()
	r.cap = 5 // small cap for testing

	for i := 0; i < 7; i++ {
		r.Add("ctx", fmt.Sprintf("msg-%d", i))
	}

	if r.Len() != 5 {
		t.Fatalf("expected Len()=5 after eviction, got %d", r.Len())
	}

	all := r.All()
	// Newest-first: msg-6, msg-5, msg-4, msg-3, msg-2
	if all[0].Message != "msg-6" {
		t.Errorf("expected newest=msg-6, got %q", all[0].Message)
	}
	if all[4].Message != "msg-2" {
		t.Errorf("expected oldest=msg-2, got %q", all[4].Message)
	}
}

func TestErrorRegistry_AllNewestFirst(t *testing.T) {
	r := NewErrorRegistry()
	r.Add("ctx", "first")
	r.Add("ctx", "second")
	r.Add("ctx", "third")

	all := r.All()
	if len(all) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(all))
	}

	expected := []string{"third", "second", "first"}
	for i, want := range expected {
		if all[i].Message != want {
			t.Errorf("position %d: expected %q, got %q", i, want, all[i].Message)
		}
	}
}

func TestErrorRegistry_AllDefensiveCopy(t *testing.T) {
	r := NewErrorRegistry()
	r.Add("ctx", "original")

	all := r.All()
	// Mutate the returned slice
	all[0].Message = "MUTATED"

	// Registry should be unaffected
	fresh := r.All()
	if fresh[0].Message != "original" {
		t.Errorf("defensive copy failed: registry was mutated, got %q", fresh[0].Message)
	}
}
