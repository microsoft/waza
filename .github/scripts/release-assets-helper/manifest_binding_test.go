//go:build darwin || linux

package main

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestObservedManifestMutationThroughEveryCaller(t *testing.T) {
	for _, command := range []string{"verify", "restore", "pack"} {
		t.Run(command, func(t *testing.T) {
			artifact, digest := packed(t)
			input := artifact
			destination := filepath.Join(temp(t), "new-output")
			if command == "pack" {
				input = fixture(t)
			}
			manifestPath := filepath.Join(artifact, "manifest.json")
			if command == "pack" {
				manifestPath = filepath.Join(destination, "manifest.json")
			}
			corruption := []byte("corrupt observed manifest")
			reads := 0
			var secondRead []byte
			faults := hooks{read: func(f *os.File) ([]byte, error) {
				data, err := io.ReadAll(f)
				if err != nil {
					return nil, err
				}
				if filepath.Base(f.Name()) == "manifest.json" {
					reads++
					switch reads {
					case 1:
						if err := os.WriteFile(manifestPath, corruption, 0o600); err != nil {
							return nil, err
						}
					case 2:
						secondRead = data
					}
				}
				return data, nil
			}}
			_, err := caller(t, arguments(command, input, destination, digest, testIdentity), faults)
			if reads < 2 || !bytes.Equal(secondRead, corruption) ||
				!bytes.Equal(read(t, manifestPath), corruption) {
				t.Fatalf("real manifest mutation/reread not exercised: reads=%d second=%q err=%v", reads, secondRead, err)
			}
			if err == nil {
				t.Fatal("caller emitted verified digest after inventory observed a different manifest")
			}
			if !strings.Contains(err.Error(), "manifest changed during inventory") {
				t.Fatalf("manifest mutation was not rejected at admission/reverify: %v", err)
			}
			if command != "pack" {
				if _, statErr := os.Stat(destination); !errors.Is(statErr, fs.ErrNotExist) {
					t.Fatalf("manifest admission failure created consumer output: %v", statErr)
				}
			}

		})
	}
}
