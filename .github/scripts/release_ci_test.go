package scripts

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type releaseStep struct {
	Name string
	If   string
	Run  string
	Uses string
	Env  map[string]string
	With map[string]string
}

type releaseJob struct {
	Uses           string
	Needs          []string
	If             string
	TimeoutMinutes int `yaml:"timeout-minutes"`
	Steps          []releaseStep
	Strategy       struct {
		FailFast *bool `yaml:"fail-fast"`
		Matrix   struct {
			Shard []int
		}
	}
}

func checkNativeShardWorkflow(source []byte) error {
	var workflow struct {
		Permissions map[string]string
		Jobs        map[string]releaseJob
	}
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		return err
	}
	job := workflow.Jobs["native"]
	if workflow.Permissions["contents"] != "read" || job.TimeoutMinutes != 40 ||
		job.Strategy.FailFast == nil || *job.Strategy.FailFast ||
		!reflect.DeepEqual(job.Strategy.Matrix.Shard, []int{0, 1, 2, 3, 4}) {
		return fmt.Errorf("native workflow must retain five exhaustive shards, permissions and 40-minute independent jobs")
	}
	foundExecution, foundUpload := false, false
	for _, step := range job.Steps {
		if step.Name == "Execute exhaustive native test shard" {
			foundExecution = step.Env["SHARD"] == "${{ matrix.shard }}" &&
				strings.Contains(step.Run, `bash .github/scripts/go_test_shard.sh "$SHARD" "coverage-native-$SHARD.txt"`)
		}
		if step.Name == "Retain coverage and test-selection evidence" {
			foundUpload = strings.HasPrefix(step.Uses, "actions/upload-artifact@") &&
				step.With["name"] == "go-coverage-native-${{ matrix.shard }}" &&
				step.With["path"] == "coverage-native-${{ matrix.shard }}.txt*" &&
				step.With["if-no-files-found"] == "error"
		}
	}
	if !foundExecution || !foundUpload {
		return fmt.Errorf("every matrix shard must execute and upload its atomic profile and selection inventories")
	}
	return nil
}

func TestNativeShardMatrixAndUploadedProfiles(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "workflows", "go-native-shards.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := checkNativeShardWorkflow(source); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ from, to string }{
		{"shard: [0, 1, 2, 3, 4]", "shard: [0, 1, 2, 3]"},
		{"shard: [0, 1, 2, 3, 4]", "shard: [0, 1, 2, 3, 3]"},
		{"timeout-minutes: 40", "timeout-minutes: 41"},
		{"fail-fast: false", "fail-fast: true"},
		{"fail-fast: false", "# fail-fast omitted"},
		{`SHARD: ${{ matrix.shard }}`, "SHARD: 0"},
		{"name: go-coverage-native-${{ matrix.shard }}", "name: go-coverage-native-0"},
		{"path: coverage-native-${{ matrix.shard }}.txt*", "path: coverage-native-${{ matrix.shard }}.txt"},
		{"if-no-files-found: error", "if-no-files-found: ignore"},
	} {
		mutated := strings.Replace(string(source), tc.from, tc.to, 1)
		if mutated == string(source) || checkNativeShardWorkflow([]byte(mutated)) == nil {
			t.Fatalf("native workflow mutation was not rejected: %s", tc.to)
		}
	}
}

func checkGoCoverageWorkflow(source []byte) error {
	var workflow struct {
		Jobs map[string]releaseJob
	}
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		return err
	}
	gate := workflow.Jobs["linux-gate"]
	if workflow.Jobs["native-test-shards"].Uses != "./.github/workflows/go-native-shards.yml" ||
		!reflect.DeepEqual(gate.Needs, []string{"build-and-test", "native-test-shards"}) ||
		gate.If != "${{ always() }}" || gate.TimeoutMinutes != 10 {
		return fmt.Errorf("Linux coverage gate must require build and reusable native shards even after failure or skip")
	}
	want := []string{"coverage-reports/coverage.txt"}
	for shard := range 5 {
		want = append(want, fmt.Sprintf("coverage-reports/coverage-native-%d.txt", shard))
	}
	foundMain, foundDownload, foundVerify, foundUpload := false, false, false, false
	for _, step := range workflow.Jobs["build-and-test"].Steps {
		if step.Name == "Retain remaining-package coverage" {
			foundMain = strings.HasPrefix(step.Uses, "actions/upload-artifact@") &&
				step.If == "matrix.os == 'ubuntu-latest'" &&
				step.With["name"] == "go-coverage-main" && step.With["path"] == "coverage.txt" &&
				step.With["if-no-files-found"] == "error"
		}
	}
	for _, step := range gate.Steps {
		if step.Name == "Download all required coverage and discovery evidence" {
			foundDownload = strings.HasPrefix(step.Uses, "actions/download-artifact@") &&
				step.With["pattern"] == "go-coverage-*" && step.With["merge-multiple"] == "true" &&
				step.With["path"] == "coverage-reports"
		}
		if step.Name == "Verify exhaustive native test selection and coverage" {
			foundVerify = foundDownload && step.Run == "bash .github/scripts/go_test_shard_verify.sh coverage-reports"
		}
		if strings.HasPrefix(step.Uses, "codecov/codecov-action@") {
			foundUpload = foundVerify && step.With["files"] == strings.Join(want, ",") &&
				step.With["disable_search"] == "true"
		}
	}
	if !foundMain || !foundDownload || !foundVerify || !foundUpload {
		return fmt.Errorf("coverage upload must follow verification and explicitly include all six atomic profiles")
	}
	return nil
}

func TestGoWorkflowCoverageProfilesAndRequiredGate(t *testing.T) {
	for _, name := range []string{"go-ci.yml", "squad-ci.yml"} {
		t.Run(name, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join("..", "workflows", name))
			if err != nil {
				t.Fatal(err)
			}
			if err := checkGoCoverageWorkflow(source); err != nil {
				t.Fatal(err)
			}
			var workflow struct{ Jobs map[string]releaseJob }
			if err := yaml.Unmarshal(source, &workflow); err != nil {
				t.Fatal(err)
			}
			var gate releaseStep
			for _, step := range workflow.Jobs["linux-gate"].Steps {
				if step.Name == "Require every build and native test shard" {
					gate = step
				}
			}
			if gate.Env["BUILD_RESULT"] != "${{ needs.build-and-test.result }}" ||
				gate.Env["NATIVE_RESULT"] != "${{ needs.native-test-shards.result }}" {
				t.Fatal("gate must consume actual build and native results")
			}
			for _, build := range []string{"success", "failure", "cancelled", "skipped", ""} { //nolint:misspell // GitHub conclusion.
				for _, native := range []string{"success", "failure", "cancelled", "skipped", ""} { //nolint:misspell // GitHub conclusion.
					cmd := exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", gate.Run)
					cmd.Env = append(os.Environ(), "BUILD_RESULT="+build, "NATIVE_RESULT="+native)
					if err := cmd.Run(); (err == nil) != (build == "success" && native == "success") {
						t.Fatalf("build=%q native=%q: gate error %v", build, native, err)
					}
				}
			}
			profiles := []string{"coverage-reports/coverage.txt"}
			for shard := range 5 {
				profiles = append(profiles, fmt.Sprintf("coverage-reports/coverage-native-%d.txt", shard))
			}
			for _, profile := range profiles {
				mutated := strings.Replace(string(source), profile, profile+".missing", 1)
				if mutated == string(source) || checkGoCoverageWorkflow([]byte(mutated)) == nil {
					t.Fatalf("missing required coverage input was accepted: %s", profile)
				}
			}
			for _, tc := range []struct{ from, to string }{
				{"name: go-coverage-main", "name: go-coverage-main-missing"},
				{"path: coverage.txt", "path: wrong-coverage.txt"},
				{"pattern: go-coverage-*", "pattern: go-coverage-native-*"},
				{"merge-multiple: true", "merge-multiple: false"},
			} {
				mutated := strings.Replace(string(source), tc.from, tc.to, 1)
				if mutated == string(source) || checkGoCoverageWorkflow([]byte(mutated)) == nil {
					t.Fatalf("coverage artifact mutation was not rejected: %s", tc.to)
				}
			}
		})
	}
}

func TestReleaseWorkflowGates(t *testing.T) {
	for _, file := range []string{"squad-release.yml", "squad-insider-release.yml"} {
		t.Run(file, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join("..", "workflows", file))
			if err != nil {
				t.Fatal(err)
			}
			var workflow struct {
				Permissions map[string]string
				Jobs        map[string]releaseJob
			}
			if err := yaml.Unmarshal(source, &workflow); err != nil {
				t.Fatal(err)
			}
			job := workflow.Jobs["release"]
			if workflow.Permissions["contents"] != "write" || job.TimeoutMinutes != 60 {
				t.Fatal("release permissions and bounded job timeout must be preserved")
			}
			if workflow.Jobs["native-test-shards"].Uses != "./.github/workflows/go-native-shards.yml" ||
				!reflect.DeepEqual(job.Needs, []string{"native-test-shards"}) {
				t.Fatal("release must explicitly gate on every reusable native shard, including skipped/canceled jobs")
			}
			t.Run("workflow-cancellation-blocks-publication", func(t *testing.T) {
				// Pin the actual GitHub status function, not a shell simulation:
				// Unlike always(), this predicate is false after cancellation.
				if job.If != "${{ !cancelled() }}" { //nolint:misspell // GitHub's exact status function.
					t.Fatal("publication must be ineligible after workflow cancellation")
				}
			})
			indexes := map[string]int{}
			steps := map[string]releaseStep{}
			for i, step := range job.Steps {
				indexes[step.Name] = i
				steps[step.Name] = step
			}
			ordered := []string{
				"Require every native test shard",
				"Checkout Repository",
				"Install and prove rejecting runtime guard",
				"Test release gate failure handling",
				"Run Go Vet",
				"Execute Tests",
				"Download native coverage and discovery evidence",
				"Verify exhaustive native test selection and coverage",
			}
			if file == "squad-release.yml" {
				ordered = append(ordered, "Build Release Binary", "Integration Test", "Create Release")
			} else {
				ordered = append(ordered, "Build Insider Release Binary", "Integration Test", "Create Insider Pre-Release")
			}
			previous := -1
			for _, name := range ordered {
				index, ok := indexes[name]
				if !ok || index <= previous {
					t.Fatalf("missing or out-of-order release gate %q", name)
				}
				previous = index
			}
			gate := steps["Require every native test shard"]
			if gate.Env["NATIVE_RESULT"] != "${{ needs.native-test-shards.result }}" {
				t.Fatal("gate must consume the actual native shard job result")
			}
			for _, result := range []string{"success", "failure", "cancelled", "skipped", ""} { //nolint:misspell // GitHub's exact job conclusion.
				cmd := exec.Command("bash", "-euo", "pipefail", "-c", gate.Run)
				cmd.Env = append(os.Environ(), "NATIVE_RESULT="+result)
				if err := cmd.Run(); (err == nil) != (result == "success") {
					t.Fatalf("native result %q: gate error = %v", result, err)
				}
			}
			if steps["Install and prove rejecting runtime guard"].Run != `bash .github/scripts/go_offline_env.sh >> "$GITHUB_ENV"` ||
				steps["Test release gate failure handling"].Run != "bash .github/scripts/release_gate_test.sh\ngo test -count=1 -timeout=10m ./.github/scripts ./.github/scripts/publish-runner-reference-guard ./.github/scripts/verify-runner-reference-guard\n" ||
				steps["Integration Test"].Run != "bash .github/scripts/release_integration_test.sh ./waza" {
				t.Fatal("release must exercise the proven offline runtime and integration failure controls")
			}
			download := steps["Download native coverage and discovery evidence"]
			if !strings.HasPrefix(download.Uses, "actions/download-artifact@") ||
				download.With["pattern"] != "go-coverage-native-*" ||
				download.With["merge-multiple"] != "true" || download.With["path"] != "coverage-reports" {
				t.Fatal("release must retrieve every native coverage profile and selection inventory")
			}
			wantVerify := "cp coverage.txt coverage-reports/coverage.txt\nbash .github/scripts/go_test_shard_verify.sh coverage-reports\n"
			if steps["Verify exhaustive native test selection and coverage"].Run != wantVerify {
				t.Fatal("release must verify remaining-package coverage and exactly-once native selection before publication")
			}
			testReleasePackageSelection(t, steps["Execute Tests"].Run)
		})
	}
}

func TestNativeShardsInstallOfflineGuard(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "workflows", "go-native-shards.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]releaseJob
	}
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		t.Fatal(err)
	}
	guard := -1
	execute := -1
	for i, step := range workflow.Jobs["native"].Steps {
		switch step.Name {
		case "Install and prove rejecting runtime guard":
			if step.Run != `bash .github/scripts/go_offline_env.sh >> "$GITHUB_ENV"` {
				t.Fatal("native shard must install the same proven offline guard as the release job")
			}
			guard = i
		case "Execute exhaustive native test shard":
			execute = i
			testNativeShardRuntimeGate(t, step.Run)
		}
	}
	if guard < 0 || execute <= guard {
		t.Fatal("native shards must install their runtime guard before actual discovery and execution")
	}
}

func testNativeShardRuntimeGate(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	scripts := filepath.Join(dir, ".github", "scripts")
	if err := os.MkdirAll(scripts, 0o700); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("go_test_shard.sh")
	if err != nil {
		t.Fatal(err)
	}
	writeReleaseFile(t, filepath.Join(scripts, "go_test_shard.sh"), source)
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	env, guard := releaseControlEnvironment(t, dir, root)
	writeReleaseFile(t, filepath.Join(dir, "go"), []byte(`#!/usr/bin/env bash
set -euo pipefail
if [[ " $* " == *" -list "* ]]; then
  printf '%s\n' TestNativeReaderFrozen15cDecisionParity TestNativeCollectionObservedFailures TestNativeLateFailurePreservesEveryStartedAttempt TestOne TestTwo TestThree TestFour
else
  printf 'executed\n' > "$TEST_EXECUTED"
  if [ "$TRY_RUNTIME" = true ]; then "$COPILOT_CLI_PATH" --version || true; fi
  exit "$TEST_STATUS"
fi
`))
	for _, tc := range []struct {
		name   string
		launch bool
		status int
		want   int
	}{
		{"clean", false, 0, 0},
		{"tolerated-launch", true, 0, 1},
		{"test-failure", false, 9, 9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executed := filepath.Join(dir, "executed")
			cmd := exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", script)
			cmd.Dir = dir
			cmd.Env = append(env, "SHARD=0", "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"TEST_EXECUTED="+executed, fmt.Sprintf("TRY_RUNTIME=%t", tc.launch), fmt.Sprintf("TEST_STATUS=%d", tc.status))
			output, err := cmd.CombinedOutput()
			if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != tc.want {
				t.Fatalf("workflow shard gate: %v\n%s", err, output)
			}
			if _, err := os.Stat(executed); err != nil {
				t.Fatalf("actual shard test command did not execute: %v", err)
			}
			if err := os.Remove(executed); err != nil {
				t.Fatal(err)
			}
			if tc.launch {
				if !strings.Contains(string(output), "Native shard attempted to launch the Copilot runtime.") {
					t.Fatalf("tolerated launch was not rejected by workflow: %s", output)
				}
				if err := os.Remove(guard + ".invoked"); err != nil {
					t.Fatal(err)
				}
			}
			assertNoReleaseRuntime(t, guard)
		})
	}
}

func testReleasePackageSelection(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	goPath := filepath.Join(dir, "go")
	fake := `#!/usr/bin/env bash
set -euo pipefail
if [ "$1" = list ]; then
  test "$#" -eq 2
  test "$2" = ./...
  if [ "${LIST_STATUS:-0}" -ne 0 ]; then exit "$LIST_STATUS"; fi
  printf '%s\n' github.com/microsoft/waza/cmd/waza github.com/microsoft/waza/internal/controlledcomparison github.com/microsoft/waza/internal/nativesnapshot github.com/microsoft/waza/internal/nativetask github.com/microsoft/waza/internal/controlledcomparison-near-miss github.com/microsoft/waza/internal/controlledcomparison/child
else
  printf '%s\n' "$@" > "$CALLS"
  exit "${TEST_STATUS:-0}"
fi
`
	if err := os.WriteFile(goPath, []byte(fake), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		list, test, want int
	}{{0, 0, 0}, {7, 0, 7}, {0, 9, 9}} {
		cmd := exec.Command("bash", "-euo", "pipefail", "-c", script)
		calls := filepath.Join(dir, "calls")
		cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
			"CALLS="+calls, fmt.Sprintf("LIST_STATUS=%d", tc.list), fmt.Sprintf("TEST_STATUS=%d", tc.test))
		output, err := cmd.CombinedOutput()
		if cmd.ProcessState == nil {
			t.Fatalf("starting workflow shell: %v\n%s", err, output)
		}
		if got := cmd.ProcessState.ExitCode(); got != tc.want {
			t.Fatalf("list=%d test=%d: exit=%d, want=%d: %v\n%s", tc.list, tc.test, got, tc.want, err, output)
		}
		if tc.want != 0 {
			continue
		}
		args, err := os.ReadFile(calls)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"test", "-v", "-race", "-timeout=30m", "-coverprofile=coverage.txt", "-covermode=atomic",
			"github.com/microsoft/waza/cmd/waza",
			"github.com/microsoft/waza/internal/nativesnapshot",
			"github.com/microsoft/waza/internal/nativetask",
			"github.com/microsoft/waza/internal/controlledcomparison-near-miss",
			"github.com/microsoft/waza/internal/controlledcomparison/child"}
		if got := strings.Split(strings.TrimSpace(string(args)), "\n"); !reflect.DeepEqual(got, want) {
			t.Fatalf("test args = %q; want %q", got, want)
		}
	}
}
