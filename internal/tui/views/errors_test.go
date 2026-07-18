package views

import (
	"testing"
	"time"
)

func TestErrorRegistry_Add(t *testing.T) {
	registry := NewErrorRegistry()

	registry.Add("test_context", "test message")

	if registry.Len() != 1 {
		t.Errorf("expected 1 error, got %d", registry.Len())
	}

	errors := registry.All()
	if len(errors) != 1 {
		t.Fatalf("expected 1 error, got %d", len(errors))
	}

	if errors[0].Context != "test_context" {
		t.Errorf("expected context 'test_context', got '%s'", errors[0].Context)
	}

	if errors[0].Message != "test message" {
		t.Errorf("expected message 'test message', got '%s'", errors[0].Message)
	}

	if errors[0].ID != 1 {
		t.Errorf("expected ID 1, got %d", errors[0].ID)
	}
}

func TestErrorRegistry_FIFO(t *testing.T) {
	registry := &ErrorRegistry{cap: 3}

	registry.Add("ctx1", "msg1")
	registry.Add("ctx2", "msg2")
	registry.Add("ctx3", "msg3")
	registry.Add("ctx4", "msg4")

	if registry.Len() != 3 {
		t.Errorf("expected 3 errors (FIFO cap), got %d", registry.Len())
	}

	errors := registry.All()
	if len(errors) != 3 {
		t.Fatalf("expected 3 errors, got %d", len(errors))
	}

	// Should be newest first: msg4, msg3, msg2
	if errors[0].Message != "msg4" {
		t.Errorf("expected first error to be 'msg4', got '%s'", errors[0].Message)
	}
	if errors[1].Message != "msg3" {
		t.Errorf("expected second error to be 'msg3', got '%s'", errors[1].Message)
	}
	if errors[2].Message != "msg2" {
		t.Errorf("expected third error to be 'msg2', got '%s'", errors[2].Message)
	}
}

func TestErrorRegistry_NewestFirst(t *testing.T) {
	registry := NewErrorRegistry()

	registry.Add("ctx1", "msg1")
	time.Sleep(1 * time.Millisecond) // ensure different timestamps
	registry.Add("ctx2", "msg2")
	time.Sleep(1 * time.Millisecond)
	registry.Add("ctx3", "msg3")

	errors := registry.All()
	if len(errors) != 3 {
		t.Fatalf("expected 3 errors, got %d", len(errors))
	}

	// Should be newest first by ID
	if errors[0].ID != 3 {
		t.Errorf("expected first error ID 3, got %d", errors[0].ID)
	}
	if errors[1].ID != 2 {
		t.Errorf("expected second error ID 2, got %d", errors[1].ID)
	}
	if errors[2].ID != 1 {
		t.Errorf("expected third error ID 1, got %d", errors[2].ID)
	}
}

func TestErrorRegistry_DefensiveCopy(t *testing.T) {
	registry := NewErrorRegistry()
	registry.Add("ctx", "msg")

	errors := registry.All()
	errors[0].Message = "modified"

	// Original should not be modified
	original := registry.All()
	if original[0].Message != "msg" {
		t.Errorf("defensive copy failed: original was modified")
	}
}

func TestErrorRegistry_Empty(t *testing.T) {
	registry := NewErrorRegistry()

	if registry.Len() != 0 {
		t.Errorf("expected 0 errors, got %d", registry.Len())
	}

	errors := registry.All()
	if len(errors) != 0 {
		t.Errorf("expected empty slice, got %d elements", len(errors))
	}
}
