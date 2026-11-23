//go:build unix

package chat

import (
	"os/exec"
	"syscall"
)

// setProcessGroup starts cmd in a process group of its own, so killing it
// kills whatever it started too.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup kills cmd's process group, cmd with it.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	killGroup(cmd)
	_ = cmd.Process.Kill()
}

// killGroup kills what is left in cmd's process group.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
