package scripts

import (
	"crypto/sha256"
	"debug/buildinfo"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const dashboardArtifactPath = "${{ runner.temp }}/release-dashboard"
const dashboardArtifactName = "release-dashboard-${{ github.sha }}-${{ github.run_attempt }}"
const dashboardManifestOutput = "${{ needs.validation.outputs.dashboard-manifest-sha256 }}"
const dashboardConsumerPathSetup = `echo "RELEASE_DASHBOARD_ARTIFACT=$RUNNER_TEMP/release-dashboard" >> "$GITHUB_ENV"`

var releaseExpressions = regexp.MustCompile(`\$\{\{(?s:.*?)\}\}`)
var releaseExpressionStrings = regexp.MustCompile(`'(?:[^']|'')*'`)
var releaseRuntimeContexts = regexp.MustCompile(`(^|[^[:alnum:]_.])(runner|env|steps|job)\s*(\.|\[)`)

func validateReleaseDashboardConsumer(job releaseJob) error {
	steps := releaseSteps(job)
	download := steps["Download exact-source dashboard"]
	for key, value := range job.Env {
		for _, expression := range releaseExpressions.FindAllString(value, -1) {
			expression = releaseExpressionStrings.ReplaceAllString(expression, "''")
			expression = strings.Join(strings.Fields(expression), "")
			if releaseRuntimeContexts.MatchString(expression) {
				return fmt.Errorf("job-level env %s cannot use runtime-only contexts", key)
			}
		}
	}
	if _, present := job.Env["RELEASE_DASHBOARD_ARTIFACT"]; present {
		return fmt.Errorf("dashboard artifact path must be exported at step scope")
	}
	if steps["Set dashboard artifact path"].Run != dashboardConsumerPathSetup ||
		job.Env["RELEASE_DASHBOARD_MANIFEST_SHA256"] != dashboardManifestOutput ||
		!strings.HasPrefix(download.Uses, "actions/download-artifact@") ||
		!reflect.DeepEqual(download.With, map[string]string{"name": dashboardArtifactName, "path": "${{ env.RELEASE_DASHBOARD_ARTIFACT }}"}) ||
		steps["Install exact-source dashboard"].Run != "bash .github/scripts/release_dashboard_handoff.sh install" ||
		steps["Require built dashboard output"].Run != releaseDashboardCheck {
		return fmt.Errorf("consumer must install same-run/source/attempt dashboard with independent producer digest and semantic admission")
	}
	for _, step := range job.Steps {
		if strings.Contains(step.Run, "npm ") {
			return fmt.Errorf("consumer must not rebuild the producer dashboard")
		}
		for _, key := range []string{"RELEASE_DASHBOARD_ARTIFACT", "RELEASE_DASHBOARD_MANIFEST_SHA256"} {
			if _, override := step.Env[key]; override {
				return fmt.Errorf("consumer step cannot override dashboard identity")
			}
		}
	}
	return nil
}

func TestReleaseDashboardConsumerPathScope(t *testing.T) {
	source := readReleaseWorkflow(t, "release.yml")
	var workflow releaseWorkflow
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"build-cli", "release-extension"} {
		t.Run(name, func(t *testing.T) {
			job := workflow.Jobs[name]
			dir := t.TempDir()
			output := filepath.Join(dir, "github-env")
			writeReleaseFile(t, output, []byte("EXISTING=retained\n"))
			runnerTemp := filepath.Join(dir, "runner temp")
			cmd := exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", releaseSteps(job)["Set dashboard artifact path"].Run)
			cmd.Env = append(os.Environ(), "RUNNER_TEMP="+runnerTemp, "GITHUB_ENV="+output)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("actual path setup: %v\n%s", err, out)
			}
			content, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			if string(content) != "EXISTING=retained\nRELEASE_DASHBOARD_ARTIFACT="+runnerTemp+"/release-dashboard\n" {
				t.Fatalf("path export must append actual runtime path, got %q", content)
			}
			for _, value := range []string{
				"${{ runner.temp }}/release-dashboard",
				"${{runner['temp']}}/release-dashboard",
				"${{ format('{0}/release-dashboard', runner . temp) }}",
			} {
				head, tail, found := strings.Cut(string(source), "  "+name+":\n")
				if !found {
					t.Fatal("consumer job missing from source")
				}
				binding := "      RELEASE_DASHBOARD_MANIFEST_SHA256: " + dashboardManifestOutput
				tail = strings.Replace(tail, binding, binding+"\n      INVALID_CONTEXT: "+value, 1)
				changed := []byte(head + "  " + name + ":\n" + tail)
				var mutated releaseWorkflow
				if err := yaml.Unmarshal(changed, &mutated); err != nil {
					t.Fatal(err)
				}
				if mutated.Jobs[name].Env["INVALID_CONTEXT"] != value {
					t.Fatal("mutation did not reach actual consumer job env")
				}
				if err := validateTagRelease(changed); err == nil || !strings.Contains(err.Error(), "job-level env") {
					t.Fatalf("actual source admitted runtime context in job env: %v", err)
				}
			}
			alternative := job
			alternative.Env = make(map[string]string, len(job.Env)+1)
			for key, value := range job.Env {
				alternative.Env[key] = value
			}
			alternative.Env["VALID_SCOPE"] = "${{ format('runner.temp', needs.runner.outputs.path) }}"
			if err := validateReleaseDashboardConsumer(alternative); err != nil {
				t.Fatalf("literal or nested property confused with runner context: %v", err)
			}
		})
	}
}

func validateReleaseDashboardProducer(source []byte) error {
	if err := validateReleaseValidation(source); err != nil {
		return err
	}
	var workflow struct {
		On struct {
			Call struct {
				Outputs map[string]struct{ Value string }
			} `yaml:"workflow_call"`
		}
		Jobs map[string]releaseJob
	}
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		return err
	}
	build := workflow.Jobs["build-test"]
	steps := releaseSteps(build)
	pack, upload := steps["Pack exact-source dashboard"], steps["Upload exact-source dashboard"]
	if workflow.On.Call.Outputs["dashboard-manifest-sha256"].Value != "${{ jobs.build-test.outputs.dashboard-manifest-sha256 }}" ||
		build.Outputs["dashboard-manifest-sha256"] != "${{ steps.dashboard.outputs.manifest_sha256 }}" ||
		pack.ID != "dashboard" || pack.Env["RELEASE_DASHBOARD_ARTIFACT"] != dashboardArtifactPath ||
		pack.Run != `bash .github/scripts/release_dashboard_handoff.sh pack >> "$GITHUB_OUTPUT"` ||
		!strings.HasPrefix(upload.Uses, "actions/upload-artifact@") ||
		!reflect.DeepEqual(upload.With, map[string]string{"name": dashboardArtifactName, "path": dashboardArtifactPath, "if-no-files-found": "error"}) {
		return fmt.Errorf("producer must carry exact manifest digest independently through reusable outputs")
	}
	count := 0
	for _, job := range workflow.Jobs {
		for _, step := range job.Steps {
			count += strings.Count(step.Run, "npm run build")
		}
	}
	if count != 1 {
		return fmt.Errorf("require exactly one dashboard compilation, got %d", count)
	}
	return orderedReleaseSteps(build, []string{"Build Web UI", "Require built dashboard output",
		"Require clean packaged source", "Pack exact-source dashboard", "Upload exact-source dashboard", "Build Release Binary"})
}

func TestReleaseDashboardWorkflowHandoff(t *testing.T) {
	producer, consumers := readReleaseWorkflow(t, "release-validation.yml"), readReleaseWorkflow(t, "release.yml")
	if err := validateReleaseDashboardProducer(producer); err != nil {
		t.Fatal(err)
	}
	if err := validateTagRelease(consumers); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct{ old, new string }{
		{"${{ jobs.build-test.outputs.dashboard-manifest-sha256 }}", "unbound"},
		{"${{ steps.dashboard.outputs.manifest_sha256 }}", "unbound"},
		{"id: dashboard", "id: other"},
		{"  build-test:\n", "  build-test:\n    if: 'false'\n"},
		{"  build-test:\n", "  build-test:\n    continue-on-error: true\n"},
		{"      - name: Pack exact-source dashboard\n", "      - name: Pack exact-source dashboard\n        if: 'false'\n"},
		{"      - name: Upload exact-source dashboard\n", "      - name: Upload exact-source dashboard\n        continue-on-error: true\n"},
		{`bash .github/scripts/release_dashboard_handoff.sh pack >> "$GITHUB_OUTPUT"`, "':'"},
		{"if-no-files-found: error", "if-no-files-found: warn"},
		{dashboardArtifactName, "release-dashboard-other-source"},
		{"run: npm ci && npm run build", "run: npm ci && npm run build && npm run build\n"},
	} {
		changed := strings.Replace(string(producer), mutation.old, mutation.new, 1)
		if changed == string(producer) {
			t.Fatalf("producer mutation did not change source: %s", mutation.old)
		}
		var parsed yaml.Node
		if err := yaml.Unmarshal([]byte(changed), &parsed); err != nil {
			t.Fatalf("producer mutation must reach semantic validation: %v", err)
		}
		if err := validateReleaseDashboardProducer([]byte(changed)); err == nil {
			t.Fatalf("producer mutation admitted: %s", mutation.old)
		}
	}
	for _, mutation := range []struct{ old, new string }{
		{dashboardManifestOutput, "unbound"},
		{dashboardArtifactName, "release-dashboard-other-source"},
		{dashboardConsumerPathSetup, "':'"},
		{"name: Set dashboard artifact path", "name: Missing path setup"},
		{"path: ${{ env.RELEASE_DASHBOARD_ARTIFACT }}", "path: /unbound-artifact"},
		{"      RELEASE_DASHBOARD_MANIFEST_SHA256: " + dashboardManifestOutput,
			"      RELEASE_DASHBOARD_ARTIFACT: " + dashboardArtifactPath + "\n      RELEASE_DASHBOARD_MANIFEST_SHA256: " + dashboardManifestOutput},
		{"run: bash .github/scripts/release_dashboard_handoff.sh install", "run: npm ci && npm run build"},
		{"name: release-dashboard-${{ github.sha }}-${{ github.run_attempt }}", "name: release-dashboard-${{ github.sha }}-${{ github.run_attempt }}\n          run-id: '123'"},
	} {
		changed := strings.Replace(string(consumers), mutation.old, mutation.new, 1)
		if changed == string(consumers) {
			t.Fatalf("consumer mutation did not change source: %s", mutation.old)
		}
		var parsed yaml.Node
		if err := yaml.Unmarshal([]byte(changed), &parsed); err != nil {
			t.Fatalf("consumer mutation must reach semantic validation: %v", err)
		}
		if err := validateTagRelease([]byte(changed)); err == nil {
			t.Fatalf("consumer mutation admitted: %s", mutation.old)
		}
	}
}

func TestReleaseDashboardHandoffCaller(t *testing.T) {
	bin := t.TempDir()
	for _, name := range []string{"release-assets-helper", "release-dashboard-check"} {
		cmd := exec.CommandContext(t.Context(), "go", "build", "-o", filepath.Join(bin, name), "./"+name)
		cmd.Env = append(os.Environ(), "GOOS=", "GOARCH=", "GOPROXY=off", "GOSUMDB=off")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("compile real %s: %v\n%s", name, err, output)
		}
	}
	writeReleaseFile(t, filepath.Join(bin, "go"), []byte(`#!/usr/bin/env bash
set -euo pipefail
if [ "$1" != run ]; then exec "$REAL_GO" "$@"; fi
test -z "$GOOS"
test -z "$GOARCH"
test "$GOPROXY" = off
test "$GOSUMDB" = off
program="${2##*/}"
shift 2
exec "$(dirname "$0")/$program" "$@"
`))
	writeReleaseFile(t, filepath.Join(bin, "npm"), []byte("#!/bin/sh\nprintf unexpected-npm >&2\nexit 99\n"))
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"valid", "alternative-valid", "dirty-before-pack", "missing-before-pack", "missing-artifact", "wrong-sha", "stale-manifest-source", "wrong-run", "wrong-attempt",
		"wrong-digest", "missing-digest", "digest-mismatch", "missing-member", "truncated", "dirty-equal-source", "dirty-after-install"} {
		t.Run(state, func(t *testing.T) {
			dir := t.TempDir()
			for path, content := range map[string]string{
				".gitignore":       "artifact/\nweb/dist/assets/\nbin/\n",
				"go.mod":           "module fixture.test/dashboard\n\ngo 1.26.4\n",
				"web/embed.go":     "package web\nimport \"embed\"\n//go:embed all:dist\nvar Dist embed.FS\n",
				"cmd/waza/main.go": "package main\nimport(\"fmt\"; \"fixture.test/dashboard/web\")\nvar version string\nfunc main(){fmt.Println(version,web.Dist)}\n",
				"version.txt":      "1.2.3\n", "extension.yaml": "version: 1.2.3\n",
				"web/dist/index.html":  "<script type='module' src='/assets/index-abcdefgh.js'></script><link rel='stylesheet' href='/assets/index-abcdefgh.css'>",
				"web/dist/favicon.svg": "<svg/>", "web/dist/assets/index-abcdefgh.js": "export {};",
				"web/dist/assets/index-abcdefgh.css": "body{}",
			} {
				if state == "alternative-valid" {
					content = strings.ReplaceAll(content, "index-abcdefgh", "app-12345678")
					path = strings.ReplaceAll(path, "index-abcdefgh", "app-12345678")
				}
				target := filepath.Join(dir, path)
				if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
					t.Fatal(err)
				}
				writeReleaseFile(t, target, []byte(content))
			}
			for _, name := range []string{"release_dashboard_handoff.sh", "release_source_check.sh", "release_binary_check.sh", "../../build.sh"} {
				content, err := os.ReadFile(name)
				if err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(dir, ".github", "scripts", name)
				if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
					t.Fatal(err)
				}
				writeReleaseFile(t, target, content)
			}
			publicationGit(t, dir, "init", "-q")
			publicationGit(t, dir, "add", ".")
			publicationGit(t, dir, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "source")
			sha := publicationGit(t, dir, "rev-parse", "HEAD")
			env := append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"GITHUB_SHA="+sha, "GITHUB_REPOSITORY=microsoft/waza", "GITHUB_RUN_ID=123", "GITHUB_RUN_ATTEMPT=1",
				"RELEASE_DASHBOARD_ARTIFACT="+filepath.Join(dir, "artifact"), "REAL_GO="+realGo)
			run := func(mode string, environment []string) ([]byte, error) {
				cmd := exec.CommandContext(t.Context(), "bash", ".github/scripts/release_dashboard_handoff.sh", mode)
				cmd.Dir, cmd.Env = dir, environment
				return cmd.CombinedOutput()
			}
			if state == "dirty-before-pack" {
				writeReleaseFile(t, filepath.Join(dir, "version.txt"), []byte("uncommitted"))
			}
			if state == "missing-before-pack" {
				if err := os.Remove(filepath.Join(dir, "web", "dist", "assets", "index-abcdefgh.js")); err != nil {
					t.Fatal(err)
				}
			}
			output, err := run("pack", env)
			if state == "dirty-before-pack" || state == "missing-before-pack" {
				if err == nil || strings.Contains(string(output), "manifest_sha256=") {
					t.Fatalf("dirty producer emitted trusted digest: %v\n%s", err, output)
				}
				if _, err := os.Stat(filepath.Join(dir, "artifact")); !os.IsNotExist(err) {
					t.Fatalf("dirty producer wrote artifact: %v", err)
				}
				return
			}
			if err != nil || !strings.HasPrefix(string(output), "manifest_sha256=") || len(strings.TrimSpace(string(output))) != 80 {
				t.Fatalf("production pack did not emit exact independent digest: %v\n%s", err, output)
			}
			digest := strings.TrimSpace(strings.TrimPrefix(string(output), "manifest_sha256="))
			artifact := filepath.Join(dir, "artifact")
			member := filepath.Join(artifact, "web", "dist", "assets", "index-abcdefgh.js")
			switch state {
			case "missing-artifact":
				if err := os.RemoveAll(artifact); err != nil {
					t.Fatal(err)
				}
			case "wrong-sha":
				env = append(env, "GITHUB_SHA="+strings.Repeat("a", 40))
			case "stale-manifest-source":
				path := filepath.Join(artifact, "manifest.json")
				content, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				stale := []byte(strings.Replace(string(content), sha, strings.Repeat("b", 40), 1))
				if string(content) == string(stale) {
					t.Fatal("source mutation did not change actual producer manifest")
				}
				writeReleaseFile(t, path, stale)
				digest = fmt.Sprintf("%x", sha256.Sum256(stale))
			case "wrong-run":
				env = append(env, "GITHUB_RUN_ID=456")
			case "wrong-attempt":
				env = append(env, "GITHUB_RUN_ATTEMPT=2")
			case "wrong-digest":
				digest = strings.Repeat("0", 64)
			case "missing-digest":
				digest = ""
			case "digest-mismatch":
				writeReleaseFile(t, member, []byte("changed bytes"))
			case "missing-member":
				if err := os.Remove(member); err != nil {
					t.Fatal(err)
				}
			case "truncated":
				if err := os.Truncate(member, 1); err != nil {
					t.Fatal(err)
				}
			case "dirty-equal-source":
				// Artifact equality must not promote uncommitted tracked bytes.
				index := filepath.Join(dir, "web", "dist", "index.html")
				content, err := os.ReadFile(index)
				if err != nil {
					t.Fatal(err)
				}
				publicationGit(t, dir, "update-index", "--cacheinfo", "100644,"+publicationGit(t, dir, "hash-object", "-w", "--stdin")+",web/dist/index.html")
				writeReleaseFile(t, index, content)
			}
			if err := os.RemoveAll(filepath.Join(dir, "web", "dist", "assets")); err != nil {
				t.Fatal(err)
			}
			env = append(env, "RELEASE_DASHBOARD_MANIFEST_SHA256="+digest)
			output, err = run("install", env)
			valid := state == "valid" || state == "alternative-valid" || state == "dirty-after-install"
			if (err == nil) != valid {
				t.Fatalf("production install %s: %v\n%s", state, err, output)
			}
			if valid {
				if state == "dirty-after-install" {
					writeReleaseFile(t, filepath.Join(dir, "version.txt"), []byte("dirty"))
				}
				output, err = run("verify-installed", env)
				if (err == nil) != (state != "dirty-after-install") {
					t.Fatalf("last-builder verification %s: %v\n%s", state, err, output)
				}
				cmd := exec.CommandContext(t.Context(), "bash", "build.sh")
				cmd.Dir, cmd.Env = dir, append(env, "REQUIRE_RELEASE_ADMISSION=1", "VERSION=1.2.3",
					"OUTPUT_DIR="+filepath.Join(dir, "bin"), "PLATFORM=", "GOPROXY=off", "GOSUMDB=off")
				output, err = cmd.CombinedOutput()
				if (err == nil) != (state != "dirty-after-install") {
					t.Fatalf("actual azd final builder %s (npm must not run): %v\n%s", state, err, output)
				}
				if state != "dirty-after-install" {
					for _, platform := range []string{"linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64", "windows-amd64.exe", "windows-arm64.exe"} {
						info, err := buildinfo.ReadFile(filepath.Join(dir, "bin", "microsoft-azd-waza-"+platform))
						if err != nil {
							t.Fatal(err)
						}
						settings := make(map[string]string)
						for _, setting := range info.Settings {
							settings[setting.Key] = setting.Value
						}
						if settings["vcs.revision"] != sha || settings["vcs.modified"] != "false" {
							t.Fatalf("shared dashboard cross-build lost clean source identity: %v", settings)
						}
					}
				}
			}
		})
	}
}
