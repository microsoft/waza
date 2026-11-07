package artifactmanifest

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/microsoft/waza/internal/rootedfile"
)

// Local retains the selected root. Only a complete, closed staging file can be
// linked into its final name. Link is no-replace; unsupported filesystems fail.
// Staging entries are deliberately retained: no competing file is ever deleted.
type Local struct {
	root       *os.Root
	openStage  func(string) (stageFile, error)
	beforeLink func(string, string) error
}

type stageFile interface {
	io.WriteCloser
	Chmod(os.FileMode) error
}

func OpenLocal(directory string) (*Local, error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, fmt.Errorf("opening immutable store root: %w", err)
	}
	return &Local{root: root}, nil
}

func (l *Local) Close() error { return l.root.Close() }

func (l *Local) ListManifests(ctx context.Context) ([]string, bool, error) {
	directory, err := l.root.Open(".")
	if err != nil {
		return nil, false, err
	}
	keys := []string{}
	scanned := 0
	var scanErr error
	truncated := false
	for {
		if scanErr = ctx.Err(); scanErr != nil {
			break
		}
		entries, err := directory.ReadDir(128)
		for _, entry := range entries {
			scanned++
			if scanned > 10000 || len(keys) == MaxCatalogEntries {
				truncated = true
				break
			}
			if strings.HasPrefix(entry.Name(), "manifest-") {
				keys = append(keys, entry.Name())
			}
		}
		if truncated || errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			scanErr = err
			break
		}
	}
	if err := errors.Join(scanErr, directory.Close()); err != nil {
		return nil, false, err
	}
	return keys, truncated, nil
}

func validKey(key string) bool {
	for _, prefix := range []string{"blob-", "manifest-", "final-"} {
		if strings.HasPrefix(key, prefix) {
			return validDigest(strings.TrimPrefix(key, prefix))
		}
	}
	return false
}

func (l *Local) Create(ctx context.Context, key string, data []byte) (bool, error) {
	if !validKey(key) || len(data) > MaxArtifactBytes {
		return false, ErrInvalid
	}
	if err := verifyObject(key, data); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if _, err := l.root.Lstat(key); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	name := ".pending-" + rand.Text()
	openStage := l.openStage
	if openStage == nil {
		openStage = func(name string) (stageFile, error) {
			return l.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		}
	}
	file, err := openStage(name)
	if err != nil {
		return false, fmt.Errorf("creating private staging object: %w", err)
	}
	n, writeErr := file.Write(data)
	if writeErr == nil && n != len(data) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = file.Chmod(0o400)
	}
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr, ctx.Err()); err != nil {
		return false, fmt.Errorf("staging object retained as %s: %w", name, err)
	}
	if l.beforeLink != nil {
		if err := l.beforeLink(name, key); err != nil {
			return false, fmt.Errorf("committing staging object: %w", err)
		}
	}
	if err := l.root.Link(name, key); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, fmt.Errorf("no-replace link failed; staging object retained as %s: %w", name, err)
	}
	published, err := l.Get(ctx, key, int64(len(data)))
	if err != nil {
		return false, fmt.Errorf("verifying linked object; staging object retained as %s: %w", name, err)
	}
	if !bytes.Equal(published, data) {
		return false, ErrConflict
	}
	return true, nil
}

func (l *Local) Get(ctx context.Context, key string, limit int64) ([]byte, error) {
	if !validKey(key) || limit < 0 || limit > MaxArtifactBytes {
		return nil, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := l.root.Lstat(key)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrMissing
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, ErrCorrupt
	}
	file, err := rootedfile.Open(l.root, key)
	if err != nil {
		return nil, err
	}
	actual, statErr := file.Stat()
	var data []byte
	var readErr error
	if statErr == nil && (!actual.Mode().IsRegular() || !os.SameFile(info, actual) || actual.Size() > limit) {
		statErr = ErrCorrupt
	}
	if statErr == nil {
		data, readErr = io.ReadAll(io.LimitReader(file, limit+1))
		if int64(len(data)) > limit {
			readErr = ErrCorrupt
		}
	}
	if err := errors.Join(statErr, readErr, file.Close(), ctx.Err()); err != nil {
		return nil, err
	}
	if err := verifyObject(key, data); err != nil {
		return nil, err
	}
	return data, nil
}
