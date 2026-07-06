// Package portforward provides Kubernetes port-forward lifecycle management
// for Stackr environments. It handles spawning kubectl port-forward subprocesses,
// monitoring their health, and cleaning up on exit.
package portforward

import (
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/stackr/stackr/internal/config"
)

// ForwardState represents the current state of a port-forward subprocess.
type ForwardState int

const (
	// ForwardStopped is the initial state; no subprocess is running.
	ForwardStopped ForwardState = iota
	// ForwardStarting means the subprocess was spawned but not yet confirmed alive.
	ForwardStarting
	// ForwardRunning means the subprocess has been running for more than 2 seconds.
	ForwardRunning
	// ForwardFailed means the subprocess exited with an error.
	ForwardFailed
)

// String returns a human-readable name for the state.
func (s ForwardState) String() string {
	switch s {
	case ForwardStopped:
		return "stopped"
	case ForwardStarting:
		return "starting"
	case ForwardRunning:
		return "running"
	case ForwardFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// PortForwardHandle wraps a single port-forward configuration and its runtime state.
// It tracks the subprocess lifecycle (start, stop, health) and any error messages.
// All state transitions are guarded by mu because Start() may run in a goroutine
// (e.g. restarting a forward after a port edit) while CheckAlive() runs in the
// main TUI update loop on a ForwardMonitorTick.
type PortForwardHandle struct {
	// Config is the immutable port-forward configuration this handle was created from.
	Config config.PortForward
	// State is the current lifecycle state of the forward.
	State ForwardState
	// Cmd is the running kubectl subprocess, or nil if not running.
	Cmd *exec.Cmd
	// Error contains the last error message, set when State is ForwardFailed.
	Error string
	// done is closed when the subprocess exits. Nil when not running.
	done chan struct{}
	// mu guards State, Cmd, Error, and done.
	mu sync.Mutex
}

// NewHandle creates a new PortForwardHandle in the Stopped state for the given config.
// The handle is also registered in a package-level registry so that StopAll can
// tear down any still-running forwards when the program exits.
func NewHandle(cfg config.PortForward) *PortForwardHandle {
	h := &PortForwardHandle{
		Config: cfg,
		State:  ForwardStopped,
	}
	handlesMu.Lock()
	handles = append(handles, h)
	handlesMu.Unlock()
	return h
}

// StopAll stops every handle created via NewHandle. It is safe to call from
// main() after the TUI exits, so port-forward subprocesses are not orphaned
// when the program terminates (Ctrl+C / SIGTERM). Stop is a no-op on handles
// that are already stopped, so calling this is always safe.
func StopAll() {
	handlesMu.Lock()
	hs := make([]*PortForwardHandle, len(handles))
	copy(hs, handles)
	handlesMu.Unlock()
	for _, h := range hs {
		_ = h.Stop()
	}
}

// handles registry tracks every handle created via NewHandle so StopAll can
// reach them from main() without the TUI model having to expose them.
var (
	handlesMu sync.Mutex
	handles   []*PortForwardHandle
)

// Start spawns a kubectl port-forward subprocess.
//
// It validates kubectl availability, constructs the correct command, and waits
// up to 2 seconds for the subprocess to either stabilise (→ ForwardRunning) or
// fail fast (→ ForwardFailed with captured stderr).
//
// Returns an error if already running/starting, kubectl is missing, or the
// subprocess exits within the 2-second window.
func (h *PortForwardHandle) Start() error {
	h.mu.Lock()
	if h.State == ForwardRunning || h.State == ForwardStarting {
		h.mu.Unlock()
		return fmt.Errorf("already %s", h.State)
	}
	h.State = ForwardStarting
	h.Error = ""
	h.mu.Unlock()

	// Validate kubectl is available
	if _, err := exec.LookPath("kubectl"); err != nil {
		h.mu.Lock()
		h.State = ForwardFailed
		h.Error = "kubectl not found in PATH"
		h.mu.Unlock()
		return fmt.Errorf("%s", h.Error)
	}

	// Build: kubectl port-forward --context <ctx> -n <ns> service/<svc> <local>:<remote>
	args := []string{
		"port-forward",
		"--context", h.Config.K8sContext,
		"-n", h.Config.K8sNamespace,
		fmt.Sprintf("service/%s", h.Config.K8sService),
		fmt.Sprintf("%d:%d", h.Config.LocalPort, h.Config.RemotePort),
	}
	cmd := exec.Command("kubectl", args...)

	// Capture stderr for diagnostics
	stderr, err := cmd.StderrPipe()
	if err != nil {
		h.mu.Lock()
		h.State = ForwardFailed
		h.Error = err.Error()
		h.mu.Unlock()
		return err
	}

	if err := cmd.Start(); err != nil {
		h.mu.Lock()
		h.State = ForwardFailed
		h.Error = err.Error()
		h.mu.Unlock()
		return err
	}

	doneCh := make(chan struct{})
	h.mu.Lock()
	h.Cmd = cmd
	h.done = doneCh
	h.mu.Unlock()

	// Background goroutine: wait for process exit, then signal via close(doneCh)
	go func() {
		cmd.Wait()
		close(doneCh)
	}()

	// Wait up to 2 seconds for fast-fail detection
	select {
	case <-doneCh:
		// Process exited quickly — try to read stderr for the error
		buf := make([]byte, 4096)
		n, _ := stderr.Read(buf)
		errMsg := string(buf[:n])
		if errMsg == "" {
			errMsg = fmt.Sprintf("process exited with code %d", cmd.ProcessState.ExitCode())
		}
		h.mu.Lock()
		h.State = ForwardFailed
		h.Error = errMsg
		h.Cmd = nil
		h.done = nil
		h.mu.Unlock()
		return fmt.Errorf("%s", errMsg)

	case <-time.After(2 * time.Second):
		// Still alive after 2 seconds — treat as running
		h.mu.Lock()
		h.State = ForwardRunning
		h.mu.Unlock()
		return nil
	}
}

// Stop terminates the port-forward subprocess gracefully (SIGTERM with 5 s
// fallback to SIGKILL). It is a no-op if the forward is not Running or Starting.
func (h *PortForwardHandle) Stop() error {
	h.mu.Lock()
	if h.State != ForwardRunning && h.State != ForwardStarting {
		h.mu.Unlock()
		return nil
	}
	cmd := h.Cmd
	doneCh := h.done
	h.mu.Unlock()

	if cmd != nil && cmd.Process != nil {
		// Send SIGTERM for graceful shutdown
		_ = cmd.Process.Signal(syscall.SIGTERM)

		// Wait up to 5 seconds for graceful exit
		select {
		case <-doneCh:
			// Process exited on SIGTERM — clean
		case <-time.After(5 * time.Second):
			// Force kill
			_ = cmd.Process.Kill()
			<-doneCh
		}
	}

	h.mu.Lock()
	h.State = ForwardStopped
	h.Cmd = nil
	h.done = nil
	h.Error = ""
	h.mu.Unlock()
	return nil
}

// CheckAlive performs a non-blocking health check on a running forward.
// If the subprocess has exited, it transitions the state to ForwardFailed.
// It is a no-op if the forward is not in Running state.
func (h *PortForwardHandle) CheckAlive() ForwardState {
	h.mu.Lock()
	if h.State != ForwardRunning {
		state := h.State
		h.mu.Unlock()
		return state
	}
	doneCh := h.done
	h.mu.Unlock()

	select {
	case <-doneCh:
		// Subprocess exited unexpectedly
		h.mu.Lock()
		h.State = ForwardFailed
		h.Error = "process exited unexpectedly"
		h.Cmd = nil
		h.done = nil
		state := h.State
		h.mu.Unlock()
		return state
	default:
		// Still alive
		return ForwardRunning
	}
}

// AssignPort finds the next free port starting from `start`,
// avoiding all ports already used in `existing`. This is used
// to auto-assign local ports for newly discovered forwards.
func AssignPort(start int, existing []*PortForwardHandle) int {
	used := make(map[int]bool)
	for _, h := range existing {
		used[h.Config.LocalPort] = true
	}
	port := start
	for used[port] {
		port++
	}
	return port
}
