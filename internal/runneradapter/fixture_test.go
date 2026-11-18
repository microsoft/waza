package runneradapter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

type browserFixture struct {
	Descriptor  string          `json:"descriptor"`
	Translation json.RawMessage `json:"translation"`
	Reply       json.RawMessage `json:"reply"`
	ExitCode    int             `json:"exit_code"`
}

// The Lab spec mocks the translate route and the public submission route with
// this fixture. It must equal real adapter output for the shipped example
// descriptor and a real public submission result. Regenerate with
// WAZA_UPDATE_ADAPTER_FIXTURE=1.
func TestBrowserFixtureMatchesAdapterOutput(t *testing.T) {
	descriptor, err := os.ReadFile("../../examples/runner/adapter/submit.json")
	require.NoError(t, err)
	translation := Translate(descriptor)
	require.Equal(t, "translated", translation.Status, "%+v", translation.Diagnostics)
	encoded, err := EncodeTranslation(translation)
	require.NoError(t, err)
	reply, err := os.ReadFile("testdata/submission-completed.json")
	require.NoError(t, err)
	mapped := MapReply(Result{Kind: ResultKind, SchemaVersion: SchemaVersion, Adapter: translation.Adapter}, OperationSubmit, Reply{Body: reply})
	require.Equal(t, StatusTerminal, mapped.Status, "%+v", mapped.Diagnostics)

	want, err := json.MarshalIndent(browserFixture{Descriptor: string(descriptor), Translation: encoded, Reply: json.RawMessage(reply)}, "", "  ")
	require.NoError(t, err)
	want = append(want, '\n')
	path := filepath.Join("..", "..", "web", "e2e", "fixtures", "runner-adapter.json")
	if os.Getenv("WAZA_UPDATE_ADAPTER_FIXTURE") == "1" {
		require.NoError(t, os.WriteFile(path, want, 0o600))
	}
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.JSONEq(t, string(want), string(got), "regenerate with WAZA_UPDATE_ADAPTER_FIXTURE=1")
}
