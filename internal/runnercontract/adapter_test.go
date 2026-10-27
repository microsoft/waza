package runnercontract

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunnerLocalAdapterHistoricalSchemas(t *testing.T) {
	for _, version := range []string{"1.0", "1.1", "1.4", "1.7", "2.0"} {
		t.Run(version, func(t *testing.T) {
			original := fixture(t)
			eval := original.Input.Eval
			if version != "2.0" {
				eval = strings.Replace(eval, "schemaVersion: \"2.0\"\nscenario: runner-conformance\n",
					"schemaVersion: \""+version+"\"\n", 1)
				eval = strings.Replace(eval, "executor: mock", "executor: mock\n  disabled_skills: [\"*\"]", 1)
			}
			adapted, err := AdaptLocal([]byte(eval), []byte(original.Input.Task),
				original.RunKey, original.Selection, original.TimeoutMS)
			require.NoError(t, err)
			require.Equal(t, eval, adapted.Input.Eval, "adapter must not rewrite source")
			result := NewService().Run(t.Context(), wire(t, adapted))
			require.Equal(t, Success, result.Outcome, "%+v", result.Diagnostics)
			require.Equal(t, original.Selection, *result.Resolved)
		})
	}
}

func TestRunnerFrozenLegacyCorpusNotSilentlyReinterpreted(t *testing.T) {
	// These independent pre-runner fixtures remain untouched. Their skill and
	// multiturn semantics are supported by local run, not the inline profile.
	eval, err := os.ReadFile("../testdata/compatibility/v1/eval-1.0.yaml")
	require.NoError(t, err)
	task, err := os.ReadFile("../testdata/compatibility/v1/task.yaml")
	require.NoError(t, err)
	selection := Selection{Shard: "historical", Task: "compatibility-task", Trial: 1, Attempt: 1}
	_, err = AdaptLocal(eval, task, "historical", selection, 30000)
	require.ErrorContains(t, err, "cannot preserve local semantics")
	require.ErrorContains(t, err, "waza run")
}

func TestRunnerPersistedEnvelopeRejectsLossyReload(t *testing.T) {
	result := NewService().Run(t.Context(), wire(t, fixture(t)))
	data := wireResult(t, &result)
	for _, bad := range []string{
		strings.Replace(data, `"outcome":"success"`, `"outcome":"success","outcome":"success"`, 1),
		strings.Replace(data, `"exit_code":0`, `"exit_code":2`, 1),
		strings.Replace(data, `"schema_version":"1.0"`, `"schema_version":"1.0","unknown":true`, 1),
		strings.Replace(data, `"selection_valid":true`, `"selection_valid":null`, 1),
	} {
		_, err := ParseResult([]byte(bad))
		require.Error(t, err)
	}
	parsed, err := ParseResult([]byte(data))
	require.NoError(t, err)
	require.Equal(t, result.Identity, parsed.Identity)
}

type shortWriter struct{}

func (shortWriter) Write(data []byte) (int, error) { return len(data) - 1, nil }

func TestRunnerTerminalWriterRejectsPartialDelivery(t *testing.T) {
	result := NewService().Run(t.Context(), wire(t, fixture(t)))
	require.ErrorIs(t, Write(shortWriter{}, &result), io.ErrShortWrite)
}

func wireResult(t *testing.T, result *Result) string {
	t.Helper()
	var b strings.Builder
	require.NoError(t, Write(&b, result))
	return b.String()
}
