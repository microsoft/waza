package execution

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateOwnedRuntimeUsesEffectiveProviderWithoutRecordingSecrets(t *testing.T) {
	for _, scenario := range []struct {
		name string
		mode string
		env  map[string]string
		deny bool
	}{
		{"embedded-default", "copilot", nil, false},
		{"canonical-provider", "copilot", map[string]string{"COPILOT_BASE_URL": "https://test.invalid", "COPILOT_API_KEY": "not-for-diagnostics"}, true},
		{"alias-provider", "copilot", map[string]string{"COPILOT_PROVIDER_BASE_URL": "https://test.invalid", "COPILOT_PROVIDER_BEARER_TOKEN": "not-for-diagnostics"}, true},
		{"invalid-provider", "copilot", map[string]string{"COPILOT_BASE_URL": "not-for-diagnostics"}, true},
		{"empty-canonical-effective-alias", "copilot", map[string]string{"COPILOT_BASE_URL": "", "COPILOT_PROVIDER_BASE_URL": "https://test.invalid"}, true},
		{"cli-override", "copilot", map[string]string{"COPILOT_CLI_PATH": "not-for-diagnostics"}, true},
		{"whitespace-cli-override", "copilot", map[string]string{"COPILOT_CLI_PATH": " "}, true},
		{"inactive-provider-fields", "copilot", map[string]string{"COPILOT_API_KEY": "not-for-diagnostics", "COPILOT_PROVIDER_TYPE": "openai"}, false},
		{"mock-ignores-native-environment", "mock", map[string]string{"COPILOT_CLI_PATH": "not-for-diagnostics", "COPILOT_BASE_URL": "https://test.invalid"}, false},
		{"unknown-mode", "", nil, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			for _, name := range []string{
				"COPILOT_CLI_PATH", "COPILOT_BASE_URL", "COPILOT_PROVIDER_BASE_URL",
				"COPILOT_PROVIDER", "COPILOT_PROVIDER_TYPE", "COPILOT_WIRE_API", "COPILOT_PROVIDER_WIRE_API",
				"COPILOT_API_KEY", "COPILOT_PROVIDER_API_KEY", "COPILOT_BEARER_TOKEN", "COPILOT_PROVIDER_BEARER_TOKEN",
			} {
				t.Setenv(name, "")
			}
			for name, value := range scenario.env {
				t.Setenv(name, value)
			}
			err := ValidateOwnedRuntime(scenario.mode)
			if scenario.deny != errors.Is(err, ErrOwnedRuntime) || (!scenario.deny && err != nil) {
				t.Fatalf("effective owned admission mismatch: %v", err)
			}
			if err != nil && (strings.Contains(err.Error(), "not-for-diagnostics") || strings.Contains(err.Error(), "https://test.invalid")) {
				t.Fatalf("runtime admission recorded provider/path/credential values: %v", err)
			}
		})
	}
}
