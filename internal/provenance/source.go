package provenance

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
)

// Source exposes bundle members by slash-separated relative path.
type Source interface {
	// List returns regular member paths and any irregular entries (symlinks,
	// devices) that a verifier must reject.
	List() (regular, irregular []string, err error)
	ReadFile(name string, limit int64) ([]byte, error)
}

var errMemberMissing = errors.New("member missing")
var errMemberTooLarge = errors.New("member exceeds size bound")
var errTooManyMembers = fmt.Errorf("bundle contains more than %d entries", MaxMembers)

type dirSource struct{ root *os.Root }

// OpenDir opens a bundle directory confined to its root; members cannot
// escape through symbolic links or parent references.
func OpenDir(dir string) (Source, func() error, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("opening bundle: %w", err)
	}
	return &dirSource{root: root}, root.Close, nil
}

func (s *dirSource) List() (regular, irregular []string, err error) {
	err = fs.WalkDir(s.root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if len(regular)+len(irregular) >= MaxMembers && !entry.IsDir() {
			return errTooManyMembers
		}
		switch {
		case entry.IsDir():
		case entry.Type().IsRegular():
			regular = append(regular, name)
		default:
			irregular = append(irregular, name)
		}
		return nil
	})
	return regular, irregular, err
}

func (s *dirSource) ReadFile(name string, limit int64) ([]byte, error) {
	if !fs.ValidPath(name) {
		return nil, errMemberMissing
	}
	info, err := s.root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errMemberMissing
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("member is not a regular file")
	}
	if info.Size() > limit {
		return nil, errMemberTooLarge
	}
	file, err := s.root.Open(name)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, limit+1))
	if err := errors.Join(readErr, file.Close()); err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errMemberTooLarge
	}
	return data, nil
}

type mapSource map[string][]byte

// MapSource exposes an in-memory bundle. Paths must be valid slash-separated
// relative paths.
func MapSource(files map[string][]byte) (Source, error) {
	if len(files) > MaxMembers {
		return nil, errTooManyMembers
	}
	for name := range files {
		if !fs.ValidPath(name) || name == "." || strings.Contains(name, "\\") {
			return nil, fmt.Errorf("invalid bundle member path %q", name)
		}
	}
	return mapSource(files), nil
}

func (m mapSource) List() (regular, irregular []string, err error) {
	for name := range m {
		regular = append(regular, name)
	}
	return regular, nil, nil
}

func (m mapSource) ReadFile(name string, limit int64) ([]byte, error) {
	data, ok := m[name]
	if !ok {
		return nil, errMemberMissing
	}
	if int64(len(data)) > limit {
		return nil, errMemberTooLarge
	}
	return data, nil
}

func underDir(name, dir string) (string, bool) {
	rest, ok := strings.CutPrefix(name, dir+"/")
	return rest, ok && rest != "" && path.Base(rest) == rest
}
