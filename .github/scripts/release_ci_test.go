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
	ID              string `yaml:"id"`
	Name            string
	Run             string
	Uses            string
	If              string
	ContinueOnError bool `yaml:"continue-on-error"`
	Env             map[string]string
	With            map[string]string
}

type releaseJob struct {
	Name           string
	Outputs        map[string]string
	Env            map[string]string
	Uses           string
	Needs          []string
	If             string
	Permissions    map[string]string
	TimeoutMinutes int `yaml:"timeout-minutes"`
	Steps          []releaseStep
}

func TestReleaseWorkflowGates(t *testing.T) {
	for _, file := range []string{"squad-release.yml", "squad-insider-release.yml"} {
		t.Run(file, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join("..", "workflows", file))
			if err != nil {
				t.Fatal(err)
			}
			if err := validateReleaseWorkflow(source, file); err != nil {
				t.Fatal(err)
			}
			var workflow releaseWorkflow
			if err := yaml.Unmarshal(source, &workflow); err != nil {
				t.Fatal(err)
			}
			steps := releaseSteps(workflow.Jobs["release"])
			gate := steps["Require every validation branch"]
			results := []string{"success", "failure", "cancelled", "skipped", ""} //nolint:misspell // GitHub's exact job conclusion.
			for _, native := range results {
				for _, build := range results {
					cmd := exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", gate.Run)
					cmd.Env = append(os.Environ(), "NATIVE_RESULT="+native, "BUILD_RESULT="+build)
					if err := cmd.Run(); (err == nil) != (native == "success" && build == "success") {
						t.Fatalf("native=%q build=%q: gate error = %v", native, build, err)
					}
				}
			}
			buildSteps := releaseSteps(workflow.Jobs["build-test"])
			testReleasePackageSelection(t, buildSteps["Execute Tests"].Run)
			testReleaseBinaryHandoff(t, buildSteps["Record tested binary digest"].Run, steps["Verify and restore tested binary"].Run)
			testReleaseCoverageEvidence(t, steps["Verify exhaustive native test selection and coverage"].Run)
			for _, mutation := range []struct{ name, old, new string }{
				{"serialized-build", "  build-test:\n", "  build-test:\n    needs: [native-test-shards]\n"},
				{"missing-build-dependency", "needs: [native-test-shards, build-test]", "needs: [native-test-shards]"},
				{"missing-native-dependency", "needs: [native-test-shards, build-test]", "needs: [build-test]"},
				{"cancellation-bypass", "${{ !cancelled() }}", "${{ always() }}"}, //nolint:misspell // GitHub expression.
				{"unbound-build-result", "${{ needs.build-test.result }}", "success"},
				{"ignored-build-result", `test "$BUILD_RESULT" = success`, ":"},
				{"ignored-native-result", `test "$NATIVE_RESULT" = success`, ":"},
				{"missing-coverage-verification", "bash .github/scripts/go_test_shard_verify.sh coverage-reports", ":"},
				{"missing-integration", "bash .github/scripts/release_integration_test.sh ./waza", ":"},
				{"skipped-integration", "      - name: Integration Test\n", "      - name: Integration Test\n        if: 'false'\n"},
				{"ignored-integration-failure", "      - name: Integration Test\n", "      - name: Integration Test\n        continue-on-error: true\n"},
				{"missing-guard", `bash .github/scripts/go_offline_env.sh >> "$GITHUB_ENV"`, ":"},
				{"different-download", "          name: release-tested-${{ github.sha }}\n          path: .", "          name: other-binary\n          path: ."},
				{"missing-binary-digest", "sha256sum --check waza.sha256", ":"},
				{"missing-executable-mode", "chmod +x waza", ":"},
				{"missing-coverage-artifact", "            coverage.txt\n", ""},
				{"rebuild-before-publication", "          chmod +x waza", "          go build -o waza ./cmd/waza\n          chmod +x waza"},
				{"missing-release-tags", "          fetch-depth: 0", "          fetch-depth: 1"},
			} {
				t.Run(mutation.name, func(t *testing.T) {
					changed := strings.Replace(string(source), mutation.old, mutation.new, 1)
					if changed == string(source) {
						t.Fatal("mutation did not change production workflow")
					}
					if err := validateReleaseWorkflow([]byte(changed), file); err == nil {
						t.Fatal("unsafe production workflow mutation was admitted")
					}
				})
			}
		})
	}
}

type releaseWorkflow struct {
	Permissions map[string]string
	Jobs        map[string]releaseJob
}

func releaseSteps(job releaseJob) map[string]releaseStep {
	steps := make(map[string]releaseStep)
	for _, step := range job.Steps {
		steps[step.Name] = step
	}
	return steps
}

func orderedReleaseSteps(job releaseJob, names []string) error {
	previous := -1
	for _, name := range names {
		found := false
		for i, step := range job.Steps {
			if step.Name != name {
				continue
			}
			if found || i <= previous || step.If != "" || step.ContinueOnError {
				return fmt.Errorf("missing, skipped, tolerated, duplicated or out-of-order gate %q", name)
			}
			previous, found = i, true
		}
		if !found {
			return fmt.Errorf("missing gate %q", name)
		}
	}
	return nil
}

func validateReleaseWorkflow(source []byte, file string) error {
	var workflow releaseWorkflow
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		return err
	}
	native, build, publish := workflow.Jobs["native-test-shards"], workflow.Jobs["build-test"], workflow.Jobs["release"]
	if workflow.Permissions["contents"] != "write" || publish.TimeoutMinutes != 60 ||
		build.TimeoutMinutes != 60 || build.Permissions["contents"] != "read" {
		return fmt.Errorf("release permissions and bounded deadlines must be preserved")
	}
	if native.Uses != "./.github/workflows/go-native-shards.yml" || len(native.Needs) != 0 ||
		len(build.Needs) != 0 || build.If != "" || native.If != "" ||
		!reflect.DeepEqual(publish.Needs, []string{"native-test-shards", "build-test"}) {
		return fmt.Errorf("test branches must run independently and publication must depend on both")
	}
	if publish.If != "${{ !cancelled() }}" { //nolint:misspell // GitHub's exact status function.
		return fmt.Errorf("publication must be ineligible after workflow cancellation")
	}
	binary, create := "Build Release Binary", "Create Release"
	if file == "squad-insider-release.yml" {
		binary, create = "Build Insider Release Binary", "Create Insider Pre-Release"
	}
	if err := orderedReleaseSteps(build, []string{"Checkout Repository", "Install and prove rejecting runtime guard",
		"Test release gate failure handling", "Run Go Vet", "Execute Tests", binary, "Integration Test",
		"Record tested binary digest", "Upload tested release binary and remaining coverage"}); err != nil {
		return err
	}
	if err := orderedReleaseSteps(publish, []string{"Require every validation branch", "Checkout Repository",
		"Download tested release binary and remaining coverage", "Download native coverage and discovery evidence",
		"Verify exhaustive native test selection and coverage", "Verify and restore tested binary", create}); err != nil {
		return err
	}
	steps, buildSteps := releaseSteps(publish), releaseSteps(build)
	if buildSteps["Checkout Repository"].With["lfs"] != "true" || buildSteps["Checkout Repository"].With["fetch-depth"] != "0" ||
		steps["Checkout Repository"].With["fetch-depth"] != "0" {
		return fmt.Errorf("tested build requires real runtime assets and release creation requires full tag history")
	}
	gate := steps["Require every validation branch"]
	if gate.Env["NATIVE_RESULT"] != "${{ needs.native-test-shards.result }}" ||
		gate.Env["BUILD_RESULT"] != "${{ needs.build-test.result }}" ||
		gate.Run != "test \"$NATIVE_RESULT\" = success\ntest \"$BUILD_RESULT\" = success\n" {
		return fmt.Errorf("gate must reject every non-success result from both validation branches")
	}
	if buildSteps["Install and prove rejecting runtime guard"].Run != `bash .github/scripts/go_offline_env.sh >> "$GITHUB_ENV"` ||
		buildSteps["Test release gate failure handling"].Run != "bash .github/scripts/release_gate_test.sh\ngo test -count=1 -timeout=10m ./.github/scripts ./.github/scripts/publish-runner-reference-guard ./.github/scripts/verify-runner-reference-guard\n" ||
		buildSteps["Run Go Vet"].Run != "go vet ./..." ||
		buildSteps[binary].Run != "go build -v -o waza ./cmd/waza\n./waza --version\n" ||
		buildSteps["Integration Test"].Run != "bash .github/scripts/release_integration_test.sh ./waza" {
		return fmt.Errorf("build branch must retain vet, runtime guard, failure controls and tested binary integration")
	}
	upload, download := buildSteps["Upload tested release binary and remaining coverage"], steps["Download tested release binary and remaining coverage"]
	if !strings.HasPrefix(upload.Uses, "actions/upload-artifact@") || upload.With["name"] != "release-tested-${{ github.sha }}" ||
		upload.With["path"] != "waza\nwaza.sha256\ncoverage.txt\n" || upload.With["if-no-files-found"] != "error" ||
		!strings.HasPrefix(download.Uses, "actions/download-artifact@") || download.With["name"] != upload.With["name"] || download.With["path"] != "." ||
		buildSteps["Record tested binary digest"].Run != "sha256sum waza > waza.sha256" ||
		steps["Verify and restore tested binary"].Run != "sha256sum --check waza.sha256\nchmod +x waza\n./waza --version\n" {
		return fmt.Errorf("publication must retrieve the exact integration-tested binary, digest and remaining coverage")
	}
	nativeDownload := steps["Download native coverage and discovery evidence"]
	if !strings.HasPrefix(nativeDownload.Uses, "actions/download-artifact@") ||
		nativeDownload.With["pattern"] != "go-coverage-native-*" || nativeDownload.With["merge-multiple"] != "true" ||
		nativeDownload.With["path"] != "coverage-reports" ||
		steps["Verify exhaustive native test selection and coverage"].Run != "cp coverage.txt coverage-reports/coverage.txt\nbash .github/scripts/go_test_shard_verify.sh coverage-reports\n" {
		return fmt.Errorf("publication must verify all five atomic profiles and exhaustive native inventories")
	}
	for _, step := range publish.Steps {
		if strings.Contains(step.Run, "go build") {
			return fmt.Errorf("publication must not rebuild its tested binary")
		}
	}
	return nil
}

func testReleaseBinaryHandoff(t *testing.T, record, restore string) {
	t.Helper()
	for _, mutation := range []string{"none", "binary", "digest", "missing-binary", "missing-digest"} {
		t.Run("binary-handoff-"+mutation, func(t *testing.T) {
			dir := t.TempDir()
			binary := filepath.Join(dir, "waza")
			content := []byte("#!/bin/sh\nprintf '%s\\n' tested-release-binary > version-observed\n")
			writeReleaseFile(t, binary, content)
			cmd := exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", record)
			cmd.Dir = dir
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("record tested digest: %v\n%s", err, output)
			}
			// Artifact download strips the executable bit.
			if err := os.Chmod(binary, 0o600); err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "binary":
				writeReleaseFile(t, binary, append(content, []byte("# altered binary\n")...))
			case "digest":
				writeReleaseFile(t, filepath.Join(dir, "waza.sha256"), []byte(strings.Repeat("0", 64)+"  waza\n"))
			case "missing-binary":
				if err := os.Remove(binary); err != nil {
					t.Fatal(err)
				}
			case "missing-digest":
				if err := os.Remove(filepath.Join(dir, "waza.sha256")); err != nil {
					t.Fatal(err)
				}
			}
			cmd = exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", restore)
			cmd.Dir = dir
			output, err := cmd.CombinedOutput()
			if (err == nil) != (mutation == "none") {
				t.Fatalf("restore mutation %s: %v\n%s", mutation, err, output)
			}
			observed, readErr := os.ReadFile(filepath.Join(dir, "version-observed"))
			if mutation == "none" {
				if readErr != nil || string(observed) != "tested-release-binary\n" {
					t.Fatalf("downloaded binary did not execute: %q, %v", observed, readErr)
				}
			} else if !os.IsNotExist(readErr) {
				t.Fatalf("unverified binary executed: %q, %v", observed, readErr)
			}
		})
	}
}

func testReleaseCoverageEvidence(t *testing.T, script string) {
	t.Helper()
	verify, err := os.ReadFile("go_test_shard_verify.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []string{
		"none", "missing-main-profile", "missing-native-profile", "non-atomic-profile",
		"missing-discovery", "different-discovery", "missing-selection", "duplicate-selection", "incomplete-selection",
	} {
		t.Run("coverage-"+mutation, func(t *testing.T) {
			dir := t.TempDir()
			for _, name := range []string{".github/scripts", "coverage-reports", "scratch"} {
				if err := os.MkdirAll(filepath.Join(dir, name), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			writeReleaseFile(t, filepath.Join(dir, ".github/scripts/go_test_shard_verify.sh"), verify)
			coverage := []byte("mode: atomic\nexample.go:1.1,2.1 1 1\n")
			writeReleaseFile(t, filepath.Join(dir, "coverage.txt"), coverage)
			inventory := []byte("TestFour\nTestOne\nTestThree\nTestTwo\n")
			selected := []string{"TestFour\n", "TestOne\n", "TestThree\n", "TestTwo\n"}
			for shard := range 4 {
				profile := filepath.Join(dir, "coverage-reports", fmt.Sprintf("coverage-native-%d.txt", shard))
				writeReleaseFile(t, profile, coverage)
				writeReleaseFile(t, profile+".all", inventory)
				writeReleaseFile(t, profile+".selected", []byte(selected[shard]))
			}
			profile := filepath.Join(dir, "coverage-reports", "coverage-native-3.txt")
			switch mutation {
			case "missing-main-profile", "missing-native-profile", "missing-discovery", "missing-selection":
				path := map[string]string{
					"missing-main-profile":   filepath.Join(dir, "coverage.txt"),
					"missing-native-profile": profile,
					"missing-discovery":      profile + ".all",
					"missing-selection":      profile + ".selected",
				}[mutation]
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "non-atomic-profile":
				writeReleaseFile(t, profile, []byte("mode: set\n"))
			case "different-discovery":
				writeReleaseFile(t, profile+".all", []byte("TestOther\n"))
			case "duplicate-selection":
				writeReleaseFile(t, profile+".selected", []byte("TestOne\nTestTwo\n"))
			case "incomplete-selection":
				writeReleaseFile(t, profile+".selected", []byte("TestOne\n"))
			}
			cmd := exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", script)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "TMPDIR="+filepath.Join(dir, "scratch"))
			output, err := cmd.CombinedOutput()
			if (err == nil) != (mutation == "none") {
				t.Fatalf("release coverage %s: %v\n%s", mutation, err, output)
			}
			if mutation == "none" && !strings.Contains(string(output), "exhaustive, nonoverlapping selection") {
				t.Fatalf("production inventory validator was not exercised: %s", output)
			}
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
	for _, file := range []string{"go_test_shard.sh", "go_test_shard_plan.py", "go_test_shard_durations.tsv"} {
		source, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		writeReleaseFile(t, filepath.Join(scripts, file), source)
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	env, guard := releaseControlEnvironment(t, dir, root)
	writeReleaseFile(t, filepath.Join(dir, "go"), []byte(`#!/usr/bin/env bash
set -euo pipefail
if [[ " $* " == *" -list "* ]]; then
  printf '%s\n' TestOne TestTwo TestThree TestFour
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
