//go:build windows

package optimizer

import (
	"os/exec"
	"strconv"
)

// configureAdapterProcess kills the adapter's whole process tree on timeout or
// cancellation, so descendants cannot keep making paid calls.
func configureAdapterProcess(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if err := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run(); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}
