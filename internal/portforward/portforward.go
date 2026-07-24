// Package portforward provides Kubernetes port-forward lifecycle management
// for Stackr environments. It handles spawning kubectl port-forward subprocesses,
// monitoring their health, and cleaning up on exit.
package portforward

import (
	"fmt"
	"io"
	"os/exec"
	"sync"
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

// ringBuffer is a fixed-size circular buffer that keeps the last `size` bytes
// written to it. It is used to capture stderr from kubectl so that mid-session
// failures can surface a useful error message instead of a generic string.
type ringBuffer struct {
	mu   sync.Mutex
	buf  []byte
	head int // write position
	size int
	full bool
}

func newRingBuffer(size int) *ringBuffer {
	return &ringBuffer{buf: make([]byte, size), size: size}
}

func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(p)
	if n >= r.size {
		// Data larger than buffer — keep the last `size` bytes.
		copy(r.buf, p[n-r.size:])
		r.head = 0
		r.full = true
		return n, nil
	}
	// Fast path: enough room from head to end.
	if r.size-r.head >= n {
		copy(r.buf[r.head:], p)
		r.head += n
	} else {
		// Wraps around.
		copied := copy(r.buf[r.head:], p)
		copy(r.buf, p[copied:])
		r.head = n - copied
		r.full = true
	}
	return n, nil
}

func (r *ringBuffer) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.full {
		out := make([]byte, r.size)
		copy(out, r.buf[r.head:])
		copy(out[r.size-r.head:], r.buf[:r.head])
		return string(out)
	}
	return string(r.buf[:r.head])
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
	// stderrBuf captures the last 512 bytes of kubectl stderr for diagnostics.
	stderrBuf *ringBuffer
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
		Config:    cfg,
		State:     ForwardStopped,
		stderrBuf: newRingBuffer(512),
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
	h.stderrBuf = newRingBuffer(512)
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

	// Pipe stderr through a ring buffer so we can surface the actual kubectl
	// error when CheckAlive detects a mid-session failure.
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		h.mu.Lock()
		h.State = ForwardFailed
		h.Error = err.Error()
		h.mu.Unlock()
		return err
	}
	// Tee stderr: kubectl writes to ring buffer AND we keep the pipe for
	// fast-fail reading in the 2-second window below.
	multiStderr := io.TeeReader(stderrPipe, h.stderrBuf)

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

	// Background goroutine: drain stderr continuously so the OS pipe buffer
	// never fills up. If kubectl writes more than ~64KB to stderr and nobody
	// reads, kubectl blocks on the write, cmd.Wait() never returns, and
	// doneCh never closes — making CheckAlive() permanently blind.
	go func() {
		buf := make([]byte, 4096)
		for {
			_, err := multiStderr.Read(buf)
			if err != nil {
				return // pipe closed (process exited or Wait finished)
			}
		}
	}()

	// Background goroutine: wait for process exit, then signal via close(doneCh).
	go func() {
		cmd.Wait()
		close(doneCh)
	}()

	// Wait up to 2 seconds for fast-fail detection
	select {
	case <-doneCh:
		// Process exited quickly — read remaining stderr for the error
		buf := make([]byte, 4096)
		n, _ := multiStderr.Read(buf)
		errMsg := string(buf[:n])
		if errMsg == "" {
			// Fall back to the ring buffer which captured everything
			errMsg = h.stderrBuf.String()
		}
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

// Stop terminates the port-forward subprocess gracefully (graceful signal
// with a 5 s fallback to force-kill). It is a no-op if the forward is
// already Stopped.
func (h *PortForwardHandle) Stop() error {
	h.mu.Lock()
	if h.State == ForwardStopped {
		h.mu.Unlock()
		return nil
	}
	// For Failed handles: the process is already dead, just clean up.
	if h.State == ForwardFailed {
		h.State = ForwardStopped
		h.Cmd = nil
		h.done = nil
		h.Error = ""
		h.mu.Unlock()
		return nil
	}
	// Running or Starting — actually terminate the subprocess.
	cmd := h.Cmd
	doneCh := h.done
	h.mu.Unlock()

	if cmd != nil && cmd.Process != nil {
		gracefulTerminate(cmd, doneCh, 5*time.Second)
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
// It is a no-op if the forward is not in Running or Starting state.
func (h *PortForwardHandle) CheckAlive() ForwardState {
	h.mu.Lock()
	// Only check Running or Starting forwards. Stopped/Failed are terminal.
	if h.State != ForwardRunning && h.State != ForwardStarting {
		state := h.State
		h.mu.Unlock()
		return state
	}
	// Capture a snapshot of the current state for the race check below.
	stateBeforeUnlock := h.State
	doneCh := h.done
	h.mu.Unlock()

	select {
	case <-doneCh:
		// Subprocess exited — acquire lock and transition to Failed,
		// but ONLY if nobody else (e.g. Stop()) changed the state first.
		h.mu.Lock()
		if h.State != stateBeforeUnlock {
			// State was changed concurrently (Stop() ran). Respect it.
			state := h.State
			h.mu.Unlock()
			return state
		}
		h.State = ForwardFailed
		// Surface the actual kubectl stderr if available, otherwise generic.
		errMsg := h.stderrBuf.String()
		if errMsg == "" {
			errMsg = "process exited unexpectedly"
		}
		h.Error = errMsg
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

// Stderr returns the last 512 bytes of kubectl stderr for diagnostics.
func (h *PortForwardHandle) Stderr() string {
	if h.stderrBuf == nil {
		return ""
	}
	return h.stderrBuf.String()
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
