//go:build windows

package views

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// createNewProcessGroup is the Windows CREATE_NEW_PROCESS_GROUP creation flag.
const createNewProcessGroup = 0x00000200

// setNewProcessGroup configures the command to run in its own process group
// so we can kill the entire group (including child processes) on stop.
func setNewProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

// killProcessGroup kills the process and its children on Windows.
// Windows has no Unix-style process groups, so we use taskkill /T /F
// to kill the process tree (the process and all its children).
func killProcessGroup(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = exec.Command("taskkill", "/T", "/F", "/PID",
			strconv.Itoa(cmd.Process.Pid)).Run()
	}
}

// processAlive checks whether the process with the given PID is still running.
// Windows has no signal-0 check, so we query tasklist instead.
func processAlive(pid int) bool {
	out, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH", "/FO", "CSV").Output()
	if err != nil {
		return false
	}
	return !strings.Contains(string(out), "No tasks")
}

// shellCommand wraps a shell command string for execution.
// On Windows, uses cmd /c to support basic shell syntax (pipes, redirects).
func shellCommand(command string) *exec.Cmd {
	return exec.Command("cmd", "/c", command)
}

// expandHome replaces a leading ~ with the user's home directory.
// On Windows, %USERPROFILE% is the equivalent of $HOME.
func expandHome(path string) string {
	if path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return home
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return home + path[1:]
	}
	return path
}
