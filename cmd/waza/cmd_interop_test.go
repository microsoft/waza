package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/interop"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestInteropProductionCLIUpstreamDecisions(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "waza")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, ".")
	log, err := build.CombinedOutput()
	require.NoError(t, err, "%s", log)
	fixture := testutil.RepoFile(t, "internal", "interop", "testdata", "agentv", "suite.yaml")
	oracle := testutil.RepoFile(t, "internal", "interop", "testdata", "agentv", "assertions.ts")
	source, err := os.ReadFile(fixture)
	require.NoError(t, err)
	invoke := func(input []byte, args ...string) ([]byte, string, error) {
		cmd := exec.CommandContext(t.Context(), binary, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "WAZA_NO_UPDATE_CHECK=1", "NO_COLOR=1", "ENABLE_COPILOT_TESTS=false")
		cmd.Stdin = bytes.NewReader(input)
		var out, diagnostics bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &diagnostics
		err := cmd.Run()
		return out.Bytes(), diagnostics.String(), err
	}
	native, diagnostics, err := invoke(source, "interop", "import", "--adapter", interop.Adapter, "--test", "contains-check", "--assertion", "1")
	require.NoError(t, err, diagnostics)
	require.Contains(t, diagnostics, "ASSERTION-ONLY PROJECTION")
	require.Contains(t, diagnostics, "NOT converted or validated")
	require.Contains(t, string(native), "contains_cs")
	exported, diagnostics, err := invoke(native, "interop", "export", "--adapter", interop.Adapter)
	require.NoError(t, err, diagnostics)
	require.Contains(t, string(exported), "value: Hello")

	eval := "schemaVersion: \"1.0\"\nname: assertion-conformance\nskill: assertion-conformance\nversion: \"1.0\"\nconfig:\n  executor: mock\n  model: mock-model\n  trials_per_task: 1\n  timeout_seconds: 30\ntasks: [task.yaml]\nmetrics: []\ngraders:\n"
	for _, line := range strings.Split(strings.TrimSpace(string(native)), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "name:") {
			eval += "  - " + line + "\n"
		} else {
			eval += "    " + line + "\n"
		}
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "eval.yaml"), []byte(eval), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"), []byte("id: contains-check\nname: Assertion conformance\ninputs:\n  prompt: Conformance uses supplied output, not inference.\n"), 0o600))
	outputs := []string{"Hello world", "hello world", "world", "", "  Hello\n", "HELLO", "你好 Hello"}
	payload, err := json.Marshal(outputs)
	require.NoError(t, err)
	// Execute the retained, reviewed upstream module itself; do not reimplement
	// includes in the test oracle. Node's type stripping requires no npm packages.
	node := exec.CommandContext(t.Context(), "node", "--experimental-strip-types", "--input-type=module", "-e",
		`import {pathToFileURL} from 'node:url'; const {runContainsAssertion}=await import(pathToFileURL(process.argv[1])); console.log(JSON.stringify(JSON.parse(process.argv[2]).map(output=>runContainsAssertion(output,"Hello"))));`,
		oracle, string(payload))
	oracleJSON, err := node.CombinedOutput()
	require.NoError(t, err, "%s", oracleJSON)
	var decisions []struct {
		Score float64 `json:"score"`
	}
	require.NoError(t, json.Unmarshal(oracleJSON, &decisions))
	require.Len(t, decisions, len(outputs))
	baseline, err := models.LoadEvaluationOutcome(testutil.RepoFile(t, "internal", "testdata", "compatibility", "v1", "results-1.0.json"))
	require.NoError(t, err)
	for i, output := range outputs {
		baseline.TestOutcomes[0].TestID = "contains-check"
		baseline.TestOutcomes[0].Runs[0].FinalOutput = output
		results, err := json.Marshal(baseline)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "results.json"), results, 0o600))
		out, diagnostic, err := invoke(nil, "grade", filepath.Join(dir, "eval.yaml"), "--results", filepath.Join(dir, "results.json"))
		require.NoError(t, err, "%s", diagnostic)
		var decision struct {
			Passed       *bool    `json:"passed"`
			OverallScore *float64 `json:"overall_score"`
			Tasks        map[string]struct {
				Passed       *bool    `json:"passed"`
				OverallScore *float64 `json:"overall_score"`
			} `json:"tasks"`
		}
		require.NoError(t, json.Unmarshal(out, &decision), "%s", out)
		require.NotNil(t, decision.Passed)
		require.NotNil(t, decision.OverallScore)
		require.Equal(t, decisions[i].Score == 1, *decision.Passed, "output %q", output)
		require.Equal(t, decisions[i].Score, *decision.OverallScore, "output %q", output)
		require.Len(t, decision.Tasks, 1)
		require.Contains(t, decision.Tasks, "contains-check")
		require.NotNil(t, decision.Tasks["contains-check"].Passed)
		require.NotNil(t, decision.Tasks["contains-check"].OverallScore)
		require.Equal(t, *decision.Passed, *decision.Tasks["contains-check"].Passed)
		require.Equal(t, decisions[i].Score, *decision.Tasks["contains-check"].OverallScore)
	}
	for _, args := range [][]string{
		{"interop", "import", "--adapter", "agentv-latest", "--test", "contains-check", "--assertion", "1"},
		{"interop", "import", "--adapter", interop.Adapter, "--test", "contains-check", "--assertion", "2"},
		{"interop", "import", "--adapter", interop.Adapter},
	} {
		out, diagnostic, err := invoke(source, args...)
		var exit *exec.ExitError
		require.ErrorAs(t, err, &exit)
		require.Equal(t, ExitError, exit.ExitCode(), diagnostic)
		require.Empty(t, out)
	}
	t.Run("selected identity cannot bypass interpolation guard", func(t *testing.T) {
		mutated := bytes.Replace(source, []byte("id: contains-check"), []byte("id: ${TEST_ID}"), 1)
		require.NotEqual(t, source, mutated)
		out, diagnostics, err := invoke(mutated, "interop", "import", "--adapter", interop.Adapter, "--test", "${TEST_ID}", "--assertion", "1")
		require.Error(t, err)
		require.Contains(t, diagnostics, "literal identity without interpolation")
		require.Empty(t, out)
	})
	t.Run("Nunjucks syntax is rejected in both directions", func(t *testing.T) {
		for _, value := range []string{"He{# note #}llo", "He{% if true %}ll{% endif %}o"} {
			t.Run(value, func(t *testing.T) {
				mutated := bytes.Replace(source, []byte("value: Hello"), []byte("value: "+strconv.Quote(value)), 1)
				require.NotEqual(t, source, mutated)
				native := "name: greeting\ntype: text\nconfig:\n  contains_cs:\n    - " + strconv.Quote(value) + "\n"
				for operation, input := range map[string][]byte{"import": mutated, "export": []byte(native)} {
					args := []string{"interop", operation, "--adapter", interop.Adapter}
					if operation == "import" {
						args = append(args, "--test", "contains-check", "--assertion", "1")
					}
					out, diagnostics, err := invoke(input, args...)
					var exit *exec.ExitError
					require.ErrorAs(t, err, &exit)
					require.Equal(t, ExitError, exit.ExitCode(), diagnostics)
					require.Contains(t, diagnostics, "expected nonempty")
					require.Empty(t, out, operation)
				}
			})
		}
	})
	t.Run("non-template braces percent and hash remain literal", func(t *testing.T) {
		for _, value := range []string{"He{note}llo", "He%llo", "He#llo", "He%}llo", "He#}llo"} {
			mutated := bytes.Replace(source, []byte("value: Hello"), []byte("value: "+value), 1)
			require.NotEqual(t, source, mutated)
			native, diagnostics, err := invoke(mutated, "interop", "import", "--adapter", interop.Adapter, "--test", "contains-check", "--assertion", "1")
			require.NoError(t, err, diagnostics)
			external, diagnostics, err := invoke(native, "interop", "export", "--adapter", interop.Adapter)
			require.NoError(t, err, diagnostics)
			require.Contains(t, string(external), "value: "+value+"\n")
		}
	})
}

type interopBrokenReader struct{}

func (interopBrokenReader) Read([]byte) (int, error) { return 0, errors.New("input failure") }

type interopShortWriter struct{}

func (interopShortWriter) Write([]byte) (int, error) { return 0, nil }

func TestInteropCommandIOAndOfflinePolicy(t *testing.T) {
	input := "name: greeting\ntype: text\nconfig:\n  contains_cs: [Hello]\n"
	for _, tc := range []struct {
		name string
		in   io.Reader
		out  io.Writer
		want string
	}{
		{"read failure", interopBrokenReader{}, io.Discard, "reading definition"},
		{"short write", strings.NewReader(input), interopShortWriter{}, "short write"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newRootCommand()
			cmd.SetIn(tc.in)
			cmd.SetOut(tc.out)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"interop", "export", "--adapter", interop.Adapter})
			require.ErrorContains(t, cmd.Execute(), tc.want)
			child, _, err := cmd.Find([]string{"interop", "export"})
			require.NoError(t, err)
			require.False(t, shouldRunUpdateCheck(child, false))
		})
	}
	cmd := newInteropCommand()
	cmd.SetIn(strings.NewReader("tests: [{id: t, assert: [{type: contains, value: Hello}]}]"))
	cmd.SetErr(interopShortWriter{})
	cmd.SetOut(io.Discard)
	cmd.SetArgs([]string{"import", "--adapter", interop.Adapter, "--test", "t", "--assertion", "1"})
	require.ErrorContains(t, cmd.Execute(), "writing projection diagnostic: short write")
}
