package execution

import (
	"context"
	"testing"

	copilot "github.com/microsoft/copilot-sdk/go/v2"
	"github.com/stretchr/testify/require"
)

// External runtimes that speak protocol 3 (the v1 SDK line, paired with CLIs such
// as 1.0.85) must be rejected during engine initialization, before any session.
func TestCopilotEngineRejectsProtocolV3Runtime(t *testing.T) {
	for _, tc := range []struct {
		name   string
		legacy bool
	}{
		{name: "connect handshake"},
		{name: "legacy ping fallback", legacy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, address := newDiagnosticRPCServer(t, func(s *diagnosticRPCServer, req diagnosticRPCRequest) {
				switch {
				case req.Method == "connect" && tc.legacy:
					s.write(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "Unhandled method connect"}})
				case req.Method == "connect", req.Method == "ping":
					s.respond(req, map[string]any{"message": "pong", "timestamp": "2026-01-01T00:00:00Z", "protocolVersion": 3}, false)
				default:
					s.t.Errorf("unexpected SDK method %s after protocol rejection", req.Method)
					s.respond(req, nil, true)
				}
			})
			engine := NewCopilotEngineBuilder("requested-model", &CopilotEngineBuilderOptions{
				NewCopilotClient: func(options *copilot.ClientOptions) CopilotClient {
					useTCPRuntime(t, options, address)
					return NewOwnedCopilotClient(options)
				},
			}).Build()
			engine.provider = customProviderConfig{}
			t.Cleanup(func() { require.NoError(t, engine.Shutdown(context.Background())) })

			err := engine.Initialize(t.Context())
			require.ErrorContains(t, err, "SDK protocol version mismatch")
			require.ErrorContains(t, err, "server reports version 3")
			wantMethod := "connect"
			if tc.legacy {
				wantMethod = "ping"
			}
			require.Equal(t, 1, server.count(wantMethod, ""), "fixture must exercise the %s path", wantMethod)
			require.Zero(t, server.count("session.create", ""))
		})
	}
}
