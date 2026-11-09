//go:build !windows

package hooks

import (
	"os/exec"
	"syscall"
)

// configureHookProcess runs the hook in its own process group, so cancellation
// also stops descendants that would otherwise keep running and hold its pipes.
func configureHookProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
