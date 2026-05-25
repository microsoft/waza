//go:build !((linux && (amd64 || arm64)) || (darwin && (amd64 || arm64)) || (windows && (amd64 || arm64)))

package embedded

import (
	"fmt"
	"runtime"
)

// Path reports that this platform does not have a bundled Copilot CLI.
func Path() (string, error) {
	return "", fmt.Errorf("embedded Copilot CLI is not bundled for %s/%s; set COPILOT_CLI_PATH to use an explicit GitHub Copilot CLI binary", runtime.GOOS, runtime.GOARCH)
}
