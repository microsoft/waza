package copilotconfig

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPureLiveMCPDecodersMatchRuntimeConversion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		config  map[string]any
		http    bool
		invalid bool
	}{
		{"stdio defaults", map[string]any{"command": "never-run"}, false, false},
		{"stdio explicit tools", map[string]any{"command": "never-run", "tools": []string{}}, false, false},
		{"HTTP defaults", map[string]any{"type": "http", "url": "https://never-contact.invalid"}, true, false},
		{"HTTP headers", map[string]any{"type": "http", "url": "https://never-contact.invalid", "headers": map[string]any{"Authorization": "secret"}}, true, false},
		{"HTTP malformed headers", map[string]any{"type": "http", "url": "https://never-contact.invalid", "headers": []string{"secret"}}, true, true},
		{"stdio malformed environment", map[string]any{"command": "never-run", "env": []string{"secret"}}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var warnings int
			converted := ConvertMCPServers(map[string]any{"server": tc.config}, func(string, ...any) { warnings++ })
			if tc.http {
				decoded, err := DecodeMCPHTTPServer(tc.config)
				if tc.invalid {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
					require.Equal(t, decoded, converted["server"])
				}
			} else {
				decoded, err := DecodeMCPStdioServer(tc.config)
				if tc.invalid {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
					require.Equal(t, decoded, converted["server"])
				}
			}
			if tc.invalid {
				require.Nil(t, converted)
				require.Equal(t, 1, warnings)
			} else {
				require.Zero(t, warnings)
			}
		})
	}
}
