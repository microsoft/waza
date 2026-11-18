//go:build !windows

package optimizer

import (
	"os/exec"
	"syscall"
)

// configureAdapterProcess runs the adapter in its own process group, so a
// timeout or cancellation also stops descendants (for example a wrapper
// script's interpreter) that could otherwise keep making paid calls.
func configureAdapterProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
