//go:build !windows && !linux

package llama

import (
	"os/exec"
	"syscall"
)

func PrepareSubprocess(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
}

func prepareSupervisedSubprocess(cmd *exec.Cmd) {
	PrepareSubprocess(cmd)
}

func attachSubprocess(_ *exec.Cmd) error {
	return nil
}

func releaseSubprocess(_ *exec.Cmd) {}

func terminateProcessTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	if pid > 0 {
		if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
			killProcessDirect(cmd)
		}
	}
}

func killProcessDirect(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if err := cmd.Process.Kill(); err != nil {
		return
	}
}
