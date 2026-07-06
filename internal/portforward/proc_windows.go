//go:build windows

package portforward

import (
	"os/exec"
	"strconv"
	"time"
)

// gracefulTerminate kills the process tree on Windows.
// Windows has no SIGTERM equivalent, so we skip the graceful phase and
// terminate the process and its children directly via taskkill /T /F.
// done is closed externally when the process exits; we wait on it (up to
// timeout) so the caller's state stays consistent.
func gracefulTerminate(cmd *exec.Cmd, done <-chan struct{}, timeout time.Duration) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = exec.Command("taskkill", "/T", "/F", "/PID",
		strconv.Itoa(cmd.Process.Pid)).Run()

	select {
	case <-done:
	case <-time.After(timeout):
	}
}
