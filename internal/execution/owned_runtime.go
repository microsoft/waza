package execution

import (
	"errors"
	"fmt"
	"os"
)

var ErrOwnedRuntime = errors.New("owned execution runtime is not admitted")

// ValidateOwnedRuntime is restricted to additive owned execution/restore paths.
// It does not change ordinary runner/SDK environment behavior, resolve a CLI,
// record credentials, or claim a revision attests runtime identity.
func ValidateOwnedRuntime(mode string) error {
	switch mode {
	case "mock":
		return nil
	case "copilot":
		if os.Getenv("COPILOT_CLI_PATH") != "" {
			return fmt.Errorf("%w: clear COPILOT_CLI_PATH for owned native execution or use the legacy runner", ErrOwnedRuntime)
		}
		provider := providerFromEnv()
		if provider.config != nil || provider.err != nil {
			return fmt.Errorf("%w: ambient custom providers are unsupported for owned native execution; clear COPILOT_BASE_URL and COPILOT_PROVIDER_BASE_URL or use the legacy runner", ErrOwnedRuntime)
		}
		return nil
	default:
		return fmt.Errorf("%w: expected mock or copilot execution mode", ErrOwnedRuntime)
	}
}
