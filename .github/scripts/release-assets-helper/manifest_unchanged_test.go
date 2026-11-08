//go:build darwin || linux

package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestIdenticalObservedManifestBytesRemainValid(t *testing.T) {
	for _, command := range []string{"verify", "restore", "pack"} {
		t.Run(command, func(t *testing.T) {
			artifact, digest := packed(t)
			input, destination := artifact, filepath.Join(temp(t), "new-output")
			if command == "pack" {
				input = fixture(t)
			}
			manifestPath := filepath.Join(artifact, "manifest.json")
			if command == "pack" {
				manifestPath = filepath.Join(destination, "manifest.json")
			}
			reads := 0
			faults := hooks{read: func(f *os.File) ([]byte, error) {
				data, err := io.ReadAll(f)
				if err != nil {
					return nil, err
				}
				if filepath.Base(f.Name()) == "manifest.json" {
					reads++
					if reads == 1 {
						if err := os.WriteFile(manifestPath, data, 0o600); err != nil {
							return nil, err
						}
					}
				}
				return data, nil
			}}
			got, err := caller(t, arguments(command, input, destination, digest, testIdentity), faults)
			if err != nil || reads < 2 || got != digest {
				t.Fatalf("unchanged observed bytes rejected: reads=%d digest=%q err=%v", reads, got, err)
			}
		})
	}
}
