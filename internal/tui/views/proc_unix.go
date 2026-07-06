//go:build !windows

package views

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// setNewProcessGroup configures the command to run in its own process group
// so we can kill the entire group (including child processes) on stop.
func setNewProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup sends SIGKILL to the entire process group of the command.
// This ensures child processes spawned by the command are also terminated.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// processAlive checks whether the process with the given PID is still running.
// signal 0 checks process existence without actually sending a signal.
func processAlive(pid int) bool {
	return syscall.Kill(pid, syscall.Signal(0)) == nil
}

// shellCommand wraps a shell command string for execution.
// On Unix, uses sh -c to support pipes, redirects, and shell syntax.
func shellCommand(command string) *exec.Cmd {
	return exec.Command("sh", "-c", command)
}

// expandHome replaces a leading ~ with the user's home directory.
// On Unix, $HOME is the standard environment variable for the home directory.
func expandHome(path string) string {
	if path == "~" {
		return os.Getenv("HOME")
	}
	if strings.HasPrefix(path, "~/") {
		return os.Getenv("HOME") + path[1:]
	}
	return path
}
