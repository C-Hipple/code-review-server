//go:build !unix

package chat

import "os/exec"

// setProcessGroup leaves cmd as it is: process groups are a Unix thing.
func setProcessGroup(*exec.Cmd) {}

// killProcessGroup kills cmd.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// killGroup does nothing: without process groups, there is no group to kill.
func killGroup(*exec.Cmd) {}
