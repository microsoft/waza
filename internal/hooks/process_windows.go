//go:build windows

package hooks

import "os/exec"

// configureHookProcess keeps the default: cancellation kills the hook process,
// and WaitDelay closes pipes a surviving descendant still holds.
func configureHookProcess(*exec.Cmd) {}
