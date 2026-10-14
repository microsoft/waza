package commandmock

import (
	"path/filepath"

	"github.com/microsoft/waza/internal/models"
)

// MatchResponse checks invocation arguments, environment, and working directory
// using the native command-mock matcher semantics.
func MatchResponse(response models.CommandMockResponse, args []string, cwd, workspace string) (bool, error) {
	workDir := ""
	if response.WorkDir != "" {
		workDir = filepath.ToSlash(filepath.Clean(response.WorkDir))
	}
	return responseMatches(storedResponse{
		Args:        response.Args,
		ArgsRegex:   response.ArgsRegex,
		Environment: response.Environment,
		WorkDir:     workDir,
		HasWorkDir:  response.WorkDir != "",
	}, args, cwd, workspace)
}

// ResponseOutput returns fixture bytes, string stdout, or JSON-encoded stdout
// using the native command-mock output semantics.
func ResponseOutput(response models.CommandMockResponse, baseDir string) ([]byte, error) {
	return responseOutput(response, baseDir)
}
