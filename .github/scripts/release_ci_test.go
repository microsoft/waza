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
	Name            string
	Outputs         map[string]string
	Env             map[string]string
	Uses            string
	Needs           []string
	If              string
	Permissions     map[string]string
	ContinueOnError bool `yaml:"continue-on-error"`
	TimeoutMinutes  int  `yaml:"timeout-minutes"`
	Steps           []releaseStep
}

func TestReleaseWorkflowGates(t *testing.T) {
	source := readReleaseWorkflow(t, "release-validation.yml")
	if err := validateReleaseValidation(source); err != nil {
		t.Fatal(err)
	}
	var workflow releaseWorkflow
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		t.Fatal(err)
	}
	steps := releaseSteps(workflow.Jobs["complete"])
	gate := steps["Require every validation branch"]
	results := []string{"success", "failure", "cancelled", "skipped", ""} //nolint:misspell // GitHub's exact job conclusion.
	for _, field := range []string{"NATIVE_RESULT", "BUILD_RESULT", "WINDOWS_RESULT", "LINT_RESULT"} {
		for _, result := range results {
			env := []string{"NATIVE_RESULT=success", "BUILD_RESULT=success", "WINDOWS_RESULT=success", "LINT_RESULT=success"}
			for i := range env {
				if strings.HasPrefix(env[i], field+"=") {
					env[i] = field + "=" + result
				}
			}
			cmd := exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", gate.Run)
			cmd.Env = append(os.Environ(), env...)
			if err := cmd.Run(); (err == nil) != (result == "success") {
				t.Fatalf("%s=%q: gate error = %v", field, result, err)
			}
		}
	}
	buildSteps := releaseSteps(workflow.Jobs["build-test"])
	testReleasePackageSelection(t, buildSteps["Execute Tests"].Run)
	testReleaseBinaryHandoff(t, buildSteps["Record tested binary digest"].Run, steps["Verify and restore tested binary"].Run)
	testReleaseCoverageEvidence(t, steps["Verify exhaustive native test selection and coverage"].Run)
	windowsSteps := releaseSteps(workflow.Jobs["windows-test"])
	for _, invoked := range []bool{false, true} {
		t.Run(fmt.Sprintf("windows-runtime-invoked-%t", invoked), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "offline-copilot.exe")
			if invoked {
				writeReleaseFile(t, path+".invoked", nil)
			}
			cmd := exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", windowsSteps["Reject forbidden Windows runtime launches"].Run)
			cmd.Env = append(os.Environ(), "COPILOT_CLI_PATH="+path)
			if err := cmd.Run(); (err == nil) == invoked {
				t.Fatalf("runtime invoked = %t: gate error = %v", invoked, err)
			}
		})
	}
	for _, mutation := range []struct{ name, old, new string }{
		{"serialized-build", "  build-test:\n", "  build-test:\n    needs: [native-test-shards]\n"},
		{"tolerated-build-job", "  build-test:\n", "  build-test:\n    continue-on-error: true\n"},
		{"tolerated-completion-job", "  complete:\n", "  complete:\n    continue-on-error: true\n"},
		{"missing-build-dependency", "needs: [native-test-shards, build-test, windows-test, lint]", "needs: [native-test-shards, windows-test, lint]"},
		{"missing-native-dependency", "needs: [native-test-shards, build-test, windows-test, lint]", "needs: [build-test, windows-test, lint]"},
		{"missing-windows-dependency", "needs: [native-test-shards, build-test, windows-test, lint]", "needs: [native-test-shards, build-test, lint]"},
		{"missing-lint-dependency", "needs: [native-test-shards, build-test, windows-test, lint]", "needs: [native-test-shards, build-test, windows-test]"},
		{"cancellation-bypass", "${{ !cancelled() }}", "${{ always() }}"}, //nolint:misspell // GitHub expression.
		{"unbound-build-result", "${{ needs.build-test.result }}", "success"},
		{"ignored-build-result", `test "$BUILD_RESULT" = success`, ":"},
		{"ignored-native-result", `test "$NATIVE_RESULT" = success`, ":"},
		{"ignored-windows-result", `test "$WINDOWS_RESULT" = success`, ":"},
		{"ignored-lint-result", `test "$LINT_RESULT" = success`, ":"},
		{"missing-coverage-verification", "bash .github/scripts/go_test_shard_verify.sh coverage-reports", ":"},
		{"missing-integration", "run: bash .github/scripts/release_integration_test.sh ./waza", "run: ':'"},
		{"skipped-integration", "      - name: Integration Test\n", "      - name: Integration Test\n        if: 'false'\n"},
		{"ignored-integration-failure", "      - name: Integration Test\n", "      - name: Integration Test\n        continue-on-error: true\n"},
		{"missing-guard", `run: bash .github/scripts/go_offline_env.sh >> "$GITHUB_ENV"`, "run: ':'"},
		{"missing-windows-race", "go test -v -race -timeout=30m ./...", "go test ./..."},
		{"skipped-windows-tests", "      - name: Execute complete Windows tests\n", "      - name: Execute complete Windows tests\n        if: 'false'\n"},
		{"tolerated-windows-tests", "      - name: Execute complete Windows tests\n", "      - name: Execute complete Windows tests\n        continue-on-error: true\n"},
		{"skipped-windows-job", "  windows-test:\n", "  windows-test:\n    if: 'false'\n"},
		{"tolerated-windows-job", "  windows-test:\n", "  windows-test:\n    continue-on-error: true\n"},
		{"skipped-windows-guard", "      - name: Install and prove Windows rejecting runtime guard\n", "      - name: Install and prove Windows rejecting runtime guard\n        if: 'false'\n"},
		{"missing-windows-launch-check", `run: test ! -e "$COPILOT_CLI_PATH.invoked"`, "run: ':'"},
		{"tolerated-windows-launch-check", "      - name: Reject forbidden Windows runtime launches\n", "      - name: Reject forbidden Windows runtime launches\n        continue-on-error: true\n"},
		{"missing-lint-schema", "run: bash .github/scripts/lint.sh", "run: golangci-lint run"},
		{"skipped-lint", "      - name: Verify pinned configuration and run golangci-lint\n", "      - name: Verify pinned configuration and run golangci-lint\n        if: 'false'\n"},
		{"tolerated-lint", "      - name: Verify pinned configuration and run golangci-lint\n", "      - name: Verify pinned configuration and run golangci-lint\n        continue-on-error: true\n"},
		{"tolerated-lint-job", "  lint:\n", "  lint:\n    continue-on-error: true\n"},
		{"skipped-build-source", "      - name: Require exact source\n", "      - name: Require exact source\n        if: 'false'\n"},
		{"different-download", "          name: release-tested-${{ github.sha }}\n          path: .", "          name: other-binary\n          path: ."},
		{"missing-binary-digest", "sha256sum --check waza.sha256", ":"},
		{"missing-executable-mode", "chmod +x waza", ":"},
		{"missing-coverage-artifact", "            coverage.txt\n", ""},
		{"stale-source", `run: test "$(git rev-parse HEAD)" = "$GITHUB_SHA"`, "run: ':'"},
		{"incomplete-evidence", "            source-sha.txt\n", ""},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			changed := strings.Replace(string(source), mutation.old, mutation.new, 1)
			if changed == string(source) {
				t.Fatal("mutation did not change production workflow")
			}
			var parsed releaseWorkflow
			if err := yaml.Unmarshal([]byte(changed), &parsed); err != nil {
				t.Fatalf("mutation must reach semantic validation: %v", err)
			}
			if err := validateReleaseValidation([]byte(changed)); err == nil {
				t.Fatal("unsafe production workflow mutation was admitted")
			}
		})
	}
}

func TestReleasePublishersRequireValidation(t *testing.T) {
	for _, file := range []string{"squad-release.yml", "squad-insider-release.yml"} {
		t.Run(file, func(t *testing.T) {
			source := readReleaseWorkflow(t, file)
			if err := validateBranchPublisher(source, file); err != nil {
				t.Fatal(err)
			}
			var workflow releaseWorkflow
			if err := yaml.Unmarshal(source, &workflow); err != nil {
				t.Fatal(err)
			}
			candidateName := "Detect exact stable tag"
			if file == "squad-insider-release.yml" {
				candidateName = "Detect exact insider tag"
			}
			testReleaseCandidateDetection(t, releaseSteps(workflow.Jobs["release-candidate"])[candidateName].Run, file)
			createName := "Create Release"
			if file == "squad-insider-release.yml" {
				createName = "Create Insider Pre-Release"
			}
			testBranchReleaseCreation(t, releaseSteps(workflow.Jobs["release"])[createName].Run, file)
			for _, mutation := range []struct{ name, old, new string }{
				{"untagged-validation", "if: needs.release-candidate.outputs.publish == 'true'", "if: always()"},
				{"tolerated-candidate-job", "  release-candidate:\n", "  release-candidate:\n    continue-on-error: true\n"},
				{"tolerated-publication-job", "  release:\n", "  release:\n    continue-on-error: true\n"},
				{"missing-validation-dependency", "needs: [release-candidate, validation]", "needs: [release-candidate]"},
				{"cancellation-bypass", "&& !cancelled()", "&& always()"}, //nolint:misspell // GitHub expression.
				{"unbound-validation-result", "${{ needs.validation.result }}", "success"},
				{"ignored-validation-result", `test "$VALIDATION_RESULT" = success`, ":"},
				{"stale-source", `test "$(cat source-sha.txt)" = "$GITHUB_SHA"`, ":"},
				{"missing-digest", "sha256sum --check waza.sha256", ":"},
				{"different-evidence", "name: release-validation-${{ github.sha }}", "name: release-validation-other"},
				{"rebuild-before-publication", "run: |\n          if gh release view", "run: |\n          go build -o waza ./cmd/waza\n          if gh release view"},
				{"included-generated-insider-tag", `if [[ "$tag" != *-insider ]]; then`, "if true; then"},
				{"existing-release-success", "            exit 1\n          else\n", "            exit 0\n          else\n"},
			} {
				t.Run(mutation.name, func(t *testing.T) {
					changed := strings.Replace(string(source), mutation.old, mutation.new, 1)
					if changed == string(source) {
						t.Fatal("mutation did not change production workflow")
					}
					if err := validateBranchPublisher([]byte(changed), file); err == nil {
						t.Fatal("unsafe branch publisher mutation was admitted")
					}
				})
			}
			if file == "squad-insider-release.yml" {
				t.Run("missing-insider-target", func(t *testing.T) {
					changed := strings.Replace(string(source), "              --target \"$GITHUB_SHA\" \\\n", "", 1)
					if changed == string(source) {
						t.Fatal("mutation did not change production workflow")
					}
					if err := validateBranchPublisher([]byte(changed), file); err == nil {
						t.Fatal("unsafe branch publisher mutation was admitted")
					}
				})
			}
		})
	}
}

func testBranchReleaseCreation(t *testing.T, run, file string) {
	t.Helper()
	for _, state := range []string{"missing", "complete", "partial", "stale-source", "wrong-tag-target"} {
		t.Run("existing-release-"+state, func(t *testing.T) {
			dir := t.TempDir()
			gh := filepath.Join(dir, "gh")
			writeReleaseFile(t, gh, []byte(`#!/bin/sh
set -eu
case "$1 $2" in
  "release view") test "$RELEASE_STATE" != missing ;;
  "release create") printf '%s\n' "$@" > "$CREATED_RELEASE" ;;
  *) exit 99 ;;
esac
`))
			if err := os.Chmod(gh, 0o700); err != nil {
				t.Fatal(err)
			}
			created := filepath.Join(dir, "created-release")
			cmd := exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", strings.ReplaceAll(run, "${{ github.repository }}", "microsoft/waza"))
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"RELEASE_STATE="+state, "CREATED_RELEASE="+created, "VERSION=v1.2.3", "GITHUB_SHA=validated-source")
			output, err := cmd.CombinedOutput()
			if (err == nil) != (state == "missing") {
				t.Fatalf("release state %s: error = %v\n%s", state, err, output)
			}
			args, readErr := os.ReadFile(created)
			if state != "missing" {
				if !os.IsNotExist(readErr) || !strings.Contains(string(output), "inspect its source and assets") {
					t.Fatalf("existing release must fail without publication: %q, %v\n%s", args, readErr, output)
				}
				return
			}
			if readErr != nil || !strings.Contains(string(args), "./waza\n./waza.sha256\n./source-sha.txt\n") {
				t.Fatalf("fresh release must publish all validated assets: %q, %v", args, readErr)
			}
			if file == "squad-insider-release.yml" && !strings.Contains(string(args), "--target\nvalidated-source\n") {
				t.Fatalf("fresh insider tag must target the validated SHA: %q", args)
			}
		})
	}
}

func testReleaseCandidateDetection(t *testing.T, run, file string) {
	t.Helper()
	for _, tc := range []struct {
		name        string
		tags        []string
		wantSuccess bool
		wantOutput  string
	}{
		{name: "untagged", wantSuccess: true, wantOutput: "publish=false\n"},
		{name: "base-tag", tags: []string{"v1.2.3"}, wantSuccess: true, wantOutput: "publish=true\ntag=v1.2.3\n"},
		{name: "base-and-insider-tags", tags: []string{"v1.2.3", "v1.2.3-insider"}, wantSuccess: true, wantOutput: "publish=true\ntag=v1.2.3\n"},
		{name: "multiple-base-tags", tags: []string{"v1.2.3", "v1.2.4"}, wantSuccess: false},
	} {
		t.Run("candidate-"+tc.name, func(t *testing.T) {
			dir := t.TempDir()
			runReleaseCommand(t, dir, "git", "init", "-q")
			runReleaseCommand(t, dir, "git", "config", "user.name", "Waza Test")
			runReleaseCommand(t, dir, "git", "config", "user.email", "waza-test@example.com")
			runReleaseCommand(t, dir, "git", "commit", "--allow-empty", "-q", "-m", "test")
			for _, tag := range tc.tags {
				runReleaseCommand(t, dir, "git", "tag", tag)
			}
			sha := strings.TrimSpace(string(runReleaseCommand(t, dir, "git", "rev-parse", "HEAD")))
			outputPath := filepath.Join(dir, "github-output")
			cmd := exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", run)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GITHUB_SHA="+sha, "GITHUB_OUTPUT="+outputPath)
			output, err := cmd.CombinedOutput()
			if (err == nil) != tc.wantSuccess {
				t.Fatalf("%s candidate error = %v\n%s", file, err, output)
			}
			if !tc.wantSuccess {
				return
			}
			got, err := os.ReadFile(outputPath)
			if err != nil {
				t.Fatal(err)
			}
			want := tc.wantOutput
			if file == "squad-insider-release.yml" {
				want = strings.Replace(want, "tag=v1.2.3\n", "tag=v1.2.3-insider\n", 1)
			}
			if string(got) != want {
				t.Fatalf("candidate output = %q; want %q", got, want)
			}
		})
	}
}

func runReleaseCommand(t *testing.T, dir, name string, args ...string) []byte {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), name, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, output)
	}
	return output
}

func TestTagReleaseRequiresValidation(t *testing.T) {
	source := readReleaseWorkflow(t, "release.yml")
	normalized := strings.ReplaceAll(string(source), "\r\n", "\n")
	if err := validateTagRelease([]byte(normalized)); err != nil {
		t.Fatal(err)
	}
	var workflow releaseWorkflow
	if err := yaml.Unmarshal([]byte(normalized), &workflow); err != nil {
		t.Fatal(err)
	}
	testTagCLIArtifactVerification(t, releaseSteps(workflow.Jobs["release-cli"])["Generate SHA256 Checksums"].Run)
	for _, mutation := range []struct{ name, old, new string }{
		{
			"missing-cli-validation",
			"  build-cli:\n    name: CLI ${{ matrix.os }}/${{ matrix.arch }}\n    needs: [setup-version, validation]",
			"  build-cli:\n    name: CLI ${{ matrix.os }}/${{ matrix.arch }}\n    needs: [setup-version]",
		},
		{
			"missing-extension-validation",
			"  release-extension:\n    name: Release Extension\n    needs: [setup-version, validation]",
			"  release-extension:\n    name: Release Extension\n    needs: [setup-version]",
		},
		{"unconditional-cli-build", "  build-cli:\n", "  build-cli:\n    if: always()\n"},
		{"unconditional-cli-publication", "  release-cli:\n", "  release-cli:\n    if: always()\n"},
		{"unconditional-extension-publication", "  release-extension:\n", "  release-extension:\n    if: always()\n"},
		{"tolerated-cli-build", "  build-cli:\n", "  build-cli:\n    continue-on-error: true\n"},
		{"tolerated-cli-publication", "  release-cli:\n", "  release-cli:\n    continue-on-error: true\n"},
		{"tolerated-extension-publication", "  release-extension:\n", "  release-extension:\n    continue-on-error: true\n"},
		{"skipped-cli-checksums", "      - name: Generate SHA256 Checksums\n", "      - name: Generate SHA256 Checksums\n        if: 'false'\n"},
		{"tolerated-cli-checksums", "      - name: Generate SHA256 Checksums\n", "      - name: Generate SHA256 Checksums\n        continue-on-error: true\n"},
		{"skipped-cli-source", "      - name: Require exact validated source\n", "      - name: Require exact validated source\n        if: 'false'\n"},
		{"skipped-extension-source", "          fetch-depth: 0\n          lfs: true\n\n      - name: Require exact validated source\n", "          fetch-depth: 0\n          lfs: true\n\n      - name: Require exact validated source\n        if: 'false'\n"},
		{"stale-cli-source", `run: test "$(git rev-parse HEAD)" = "$GITHUB_SHA"`, "run: ':'"},
		{"missing-cli-source-record", `printf '%s\n' "$GITHUB_SHA" > "${BINARY_NAME}.source-sha.txt"`, ":"},
		{"missing-cli-digest", `sha256sum "${BINARY_NAME}" > "${BINARY_NAME}.sha256"`, ":"},
		{"missing-cli-matrix-member", "            waza-windows-arm64.exe\n", ""},
		{"missing-cli-binary-file", `test -f "$binary"`, ":"},
		{"missing-cli-source-file", `test -f "$source"`, ":"},
		{"missing-cli-digest-file", `test -f "$digest"`, ":"},
		{"missing-cli-source-verification", `test "$(cat "$source")" = "$GITHUB_SHA"`, ":"},
		{"missing-extension-provenance", `printf 'source_sha=%s\n' "$GITHUB_SHA"`, ":"},
		{"unpublished-extension-provenance", "Publish Extension Provenance", "Do Not Publish Extension Provenance"},
		{"wrong-extension-provenance-tag", `azd-ext-microsoft-azd-waza_${VERSION}`, "${{ needs.setup-version.outputs.tag }}"},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			changed := strings.Replace(normalized, mutation.old, mutation.new, 1)
			if changed == normalized {
				t.Fatal("mutation did not change production workflow")
			}
			var parsed releaseWorkflow
			if err := yaml.Unmarshal([]byte(changed), &parsed); err != nil {
				t.Fatalf("mutation must reach semantic validation: %v", err)
			}
			if err := validateTagRelease([]byte(changed)); err == nil {
				t.Fatal("unsafe tag release mutation was admitted")
			}
		})
	}
}

func testTagCLIArtifactVerification(t *testing.T, run string) {
	t.Helper()
	binaries := []string{
		"waza-linux-amd64",
		"waza-linux-arm64",
		"waza-darwin-amd64",
		"waza-darwin-arm64",
		"waza-windows-amd64.exe",
		"waza-windows-arm64.exe",
	}
	for _, mutation := range []string{"none", "missing-binary", "missing-source", "missing-digest", "stale-source", "digest-mismatch", "foreign-digest"} {
		t.Run("cli-artifacts-"+mutation, func(t *testing.T) {
			dir := t.TempDir()
			for _, binary := range binaries {
				writeReleaseFile(t, filepath.Join(dir, binary), []byte(binary))
				writeReleaseFile(t, filepath.Join(dir, binary+".source-sha.txt"), []byte("validated-source\n"))
				cmd := exec.CommandContext(t.Context(), "sha256sum", binary)
				cmd.Dir = dir
				digest, err := cmd.Output()
				if err != nil {
					t.Fatal(err)
				}
				writeReleaseFile(t, filepath.Join(dir, binary+".sha256"), digest)
			}
			target := binaries[len(binaries)-1]
			switch mutation {
			case "missing-binary":
				if err := os.Remove(filepath.Join(dir, target)); err != nil {
					t.Fatal(err)
				}
			case "missing-source":
				if err := os.Remove(filepath.Join(dir, target+".source-sha.txt")); err != nil {
					t.Fatal(err)
				}
			case "missing-digest":
				if err := os.Remove(filepath.Join(dir, target+".sha256")); err != nil {
					t.Fatal(err)
				}
			case "stale-source":
				writeReleaseFile(t, filepath.Join(dir, target+".source-sha.txt"), []byte("stale-source\n"))
			case "digest-mismatch":
				writeReleaseFile(t, filepath.Join(dir, target), []byte("altered binary"))
			case "foreign-digest":
				foreign, err := os.ReadFile(filepath.Join(dir, binaries[0]+".sha256"))
				if err != nil {
					t.Fatal(err)
				}
				writeReleaseFile(t, filepath.Join(dir, target+".sha256"), foreign)
			}
			cmd := exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", run)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GITHUB_SHA=validated-source")
			output, err := cmd.CombinedOutput()
			if (err == nil) != (mutation == "none") {
				t.Fatalf("verification error = %v\n%s", err, output)
			}
		})
	}
}

func readReleaseWorkflow(t *testing.T, file string) []byte {
	t.Helper()
	return readNormalizedReleaseWorkflow(t, filepath.Join("..", "workflows", file))
}

func readNormalizedReleaseWorkflow(t *testing.T, path string) []byte {
	t.Helper()
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return []byte(strings.ReplaceAll(string(source), "\r\n", "\n"))
}

func TestReleaseWorkflowLineEndings(t *testing.T) {
	for _, file := range []string{"release-validation.yml", "release.yml", "squad-release.yml", "squad-insider-release.yml"} {
		t.Run(file, func(t *testing.T) {
			source := readReleaseWorkflow(t, file)
			for _, ending := range []string{"\n", "\r\n"} {
				t.Run(fmt.Sprintf("%q", ending), func(t *testing.T) {
					wire := strings.ReplaceAll(string(source), "\n", ending)
					if ending == "\r\n" && wire == string(source) {
						t.Fatal("CRLF fixture did not change the production source")
					}
					path := filepath.Join(t.TempDir(), file)
					writeReleaseFile(t, path, []byte(wire))
					normalized := readNormalizedReleaseWorkflow(t, path)
					if string(normalized) != string(source) {
						t.Fatal("workflow reader changed more than line endings")
					}
					mutated := strings.Replace(string(normalized), "if: needs.release-candidate.outputs.publish == 'true'", "if: always()", 1)
					validate := func(input []byte) error { return validateBranchPublisher(input, file) }
					switch file {
					case "release-validation.yml":
						validate = validateReleaseValidation
						mutated = strings.Replace(string(normalized), "  windows-test:\n", "  windows-test:\n    if: 'false'\n", 1)
					case "release.yml":
						validate = validateTagRelease
						mutated = strings.Replace(string(normalized), "  validation:\n", "  validation:\n    if: always()\n", 1)
					}
					if err := validate(normalized); err != nil {
						t.Fatalf("valid normalized workflow: %v", err)
					}
					if mutated == string(normalized) {
						t.Fatal("normalized mutation did not reach its target")
					}
					var parsed releaseWorkflow
					if err := yaml.Unmarshal([]byte(mutated), &parsed); err != nil {
						t.Fatalf("mutation must reach semantic validation: %v", err)
					}
					if err := validate([]byte(mutated)); err == nil {
						t.Fatal("unsafe normalized workflow mutation was admitted")
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

func validateReleaseValidation(source []byte) error {
	var workflow releaseWorkflow
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		return err
	}
	native := workflow.Jobs["native-test-shards"]
	build := workflow.Jobs["build-test"]
	windows := workflow.Jobs["windows-test"]
	lint := workflow.Jobs["lint"]
	complete := workflow.Jobs["complete"]
	if workflow.Permissions["contents"] != "read" || complete.TimeoutMinutes != 15 ||
		build.TimeoutMinutes != 60 || windows.TimeoutMinutes != 60 || lint.TimeoutMinutes != 10 ||
		build.ContinueOnError || windows.ContinueOnError || lint.ContinueOnError || complete.ContinueOnError {
		return fmt.Errorf("validation permissions and bounded deadlines must be preserved")
	}
	if native.Uses != "./.github/workflows/go-native-shards.yml" || len(native.Needs) != 0 ||
		len(build.Needs) != 0 || len(windows.Needs) != 0 || len(lint.Needs) != 0 ||
		build.If != "" || windows.If != "" || lint.If != "" || native.If != "" ||
		!reflect.DeepEqual(complete.Needs, []string{"native-test-shards", "build-test", "windows-test", "lint"}) {
		return fmt.Errorf("validation branches must run independently and completion must depend on all of them")
	}
	if complete.If != "${{ !cancelled() }}" { //nolint:misspell // GitHub's exact status function.
		return fmt.Errorf("validation completion must be ineligible after workflow cancellation")
	}
	if err := orderedReleaseSteps(build, []string{"Checkout Repository", "Require exact source", "Install and prove rejecting runtime guard",
		"Test release gate failure handling", "Run Go Vet", "Execute Tests", "Build Release Binary", "Integration Test",
		"Record tested binary digest", "Upload tested release binary and remaining coverage"}); err != nil {
		return err
	}
	if err := orderedReleaseSteps(complete, []string{"Require every validation branch", "Checkout Repository", "Require exact source",
		"Download tested release binary and remaining coverage", "Download native coverage and discovery evidence",
		"Verify exhaustive native test selection and coverage", "Verify and restore tested binary",
		"Record validated source", "Upload complete validation evidence"}); err != nil {
		return err
	}
	if err := orderedReleaseSteps(windows, []string{"Checkout Repository", "Require exact source",
		"Setup Go Environment", "Setup Node.js (offline assertion oracle)", "Verify Module Dependencies",
		"Ensure web/dist exists", "Run Go Vet", "Install and prove Windows rejecting runtime guard",
		"Execute complete Windows tests", "Reject forbidden Windows runtime launches"}); err != nil {
		return err
	}
	if err := orderedReleaseSteps(lint, []string{"Checkout Repository", "Require exact source",
		"Setup Go Environment", "Ensure web/dist exists (stub for go:embed)", "Install golangci-lint",
		"Test lint script failure handling", "Verify pinned configuration and run golangci-lint"}); err != nil {
		return err
	}
	steps, buildSteps := releaseSteps(complete), releaseSteps(build)
	if buildSteps["Checkout Repository"].With["lfs"] != "true" || buildSteps["Checkout Repository"].With["fetch-depth"] != "0" ||
		strings.Count(string(source), `test "$(git rev-parse HEAD)" = "$GITHUB_SHA"`) != 4 {
		return fmt.Errorf("every validation branch must bind checkout to the exact source SHA")
	}
	gate := steps["Require every validation branch"]
	if gate.Env["NATIVE_RESULT"] != "${{ needs.native-test-shards.result }}" ||
		gate.Env["BUILD_RESULT"] != "${{ needs.build-test.result }}" ||
		gate.Env["WINDOWS_RESULT"] != "${{ needs.windows-test.result }}" ||
		gate.Env["LINT_RESULT"] != "${{ needs.lint.result }}" ||
		gate.Run != "test \"$NATIVE_RESULT\" = success\ntest \"$BUILD_RESULT\" = success\ntest \"$WINDOWS_RESULT\" = success\ntest \"$LINT_RESULT\" = success\n" {
		return fmt.Errorf("completion must reject every non-success validation result")
	}
	if buildSteps["Install and prove rejecting runtime guard"].Run != `bash .github/scripts/go_offline_env.sh >> "$GITHUB_ENV"` ||
		buildSteps["Test release gate failure handling"].Run != "bash .github/scripts/release_gate_test.sh\ngo test -count=1 -timeout=10m ./.github/scripts ./.github/scripts/publish-runner-reference-guard ./.github/scripts/verify-runner-reference-guard\n" ||
		buildSteps["Run Go Vet"].Run != "go vet ./..." ||
		buildSteps["Build Release Binary"].Run != "go build -v -o waza ./cmd/waza\n./waza --version\n" ||
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
	windowsSteps := releaseSteps(windows)
	if windowsSteps["Run Go Vet"].Run != "go vet ./..." ||
		windowsSteps["Install and prove Windows rejecting runtime guard"].Run != `bash .github/scripts/go_offline_env.sh >> "$GITHUB_ENV"` ||
		windowsSteps["Reject forbidden Windows runtime launches"].Run != `test ! -e "$COPILOT_CLI_PATH.invoked"` ||
		windowsSteps["Execute complete Windows tests"].Run != "go test -v -race -timeout=30m ./..." {
		return fmt.Errorf("release validation must retain native Windows vet, guarded race execution and launch detection")
	}
	lintSteps := releaseSteps(lint)
	if lintSteps["Test lint script failure handling"].Run != "bash .github/scripts/lint_test.sh" ||
		lintSteps["Verify pinned configuration and run golangci-lint"].Run != "bash .github/scripts/lint.sh" {
		return fmt.Errorf("release validation must retain pinned lint schema verification")
	}
	evidence := steps["Upload complete validation evidence"]
	if evidence.With["name"] != "release-validation-${{ github.sha }}" ||
		evidence.With["path"] != "waza\nwaza.sha256\nsource-sha.txt\n" ||
		evidence.With["if-no-files-found"] != "error" ||
		steps["Record validated source"].Run != "printf '%s\\n' \"$GITHUB_SHA\" > source-sha.txt" {
		return fmt.Errorf("complete evidence must identify the validated source and tested binary")
	}
	for _, step := range complete.Steps {
		if strings.Contains(step.Run, "go build") {
			return fmt.Errorf("validation completion must not rebuild its tested binary")
		}
	}
	return nil
}

func validateBranchPublisher(source []byte, file string) error {
	var workflow releaseWorkflow
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		return err
	}
	candidate := workflow.Jobs["release-candidate"]
	validation := workflow.Jobs["validation"]
	publish := workflow.Jobs["release"]
	if workflow.Permissions["contents"] != "write" || candidate.TimeoutMinutes != 5 || publish.TimeoutMinutes != 15 ||
		candidate.ContinueOnError || publish.ContinueOnError {
		return fmt.Errorf("publisher permissions and bounded deadlines must be preserved")
	}
	if validation.Uses != "./.github/workflows/release-validation.yml" ||
		!reflect.DeepEqual(validation.Needs, []string{"release-candidate"}) ||
		validation.If != "needs.release-candidate.outputs.publish == 'true'" ||
		validation.Permissions["contents"] != "read" {
		return fmt.Errorf("tagged branch publication must invoke the canonical validation workflow")
	}
	if !reflect.DeepEqual(publish.Needs, []string{"release-candidate", "validation"}) ||
		publish.If != "needs.release-candidate.outputs.publish == 'true' && !cancelled()" { //nolint:misspell // GitHub expression.
		return fmt.Errorf("publication must require a tag, validation and non-canceled run")
	}
	candidateSteps := releaseSteps(candidate)
	tagStep := candidateSteps["Detect exact stable tag"]
	createName := "Create Release"
	if file == "squad-insider-release.yml" {
		tagStep = candidateSteps["Detect exact insider tag"]
		createName = "Create Insider Pre-Release"
	}
	if tagStep.Run == "" || !strings.Contains(tagStep.Run, `git tag --points-at "$GITHUB_SHA" --list 'v*.*.*'`) ||
		!strings.Contains(tagStep.Run, `if [[ "$tag" != *-insider ]]; then`) ||
		!strings.Contains(tagStep.Run, `echo "publish=false" >> "$GITHUB_OUTPUT"`) ||
		!strings.Contains(tagStep.Run, `if [ "${#tags[@]}" -gt 1 ]`) {
		return fmt.Errorf("candidate detection must fail closed and skip untagged branch pushes")
	}
	if err := orderedReleaseSteps(publish, []string{"Require successful complete validation",
		"Download complete validation evidence", "Require exact validated source and tested binary", createName}); err != nil {
		return err
	}
	steps := releaseSteps(publish)
	gate := steps["Require successful complete validation"]
	if gate.Env["VALIDATION_RESULT"] != "${{ needs.validation.result }}" ||
		gate.Run != `test "$VALIDATION_RESULT" = success` {
		return fmt.Errorf("publisher must reject missing, failed, canceled or skipped validation")
	}
	download := steps["Download complete validation evidence"]
	if download.With["name"] != "release-validation-${{ github.sha }}" || download.With["path"] != "." ||
		steps["Require exact validated source and tested binary"].Run != "test \"$(cat source-sha.txt)\" = \"$GITHUB_SHA\"\nsha256sum --check waza.sha256\nchmod +x waza\n./waza --version\n" {
		return fmt.Errorf("publisher must consume complete exact-source tested evidence")
	}
	if !strings.Contains(steps[createName].Run, "./waza ./waza.sha256 ./source-sha.txt") {
		return fmt.Errorf("publisher must publish source and digest evidence with the tested binary")
	}
	if !strings.Contains(steps[createName].Run, "before retrying publication.\" >&2\n  exit 1\nelse\n") {
		return fmt.Errorf("publisher must reject unverified existing releases instead of succeeding")
	}
	for _, step := range publish.Steps {
		if strings.Contains(step.Run, "go build") {
			return fmt.Errorf("publisher must not rebuild its validated binary")
		}
	}
	if file == "squad-insider-release.yml" &&
		!strings.Contains(steps[createName].Run, `--target "$GITHUB_SHA"`) {
		return fmt.Errorf("insider release tag must target the validated source")
	}
	return nil
}

func validateTagRelease(source []byte) error {
	var workflow releaseWorkflow
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		return err
	}
	validation := workflow.Jobs["validation"]
	build := workflow.Jobs["build-cli"]
	release := workflow.Jobs["release-cli"]
	extension := workflow.Jobs["release-extension"]
	if validation.Uses != "./.github/workflows/release-validation.yml" ||
		validation.Permissions["contents"] != "read" {
		return fmt.Errorf("tag release must invoke canonical exact-source validation")
	}
	if !reflect.DeepEqual(build.Needs, []string{"setup-version", "validation"}) ||
		!reflect.DeepEqual(extension.Needs, []string{"setup-version", "validation"}) ||
		!reflect.DeepEqual(release.Needs, []string{"setup-version", "build-cli", "validation"}) {
		return fmt.Errorf("every tag publisher and artifact builder must depend on validation")
	}
	if validation.If != "" || build.If != "" || release.If != "" || extension.If != "" ||
		build.ContinueOnError || release.ContinueOnError || extension.ContinueOnError {
		return fmt.Errorf("tag validation, builders and publishers must retain success-only dependency admission")
	}
	if err := orderedReleaseSteps(build, []string{"Checkout Repository", "Require exact validated source",
		"Setup Go Environment", "Build Binary", "Upload Binary Artifact"}); err != nil {
		return err
	}
	if err := orderedReleaseSteps(release, []string{"Checkout Repository", "Download CLI Artifacts",
		"Generate SHA256 Checksums", "Create or Update GitHub Release"}); err != nil {
		return err
	}
	if err := orderedReleaseSteps(extension, []string{"Checkout Repository", "Require exact validated source",
		"Setup Go Environment", "Sync Version Files", "Build Extension", "Pack Extension",
		"Record extension artifact provenance", "Release Extension", "Publish Extension Provenance",
		"Publish Extension", "Create Release PR"}); err != nil {
		return err
	}
	buildSteps := releaseSteps(build)
	publishSteps := releaseSteps(release)
	extensionSteps := releaseSteps(extension)
	if buildSteps["Require exact validated source"].Run != `test "$(git rev-parse HEAD)" = "$GITHUB_SHA"` ||
		extensionSteps["Require exact validated source"].Run != `test "$(git rev-parse HEAD)" = "$GITHUB_SHA"` {
		return fmt.Errorf("tag artifacts must be built from the validated SHA")
	}
	buildRun := buildSteps["Build Binary"].Run
	if !strings.Contains(buildRun, `printf '%s\n' "$GITHUB_SHA" > "${BINARY_NAME}.source-sha.txt"`) ||
		!strings.Contains(buildRun, `sha256sum "${BINARY_NAME}" > "${BINARY_NAME}.sha256"`) {
		return fmt.Errorf("each CLI artifact must carry source and digest evidence")
	}
	checks := publishSteps["Generate SHA256 Checksums"].Run
	expectedCLIBinaries := `expected=(
  waza-linux-amd64
  waza-linux-arm64
  waza-darwin-amd64
  waza-darwin-arm64
  waza-windows-amd64.exe
  waza-windows-arm64.exe
)`
	if !strings.Contains(checks, expectedCLIBinaries) ||
		!strings.Contains(checks, `for binary in "${expected[@]}"`) ||
		!strings.Contains(checks, `test -f "$binary"`) ||
		!strings.Contains(checks, `test -f "$source"`) ||
		!strings.Contains(checks, `test -f "$digest"`) ||
		!strings.Contains(checks, `test "$(cat "$source")" = "$GITHUB_SHA"`) ||
		!strings.Contains(checks, `test "$(cat "$digest")" = "$(sha256sum "$binary")"`) ||
		!strings.Contains(checks, `sha256sum "${expected[@]}" > checksums.txt`) {
		return fmt.Errorf("CLI publication must reject stale or incomplete artifact evidence")
	}
	provenance := extensionSteps["Record extension artifact provenance"].Run
	if !strings.Contains(provenance, `printf 'source_sha=%s\n' "$GITHUB_SHA"`) ||
		!strings.Contains(provenance, `sha256sum "${packages[@]}"`) ||
		!strings.Contains(extensionSteps["Publish Extension Provenance"].Run, `gh release upload "azd-ext-microsoft-azd-waza_${VERSION}"`) ||
		!strings.Contains(extensionSteps["Publish Extension Provenance"].Run, "artifacts/release-provenance.txt") {
		return fmt.Errorf("extension release must publish source-bound package digests")
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
