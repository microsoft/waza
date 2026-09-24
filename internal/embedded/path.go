//go:build (linux && (amd64 || arm64)) || (darwin && (amd64 || arm64)) || (windows && (amd64 || arm64))

package embedded

import (
	"fmt"

	"github.com/github/copilot-sdk/go/embeddedcli"
)

// Path installs the embedded Copilot CLI and its runtime assets, if needed.
func Path() (string, error) {
	path := embeddedcli.Path()
	if path == "" {
		return "", fmt.Errorf("installing embedded Copilot CLI failed; check cache directory permissions and disk space, set COPILOT_CLI_INSTALL_VERBOSE=1 for details, or set COPILOT_CLI_PATH to a compatible CLI")
	}
	return path, nil
}
