package execution

import (
	"context"
	"errors"
	"fmt"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestCopilotAuthenticationCleanupOwnership(t *testing.T) {
	for _, diagnostic := range []bool{false, true} {
		for _, owned := range []bool{false, true} {
			for _, outcome := range []string{"healthy", "unauthenticated", "nil-auth", "auth-error"} {
				t.Run(fmt.Sprintf("diagnostic=%t/owned=%t/%s", diagnostic, owned, outcome), func(t *testing.T) {
					resetSharedClientForTest()
					t.Cleanup(resetSharedClientForTest)
					t.Setenv("COPILOT_BASE_URL", "")
					t.Setenv("COPILOT_PROVIDER_BASE_URL", "")
					client := NewMockCopilotClient(gomock.NewController(t))
					var lifetime context.Context
					client.EXPECT().Start(gomock.Any()).Times(1).DoAndReturn(func(ctx context.Context) error {
						lifetime = ctx
						return nil
					})
					auth := &copilot.GetAuthStatusResponse{IsAuthenticated: outcome == "healthy"}
					var authErr error
					if outcome == "nil-auth" {
						auth = nil
					}
					if outcome == "auth-error" {
						authErr = errors.New("authentication failure sentinel")
					}
					client.EXPECT().GetAuthStatus(gomock.Any()).Times(1).Return(auth, authErr)
					failed := outcome != "healthy"
					expectedStops := 1
					if owned && failed {
						expectedStops = 2
					}
					stops := 0
					client.EXPECT().Stop().Times(expectedStops).DoAndReturn(func() error {
						stops++
						if stops == 1 {
							assert.NoError(t, lifetime.Err(), "graceful SDK cleanup must precede lifetime cancellation")
						} else {
							assert.ErrorIs(t, lifetime.Err(), context.Canceled)
						}
						return nil
					})
					options := &CopilotEngineBuilderOptions{}
					if owned {
						options.NewCopilotClient = func(*copilot.ClientOptions) CopilotClient { return client }
					} else {
						originalConstruct := sharedConstruct
						t.Cleanup(func() { sharedConstruct = originalConstruct })
						sharedConstruct = func(*copilot.ClientOptions) CopilotClient { return client }
					}
					if diagnostic {
						options.DiagnosticObserver = func(ExecutionDiagnostic) error { return nil }
					}
					engine := NewCopilotEngineBuilder("test", options).Build()
					require.Equal(t, owned, engine.ownsClient)
					initErr := engine.Initialize(t.Context())
					if failed {
						require.Error(t, initErr)
						if authErr != nil && !diagnostic {
							require.ErrorIs(t, initErr, authErr)
						}
						require.EqualError(t, engine.Initialize(t.Context()), initErr.Error())
					} else {
						require.NoError(t, initErr)
						ctx, cancel := context.WithCancel(t.Context())
						cancel()
						require.NoError(t, engine.Initialize(ctx))
					}
					if owned && failed {
						require.Equal(t, 1, stops, "authentication failure cleans the owned client immediately")
					} else {
						require.Zero(t, stops, "healthy or shared clients are not stopped by Initialize")
					}
					require.NoError(t, engine.Shutdown(t.Context()))
					require.NoError(t, engine.Shutdown(t.Context()), "engine shutdown remains idempotent")
					if owned {
						require.Equal(t, expectedStops, stops)
					} else {
						require.Zero(t, stops, "an engine must not stop another shared owner")
						require.NoError(t, lifetime.Err())
					}
					require.NoError(t, ShutdownSharedClient(t.Context()))
					require.Equal(t, expectedStops, stops)
					require.ErrorIs(t, lifetime.Err(), context.Canceled)
				})
			}
		}
	}
}

func TestCopilotAuthenticationCleanupFailureRetained(t *testing.T) {
	for _, diagnostic := range []bool{false, true} {
		t.Run(fmt.Sprintf("diagnostic=%t", diagnostic), func(t *testing.T) {
			client := NewMockCopilotClient(gomock.NewController(t))
			engine := startupTestEngine(t, client, diagnostic)
			cleanupErr := errors.New("authentication cleanup sentinel")
			gomock.InOrder(
				client.EXPECT().Start(gomock.Any()).Times(1),
				client.EXPECT().GetAuthStatus(gomock.Any()).Times(1).Return(&copilot.GetAuthStatusResponse{}, nil),
				client.EXPECT().Stop().Times(1).Return(cleanupErr),
				client.EXPECT().Stop().Times(1).Return(nil),
			)
			initErr := engine.Initialize(t.Context())
			require.Error(t, initErr)
			if diagnostic {
				require.ErrorContains(t, initErr, string(CodeCleanup))
				require.NotContains(t, initErr.Error(), cleanupErr.Error())
			} else {
				require.ErrorIs(t, initErr, cleanupErr)
				require.ErrorContains(t, initErr, "not authenticated")
			}
			require.EqualError(t, engine.Initialize(t.Context()), initErr.Error())
			require.NoError(t, engine.Shutdown(t.Context()), "final cleanup is still attempted after auth cleanup fails")
		})
	}
}
