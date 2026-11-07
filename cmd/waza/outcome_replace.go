//go:build !windows

package main

import "os"

func replaceOutcomeFile(source, destination string) error {
	return os.Rename(source, destination)
}
