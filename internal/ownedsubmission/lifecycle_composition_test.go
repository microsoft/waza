package ownedsubmission

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/stretchr/testify/require"
)

func TestLifecycleCaptureCompositionRejectsMissingClockWithoutRuntime(t *testing.T) {
	hooks, err := NewLifecycleCaptureHooks(nil)
	require.Error(t, err)
	require.Nil(t, hooks.Factory)
	require.Nil(t, hooks.SelectLifecycle)
}

func TestLifecycleCaptureCompositionActualSourceOriginalFailureCancellationAndReuse(t *testing.T) {
	for _, control := range []string{"success", "grader-failure", "canceled"} {
		t.Run(control, func(t *testing.T) {
			backend := captureBackend(t)
			request := captureRequest(t)
			wantCode := 0
			if control == "grader-failure" {
				request.Input.Task = strings.Replace(request.Input.Task, "- hello", "- absent-composition-control", 1)
				require.Contains(t, request.Input.Task, "absent-composition-control")
				wantCode = 1
			}
			hooks, err := NewLifecycleCaptureHooks(time.Now)
			require.NoError(t, err)
			calls := 0
			factory := hooks.Factory
			hooks.Factory = func(config models.Config) (execution.AgentEngine, error) {
				calls++
				return factory(config)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if control == "canceled" {
				wantCode = 3
				reserved := hooks.Reserved
				hooks.Reserved = func(ctx context.Context, ledger *executionledger.Ledger,
					creator executionledger.Creator, reservation executionledger.Reservation) error {
					if err := reserved(ctx, ledger, creator, reservation); err != nil {
						return err
					}
					cancel()
					return nil
				}
			}
			service := runnerunit.NewService(func(models.Config) (execution.AgentEngine, error) {
				t.Fatal("composition must not mutate or call retained Service fallback")
				return nil, nil
			})
			result, err := service.Submit(ctx, captureWire(t, request, nil), backend, hooks)
			require.NoError(t, err)
			require.Equal(t, wantCode, result.ExitCode)
			require.Empty(t, result.Report)
			ledger, err := executionledger.New(backend)
			require.NoError(t, err)
			source := executionattempt.Source{ExecutionID: result.Reservation.ExecutionID, ManifestDigest: result.Completion.ManifestDigest}
			completed, err := ledger.ReadCompleted(t.Context(), source, "lifecycle.json",
				"lifecycle-observation-001.json", "lifecycle-observation-002.json", "terminal.json", "execution-attempt.json")
			require.NoError(t, err)
			require.Equal(t, completed.Reservation.Bytes, completed.Publication.ArtifactBytes["execution-attempt.json"])
			var capture runnerunit.LifecycleCapture
			require.NoError(t, json.Unmarshal(completed.Publication.ArtifactBytes["lifecycle.json"], &capture))
			require.Equal(t, completed.Reservation.Bytes, []byte(capture.Reservation))
			require.Equal(t, completed.Reservation.Attempt.Binding, capture.OriginalAdmission)
			require.Equal(t, result.Completion.ReservationDigest, capture.ReservationDigest)
			require.False(t, capture.Runtime.OwnedEngineBound, "mock/canceled paths must not invent a native actor")
			require.NotNil(t, capture.Runtime.Observations)
			require.Empty(t, capture.Runtime.Observations)
			require.Equal(t, "unknown", capture.Runtime.ProcessExit)
			encoded, err := runnerterminal.Encode(*capture.Observations[1].Terminal, capture.OriginalAdmission.TerminalBinding())
			require.NoError(t, err)
			require.Equal(t, completed.Publication.ArtifactBytes["terminal.json"], encoded)
			for i, name := range []string{"lifecycle-observation-001.json", "lifecycle-observation-002.json"} {
				require.Equal(t, capture.ObservationDigests[i], artifactmanifest.Hash(completed.Publication.ArtifactBytes[name]))
			}
			originalCalls := calls
			request.Selection.Attempt++
			reuseHooks, err := NewLifecycleCaptureHooks(time.Now)
			require.NoError(t, err)
			reuseHooks.Factory = func(models.Config) (execution.AgentEngine, error) {
				t.Fatal("reuse must not construct an engine or bind new observer")
				return nil, nil
			}
			reused, err := service.Submit(t.Context(), captureWire(t, request, &source), backend, reuseHooks)
			require.NoError(t, err)
			require.Equal(t, originalCalls, calls)
			require.Zero(t, reused.EngineCalls)
			require.Equal(t, result.Terminal, reused.Terminal)
		})
	}
}
