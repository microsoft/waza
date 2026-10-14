package faultfixture_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/commandmock"
	"github.com/microsoft/waza/internal/faultfixture"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func nativeCommandSession(t *testing.T, expectedCalls int) (string, string, *commandmock.Session) {
	t.Helper()
	environment, err := commandmock.RuntimeEnvironment()
	require.NoError(t, err)
	var root string
	for _, entry := range environment {
		if value, ok := strings.CutPrefix(entry, "WAZA_COMMAND_MOCK_ROOT="); ok {
			root = value
		}
	}
	require.NotEmpty(t, root)
	workspace := t.TempDir()
	mock := models.CommandMockConfig{Name: "inventory-cli", ExpectCalls: &expectedCalls,
		Responses: []models.CommandMockResponse{{Args: []string{"read"}, Stdout: "legacy must not run"}}}
	session, err := commandmock.NewNativeSession(context.Background(), workspace, []models.CommandMockConfig{mock}, workspace,
		func(_ context.Context, request commandmock.NativePrepareRequest) (json.RawMessage, error) {
			adapter, err := faultfixture.NewCommandAdapter(faultfixture.Envelope{
				Eval: []byte("schemaVersion: \"2.0\"\nscenario: native-command\n"),
				Kind: "command", Name: mock.Name,
				Responses: []faultfixture.Fragment{{Format: faultfixture.JSON, Data: []byte(
					`{"args":["read"],"sequence":[{"stderr":"temporarily unavailable","exit_code":75},{"stdout":"ready","exit_code":0}]}`,
				)}},
			}, request.StateDir, request.Workspace)
			if err != nil {
				return nil, err
			}
			return adapter.Configuration()
		})
	require.NoError(t, err)
	t.Cleanup(func() {
		// Individual tests assert capture errors before cleanup.
		_, closeErr := session.Close()
		if closeErr != nil {
			t.Errorf("closing native command session: %v", closeErr)
		}
		require.NoError(t, commandmock.CloseRuntime())
	})
	return root, workspace, session
}

func nativeCommandDispatch(ctx context.Context, request commandmock.NativeDispatchRequest, emit func(commandmock.NativeOutput) error, receipt func() error) (int, error) {
	envelope, err := faultfixture.DecodeEnvelope(request.Configuration)
	if err != nil {
		return 1, err
	}
	adapter, err := faultfixture.NewCommandAdapter(envelope, request.StateDir, request.Workspace)
	if err != nil {
		return 1, err
	}
	matcher, exitCode := -1, 1
	observer := func(response, _ int, transition faultfixture.Transition) error {
		if transition == faultfixture.Reserved {
			matcher = response
		}
		if transition == faultfixture.Delivered {
			return receipt()
		}
		return nil
	}
	err = adapter.Invoke(ctx, request.Args, request.CWD, observer, func(output faultfixture.CommandOutput) error {
		exitCode = output.ExitCode
		return emit(commandmock.NativeOutput{
			Stdout: output.Stdout, Stderr: output.Stderr, ExitCode: output.ExitCode, ResponseIndex: matcher,
		})
	})
	if err != nil {
		return 1, err
	}
	return exitCode, nil
}

func TestNativeCommandActualAdapterEmitsBeforePublishingReceipt(t *testing.T) {
	root, workspace, session := nativeCommandSession(t, 2)
	var stdout, stderr bytes.Buffer
	options := commandmock.NativeDispatchOptions{Dispatch: nativeCommandDispatch, Stdout: &stdout, Stderr: &stderr}
	code, err := commandmock.RunNativeCommand(context.Background(), root, session.ID(), "inventory-cli", []string{"read"}, workspace, options)
	require.NoError(t, err)
	require.Equal(t, 75, code)
	require.Empty(t, stdout.String())
	require.Equal(t, "temporarily unavailable", stderr.String())
	first := session.Invocations()
	require.Len(t, first, 1)
	require.Equal(t, 75, first[0].ExitCode)
	require.Equal(t, 0, first[0].ResponseIndex)
	code, err = commandmock.RunNativeCommand(context.Background(), root, session.ID(), "inventory-cli", []string{"read"}, workspace, options)
	require.NoError(t, err)
	require.Zero(t, code)
	require.Equal(t, "ready", stdout.String())
	require.NotContains(t, stdout.String(), "legacy")
	receipts, err := session.Close()
	require.NoError(t, err)
	require.Len(t, receipts, 2)
	require.Equal(t, 0, receipts[1].ExitCode)
}

func TestNativeCommandActualAdapterCannotReceiptFailedEmission(t *testing.T) {
	root, workspace, session := nativeCommandSession(t, 2)
	writerErr := errors.New("native stderr pipe unavailable")
	options := commandmock.NativeDispatchOptions{
		Dispatch: nativeCommandDispatch, Stdout: io.Discard,
		Stderr: nativeTransportWriter(func([]byte) (int, error) { return 0, writerErr }),
	}
	code, err := commandmock.RunNativeCommand(context.Background(), root, session.ID(), "inventory-cli", []string{"read"}, workspace, options)
	require.ErrorIs(t, err, writerErr)
	require.NotZero(t, code)
	require.Empty(t, session.Invocations(), "undelivered selection cannot create an exit0 receipt")
	var stdout bytes.Buffer
	options.Stdout, options.Stderr = &stdout, io.Discard
	code, err = commandmock.RunNativeCommand(context.Background(), root, session.ID(), "inventory-cli", []string{"read"}, workspace, options)
	require.NoError(t, err)
	require.Zero(t, code)
	require.Equal(t, "ready", stdout.String(), "failed emission consumed the first allocation")
	receipts, err := session.Close()
	require.ErrorContains(t, err, "capture")
	require.NotContains(t, err.Error(), "expected 2 call(s)", "attempt count must not be reduced to delivered receipts")
	require.Len(t, receipts, 1)
	require.Equal(t, 0, receipts[0].ExitCode, "the one real emission may report its configured code")
}
