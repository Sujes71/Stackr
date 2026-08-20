package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRunWithTimeoutTimeoutMessage verifies that when a git command is killed
// by the context deadline, the returned error explains the timeout instead of
// surfacing an opaque "signal: killed" (which the UI would show as
// "git pull --rebase:" — easy to misread as an instruction).
func TestRunWithTimeoutTimeoutMessage(t *testing.T) {
	// Shim `git` with a command that hangs, so the timeout path triggers
	// against the real runWithTimeout implementation.
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(bin, "git")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := os.Getenv("PATH")
	t.Cleanup(func() { os.Setenv("PATH", oldPath) })
	os.Setenv("PATH", bin+string(os.PathListSeparator)+oldPath)

	_, err := runWithTimeout(dir, 200*time.Millisecond, "pull", "--rebase")
	if err == nil {
		t.Fatal("expected a timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timed out after") {
		t.Fatalf("expected friendly timeout message, got: %v", err)
	}
	if strings.Contains(err.Error(), "signal: killed") {
		t.Fatalf("timeout error must not expose the raw kill signal: %v", err)
	}
}

// TestRunWithTimeoutSuccess verifies that the WaitDelay change does not break
// normal fast commands: they still return their output with no error.
func TestRunWithTimeoutSuccess(t *testing.T) {
	dir := t.TempDir()
	out, err := runWithTimeout(dir, 5*time.Second, "--version")
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if !strings.Contains(out, "git version") {
		t.Fatalf("expected git version output, got: %q", out)
	}
}
