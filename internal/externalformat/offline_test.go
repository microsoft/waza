package externalformat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/microsoft/waza/internal/graders"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	if os.Getenv("WAZA_EXTERNAL_PROCESS_TRIPWIRE") == "1" {
		if err := os.WriteFile(os.Getenv("WAZA_EXTERNAL_PROCESS_MARKER"), []byte("attempted"), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(98)
		}
		os.Exit(97)
	}
	os.Exit(m.Run())
}

type rejectingTransport struct {
	calls atomic.Int32
}

func (r *rejectingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	r.calls.Add(1)
	return nil, errors.New("external admission must not access the network")
}

func TestStaticAdmissionAndGradingSentinels(t *testing.T) {
	source := conformanceSource(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "process-attempt")
	t.Setenv("WAZA_EXTERNAL_PROCESS_TRIPWIRE", "1")
	t.Setenv("WAZA_EXTERNAL_PROCESS_MARKER", marker)
	t.Setenv("COPILOT_CLI_PATH", os.Args[0])

	// Arm the process guard before relying on its absence as evidence.
	err := exec.CommandContext(context.Background(), os.Args[0]).Run()
	require.Error(t, err)
	var exitError *exec.ExitError
	require.ErrorAs(t, err, &exitError)
	require.Equal(t, 97, exitError.ExitCode())
	contents, err := os.ReadFile(marker)
	require.NoError(t, err)
	require.Equal(t, "attempted", string(contents))
	require.NoError(t, os.Remove(marker))

	transport := &rejectingTransport{}
	previous := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = previous })
	response, err := http.Get("http://offline-sentinel.invalid")
	require.ErrorContains(t, err, "must not access the network")
	require.Nil(t, response)
	require.Equal(t, int32(1), transport.calls.Load())
	transport.calls.Store(0)

	// An accidental native runtime-config discovery would encounter invalid YAML.
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".waza.yaml"), []byte("invalid: ["), 0o600))
	t.Chdir(dir)
	suite, err := Import(source, selected)
	require.NoError(t, err)
	_, err = Export(*suite, selected)
	require.NoError(t, err)
	for _, file := range suite.Tasks {
		task := loadNative(t, file) // rejecting captured-source read callback
		results, err := graders.RunAll(context.Background(), nil, task,
			&graders.Context{Output: "Hello world", OutputPresent: true}, "", "", false)
		require.NoError(t, err)
		require.Len(t, results, 1)
	}
	unsupported := append([]byte("providers: [llm]\n"), source...)
	rejected, err := Import(unsupported, selected)
	require.Error(t, err)
	require.Nil(t, rejected)
	require.Zero(t, transport.calls.Load())
	_, err = os.Stat(marker)
	require.ErrorIs(t, err, os.ErrNotExist)
}
