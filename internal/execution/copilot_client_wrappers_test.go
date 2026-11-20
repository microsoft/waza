package execution

import (
	"context"
	"errors"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestCopilotSessionWrapper_CheckSandboxRuntime(t *testing.T) {
	statusErr := errors.New("status unavailable")
	for name, tc := range map[string]struct {
		status  *copilot.GetStatusResponse
		err     error
		wantErr string
	}{
		"minimum":          {status: &copilot.GetStatusResponse{Version: "1.0.80"}},
		"newer":            {status: &copilot.GetStatusResponse{Version: "1.0.85"}},
		"future major":     {status: &copilot.GetStatusResponse{Version: "2.0.0"}},
		"build metadata":   {status: &copilot.GetStatusResponse{Version: "1.0.80+build"}},
		"older":            {status: &copilot.GetStatusResponse{Version: "1.0.79"}, wantErr: "upgrade COPILOT_CLI_PATH"},
		"older major":      {status: &copilot.GetStatusResponse{Version: "0.99.999"}, wantErr: "upgrade COPILOT_CLI_PATH"},
		"prerelease":       {status: &copilot.GetStatusResponse{Version: "1.0.80-preview"}, wantErr: "upgrade COPILOT_CLI_PATH"},
		"unknown":          {status: &copilot.GetStatusResponse{Version: "development"}, wantErr: "cannot verify runtime version"},
		"partial version":  {status: &copilot.GetStatusResponse{Version: "2"}, wantErr: "cannot verify runtime version"},
		"empty version":    {status: &copilot.GetStatusResponse{}, wantErr: "cannot verify runtime version"},
		"missing response": {wantErr: "runtime returned no version"},
		"RPC failure":      {err: statusErr, wantErr: "status unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			session := &copilotSessionWrapper{
				getStatus: func(ctx context.Context) (*copilot.GetStatusResponse, error) {
					require.Equal(t, t.Context(), ctx)
					return tc.status, tc.err
				},
			}
			err := session.checkSandboxRuntime(t.Context())
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.wantErr)
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
			}
			// No SDK session is needed: rejection must precede every policy RPC.
			err = session.ConfigureSandbox(t.Context(), "", nil, models.SandboxConfig{Enabled: true})
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestCopilotSessionWrapper_DisabledSandboxSkipsRuntimeCheck(t *testing.T) {
	session := &copilotSessionWrapper{
		getStatus: func(context.Context) (*copilot.GetStatusResponse, error) {
			t.Fatal("disabled sandbox must not query the runtime version")
			return nil, nil
		},
	}
	require.NoError(t, session.ConfigureSandbox(t.Context(), "", nil, models.SandboxConfig{}))
}
