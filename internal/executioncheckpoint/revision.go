package executioncheckpoint

import (
	"fmt"
	"runtime/debug"
)

// CurrentRunnerRevision reads the owning CLI's embedded VCS metadata, never a
// caller flag, working directory, subprocess or version-shaped fallback.
func CurrentRunnerRevision() (string, error) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", fmt.Errorf("%w: runner build metadata is unavailable; build the CLI from a clean versioned source tree with VCS stamping enabled", ErrIncompatible)
	}
	return runnerRevision(info)
}

func runnerRevision(info *debug.BuildInfo) (string, error) {
	if info == nil || info.Main.Path != "github.com/microsoft/waza" || info.Path != "github.com/microsoft/waza/cmd/waza" {
		return "", fmt.Errorf("%w: build metadata does not identify the owning Waza CLI; checkpoint recovery requires its actual versioned build", ErrIncompatible)
	}
	values := make(map[string]string, 3)
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs", "vcs.revision", "vcs.modified":
			if _, exists := values[setting.Key]; exists {
				return "", fmt.Errorf("%w: duplicate runner build setting %s", ErrIncompatible, setting.Key)
			}
			values[setting.Key] = setting.Value
		}
	}
	revision := values["vcs.revision"]
	if values["vcs"] != "git" || values["vcs.modified"] != "false" || len(revision) != 40 {
		return "", fmt.Errorf("%w: runner revision is missing, dirty or unstamped; build from a clean Git source tree with VCS stamping enabled and preserve the existing checkpoint", ErrIncompatible)
	}
	for _, character := range revision {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return "", fmt.Errorf("%w: runner VCS revision is not an exact lowercase Git commit", ErrIncompatible)
		}
	}
	return revision, nil
}
