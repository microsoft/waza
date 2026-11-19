//go:build !windows

package artifactmanifest

func retainedRootRenameBlocked(error) bool { return false }
