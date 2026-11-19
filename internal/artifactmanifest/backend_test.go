package artifactmanifest

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenBackendPreservesExplicitProviderSelection(t *testing.T) {
	directory := t.TempDir()
	backend, closeBackend, err := OpenBackend(directory, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := backend.(*Local); !ok {
		t.Fatalf("local selection created %T", backend)
	}
	if err := closeBackend(); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(directory, "must-not-be-created")
	if backend, closeBackend, err := OpenBackend(missing, ""); err == nil || backend != nil || closeBackend != nil {
		t.Fatalf("missing local directory was created/defaulted: %T %v", backend, err)
	}
	for _, invalid := range []string{"http://offline.invalid/container", "https://offline.invalid/container?sig=untrusted", "not a URL"} {
		if backend, closeBackend, err := OpenBackend(missing, invalid); err == nil || backend != nil || closeBackend != nil {
			t.Fatalf("invalid explicit Azure selection fell back: %T %v", backend, err)
		}
	}
	backend, closeBackend, err = OpenBackend(missing, "https://offline.invalid/container")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := backend.(*Azure); !ok {
		t.Fatalf("explicit Azure selection created %T", backend)
	}
	if err := closeBackend(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Azure construction touched local fallback: %v", err)
	}
}
