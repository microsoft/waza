package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/controlledcomparison"
	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/microsoft/waza/internal/testutil"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func buildOfflineComparisonCLI(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "waza")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	log, err := exec.CommandContext(t.Context(), "go", "build", "-o", binary, ".").CombinedOutput()
	require.NoError(t, err, "%s", log)
	return binary
}

func runOfflineComparisonCLI(t *testing.T, binary string, args ...string) ([]byte, int) {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.CommandContext(t.Context(), binary, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "ENABLE_COPILOT_TESTS=false", "NO_COLOR=1",
		"HOME="+dir, "USERPROFILE="+dir, "XDG_CONFIG_HOME="+dir, "WAZA_NO_UPDATE_CHECK=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code := 0
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		require.ErrorAs(t, err, &exit, "stderr=%s", stderr.String())
		code = exit.ExitCode()
	}
	t.Logf("command=%v exit=%d stderr=%s", args, code, stderr.String())
	return stdout.Bytes(), code
}

func acceptedOfflineComparison(t *testing.T) (string, string) {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("private durable release collection unsupported")
	}
	root := testutil.RepoFile(t, "examples", "controlled-comparison")
	read := func(name string) []byte {
		data, err := os.ReadFile(filepath.Join(root, name))
		require.NoError(t, err)
		return data
	}
	sourceDir := t.TempDir()
	eval, task := string(read("eval.yaml")), string(read("task.yaml"))
	var design releasepolicy.Design
	require.NoError(t, json.Unmarshal(read("design.json"), &design))
	design.Clusters = nil
	design.Margin = "1"
	design.Independence = releasepolicy.Independence{
		Assessment: "justified", Justification: "Independent synthetic test clusters for CLI admission only.",
		Limitations: []string{"Deterministic mock observations are not live agent-quality evidence."},
	}
	taskNames := []string{}
	for i := range 8 {
		id, name := fmt.Sprintf("task-%d", i), fmt.Sprintf("task-%d.yaml", i)
		require.NoError(t, os.WriteFile(filepath.Join(sourceDir, name),
			[]byte(strings.Replace(task, "id: hello", "id: "+id, 1)), 0o600))
		taskNames = append(taskNames, "  - "+name)
		design.Clusters = append(design.Clusters, releasepolicy.Cluster{
			ID: fmt.Sprintf("cluster-%d", i), Weight: "0.125",
			Tasks: []releasepolicy.PlannedTask{{ID: id, Weight: "1", TrialOrdinals: []int{1, 2}}},
		})
	}
	evalPath := filepath.Join(sourceDir, "eval.yaml")
	require.NoError(t, os.WriteFile(evalPath,
		[]byte(strings.Replace(eval, "  - task.yaml", strings.Join(taskNames, "\n"), 1)), 0o600))
	requirements, err := releasepolicy.DecodeRequirementsInput(read("requirements.json"))
	require.NoError(t, err)
	source := controlledcomparison.Source{EvalPath: evalPath}
	policy, err := controlledcomparison.Plan(source, source, design, requirements, nil)
	require.NoError(t, err)
	dir := filepath.Join(t.TempDir(), "collection")
	require.NoError(t, controlledcomparison.Collect(t.Context(), policy, source, source, dir))
	decision, err := releasepolicy.ReadDecision(dir)
	require.NoError(t, err)
	require.True(t, decision.Accepted, "%+v", decision)
	return filepath.Join(dir, "policy.json"), dir
}

func TestSelectedReleasePolicyAcceptedAndMismatchProcessExits(t *testing.T) {
	policyPath, collection := acceptedOfflineComparison(t)
	binary := buildOfflineComparisonCLI(t)
	expected, err := releasepolicy.ReadDecision(collection)
	require.NoError(t, err)
	data, err := os.ReadFile(policyPath)
	require.NoError(t, err)
	policy, err := releasepolicy.DecodePolicy(data)
	require.NoError(t, err)
	policy.Design.Independence.Limitations = append(policy.Design.Independence.Limitations, "Different selected synthetic policy.")
	data, err = releasepolicy.SealJSON(policy)
	require.NoError(t, err)
	mismatch := filepath.Join(t.TempDir(), "different-policy.json")
	require.NoError(t, os.WriteFile(mismatch, data, 0o600))
	_, err = releasepolicy.DecodePolicy(data)
	require.NoError(t, err)
	for _, command := range []string{"compare", "gate"} {
		for _, tc := range []struct {
			name   string
			policy string
			code   int
		}{{"accepted", policyPath, 0}, {"mismatch", mismatch, 1}} {
			t.Run(command+"/"+tc.name, func(t *testing.T) {
				output, code := runOfflineComparisonCLI(t, binary, command,
					"--release-policy", tc.policy, "--collection-dir", collection, "--format", "json")
				require.Equal(t, tc.code, code, "%s", output)
				var decision releasepolicy.Decision
				require.NoError(t, json.Unmarshal(output, &decision), "%s", output)
				require.Equal(t, tc.code == 0, decision.Accepted)
				if tc.code == 0 {
					require.Equal(t, expected, decision)
				} else {
					require.Equal(t, "mismatched", decision.Compatibility.State)
				}
			})
		}
	}
}

func TestSelectedReleasePolicyExitBoundary(t *testing.T) {
	for _, command := range []string{"gate", "compare"} {
		for _, args := range [][]string{
			{"--release-policy", ""},
			{"--collection-dir", ""},
			{"--release-policy", "absent-policy.json"},
			{"--collection-dir", "absent-collection"},
			{"--release-policy", "absent-policy.json", "--collection-dir", "absent-collection", "--format", "json"},
		} {
			t.Run(command+strings.Join(args, " "), func(t *testing.T) {
				var cmd *cobra.Command
				if command == "gate" {
					cmd = newGateCommand()
				} else {
					cmd = newCompareCommand()
				}
				cmd.SetArgs(args)
				var out bytes.Buffer
				cmd.SetOut(&out)
				cmd.SetErr(io.Discard)
				err := cmd.Execute()
				var exit *ExitCodeError
				if !errors.As(err, &exit) || exit.Code != 1 || !strings.Contains(out.String(), "invalid") {
					t.Fatalf("strict selection error=%v output=%s", err, out.String())
				}
			})
		}
	}
}

func TestSelectedReleaseCannotSilentlyIgnoreLegacyFlags(t *testing.T) {
	for _, flag := range []string{"baseline", "current", "max-regression-pct", "golden-must-pass", "on-new-tasks", "on-removed-tasks"} {
		t.Run(flag, func(t *testing.T) {
			cmd := newGateCommand()
			value := "unused"
			if flag == "golden-must-pass" {
				value = "false"
			}
			if flag == "max-regression-pct" {
				value = "5"
			}
			cmd.SetArgs([]string{"--release-policy", "unused-policy", "--collection-dir", "unused-collection", "--" + flag + "=" + value})
			cmd.SetErr(io.Discard)
			var exit *ExitCodeError
			if err := cmd.Execute(); !errors.As(err, &exit) || exit.Code != 1 ||
				!strings.Contains(err.Error(), "cannot be combined") {
				t.Fatalf("ignored legacy flag: %v", err)
			}
		})
	}
	cmd := newCompareCommand()
	cmd.SetArgs([]string{"historical.json", "current.json", "--release-policy", "unused", "--collection-dir", "unused"})
	cmd.SetErr(io.Discard)
	var exit *ExitCodeError
	if err := cmd.Execute(); !errors.As(err, &exit) || exit.Code != 1 {
		t.Fatalf("strict historical adoption was not rejected: %v", err)
	}
}

func TestSelectedReleaseRendering(t *testing.T) {
	d := releasepolicy.InitialDecision()
	d.Golden.State, d.Billing.State, d.Statistics.State = "missing_required_evidence", "unavailable", "underpowered"
	for _, format := range []string{"human", "json", "markdown", "github-actions"} {
		var out bytes.Buffer
		if err := renderControlledDecision(&out, d, format); err != nil {
			t.Fatal(err)
		}
		for _, state := range []string{"not_assessed", "missing_required_evidence", "unavailable", "underpowered"} {
			if !strings.Contains(out.String(), state) {
				t.Fatalf("%s lost distinct state %s: %s", format, state, out.String())
			}
		}
	}
	if err := renderControlledDecision(io.Discard, d, "unsupported"); err == nil {
		t.Fatal("unsupported format accepted")
	}
	if err := renderControlledDecision(alwaysFailWriter{}, d, "human"); err == nil {
		t.Fatal("write failure hidden")
	}
	var out bytes.Buffer
	err := runControlledAssessment(&out, filepath.Join(t.TempDir(), "missing"), "collection", "human")
	var exit *ExitCodeError
	if !errors.As(err, &exit) || exit.Code != 1 || !strings.Contains(out.String(), "invalid") {
		t.Fatalf("unreadable selected policy passed: %v", err)
	}
}

type alwaysFailWriter struct{}

func (alwaysFailWriter) Write([]byte) (int, error) { return 0, errors.New("test write error") }
