// Package portforward provides Kubernetes port-forward lifecycle management
// for Stackr environments. It handles spawning kubectl port-forward subprocesses,
// monitoring their health, and cleaning up on exit.
package portforward

import (
	"fmt"
	"os/exec"
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
}

// NewHandle creates a new PortForwardHandle in the Stopped state for the given config.
func NewHandle(cfg config.PortForward) *PortForwardHandle {
	return &PortForwardHandle{
		Config: cfg,
		State:  ForwardStopped,
	}
}

// Start spawns a kubectl port-forward subprocess.
//
// It validates kubectl availability, constructs the correct command, and waits
// up to 2 seconds for the subprocess to either stabilise (→ ForwardRunning) or
// fail fast (→ ForwardFailed with captured stderr).
//
// Returns an error if already running/starting, kubectl is missing, or the
// subprocess exits within the 2-second window.
func (h *PortForwardHandle) Start() error {
	if h.State == ForwardRunning || h.State == ForwardStarting {
		return fmt.Errorf("already %s", h.State)
	}

	h.State = ForwardStarting
	h.Error = ""

	// Validate kubectl is available
	if _, err := exec.LookPath("kubectl"); err != nil {
		h.State = ForwardFailed
		h.Error = "kubectl not found in PATH"
		return fmt.Errorf(h.Error)
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
		h.State = ForwardFailed
		h.Error = err.Error()
		return err
	}

	if err := cmd.Start(); err != nil {
		h.State = ForwardFailed
		h.Error = err.Error()
		return err
	}

	h.Cmd = cmd
	h.done = make(chan struct{})

	// Background goroutine: wait for process exit, then signal via close(done)
	go func() {
		cmd.Wait()
		close(h.done)
	}()

	// Wait up to 2 seconds for fast-fail detection
	select {
	case <-h.done:
		// Process exited quickly — try to read stderr for the error
		h.State = ForwardFailed
		buf := make([]byte, 4096)
		n, _ := stderr.Read(buf)
		errMsg := string(buf[:n])
		if errMsg == "" {
			errMsg = fmt.Sprintf("process exited with code %d", cmd.ProcessState.ExitCode())
		}
		h.Error = errMsg
		h.Cmd = nil
		h.done = nil
		return fmt.Errorf(errMsg)

	case <-time.After(2 * time.Second):
		// Still alive after 2 seconds — treat as running
		h.State = ForwardRunning
		return nil
	}
}

// Stop terminates the port-forward subprocess gracefully (SIGTERM with 5 s
// fallback to SIGKILL). It is a no-op if the forward is not Running or Starting.
func (h *PortForwardHandle) Stop() error {
	if h.State != ForwardRunning && h.State != ForwardStarting {
		return nil
	}

	if h.Cmd != nil && h.Cmd.Process != nil {
		// Send SIGTERM for graceful shutdown
		_ = h.Cmd.Process.Signal(syscall.SIGTERM)

		// Wait up to 5 seconds for graceful exit
		select {
		case <-h.done:
			// Process exited on SIGTERM — clean
		case <-time.After(5 * time.Second):
			// Force kill
			_ = h.Cmd.Process.Kill()
			<-h.done
		}
	}

	h.State = ForwardStopped
	h.Cmd = nil
	h.done = nil
	h.Error = ""
	return nil
}

// CheckAlive performs a non-blocking health check on a running forward.
// If the subprocess has exited, it transitions the state to ForwardFailed.
// It is a no-op if the forward is not in Running state.
func (h *PortForwardHandle) CheckAlive() ForwardState {
	if h.State != ForwardRunning {
		return h.State
	}

	select {
	case <-h.done:
		// Subprocess exited unexpectedly
		h.State = ForwardFailed
		h.Error = "process exited unexpectedly"
		h.Cmd = nil
		h.done = nil
	default:
		// Still alive
	}

	return h.State
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
