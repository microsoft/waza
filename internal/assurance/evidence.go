package assurance

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/microsoft/waza/internal/graders"
	"github.com/microsoft/waza/internal/models"
)

var errMissingArtifact = errors.New("reference artifact is missing")

func prepareLocalEvidence(parameters models.GraderParameters, input *graders.Context) (*graders.Context, models.GraderParameters, func() error, ObservationState, error) {
	evidence := *input
	noop := func() error { return nil }
	switch params := parameters.(type) {
	case models.FileGraderParameters:
		if input.WorkspaceDir == "" {
			return nil, nil, nil, InsufficientEvidence, errors.New("file reference requires an evaluator-owned disk workspace")
		}
		info, err := os.Stat(input.WorkspaceDir)
		if err != nil {
			state, wrapped := evidenceReadError(input.WorkspaceDir, err)
			return nil, nil, nil, state, wrapped
		}
		if !info.IsDir() {
			return nil, nil, nil, InsufficientEvidence, errors.New("reference workspace is not a directory")
		}
		workspace, err := os.MkdirTemp("", "waza-reference-")
		if err != nil {
			return nil, nil, nil, OperationalError, fmt.Errorf("creating private reference workspace: %w", err)
		}
		cleanup := func() error { return os.RemoveAll(workspace) }
		captured := map[string]bool{}
		copyPath := func(path string, required bool) (ObservationState, error) {
			if captured[path] {
				return Observed, nil
			}
			state, err := captureArtifact(input.WorkspaceDir, workspace, path, false)
			if !required && errors.Is(err, errMissingArtifact) {
				state, err = Observed, nil
			}
			if err == nil {
				captured[path] = true
			}
			return state, err
		}
		for _, path := range params.MustExist {
			if state, err := copyPath(path, true); err != nil {
				return failedPreparation(cleanup, state, err)
			}
		}
		for _, pattern := range params.ContentPatterns {
			if state, err := copyPath(pattern.Path, true); err != nil {
				return failedPreparation(cleanup, state, err)
			}
			if _, err := os.ReadFile(filepath.Join(workspace, pattern.Path)); err != nil {
				return failedPreparation(cleanup, OperationalError, fmt.Errorf("reading prepared content %q: %w", pattern.Path, err))
			}
		}
		for _, path := range params.MustNotExist {
			if state, err := copyPath(path, false); err != nil {
				return failedPreparation(cleanup, state, err)
			}
		}
		evidence.WorkspaceDir = workspace
		return &evidence, params, cleanup, Observed, nil
	case models.DiffGraderParameters:
		evidence.WorkspaceDir = ""
		evidence.WorkspaceFiles = map[string][]byte{}
		references, err := os.MkdirTemp("", "waza-reference-snapshots-")
		if err != nil {
			return nil, nil, nil, OperationalError, fmt.Errorf("creating private reference snapshot directory: %w", err)
		}
		cleanup := func() error { return os.RemoveAll(references) }
		for _, file := range params.ExpectedFiles {
			if err := validateArtifactPath(file.Path); err != nil {
				return failedPreparation(cleanup, Invalid, err)
			}
			content, captured := input.WorkspaceFiles[filepath.ToSlash(file.Path)]
			if !captured {
				if input.WorkspaceDir == "" {
					return failedPreparation(cleanup, InsufficientEvidence, fmt.Errorf("required captured artifact %q is unavailable", file.Path))
				}
				content, err = readArtifact(input.WorkspaceDir, file.Path)
				if err != nil {
					state, wrapped := evidenceReadError(file.Path, err)
					return failedPreparation(cleanup, state, wrapped)
				}
			}
			evidence.WorkspaceFiles[filepath.ToSlash(file.Path)] = append([]byte{}, content...)
			if file.Snapshot != "" {
				contextDir := params.ContextDir
				if contextDir == "" {
					contextDir = "."
				}
				if state, err := captureArtifact(contextDir, references, file.Snapshot, true); err != nil {
					return failedPreparation(cleanup, state, err)
				}
			}
		}
		params.ContextDir = references
		return &evidence, params, cleanup, Observed, nil
	default:
		return &evidence, parameters, noop, Observed, nil
	}
}

func failedPreparation(cleanup func() error, state ObservationState, err error) (*graders.Context, models.GraderParameters, func() error, ObservationState, error) {
	if closeErr := cleanup(); closeErr != nil {
		state = OperationalError
		err = errors.Join(err, fmt.Errorf("cleaning failed reference preparation: %w", closeErr))
	}
	return nil, nil, nil, state, err
}

func validateArtifactPath(path string) error {
	if !filepath.IsLocal(path) || filepath.Clean(path) == "." {
		return fmt.Errorf("reference artifact path %q must stay within its evidence directory", path)
	}
	return nil
}

func captureArtifact(source, destination, path string, requireFile bool) (ObservationState, error) {
	if err := validateArtifactPath(path); err != nil {
		return Invalid, err
	}
	root, err := os.OpenRoot(source)
	if err != nil {
		return evidenceReadError(source, err)
	}
	info, err := root.Stat(path)
	missing := errors.Is(err, os.ErrNotExist)
	var content []byte
	if err == nil && info.Mode().IsRegular() {
		content, err = root.ReadFile(path)
	}
	if closeErr := root.Close(); closeErr != nil {
		return OperationalError, errors.Join(err, fmt.Errorf("closing reference source: %w", closeErr))
	}
	if err != nil {
		if missing {
			err = errors.Join(errMissingArtifact, err)
		}
		return evidenceReadError(path, err)
	}
	target := filepath.Join(destination, path)
	if info.IsDir() && !requireFile {
		if err := os.MkdirAll(target, 0o700); err != nil {
			return OperationalError, fmt.Errorf("preparing reference directory %q: %w", path, err)
		}
		return Observed, nil
	}
	if !info.Mode().IsRegular() {
		return NotAssessed, fmt.Errorf("reference artifact %q is not a supported regular file", path)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return OperationalError, fmt.Errorf("preparing reference parent directory: %w", err)
	}
	if err := os.WriteFile(target, content, 0o600); err != nil {
		return OperationalError, fmt.Errorf("preparing reference file %q: %w", path, err)
	}
	return Observed, nil
}

func readArtifact(directory, path string) ([]byte, error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	info, err := root.Stat(path)
	if err == nil && !info.Mode().IsRegular() {
		err = fmt.Errorf("artifact %q is not a regular file", path)
	}
	var content []byte
	if err == nil {
		content, err = root.ReadFile(path)
	}
	if closeErr := root.Close(); closeErr != nil {
		err = errors.Join(err, fmt.Errorf("closing reference source: %w", closeErr))
	}
	return content, err
}

func evidenceReadError(path string, err error) (ObservationState, error) {
	state := OperationalError
	if errors.Is(err, os.ErrNotExist) {
		state = InsufficientEvidence
	}
	return state, fmt.Errorf("reading reference evidence %q: %w", path, err)
}
