package orchestration

import (
	"context"
	"fmt"
	"io/fs"
	"os"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
)

type RequestInputKind string

const (
	RequestInputRegular   RequestInputKind = "regular"
	RequestInputDirectory RequestInputKind = "directory"
	RequestInputSymlink   RequestInputKind = "symlink"
	RequestInputOther     RequestInputKind = "other"
)

// RequestInputs supplies semantic traversal facts without filesystem metadata.
// Implementations own synchronous walk order and callback error propagation.
type RequestInputs interface {
	ReadFile(string) ([]byte, error)
	Kind(string) (RequestInputKind, error)
	Resolve(string) (string, error)
	Walk(string, func(string, RequestInputKind) error) error
}

type requestFilesInputs struct{ files RequestFiles }

// AdaptRequestFiles preserves the original provider's observations and errors.
// It exposes no native selection, engine, filesystem metadata or callback.
// Nil remains nil so the existing constructor's input guards own rejection.
func AdaptRequestFiles(files RequestFiles) RequestInputs {
	if files == nil {
		return nil
	}
	return requestFilesInputs{files}
}

func (f requestFilesInputs) ReadFile(path string) ([]byte, error) {
	return f.files.ReadFile(path)
}

func (f requestFilesInputs) Kind(path string) (RequestInputKind, error) {
	info, err := f.files.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return RequestInputDirectory, nil
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return RequestInputSymlink, nil
	}
	if info.Mode().IsRegular() {
		return RequestInputRegular, nil
	}
	return RequestInputOther, nil
}

func (f requestFilesInputs) Resolve(path string) (string, error) {
	return f.files.EvalSymlinks(path)
}

func (f requestFilesInputs) Walk(path string, visit func(string, RequestInputKind) error) error {
	return f.files.WalkDir(path, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		kind := RequestInputOther
		switch {
		case entry.Type()&os.ModeSymlink != 0:
			kind = RequestInputSymlink
		case entry.IsDir():
			kind = RequestInputDirectory
		case entry.Type().IsRegular():
			kind = RequestInputRegular
		}
		return visit(path, kind)
	})
}

func (f requestFilesInputs) SetInputRole(role string) {
	if files, ok := f.files.(interface{ SetInputRole(string) }); ok {
		files.SetInputRole(role)
	}
}

// BuildRetainedNativeRequest is an inspection-only, engine-free constructor.
// It uses the same declaration guards and request logic as captured files.
func BuildRetainedNativeRequest(ctx context.Context, spec *models.EvalSpec, tc *models.TestCase, specDir, contextDir string, inputs RequestInputs) (*execution.ExecutionRequest, error) {
	if ctx == nil {
		return nil, fmt.Errorf("native request requires context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateNativeSnapshotConfiguration(spec, tc); err != nil {
		return nil, err
	}
	if inputs == nil {
		return nil, fmt.Errorf("native request requires retained inputs")
	}
	req, err := buildNativeRequest(spec, tc, specDir, contextDir, inputs)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return req, nil
}
