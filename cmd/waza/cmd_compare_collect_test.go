package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
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

func TestControlledCLIWorkflow(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("new durable collection unsupported")
	}
	example := testutil.RepoFile(t, "examples", "controlled-comparison")
	dir := t.TempDir()
	policy := filepath.Join(dir, "policy.json")
	sources := []string{"--baseline-eval", filepath.Join(example, "eval.yaml"),
		"--candidate-eval", filepath.Join(example, "eval.yaml")}
	cmd := newComparePlanCommand()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(append(append([]string{}, sources...), "--design", filepath.Join(example, "design.json"),
		"--requirements", filepath.Join(example, "requirements.json"), "--output", policy))
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(policy)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("policy not private: %v %v", info, err)
	}
	// Reusing the artifact path must not overwrite a precollection commitment.
	if err := cmd.Execute(); err == nil {
		t.Fatal("existing policy overwritten")
	}
	directory := filepath.Join(dir, "collection")
	collect := newCompareCollectCommand()
	collect.SetOut(io.Discard)
	collect.SetErr(io.Discard)
	collect.SetArgs(append(append([]string{}, sources...), "--release-policy", policy, "--collection-dir", directory))
	if err := collect.Execute(); err != nil {
		t.Fatal(err)
	}
	gate := newGateCommand()
	var out bytes.Buffer
	gate.SetOut(&out)
	gate.SetErr(io.Discard)
	gate.SetArgs([]string{"--release-policy", policy, "--collection-dir", directory, "--format", "json"})
	var exit *ExitCodeError
	if err := gate.Execute(); !errors.As(err, &exit) || exit.Code != 1 ||
		!strings.Contains(out.String(), `"inconclusive"`) || !strings.Contains(out.String(), `"accepted": false`) {
		t.Fatalf("one-cluster mock example improperly strict-passed: %v %s", err, out.String())
	}
}

func TestControlledCLIRequiredBoundaries(t *testing.T) {
	for _, cmd := range []*cobra.Command{newComparePlanCommand(), newCompareCollectCommand()} {
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		var exit *ExitCodeError
		if err := cmd.Execute(); !errors.As(err, &exit) || exit.Code != 1 {
			t.Fatalf("missing explicit inputs did not reject: %v", err)
		}

	}
	t.Run("native explicit selection", func(t *testing.T) {
		if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
			t.Skip("native durable planning requires Linux or macOS")
		}
		directory := t.TempDir()
		write := func(name, value string) string {
			t.Helper()
			path := filepath.Join(directory, name)
			require.NoError(t, os.WriteFile(path, []byte(value), 0o600))
			return path
		}
		eval := write("eval.yaml", "schemaVersion: '1.0'\nname: native\nconfig:\n  executor: copilot-sdk\n  model: fixed-model\n  disabled_skills: ['*']\n  trials_per_task: 1\n  timeout_seconds: 30\ntasks: [task.yaml]\ngraders:\n  - name: text\n    type: text\n    config:\n      contains: [hello]\n")
		write("task.yaml", "id: task\nname: native\ngolden: true\ninputs:\n  prompt: hello\n")
		rawDesign, err := json.Marshal(releasepolicy.Design{
			Estimand: "fixed_suite_repeated_execution", Endpoint: "first_attempt_pass", Alpha: "0.05",
			FamilySize: 1, MaximumHalfWidth: "1", Margin: "1", Accept: "noninferiority",
			Independence: releasepolicy.Independence{Assessment: "unjustified", Justification: "Synthetic CLI contract test.",
				Limitations: []string{"Not live quality evidence."}},
			Clusters: []releasepolicy.Cluster{{ID: "cluster", Weight: "1", Tasks: []releasepolicy.PlannedTask{
				{ID: "task", Weight: "1", TrialOrdinals: []int{1}}}}},
		})
		require.NoError(t, err)
		design := write("design.json", string(rawDesign))
		// Reuse the canonical input serializer rather than inventing requirement
		// field names or defaults in a CLI-only fixture.
		requirements := releasepolicy.Requirements{IdentityDomains: []string{"task_definition", "resolved_prompt", "fixture_inventory",
			"instruction_inventory", "grader_configuration", "dependency_mode", "grader_implementation", "rubric_inventory", "lock_inventory"},
			Billing: []releasepolicy.BillingRequirement{}}
		raw, err := json.Marshal(requirements)
		require.NoError(t, err)
		required := write("requirements.json", string(raw))
		policy := filepath.Join(directory, "policy.json")
		plan := newComparePlanCommand()
		plan.SetOut(io.Discard)
		plan.SetErr(io.Discard)
		plan.SetArgs([]string{"--collector", "native-copilot", "--baseline-eval", eval, "--candidate-eval", eval,
			"--design", design, "--requirements", required, "--output", policy})
		require.NoError(t, plan.Execute())
		raw, err = os.ReadFile(policy)
		require.NoError(t, err)
		_, _, _, err = controlledcomparison.DecodeNativePolicy(raw)
		require.NoError(t, err)
		for _, extra := range [][]string{nil, {"--collector", "mock"}, {"--collector", "unknown"}, {"--collector", "native-copilot", "--assurance-contract", "not-loaded.json"}} {
			collection := filepath.Join(t.TempDir(), "must-not-exist")
			command := newCompareCollectCommand()
			command.SetOut(io.Discard)
			command.SetErr(io.Discard)
			args := []string{"--release-policy", policy, "--baseline-eval", eval, "--candidate-eval", eval, "--collection-dir", collection}
			command.SetArgs(append(args, extra...))
			require.Error(t, command.Execute())
			require.NoDirExists(t, collection, "wrong/default collector or supplied assurance cannot enter paid native execution")
		}
	})
	for _, args := range [][]string{{"compare-plan"}, {"compare-collect"}, {"compare", "--release-policy=policy"},
		{"gate", "--release-policy=policy"}, {"compare", "--collection-dir=collection"},
		{"gate", "--collection-dir=collection"}, {"compare", "--collection-dir="}, {"gate", "--collection-dir="}} {
		root := newRootCommand()
		root.SetArgs(args)
		command, _, err := root.Find(args)
		if err != nil {
			t.Fatal(err)
		}
		if len(args) > 1 {
			if err := command.ParseFlags(args[1:]); err != nil {
				t.Fatal(err)
			}
		}
		if shouldRunUpdateCheck(command, false) {
			t.Fatalf("explicit offline policy path enabled network update check: %v", args)
		}
	}
}
