package scripts

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const releaseSourceCheck = "source_sha=\"$(git rev-parse HEAD)\"\ntest \"$source_sha\" = \"$GITHUB_SHA\"\n"
const releaseDigestCheck = "recorded_digest=\"$(cat waza.sha256)\"\nactual_digest=\"$(sha256sum waza)\"\ntest \"$recorded_digest\" = \"$actual_digest\"\n"
const releaseRecordedSourceCheck = "source_sha=\"$(cat source-sha.txt)\"\ntest \"$source_sha\" = \"$GITHUB_SHA\"\n"
const releaseDashboardCheck = "node .github/scripts/release_dashboard_check.mjs"

type releaseStep struct {
	ID               string `yaml:"id"`
	Name             string
	Run              string
	Uses             string
	If               string
	ContinueOnError  bool   `yaml:"continue-on-error"`
	WorkingDirectory string `yaml:"working-directory"`
	Env              map[string]string
	With             map[string]string
}

type releaseJob struct {
	Name            string
	RunsOn          string `yaml:"runs-on"`
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
	testReleaseRunnerContracts(t, source, map[string]string{
		"build-test": "ubuntu-latest", "windows-test": "windows-latest",
		"lint": "ubuntu-latest", "complete": "ubuntu-latest", "native-test-shards": "",
	}, validateReleaseValidation)
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
	for _, job := range []string{"build-test", "windows-test", "lint", "complete"} {
		testReleaseSourceLookup(t, releaseSteps(workflow.Jobs[job])["Require exact source"].Run)
	}
	testReleasePackageSelection(t, buildSteps["Execute Tests"].Run)
	testReleaseDashboardOutput(t, buildSteps["Require built dashboard output"].Run, buildSteps["Build Release Binary"].Run)
	testReleaseDashboardMutations(t, source, "build-test", validateReleaseValidation)
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
		{"skipped-web-build", "      - name: Build Web UI\n", "      - name: Build Web UI\n        if: 'false'\n"},
		{"tolerated-web-build", "      - name: Build Web UI\n", "      - name: Build Web UI\n        continue-on-error: true\n"},
		{"skipped-web-node", "      - name: Setup Node.js (for web UI build)\n", "      - name: Setup Node.js (for web UI build)\n        if: 'false'\n"},
		{"wrong-web-directory", "        working-directory: web\n", "        working-directory: .\n"},
		{"missing-web-build", "run: npm ci && npm run build", "run: ':'"},
		{"missing-dashboard-admission", "      - name: Require built dashboard output\n", "      - name: Removed dashboard admission\n"},
		{"skipped-dashboard-admission", "      - name: Require built dashboard output\n", "      - name: Require built dashboard output\n        if: 'false'\n"},
		{"tolerated-dashboard-admission", "      - name: Require built dashboard output\n", "      - name: Require built dashboard output\n        continue-on-error: true\n"},
		{"missing-dashboard-check", "run: " + releaseDashboardCheck, "run: ':'"},
		{"ignored-dashboard-error", "run: " + releaseDashboardCheck, "run: " + releaseDashboardCheck + " || true"},
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
		{"missing-windows-race", "go test -v -race -count=1 -timeout=30m ./...", "go test ./..."},
		{"cached-windows-tests", "go test -v -race -count=1 -timeout=30m ./...", "go test -v -race -timeout=30m ./..."},
		{"cached-linux-tests", `go test -v -race -count=1 -timeout=30m -coverprofile=coverage.txt -covermode=atomic "${selected[@]}"`, `go test -v -race -timeout=30m -coverprofile=coverage.txt -covermode=atomic "${selected[@]}"`},
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
		{"missing-binary-digest", `test "$recorded_digest" = "$actual_digest"`, ":"},
		{"unchecked-binary-hash", `actual_digest="$(sha256sum waza)"`, `actual_digest="$(sha256sum waza)" || true`},
		{"unchecked-sidecar-read", `recorded_digest="$(cat waza.sha256)"`, `recorded_digest="$(cat waza.sha256)" || true`},
		{"missing-executable-mode", "chmod +x waza", ":"},
		{"missing-coverage-artifact", "            coverage.txt\n", ""},
		{"stale-source", `test "$source_sha" = "$GITHUB_SHA"`, ":"},
		{"unchecked-source-lookup", `source_sha="$(git rev-parse HEAD)"`, `source_sha="$(git rev-parse HEAD)" || true`},
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

func testReleaseDashboardOutput(t *testing.T, admission, build string) {
	t.Helper()
	checker, err := os.ReadFile("release_dashboard_check.mjs")
	if err != nil {
		t.Fatal(err)
	}
	index, favicon := "web/dist/index.html", "web/dist/favicon.svg"
	js, css := "web/dist/assets/index-a.js", "web/dist/assets/index-b.css"
	currentIndex, err := os.ReadFile("../../web/dist/index.html")
	if err != nil {
		t.Fatal(err)
	}
	currentFiles := []string{index, favicon}
	currentRefs := regexp.MustCompile(`/assets/[^"']+`).FindAllString(string(currentIndex), -1)
	if len(currentRefs) < 2 {
		t.Fatal("current tracked index must provide actual JavaScript/CSS reference controls")
	}
	for _, ref := range currentRefs {
		currentFiles = append(currentFiles, "web/dist"+ref)
	}
	for _, fixture := range []struct {
		name        string
		files       []string
		directories []string
		empty       string
		admitted    bool
	}{
		{"complete", []string{index, favicon, js, css}, nil, "", true},
		{"current-index", currentFiles, nil, "", true},
		{"alternative-bundles", []string{index, favicon, "web/dist/assets/app.js", "web/dist/assets/theme.css", "web/dist/assets/chunk.js"}, nil, "", true},
		{"missing-tree", nil, nil, "", false},
		{"missing-index", []string{favicon, js, css}, nil, "", false},
		{"missing-favicon", []string{index, js, css}, nil, "", false},
		{"missing-assets", []string{index, favicon}, nil, "", false},
		{"empty-assets", []string{index, favicon}, []string{"web/dist/assets"}, "", false},
		{"missing-js", []string{index, favicon, css}, nil, "", false},
		{"missing-css", []string{index, favicon, js}, nil, "", false},
		{"empty-index", []string{index, favicon, js, css}, nil, index, false},
		{"empty-favicon", []string{index, favicon, js, css}, nil, favicon, false},
		{"empty-js", []string{index, favicon, js, css}, nil, js, false},
		{"empty-css", []string{index, favicon, js, css}, nil, css, false},
		{"directory-index", []string{favicon, js, css}, []string{index}, "", false},
		{"directory-favicon", []string{index, js, css}, []string{favicon}, "", false},
		{"directory-js", []string{index, favicon, css}, []string{js}, "", false},
		{"directory-css", []string{index, favicon, js}, []string{css}, "", false},
		{"symlink-index", []string{index, favicon, js, css}, nil, "", false},
		{"symlink-favicon", []string{index, favicon, js, css}, nil, "", false},
		{"symlink-js", []string{index, favicon, js, css}, nil, "", false},
		{"symlink-css", []string{index, favicon, js, css}, nil, "", false},
		{"symlink-dist", []string{index, favicon, js, css}, nil, "", false},
		{"symlink-assets", []string{index, favicon, js, css}, nil, "", false},
		{"missing-referenced-js", []string{index, favicon, js, css}, nil, "", false},
		{"missing-referenced-css", []string{index, favicon, js, css}, nil, "", false},
		{"missing-modulepreload", []string{index, favicon, js, css}, nil, "", false},
		{"empty-referenced-js", []string{index, favicon, js, css}, nil, "", false},
		{"directory-referenced-js", []string{index, favicon, js, css}, nil, "", false},
		{"symlink-referenced-js", []string{index, favicon, js, css}, nil, "", false},
		{"placeholder-index", []string{index, favicon, js, css}, nil, "", false},
		{"escaping-reference", []string{index, favicon, js, css}, nil, "", false},
	} {
		t.Run("dashboard-"+fixture.name, func(t *testing.T) {
			dir := t.TempDir()
			scripts := filepath.Join(dir, ".github", "scripts")
			if err := os.MkdirAll(scripts, 0o700); err != nil {
				t.Fatal(err)
			}
			writeReleaseFile(t, filepath.Join(scripts, "release_dashboard_check.mjs"), checker)
			refs := []string{}
			for _, path := range fixture.directories {
				if err := os.MkdirAll(filepath.Join(dir, path), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			for _, path := range fixture.files {
				if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0o700); err != nil {
					t.Fatal(err)
				}
				content := []byte("fixture dashboard output")
				if strings.HasSuffix(path, ".js") || strings.HasSuffix(path, ".css") {
					refs = append(refs, strings.TrimPrefix(path, "web/dist"))
				}
				if path == fixture.empty {
					content = nil
				}
				writeReleaseFile(t, filepath.Join(dir, path), content)
			}
			if slices.Contains(fixture.files, index) && fixture.empty != index {
				var html strings.Builder
				for _, ref := range refs {
					if strings.HasSuffix(ref, ".js") {
						fmt.Fprintf(&html, "<script src=\"%s\"></script>\n", ref)
					} else {
						fmt.Fprintf(&html, "<link href=\"%s\" rel=\"stylesheet\">\n", ref)
					}
				}
				extra := "web/dist/assets/referenced.js"
				switch fixture.name {
				case "missing-referenced-js", "empty-referenced-js", "directory-referenced-js", "symlink-referenced-js":
					html.WriteString("<script src=\"/assets/referenced.js\"></script>\n")
				case "missing-referenced-css":
					html.WriteString("<link href=\"/assets/missing.css\" rel=\"stylesheet\">\n")
				case "missing-modulepreload":
					html.WriteString("<link rel=\"modulepreload\" href=\"/assets/preload.js\">\n")
				case "placeholder-index":
					html.Reset()
					html.WriteString("<html>placeholder</html>")
				case "escaping-reference":
					html.WriteString("<script src=\"/assets/../outside.js\"></script>\n")
				}
				switch fixture.name {
				case "empty-referenced-js":
					writeReleaseFile(t, filepath.Join(dir, extra), nil)
				case "directory-referenced-js":
					if err := os.Mkdir(filepath.Join(dir, extra), 0o700); err != nil {
						t.Fatal(err)
					}
				case "symlink-referenced-js":
					if err := os.Symlink(filepath.Join(dir, js), filepath.Join(dir, extra)); err != nil {
						t.Fatal(err)
					}
				}
				writeReleaseFile(t, filepath.Join(dir, index), []byte(html.String()))
				if fixture.name == "current-index" {
					writeReleaseFile(t, filepath.Join(dir, index), currentIndex)
				}
			}
			links := map[string]string{
				"symlink-index": index, "symlink-favicon": favicon,
				"symlink-js": js, "symlink-css": css,
				"symlink-dist": "web/dist", "symlink-assets": "web/dist/assets",
			}
			if path := links[fixture.name]; path != "" {
				original, target := filepath.Join(dir, path), filepath.Join(dir, "link-target")
				if err := os.Rename(original, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, original); err != nil {
					t.Fatal(err)
				}
			}
			bin := filepath.Join(dir, "bin")
			if err := os.Mkdir(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			mockGo := "#!/bin/bash\nset -euo pipefail\ntest \"$1\" = build\nbinary=''\nwhile [ \"$#\" -gt 0 ]; do\n  if [ \"$1\" = -o ]; then binary=\"$2\"; shift 2; else shift; fi\ndone\ntest -n \"$binary\"\nprintf '%s' \"$binary\" > build-started\nprintf '#!/bin/bash\\nprintf executed > binary-executed\\n' > \"$binary\"\nchmod +x \"$binary\"\n"
			writeReleaseFile(t, filepath.Join(bin, "go"), []byte(mockGo))
			writeReleaseFile(t, filepath.Join(bin, "azd"), []byte("#!/bin/bash\nset -euo pipefail\ntest \"$1\" = x\ntest \"$2\" = build\nprintf extension-binary > build-started\nprintf fixture > extension-binary\n"))
			cmd := exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", admission+"\n"+build)
			cmd.Dir, cmd.Env = dir, append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			output, err := cmd.CombinedOutput()
			if (err == nil) != fixture.admitted {
				t.Fatalf("dashboard admission = %v: %v\n%s", fixture.admitted, err, output)
			}
			for _, marker := range []string{"build-started", "binary-executed"} {
				_, statErr := os.Stat(filepath.Join(dir, marker))
				expected := fixture.admitted && (marker == "build-started" || strings.Contains(build, "./waza --version"))
				if expected && statErr != nil || !expected && !os.IsNotExist(statErr) {
					t.Fatalf("dashboard admission = %v, downstream %s: %v", fixture.admitted, marker, statErr)
				}
			}
			if fixture.admitted {
				binary, err := os.ReadFile(filepath.Join(dir, "build-started"))
				if err != nil {
					t.Fatal(err)
				}
				if info, err := os.Stat(filepath.Join(dir, string(binary))); err != nil || !info.Mode().IsRegular() {
					t.Fatalf("admitted producer did not compile captured binary %q: %v", binary, err)
				}
			}
			if !fixture.admitted {
				for _, path := range []string{index, favicon} {
					if _, err := os.Stat(filepath.Join(dir, path)); os.IsNotExist(err) {
						continue
					} else if err != nil {
						t.Fatal(err)
					}
					if !slices.Contains(fixture.files, path) && !slices.Contains(fixture.directories, path) {
						t.Fatalf("admission manufactured missing %s", path)
					}
				}
			}
		})
	}
}

func testReleaseDashboardMutations(t *testing.T, source []byte, jobName string, validate func([]byte) error) {
	t.Helper()
	for _, mutation := range []struct{ step, field, value string }{
		{"Setup Node.js (for web UI build)", "if", "false"},
		{"Setup Node.js (for web UI build)", "uses", ""},
		{"Build Web UI", "if", "false"},
		{"Build Web UI", "continue-on-error", "true"},
		{"Build Web UI", "working-directory", "."},
		{"Build Web UI", "run", ":"},
		{"Build Web UI", "run", "mkdir -p web/dist/assets; printf placeholder > web/dist/index.html"},
		{"Require built dashboard output", "if", "false"},
		{"Require built dashboard output", "continue-on-error", "true"},
		{"Require built dashboard output", "name", "Removed dashboard admission"},
		{"Require built dashboard output", "run", ":"},
		{"Require built dashboard output", "run", releaseDashboardCheck + " || true"},
	} {
		t.Run("dashboard-mutation-"+jobName+"-"+mutation.step+"-"+mutation.field+"-"+mutation.value, func(t *testing.T) {
			var wire yaml.Node
			if err := yaml.Unmarshal(source, &wire); err != nil {
				t.Fatal(err)
			}
			baseline, err := yaml.Marshal(&wire)
			if err != nil {
				t.Fatal(err)
			}
			if err := validate(baseline); err != nil {
				t.Fatalf("positive workflow round trip: %v", err)
			}
			job := releaseMappingValue(t, releaseMappingValue(t, wire.Content[0], "jobs"), jobName)
			var target *yaml.Node
			for _, step := range releaseMappingValue(t, job, "steps").Content {
				if releaseMappingValue(t, step, "name").Value == mutation.step {
					target = step
				}
			}
			if target == nil {
				t.Fatal("production dashboard consumer missing")
			}
			var value *yaml.Node
			for i := 0; i+1 < len(target.Content); i += 2 {
				if target.Content[i].Value == mutation.field {
					value = target.Content[i+1]
				}
			}
			if value == nil {
				value = &yaml.Node{Kind: yaml.ScalarNode}
				target.Content = append(target.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: mutation.field}, value)
			}
			value.Tag, value.Value = "!!str", mutation.value
			if mutation.field == "continue-on-error" {
				value.Tag = "!!bool"
			}
			changed, err := yaml.Marshal(&wire)
			if err != nil {
				t.Fatal(err)
			}
			var actual yaml.Node
			if err := yaml.Unmarshal(changed, &actual); err != nil {
				t.Fatalf("mutation must remain valid YAML: %v", err)
			}
			actualJob := releaseMappingValue(t, releaseMappingValue(t, actual.Content[0], "jobs"), jobName)
			found := false
			for _, step := range releaseMappingValue(t, actualJob, "steps").Content {
				name := releaseMappingValue(t, step, "name").Value
				if name == mutation.step || mutation.field == "name" && name == mutation.value {
					found = releaseMappingValue(t, step, mutation.field).Value == mutation.value
					break
				}
			}
			if !found || string(changed) == string(baseline) {
				t.Fatal("mutation did not change intended dashboard consumer field")
			}
			if err := validate(changed); err == nil {
				t.Fatal("unsafe dashboard consumer admitted")
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
			testReleaseRunnerContracts(t, source, map[string]string{
				"release-candidate": "ubuntu-latest", "release": "ubuntu-latest", "validation": "",
			}, func(changed []byte) error { return validateBranchPublisher(changed, file) })
			var workflow releaseWorkflow
			if err := yaml.Unmarshal(source, &workflow); err != nil {
				t.Fatal(err)
			}
			candidateName := "Detect exact stable tag"
			if file == "squad-insider-release.yml" {
				candidateName = "Detect exact insider tag"
			}
			testReleaseCandidateDetection(t, releaseSteps(workflow.Jobs["release-candidate"])[candidateName].Run, file)
			testReleaseBinaryHandoff(t, "sha256sum waza > waza.sha256",
				releaseSteps(workflow.Jobs["release"])["Require exact validated source and tested binary"].Run)
			createName := "Create Release"
			if file == "squad-insider-release.yml" {
				createName = "Create Insider Pre-Release"
			}
			testBranchReleaseCreation(t, releaseSteps(workflow.Jobs["release"])[createName].Run, file)
			testBranchVersionPropagation(t, workflow, candidateName, createName, file)
			testReleaseVersionMutations(t, source, "release", createName,
				func(changed []byte) error { return validateBranchPublisher(changed, file) })
			for _, condition := range []string{"if: 'false'", "continue-on-error: true"} {
				t.Run("detector-"+condition, func(t *testing.T) {
					old := "      - name: " + candidateName + "\n"
					changed := strings.Replace(string(source), old, old+"        "+condition+"\n", 1)
					if changed == string(source) {
						t.Fatal("mutation did not change production detector")
					}
					if err := validateBranchPublisher([]byte(changed), file); err == nil {
						t.Fatal("unsafe detector mutation was admitted")
					}
				})
			}
			for _, mutation := range []struct{ name, old, new string }{
				{"shallow-candidate-checkout", "          fetch-depth: 0", "          fetch-depth: 1"},
				{"missing-candidate-history", "          fetch-depth: 0\n", ""},
				{"disabled-candidate-tags", "          fetch-tags: true", "          fetch-tags: false"},
				{"missing-candidate-tags", "          fetch-tags: true\n", ""},
				{"wrong-candidate-ref", "          ref: ${{ github.sha }}", "          ref: main"},
				{"unverified-candidate-source", `test "$source_sha" = "$GITHUB_SHA"`, ":"},
				{"unchecked-candidate-source", `source_sha="$(git rev-parse HEAD)"`, `source_sha="$(git rev-parse HEAD)" || true`},
				{"missing-detector-id", "        id: tag\n", ""},
				{"wrong-detector-id", "        id: tag", "        id: other"},
				{"unbound-publish-output", "publish: ${{ steps.tag.outputs.publish }}", "publish: ${{ steps.other.outputs.publish }}"},
				{"missing-publish-output", "      publish: ${{ steps.tag.outputs.publish }}\n", ""},
				{"unbound-tag-output", "tag: ${{ steps.tag.outputs.tag }}", "tag: ${{ steps.other.outputs.tag }}"},
				{"missing-tag-output", "      tag: ${{ steps.tag.outputs.tag }}\n", ""},
				{"untagged-validation", "if: needs.release-candidate.outputs.publish == 'true'", "if: always()"},
				{"tolerated-candidate-job", "  release-candidate:\n", "  release-candidate:\n    continue-on-error: true\n"},
				{"skipped-candidate-job", "  release-candidate:\n", "  release-candidate:\n    if: 'false'\n"},
				{"tolerated-publication-job", "  release:\n", "  release:\n    continue-on-error: true\n"},
				{"missing-validation-dependency", "needs: [release-candidate, validation]", "needs: [release-candidate]"},
				{"cancellation-bypass", "&& !cancelled()", "&& always()"}, //nolint:misspell // GitHub expression.
				{"unbound-validation-result", "${{ needs.validation.result }}", "success"},
				{"ignored-validation-result", `run: test "$VALIDATION_RESULT" = success`, "run: ':'"},
				{"stale-source", `test "$source_sha" = "$GITHUB_SHA"`, ":"},
				{"missing-digest", `test "$recorded_digest" = "$actual_digest"`, ":"},
				{"unchecked-binary-hash", `actual_digest="$(sha256sum waza)"`, `actual_digest="$(sha256sum waza)" || true`},
				{"unchecked-sidecar-read", `recorded_digest="$(cat waza.sha256)"`, `recorded_digest="$(cat waza.sha256)" || true`},
				{"unchecked-recorded-source", `source_sha="$(cat source-sha.txt)"`, `source_sha="$(cat source-sha.txt)" || true`},
				{"unchecked-tag-lookup", `tag_output="$(git tag --points-at "$GITHUB_SHA" --list 'v*.*.*')"`, `mapfile -t candidates < <(git tag --points-at "$GITHUB_SHA" --list 'v*.*.*')`},
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
					var parsed releaseWorkflow
					if err := yaml.Unmarshal([]byte(changed), &parsed); err != nil {
						t.Fatalf("mutation must reach semantic validation: %v", err)
					}
					if err := validateBranchPublisher([]byte(changed), file); err == nil {
						t.Fatal("unsafe branch publisher mutation was admitted")
					}
				})
			}
			if file == "squad-insider-release.yml" {
				t.Run("existing-insider-tag-bypass", func(t *testing.T) {
					changed := strings.Replace(string(source), `if [ "$tag_status" -ne 2 ] || [ -n "$remote_tag" ]; then`, "if false; then", 1)
					if changed == string(source) {
						t.Fatal("mutation did not change production workflow")
					}
					if err := validateBranchPublisher([]byte(changed), file); err == nil {
						t.Fatal("unsafe existing insider tag mutation was admitted")
					}
				})
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
			writeRejectingTagLookup(t, dir)
			created := filepath.Join(dir, "created-release")
			script := strings.ReplaceAll(strings.ReplaceAll(run, "${{ github.repository }}", "microsoft/waza"), "${{ github.server_url }}", "https://github.com")
			cmd := exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", script)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"RELEASE_STATE="+state, "REMOTE_TAG_STATE=missing", "CREATED_RELEASE="+created, "VERSION=v1.2.3", "GITHUB_SHA=validated-source")
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
	if file == "squad-insider-release.yml" {
		for _, state := range []string{"matching", "wrong-source", "annotated", "lookup-error"} {
			t.Run("tag-exists-release-missing-"+state, func(t *testing.T) {
				dir := t.TempDir()
				writeRejectingTagLookup(t, dir)
				gh := filepath.Join(dir, "gh")
				writeReleaseFile(t, gh, []byte("#!/bin/sh\nif [ \"$2\" = view ]; then exit 1; fi\nprintf '%s\\n' \"$@\" > \"$CREATED_RELEASE\"\n"))
				if err := os.Chmod(gh, 0o700); err != nil {
					t.Fatal(err)
				}
				created := filepath.Join(dir, "created-release")
				script := strings.ReplaceAll(strings.ReplaceAll(run, "${{ github.repository }}", "microsoft/waza"), "${{ github.server_url }}", "https://github.com")
				cmd := exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", script)
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
					"REMOTE_TAG_STATE="+state, "CREATED_RELEASE="+created, "VERSION=v1.2.3-insider", "GITHUB_SHA=validated-source")
				output, err := cmd.CombinedOutput()
				if err == nil || !strings.Contains(string(output), "tag exists or remote tag lookup failed") {
					t.Fatalf("existing tag or lookup failure admitted: %v\n%s", err, output)
				}
				if _, err := os.Stat(created); !os.IsNotExist(err) {
					t.Fatalf("release created despite existing tag or failed lookup: %v", err)
				}
			})
		}
	}
}

func testReleaseVersionMutations(t *testing.T, source []byte, jobName, stepName string, validate func([]byte) error) {
	t.Helper()
	for _, version := range []*string{nil, new(""), new("v9.9.9"), new("${{ needs.other.outputs.tag }}")} {
		name := "omitted"
		if version != nil {
			name = *version
		}
		t.Run("version-binding-"+jobName+"-"+stepName+"-"+name, func(t *testing.T) {
			var wire yaml.Node
			if err := yaml.Unmarshal(source, &wire); err != nil {
				t.Fatal(err)
			}
			baseline, err := yaml.Marshal(&wire)
			if err != nil {
				t.Fatal(err)
			}
			if err := validate(baseline); err != nil {
				t.Fatalf("positive workflow round trip must remain admitted: %v", err)
			}
			job := releaseMappingValue(t, releaseMappingValue(t, wire.Content[0], "jobs"), jobName)
			target := job
			if stepName != "" {
				target = nil
				for _, step := range releaseMappingValue(t, job, "steps").Content {
					if releaseMappingValue(t, step, "name").Value == stepName {
						target = step
						break
					}
				}
			}
			if target == nil {
				t.Fatal("production version consumer missing")
			}
			env := releaseMappingValue(t, target, "env")
			found := false
			for i := 0; i+1 < len(env.Content); i += 2 {
				if env.Content[i].Value == "VERSION" {
					if version == nil {
						env.Content = append(env.Content[:i], env.Content[i+2:]...)
					} else {
						env.Content[i+1].Value = *version
					}
					found = true
					break
				}
			}
			if !found {
				t.Fatal("production VERSION environment missing")
			}
			changed, err := yaml.Marshal(&wire)
			if err != nil {
				t.Fatal(err)
			}
			var parsed releaseWorkflow
			if err := yaml.Unmarshal(changed, &parsed); err != nil {
				t.Fatalf("version mutation must remain valid YAML: %v", err)
			}
			actual := parsed.Jobs[jobName].Env
			if stepName != "" {
				actual = releaseSteps(parsed.Jobs[jobName])[stepName].Env
			}
			got, exists := actual["VERSION"]
			if version == nil && exists || version != nil && (!exists || got != *version) {
				t.Fatal("mutation did not change intended VERSION binding")
			}
			if err := validate(changed); err == nil {
				t.Fatal("wrong publication VERSION binding admitted")
			}
		})
	}
}

func testBranchVersionPropagation(t *testing.T, workflow releaseWorkflow, candidateName, createName, file string) {
	t.Helper()
	candidate := releaseSteps(workflow.Jobs["release-candidate"])[candidateName]
	create := releaseSteps(workflow.Jobs["release"])[createName]
	if create.Env["VERSION"] != "${{ needs.release-candidate.outputs.tag }}" {
		t.Fatal("production release version must come from candidate tag")
	}
	for _, state := range []string{"v1.2.3", "v2.4.6", "untagged", "source-error", "wrong-source", "tag-lookup-error"} {
		t.Run("candidate-to-release-"+state, func(t *testing.T) {
			dir := t.TempDir()
			var tags []string
			if state != "untagged" {
				tag := state
				if !strings.HasPrefix(state, "v") {
					tag = "v1.2.3"
				}
				tags = []string{tag, tag + "-insider"}
			}
			sha := initReleaseTestRepository(t, dir, tags)
			run := candidate.Run
			if state == "source-error" {
				run = failingReleaseCommand("git", "rev-parse") + run
			}
			if state == "tag-lookup-error" {
				run = failingReleaseCommand("git", "tag") + run
			}
			eventSHA := sha
			if state == "wrong-source" {
				eventSHA = strings.Repeat("0", len(sha))
			}
			outputPath := filepath.Join(dir, "candidate-output")
			created := filepath.Join(dir, "created-release")
			cmd := exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", run)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GITHUB_SHA="+eventSHA, "GITHUB_OUTPUT="+outputPath)
			output, err := cmd.CombinedOutput()
			valid := strings.HasPrefix(state, "v")
			if (err == nil) != (valid || state == "untagged") {
				t.Fatalf("candidate propagation %s: %v\n%s", state, err, output)
			}
			if !valid {
				if state == "source-error" || state == "tag-lookup-error" {
					assertReleaseFailureObserved(t, dir)
				}
				if state == "untagged" {
					got, err := os.ReadFile(outputPath)
					if err != nil || string(got) != "publish=false\n" {
						t.Fatalf("untagged candidate output: %q, %v", got, err)
					}
				} else if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
					t.Fatalf("failed candidate emitted publication outputs: %v", err)
				}
				if _, err := os.Stat(created); !os.IsNotExist(err) {
					t.Fatalf("non-admitted candidate created release: %v", err)
				}
				return
			}
			wire, err := os.ReadFile(outputPath)
			if err != nil {
				t.Fatal(err)
			}
			tag := state
			if file == "squad-insider-release.yml" {
				tag += "-insider"
			}
			if string(wire) != "publish=true\ntag="+tag+"\n" {
				t.Fatalf("candidate tag propagation: %q", wire)
			}
			version := strings.TrimSuffix(strings.TrimPrefix(string(wire), "publish=true\ntag="), "\n")
			gh := filepath.Join(dir, "gh")
			writeReleaseFile(t, gh, []byte("#!/bin/sh\nif [ \"$2\" = view ]; then exit 1; fi\nprintf '%s\\n' \"$@\" > \"$CREATED_RELEASE\"\n"))
			if err := os.Chmod(gh, 0o700); err != nil {
				t.Fatal(err)
			}
			writeRejectingTagLookup(t, dir)
			run = strings.ReplaceAll(strings.ReplaceAll(create.Run, "${{ github.repository }}", "microsoft/waza"), "${{ github.server_url }}", "https://github.com")
			cmd = exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", run)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"CREATED_RELEASE="+created, "REMOTE_TAG_STATE=missing", "VERSION="+version, "GITHUB_SHA="+sha)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("publish detected version: %v\n%s", err, output)
			}
			args, err := os.ReadFile(created)
			if err != nil || !strings.HasPrefix(string(args), "release\ncreate\n"+tag+"\n") {
				t.Fatalf("published tag differs from production detector: %q, %v", args, err)
			}
			if file == "squad-insider-release.yml" && !strings.Contains(string(args), "--target\n"+sha+"\n") {
				t.Fatalf("detected insider tag lost source binding: %q", args)
			}
		})
	}
}

func writeRejectingTagLookup(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "git")
	writeReleaseFile(t, path, []byte(`#!/bin/sh
set -eu
test "$1" = ls-remote
test "$2" = --exit-code
test "$3" = https://github.com/microsoft/waza.git
test "$4" = "refs/tags/$VERSION"
test "$5" = "refs/tags/$VERSION^{}"
case "$REMOTE_TAG_STATE" in
  missing) exit 2 ;;
  matching) printf 'validated-source\trefs/tags/%s\n' "$VERSION" ;;
  wrong-source) printf 'other-source\trefs/tags/%s\n' "$VERSION" ;;
  annotated) printf 'tag-object\trefs/tags/%s\nother-source\trefs/tags/%s^{}\n' "$VERSION" "$VERSION" ;;
  lookup-error) exit 73 ;;
  *) exit 99 ;;
esac
`))
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

func testReleaseCandidateDetection(t *testing.T, run, file string) {
	t.Helper()
	for _, tc := range []struct {
		name        string
		tags        []string
		lookupError bool
		wrongSource bool
		sourceError bool
		wantSuccess bool
		wantOutput  string
	}{
		{name: "untagged", wantSuccess: true, wantOutput: "publish=false\n"},
		{name: "base-tag", tags: []string{"v1.2.3"}, wantSuccess: true, wantOutput: "publish=true\ntag=v1.2.3\n"},
		{name: "base-and-insider-tags", tags: []string{"v1.2.3", "v1.2.3-insider"}, wantSuccess: true, wantOutput: "publish=true\ntag=v1.2.3\n"},
		{name: "multiple-base-tags", tags: []string{"v1.2.3", "v1.2.4"}, wantSuccess: false},
		{name: "failed-tag-lookup", lookupError: true, wantSuccess: false},
		{name: "wrong-checkout-source", tags: []string{"v1.2.3"}, wrongSource: true, wantSuccess: false},
		{name: "matching-source-lookup-error", tags: []string{"v1.2.3"}, sourceError: true, wantSuccess: false},
	} {
		t.Run("candidate-"+tc.name, func(t *testing.T) {
			dir := t.TempDir()
			sha := initReleaseTestRepository(t, dir, tc.tags)
			if tc.wrongSource {
				sha = strings.Repeat("0", len(sha))
			}
			outputPath := filepath.Join(dir, "github-output")
			script := run
			if tc.sourceError {
				script = failingReleaseCommand("git", "rev-parse") + script
			}
			cmd := exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", script)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GITHUB_SHA="+sha, "GITHUB_OUTPUT="+outputPath)
			if tc.lookupError {
				realGit, err := exec.LookPath("git")
				if err != nil {
					t.Fatal(err)
				}
				wrapper := filepath.Join(dir, "git")
				writeReleaseFile(t, wrapper, []byte("#!/bin/sh\nset -eu\nif [ \"$1\" = rev-parse ]; then exec \"$REAL_GIT\" \"$@\"; fi\ntest \"$1\" = tag\nprintf 'tag lookup failed\\n' >&2\nexit 73\n"))
				if err := os.Chmod(wrapper, 0o700); err != nil {
					t.Fatal(err)
				}
				cmd.Env = append(cmd.Env, "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "REAL_GIT="+realGit)
			}
			output, err := cmd.CombinedOutput()
			if (err == nil) != tc.wantSuccess {
				t.Fatalf("%s candidate error = %v\n%s", file, err, output)
			}
			if !tc.wantSuccess {
				if tc.lookupError {
					if !strings.Contains(string(output), "tag lookup failed") {
						t.Fatal("failed lookup control did not execute")
					}
				}
				if tc.sourceError {
					assertReleaseFailureObserved(t, dir)
				}
				if tc.lookupError || tc.wrongSource || tc.sourceError {
					if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
						t.Fatalf("failed candidate admission emitted outputs: %v", err)
					}
				}
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

func initReleaseTestRepository(t *testing.T, dir string, tags []string) string {
	t.Helper()
	runReleaseCommand(t, dir, "git", "init", "-q")
	runReleaseCommand(t, dir, "git", "config", "user.name", "Waza Test")
	runReleaseCommand(t, dir, "git", "config", "user.email", "waza-test@example.com")
	runReleaseCommand(t, dir, "git", "commit", "--allow-empty", "-q", "-m", "test")
	for _, tag := range tags {
		runReleaseCommand(t, dir, "git", "tag", tag)
	}
	return strings.TrimSpace(string(runReleaseCommand(t, dir, "git", "rev-parse", "HEAD")))
}

func releaseMappingValue(t *testing.T, node *yaml.Node, key string) *yaml.Node {
	t.Helper()
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	t.Fatalf("missing production mapping %q", key)
	return nil
}

func failingReleaseCommand(tool, argument string) string {
	return fmt.Sprintf("%s() {\n  command %s \"$@\" || return $?\n  if [ \"$1\" = %q ]; then\n    printf invoked > failure-observed\n    return 73\n  fi\n}\n", tool, tool, argument)
}

func assertReleaseFailureObserved(t *testing.T, dir string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(dir, "failure-observed"))
	if err != nil || string(got) != "invoked" {
		t.Fatalf("matching-output command failure did not execute: %q, %v", got, err)
	}
}

func testReleaseSourceLookup(t *testing.T, script string) {
	t.Helper()
	for _, state := range []string{"valid", "wrong-source", "matching-output-error"} {
		t.Run("source-lookup-"+state, func(t *testing.T) {
			dir := t.TempDir()
			sha := initReleaseTestRepository(t, dir, nil)
			run := script
			if state == "wrong-source" {
				sha = strings.Repeat("0", len(sha))
			}
			if state == "matching-output-error" {
				run = failingReleaseCommand("git", "rev-parse") + run
			}
			cmd := exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", run+"printf admitted > source-admitted\n")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GITHUB_SHA="+sha)
			output, err := cmd.CombinedOutput()
			if (err == nil) != (state == "valid") {
				t.Fatalf("source check %s: %v\n%s", state, err, output)
			}
			if state == "matching-output-error" {
				assertReleaseFailureObserved(t, dir)
			}
			_, err = os.Stat(filepath.Join(dir, "source-admitted"))
			if state == "valid" && err != nil || state != "valid" && !os.IsNotExist(err) {
				t.Fatalf("source admission marker %s: %v", state, err)
			}
		})
	}
}

func TestTagReleaseRequiresValidation(t *testing.T) {
	source := readReleaseWorkflow(t, "release.yml")
	normalized := strings.ReplaceAll(string(source), "\r\n", "\n")
	if err := validateTagRelease([]byte(normalized)); err != nil {
		t.Fatal(err)
	}
	testReleaseRunnerContracts(t, source, map[string]string{
		"setup-version": "ubuntu-latest", "build-cli": "ubuntu-latest",
		"release-cli": "ubuntu-latest", "release-extension": "ubuntu-latest", "validation": "",
	}, validateTagRelease)
	var workflow releaseWorkflow
	if err := yaml.Unmarshal([]byte(normalized), &workflow); err != nil {
		t.Fatal(err)
	}
	testTagCLIArtifactVerification(t, releaseSteps(workflow.Jobs["release-cli"])["Generate SHA256 Checksums"].Run)
	for _, job := range []string{"build-cli", "release-extension"} {
		testReleaseSourceLookup(t, releaseSteps(workflow.Jobs[job])["Require exact validated source"].Run)
		testReleaseDashboardMutations(t, source, job, validateTagRelease)
		if job == "build-cli" {
			for _, platform := range []string{"linux", "darwin", "windows"} {
				for _, arch := range []string{"amd64", "arm64"} {
					t.Run(job+"-dashboard-"+platform+"-"+arch, func(t *testing.T) {
						t.Setenv("GITHUB_SHA", "fixture-source")
						build := strings.NewReplacer("${{ needs.setup-version.outputs.version }}", "1.2.3",
							"${{ matrix.os }}", platform, "${{ matrix.arch }}", arch).
							Replace(releaseSteps(workflow.Jobs[job])["Build Binary"].Run)
						testReleaseDashboardOutput(t, releaseSteps(workflow.Jobs[job])["Require built dashboard output"].Run, build)
					})
				}
			}
		} else {
			t.Run(job+"-dashboard-output", func(t *testing.T) {
				testReleaseDashboardOutput(t, releaseSteps(workflow.Jobs[job])["Require built dashboard output"].Run,
					releaseSteps(workflow.Jobs[job])["Build Extension"].Run)
			})
		}
	}
	testExtensionProvenance(t, releaseSteps(workflow.Jobs["release-extension"])["Record extension artifact provenance"].Run)
	testReleaseVersionMutations(t, source, "release-extension", "", validateTagRelease)
	testTagVersionPropagation(t, workflow)
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
		{"stale-cli-source", `test "$source_sha" = "$GITHUB_SHA"`, ":"},
		{"unchecked-cli-source-lookup", `source_sha="$(git rev-parse HEAD)"`, `source_sha="$(git rev-parse HEAD)" || true`},
		{"missing-cli-source-record", `printf '%s\n' "$GITHUB_SHA" > "${BINARY_NAME}.source-sha.txt"`, ":"},
		{"missing-cli-digest", `sha256sum "${BINARY_NAME}" > "${BINARY_NAME}.sha256"`, ":"},
		{"missing-cli-matrix-member", "            waza-windows-arm64.exe\n", ""},
		{"missing-cli-binary-file", `test -f "$binary"`, ":"},
		{"missing-cli-source-file", `test -f "$source"`, ":"},
		{"missing-cli-digest-file", `test -f "$digest"`, ":"},
		{"missing-cli-source-verification", `            test "$source_sha" = "$GITHUB_SHA"`, "            :"},
		{"unchecked-cli-artifact-source", `source_sha="$(cat "$source")"`, `source_sha="$(cat "$source")" || true`},
		{"unchecked-cli-artifact-sidecar", `recorded_digest="$(cat "$digest")"`, `recorded_digest="$(cat "$digest")" || true`},
		{"unchecked-cli-artifact-hash", `actual_digest="$(sha256sum "$binary")"`, `actual_digest="$(sha256sum "$binary")" || true`},
		{"unbound-version-output", "version: ${{ steps.version.outputs.version }}", "version: ${{ steps.other.outputs.version }}"},
		{"unbound-tag-output", "tag: ${{ steps.version.outputs.tag }}", "tag: ${{ steps.other.outputs.tag }}"},
		{"wrong-version-detector", "        id: version", "        id: other"},
		{"wrong-build-version", `VERSION="${{ needs.setup-version.outputs.version }}"`, `VERSION="9.9.9"`},
		{"wrong-published-tag", `TAG="${{ needs.setup-version.outputs.tag }}"`, `TAG="v9.9.9"`},
		{"overridden-extension-version", "      - name: Release Extension\n", "      - name: Release Extension\n        env:\n          VERSION: '9.9.9'\n"},
		{"missing-extension-provenance", `printf 'source_sha=%s\n' "$GITHUB_SHA"`, ":"},
		{"unchecked-extension-discovery", "packages=(artifacts/*.zip artifacts/*.tar.gz)", "mapfile -d '' packages < <(find artifacts -type f -print0)"},
		{"missing-extension-inventory", `test "${#packages[@]}" -gt 0`, ":"},
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

func testTagVersionPropagation(t *testing.T, workflow releaseWorkflow) {
	t.Helper()
	detector := releaseSteps(workflow.Jobs["setup-version"])["Determine Version"]
	for _, tc := range []struct{ tag, version string }{
		{"v1.2.3", "1.2.3"}, {"v2.4.6-beta.2", "2.4.6-beta.2"},
	} {
		t.Run("tag-version-propagation-"+tc.tag, func(t *testing.T) {
			dir := t.TempDir()
			outputPath := filepath.Join(dir, "version-output")
			cmd := exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", detector.Run)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GITHUB_REF_NAME="+tc.tag, "GITHUB_OUTPUT="+outputPath)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("production version detector: %v\n%s", err, output)
			}
			wire, err := os.ReadFile(outputPath)
			if err != nil || string(wire) != "version="+tc.version+"\ntag="+tc.tag+"\n" {
				t.Fatalf("version detector outputs: %q, %v", wire, err)
			}
			values := strings.Split(strings.TrimSuffix(string(wire), "\n"), "\n")
			version := strings.TrimPrefix(values[0], "version=")
			tag := strings.TrimPrefix(values[1], "tag=")
			build := releaseSteps(workflow.Jobs["build-cli"])["Build Binary"].Run
			build = strings.NewReplacer("${{ needs.setup-version.outputs.version }}", version,
				"${{ matrix.os }}", "linux", "${{ matrix.arch }}", "amd64").Replace(build)
			mockBuild := "go() { test \"$1\" = build; printf '%s\\n' \"$@\" > build-args; printf fixture > waza-linux-amd64; }\n"
			cmd = exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", mockBuild+build)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GITHUB_SHA=validated-source")
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("detected build version: %v\n%s", err, output)
			}
			args, err := os.ReadFile(filepath.Join(dir, "build-args"))
			if err != nil || !strings.Contains(string(args), "-ldflags\n-X main.version="+version+"\n") {
				t.Fatalf("build lost detected version: %q, %v", args, err)
			}
			cli := releaseSteps(workflow.Jobs["release-cli"])["Create or Update GitHub Release"].Run
			cli = strings.NewReplacer("${{ needs.setup-version.outputs.tag }}", tag,
				"${{ github.repository }}", "microsoft/waza").Replace(cli)
			mockCLI := "gh() { if [ \"$2\" = view ]; then return 1; fi; printf '%s\\n' \"$@\" > cli-args; }\n"
			cmd = exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", mockCLI+cli)
			cmd.Dir = dir
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("detected CLI release tag: %v\n%s", err, output)
			}
			args, err = os.ReadFile(filepath.Join(dir, "cli-args"))
			if err != nil || !strings.HasPrefix(string(args), "release\ncreate\n"+tag+"\n") {
				t.Fatalf("CLI publication lost detected tag: %q, %v", args, err)
			}
			extension := workflow.Jobs["release-extension"]
			if extension.Env["VERSION"] != "${{ needs.setup-version.outputs.version }}" {
				t.Fatal("extension must consume detected version output")
			}
			for _, name := range []string{"Release Extension", "Publish Extension"} {
				release := strings.ReplaceAll(releaseSteps(extension)[name].Run, "${{ github.repository }}", "microsoft/waza")
				mockExtension := "azd() { printf '%s\\n' \"$@\" > extension-args; }\n"
				cmd = exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", mockExtension+release)
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), "VERSION="+version)
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("detected %s version: %v\n%s", name, err, output)
				}
				args, err = os.ReadFile(filepath.Join(dir, "extension-args"))
				if err != nil || !strings.Contains(string(args), "--version\n"+version+"\n") {
					t.Fatalf("%s lost detected version: %q, %v", name, args, err)
				}
			}
		})
	}
}

func testExtensionProvenance(t *testing.T, script string) {
	t.Helper()
	for _, state := range []string{"valid", "missing-directory", "empty", "archive-directory", "digest-failure", "discovery-failure"} {
		t.Run("extension-provenance-"+state, func(t *testing.T) {
			dir := t.TempDir()
			artifacts := filepath.Join(dir, "artifacts")
			if state != "missing-directory" {
				if err := os.Mkdir(artifacts, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			valid := state == "valid" || state == "discovery-failure"
			if valid || state == "digest-failure" {
				writeReleaseFile(t, filepath.Join(artifacts, "with space.zip"), []byte("zip"))
				writeReleaseFile(t, filepath.Join(artifacts, "bundle.tar.gz"), []byte("tar"))
			}
			if state == "archive-directory" {
				if err := os.Mkdir(filepath.Join(artifacts, "directory.zip"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", script)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GITHUB_SHA=validated-source")
			if state == "digest-failure" {
				path := filepath.Join(dir, "sha256sum")
				writeReleaseFile(t, path, []byte("#!/bin/sh\nprintf 'partial digest\\n'\nexit 73\n"))
				if err := os.Chmod(path, 0o700); err != nil {
					t.Fatal(err)
				}
				cmd.Env = append(cmd.Env, "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			}
			if state == "discovery-failure" {
				for _, tool := range []string{"find", "sort"} {
					path := filepath.Join(dir, tool)
					writeReleaseFile(t, path, []byte("#!/bin/sh\nprintf invoked > discovery.invoked\nprintf 'artifacts/with space.zip\\000'\nexit 73\n"))
					if err := os.Chmod(path, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				cmd.Env = append(cmd.Env, "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			}
			output, err := cmd.CombinedOutput()
			if (err == nil) != valid {
				t.Fatalf("provenance state %s: %v\n%s", state, err, output)
			}
			if state == "discovery-failure" {
				if _, err := os.Stat(filepath.Join(dir, "discovery.invoked")); !os.IsNotExist(err) {
					t.Fatalf("provenance invoked fallible discovery tool: %v", err)
				}
			}
			if valid {
				got, err := os.ReadFile(filepath.Join(artifacts, "release-provenance.txt"))
				if err != nil {
					t.Fatal(err)
				}
				digests := runReleaseCommand(t, dir, "sha256sum", "artifacts/with space.zip", "artifacts/bundle.tar.gz")
				want := "source_sha=validated-source\n" + string(digests)
				if string(got) != want {
					t.Fatalf("incomplete provenance = %q; want %q", got, want)
				}
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
	for _, mutation := range []string{"none", "missing-binary", "missing-source", "missing-digest", "stale-source", "digest-mismatch", "foreign-digest",
		"matching-source-error", "matching-sidecar-error", "matching-hash-error"} {
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
			script := run
			switch mutation {
			case "matching-source-error":
				script = failingReleaseCommand("cat", target+".source-sha.txt") + script
			case "matching-sidecar-error":
				script = failingReleaseCommand("cat", target+".sha256") + script
			case "matching-hash-error":
				script = failingReleaseCommand("sha256sum", target) + script
			}
			cmd := exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", script)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GITHUB_SHA=validated-source")
			output, err := cmd.CombinedOutput()
			if (err == nil) != (mutation == "none") {
				t.Fatalf("verification error = %v\n%s", err, output)
			}
			if strings.HasPrefix(mutation, "matching-") {
				assertReleaseFailureObserved(t, dir)
			}
			if mutation != "none" {
				if _, err := os.Stat(filepath.Join(dir, "checksums.txt")); !os.IsNotExist(err) {
					t.Fatalf("invalid artifacts reached publication checksum generation: %v", err)
				}
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

func validateReleaseRunners(workflow releaseWorkflow, expected map[string]string) error {
	for name, runner := range expected {
		if workflow.Jobs[name].RunsOn != runner {
			return fmt.Errorf("%s must retain runner %q", name, runner)
		}
	}
	return nil
}

func testReleaseRunnerContracts(t *testing.T, source []byte, expected map[string]string, validate func([]byte) error) {
	t.Helper()
	var baseline yaml.Node
	if err := yaml.Unmarshal(source, &baseline); err != nil {
		t.Fatal(err)
	}
	roundTrip, err := yaml.Marshal(&baseline)
	if err != nil {
		t.Fatal(err)
	}
	if err := validate(roundTrip); err != nil {
		t.Fatalf("valid production YAML round trip must remain admitted: %v", err)
	}
	for job, runner := range expected {
		for _, replacement := range []string{"ubuntu-latest", "windows-latest", "macos-latest", ""} {
			if replacement == runner {
				continue
			}
			t.Run("runner-"+job+"-"+replacement, func(t *testing.T) {
				var wire yaml.Node
				if err := yaml.Unmarshal(source, &wire); err != nil {
					t.Fatal(err)
				}
				jobs := releaseMappingValue(t, wire.Content[0], "jobs")
				target := releaseMappingValue(t, jobs, job)
				found := false
				for i := 0; i+1 < len(target.Content); i += 2 {
					if target.Content[i].Value == "runs-on" {
						if target.Content[i+1].Value != runner {
							t.Fatal("production runner differs from fixture contract")
						}
						if replacement == "" {
							target.Content = append(target.Content[:i], target.Content[i+2:]...)
						} else {
							target.Content[i+1].Value = replacement
						}
						found = true
						break
					}
				}
				if !found {
					if runner != "" {
						t.Fatal("production runner missing")
					}
					target.Content = append(target.Content,
						&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "runs-on"},
						&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: replacement})
				}
				changed, err := yaml.Marshal(&wire)
				if err != nil {
					t.Fatal(err)
				}
				var parsed releaseWorkflow
				if err := yaml.Unmarshal(changed, &parsed); err != nil {
					t.Fatalf("runner mutation must reach semantic validation: %v", err)
				}
				if parsed.Jobs[job].RunsOn != replacement {
					t.Fatal("mutation did not change selected production runner")
				}
				if err := validate(changed); err == nil {
					t.Fatal("wrong-platform production workflow admitted")
				}
			})
		}
	}
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
	if err := validateReleaseRunners(workflow, map[string]string{
		"build-test": "ubuntu-latest", "windows-test": "windows-latest",
		"lint": "ubuntu-latest", "complete": "ubuntu-latest", "native-test-shards": "",
	}); err != nil {
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
	if err := orderedReleaseSteps(build, []string{"Checkout Repository", "Require exact source",
		"Setup Node.js (for web UI build)", "Build Web UI", "Require built dashboard output", "Install and prove rejecting runtime guard",
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
	if err := validateReleaseDashboard(build); err != nil {
		return err
	}
	if !strings.Contains(buildSteps["Execute Tests"].Run, `go test -v -race -count=1 -timeout=30m -coverprofile=coverage.txt -covermode=atomic "${selected[@]}"`) {
		return fmt.Errorf("release Linux tests must execute freshly under the current runtime guard")
	}
	if buildSteps["Checkout Repository"].With["lfs"] != "true" || buildSteps["Checkout Repository"].With["fetch-depth"] != "0" ||
		buildSteps["Require exact source"].Run != releaseSourceCheck ||
		steps["Require exact source"].Run != releaseSourceCheck ||
		releaseSteps(windows)["Require exact source"].Run != releaseSourceCheck ||
		releaseSteps(lint)["Require exact source"].Run != releaseSourceCheck {
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
		steps["Verify and restore tested binary"].Run != releaseDigestCheck+"chmod +x waza\n./waza --version\n" {
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
		windowsSteps["Execute complete Windows tests"].Run != "go test -v -race -count=1 -timeout=30m ./..." {
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
	if err := validateReleaseRunners(workflow, map[string]string{
		"release-candidate": "ubuntu-latest", "release": "ubuntu-latest", "validation": "",
	}); err != nil {
		return err
	}
	candidate := workflow.Jobs["release-candidate"]
	validation := workflow.Jobs["validation"]
	publish := workflow.Jobs["release"]
	if workflow.Permissions["contents"] != "write" || candidate.TimeoutMinutes != 5 || publish.TimeoutMinutes != 15 ||
		candidate.If != "" || candidate.ContinueOnError || publish.ContinueOnError {
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
	checkout := candidateSteps["Checkout Repository"]
	if !strings.HasPrefix(checkout.Uses, "actions/checkout@") ||
		checkout.With["ref"] != "${{ github.sha }}" ||
		checkout.With["fetch-depth"] != "0" || checkout.With["fetch-tags"] != "true" {
		return fmt.Errorf("candidate checkout must bind exact source and fetch complete tag history")
	}
	if !reflect.DeepEqual(candidate.Outputs, map[string]string{
		"publish": "${{ steps.tag.outputs.publish }}",
		"tag":     "${{ steps.tag.outputs.tag }}",
	}) {
		return fmt.Errorf("candidate outputs must bind the production detector")
	}
	tagStep := candidateSteps["Detect exact stable tag"]
	createName := "Create Release"
	if file == "squad-insider-release.yml" {
		tagStep = candidateSteps["Detect exact insider tag"]
		createName = "Create Insider Pre-Release"
	}
	if tagStep.ID != "tag" || !strings.HasPrefix(tagStep.Run, releaseSourceCheck) ||
		!strings.Contains(tagStep.Run, `tag_output="$(git tag --points-at "$GITHUB_SHA" --list 'v*.*.*')"`) ||
		!strings.Contains(tagStep.Run, `if [[ "$tag" != *-insider ]]; then`) ||
		!strings.Contains(tagStep.Run, `echo "publish=false" >> "$GITHUB_OUTPUT"`) ||
		!strings.Contains(tagStep.Run, `if [ "${#tags[@]}" -gt 1 ]`) {
		return fmt.Errorf("candidate detection must fail closed and skip untagged branch pushes")
	}
	if err := orderedReleaseSteps(candidate, []string{"Checkout Repository", tagStep.Name}); err != nil {
		return err
	}
	if err := orderedReleaseSteps(publish, []string{"Require successful complete validation",
		"Download complete validation evidence", "Require exact validated source and tested binary", createName}); err != nil {
		return err
	}
	steps := releaseSteps(publish)
	if steps[createName].Env["VERSION"] != "${{ needs.release-candidate.outputs.tag }}" {
		return fmt.Errorf("publication version must bind the detected candidate tag")
	}
	gate := steps["Require successful complete validation"]
	if gate.Env["VALIDATION_RESULT"] != "${{ needs.validation.result }}" ||
		gate.Run != `test "$VALIDATION_RESULT" = success` {
		return fmt.Errorf("publisher must reject missing, failed, canceled or skipped validation")
	}
	download := steps["Download complete validation evidence"]
	if download.With["name"] != "release-validation-${{ github.sha }}" || download.With["path"] != "." ||
		steps["Require exact validated source and tested binary"].Run != releaseRecordedSourceCheck+releaseDigestCheck+"chmod +x waza\n./waza --version\n" {
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
		(!strings.Contains(steps[createName].Run, `--target "$GITHUB_SHA"`) ||
			!strings.Contains(steps[createName].Run, `remote_tag="$(git ls-remote --exit-code`) ||
			!strings.Contains(steps[createName].Run, `if [ "$tag_status" -ne 2 ] || [ -n "$remote_tag" ]; then`) ||
			!strings.Contains(steps[createName].Run, "\"refs/tags/$VERSION\" \"refs/tags/$VERSION^{}\")\" || tag_status=$?")) {
		return fmt.Errorf("insider release tag must target the validated source")
	}
	return nil
}

func validateReleaseDashboard(job releaseJob) error {
	steps := releaseSteps(job)
	if !strings.HasPrefix(steps["Setup Node.js (for web UI build)"].Uses, "actions/setup-node@") ||
		steps["Setup Node.js (for web UI build)"].With["node-version"] != "22" ||
		steps["Build Web UI"].WorkingDirectory != "web" || steps["Build Web UI"].Run != "npm ci && npm run build" ||
		steps["Require built dashboard output"].Run != releaseDashboardCheck {
		return fmt.Errorf("release binary requires the real same-run dashboard build and complete nonempty regular referenced output")
	}
	return nil
}

func validateTagRelease(source []byte) error {
	var workflow releaseWorkflow
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		return err
	}
	if err := validateReleaseRunners(workflow, map[string]string{
		"setup-version": "ubuntu-latest", "build-cli": "ubuntu-latest",
		"release-cli": "ubuntu-latest", "release-extension": "ubuntu-latest", "validation": "",
	}); err != nil {
		return err
	}
	validation := workflow.Jobs["validation"]
	build := workflow.Jobs["build-cli"]
	release := workflow.Jobs["release-cli"]
	extension := workflow.Jobs["release-extension"]
	setup := workflow.Jobs["setup-version"]
	version := releaseSteps(setup)["Determine Version"]
	if version.ID != "version" ||
		!reflect.DeepEqual(setup.Outputs, map[string]string{
			"version": "${{ steps.version.outputs.version }}", "tag": "${{ steps.version.outputs.tag }}",
		}) || !strings.Contains(version.Run, `VERSION="${GITHUB_REF_NAME#v}"`) ||
		!strings.Contains(version.Run, `echo "version=$VERSION" >> "$GITHUB_OUTPUT"`) ||
		!strings.Contains(version.Run, `echo "tag=$GITHUB_REF_NAME" >> "$GITHUB_OUTPUT"`) ||
		extension.Env["VERSION"] != "${{ needs.setup-version.outputs.version }}" {
		return fmt.Errorf("tag and extension versions must bind the production version detector")
	}
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
		"Setup Go Environment", "Setup Node.js (for web UI build)", "Build Web UI", "Require built dashboard output",
		"Build Binary", "Upload Binary Artifact"}); err != nil {
		return err
	}
	if err := orderedReleaseSteps(release, []string{"Checkout Repository", "Download CLI Artifacts",
		"Generate SHA256 Checksums", "Create or Update GitHub Release"}); err != nil {
		return err
	}
	if err := orderedReleaseSteps(extension, []string{"Checkout Repository", "Require exact validated source",
		"Setup Go Environment", "Setup Node.js (for web UI build)", "Build Web UI", "Require built dashboard output",
		"Sync Version Files", "Build Extension", "Pack Extension",
		"Record extension artifact provenance", "Release Extension", "Publish Extension Provenance",
		"Publish Extension", "Create Release PR"}); err != nil {
		return err
	}
	buildSteps := releaseSteps(build)
	for _, producer := range []releaseJob{build, extension} {
		if err := validateReleaseDashboard(producer); err != nil {
			return err
		}
	}
	publishSteps := releaseSteps(release)
	extensionSteps := releaseSteps(extension)
	if !strings.HasPrefix(buildSteps["Build Binary"].Run, "VERSION=\"${{ needs.setup-version.outputs.version }}\"\n") ||
		!strings.HasPrefix(releaseSteps(release)["Create or Update GitHub Release"].Run, "TAG=\"${{ needs.setup-version.outputs.tag }}\"\n") {
		return fmt.Errorf("CLI build and publication must consume detected version and tag")
	}

	for _, step := range extension.Steps {
		if _, overrides := step.Env["VERSION"]; overrides {
			return fmt.Errorf("extension step %q must not override its detected version", step.Name)
		}
	}
	if buildSteps["Require exact validated source"].Run != releaseSourceCheck ||
		extensionSteps["Require exact validated source"].Run != releaseSourceCheck {
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
		!strings.Contains(checks, "source_sha=\"$(cat \"$source\")\"\n  test \"$source_sha\" = \"$GITHUB_SHA\"\n") ||
		!strings.Contains(checks, "recorded_digest=\"$(cat \"$digest\")\"\n  actual_digest=\"$(sha256sum \"$binary\")\"\n  test \"$recorded_digest\" = \"$actual_digest\"\n") ||
		!strings.Contains(checks, `sha256sum "${expected[@]}" > checksums.txt`) {
		return fmt.Errorf("CLI publication must reject stale or incomplete artifact evidence")
	}
	provenance := extensionSteps["Record extension artifact provenance"].Run
	if !strings.Contains(provenance, "test -d artifacts\nshopt -s nullglob\npackages=(artifacts/*.zip artifacts/*.tar.gz)\n") ||
		!strings.Contains(provenance, `test "${#packages[@]}" -gt 0`) ||
		!strings.Contains(provenance, "for package in \"${packages[@]}\"; do\n  test -f \"$package\"\ndone\n") ||
		!strings.Contains(provenance, `printf 'source_sha=%s\n' "$GITHUB_SHA"`) ||
		!strings.Contains(provenance, `sha256sum "${packages[@]}"`) ||
		!strings.Contains(extensionSteps["Publish Extension Provenance"].Run, `gh release upload "azd-ext-microsoft-azd-waza_${VERSION}"`) ||
		!strings.Contains(extensionSteps["Publish Extension Provenance"].Run, "artifacts/release-provenance.txt") {
		return fmt.Errorf("extension release must publish source-bound package digests")
	}
	return nil
}

func testReleaseBinaryHandoff(t *testing.T, record, restore string) {
	t.Helper()
	mutations := []string{"none", "binary", "digest", "foreign-filename", "missing-binary", "missing-digest",
		"matching-sidecar-error", "matching-hash-error"}
	if strings.Contains(restore, "source-sha.txt") {
		mutations = append(mutations, "missing-source", "matching-source-error")
	}
	for _, mutation := range mutations {
		t.Run("binary-handoff-"+mutation, func(t *testing.T) {
			dir := t.TempDir()
			binary := filepath.Join(dir, "waza")
			content := []byte("#!/bin/sh\nprintf '%s\\n' tested-release-binary > version-observed\n")
			writeReleaseFile(t, binary, content)
			writeReleaseFile(t, filepath.Join(dir, "source-sha.txt"), []byte("validated-source\n"))
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
			case "foreign-filename":
				writeReleaseFile(t, filepath.Join(dir, "other"), content)
				foreign := exec.CommandContext(t.Context(), "sha256sum", "other")
				foreign.Dir = dir
				digest, err := foreign.Output()
				if err != nil {
					t.Fatal(err)
				}
				writeReleaseFile(t, filepath.Join(dir, "waza.sha256"), digest)
			case "missing-binary":
				if err := os.Remove(binary); err != nil {
					t.Fatal(err)
				}
			case "missing-digest":
				if err := os.Remove(filepath.Join(dir, "waza.sha256")); err != nil {
					t.Fatal(err)
				}
			case "missing-source":
				if err := os.Remove(filepath.Join(dir, "source-sha.txt")); err != nil {
					t.Fatal(err)
				}
			}
			script := restore
			switch mutation {
			case "matching-source-error":
				script = failingReleaseCommand("cat", "source-sha.txt") + script
			case "matching-sidecar-error":
				script = failingReleaseCommand("cat", "waza.sha256") + script
			case "matching-hash-error":
				script = failingReleaseCommand("sha256sum", "waza") + script
			}
			cmd = exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", script)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GITHUB_SHA=validated-source")
			output, err := cmd.CombinedOutput()
			if (err == nil) != (mutation == "none") {
				t.Fatalf("restore mutation %s: %v\n%s", mutation, err, output)
			}
			if strings.HasPrefix(mutation, "matching-") {
				assertReleaseFailureObserved(t, dir)
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
		want := []string{"test", "-v", "-race", "-count=1", "-timeout=30m", "-coverprofile=coverage.txt", "-covermode=atomic",
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
