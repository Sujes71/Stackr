//go:build !windows

package portforward

import (
	"os/exec"
	"syscall"
	"time"
)

// gracefulTerminate sends SIGTERM, waits up to timeout for the process to
// exit (signalled by done closing), then SIGKILLs it if still alive. This
// gives the process a chance to clean up before being force-killed. done is
// closed by the caller's background goroutine that owns cmd.Wait().
func gracefulTerminate(cmd *exec.Cmd, done <-chan struct{}, timeout time.Duration) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)

	select {
	case <-done:
		// Process exited on SIGTERM — clean.
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		<-done
	}
}
