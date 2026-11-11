package scripts

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const acceptanceSource = "76fe63e71fcf488ce55ef1d8cbe5077a187c197f"
const acceptanceArtifact = "release-dashboard-acceptance-" + acceptanceSource + "-${{ github.run_id }}-${{ github.run_attempt }}"
const acceptancePath = "${{ runner.temp }}/release-dashboard"

type dashboardAcceptanceWorkflow struct {
	Name        string
	On          map[string]any
	Permissions map[string]string
	Concurrency struct {
		Group            string
		CancelInProgress bool `yaml:"cancel-in-progress"`
	}
	Env  map[string]string
	Jobs map[string]releaseJob
}

func acceptanceWorkflow(source []byte) (dashboardAcceptanceWorkflow, error) {
	var workflow dashboardAcceptanceWorkflow
	decoder := yaml.NewDecoder(strings.NewReader(string(source)))
	decoder.KnownFields(true)
	err := decoder.Decode(&workflow)
	return workflow, err
}

func validateDashboardAcceptance(source []byte) error {
	workflow, err := acceptanceWorkflow(source)
	if err != nil {
		return err
	}
	if workflow.Name != "Nonpublishing dashboard handoff acceptance" ||
		!reflect.DeepEqual(workflow.On, map[string]any{"workflow_dispatch": nil}) ||
		!reflect.DeepEqual(workflow.Permissions, map[string]string{"contents": "read"}) ||
		workflow.Concurrency.Group != "release-dashboard-acceptance" || workflow.Concurrency.CancelInProgress ||
		!reflect.DeepEqual(workflow.Env, map[string]string{
			"ACCEPTED_SOURCE_SHA":  acceptanceSource,
			"CONTROL_WORKFLOW_SHA": "${{ github.workflow_sha }}",
			"CONTROL_EVENT_SHA":    "${{ github.sha }}",
		}) {
		return fmt.Errorf("manual trigger, read-only permission, queue and authentic control/source identity required")
	}
	action := func(name, uses string, with map[string]string) releaseStep {
		return releaseStep{Name: name, Uses: uses, With: with}
	}
	run := func(name string) releaseStep {
		return releaseStep{Name: name, Shell: "bash"}
	}
	checkout := action("Checkout accepted source",
		"actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1",
		map[string]string{"ref": acceptanceSource, "persist-credentials": "false"})
	goSetup := action("Setup Go", "actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e",
		map[string]string{"go-version": "1.26", "cache-dependency-path": "go.sum"})
	dependencies := releaseStep{Name: "Prepare offline checker dependencies"}
	pack := run("Pack accepted-source dashboard")
	pack.ID = "dashboard"
	pack.Env = map[string]string{"RELEASE_DASHBOARD_ARTIFACT": acceptancePath}
	upload := action("Upload same-run dashboard",
		"actions/upload-artifact@cf430e030ddbb5b0abf93d22962f4752f3646cd9",
		map[string]string{"name": acceptanceArtifact, "path": acceptancePath, "if-no-files-found": "error", "retention-days": "3"})
	upload.ID = "upload"
	record := run("Record observed producer evidence")
	record.Env = map[string]string{
		"MANIFEST_SHA256":  "${{ steps.dashboard.outputs.manifest_sha256 }}",
		"TRANSPORT_DIGEST": "${{ steps.upload.outputs.artifact-digest }}",
		"ARTIFACT_ID":      "${{ steps.upload.outputs.artifact-id }}",
	}
	install := run("Install and verify actual hosted handoff")
	install.Env = map[string]string{
		"RELEASE_DASHBOARD_ARTIFACT":        acceptancePath,
		"RELEASE_DASHBOARD_MANIFEST_SHA256": "${{ needs.producer.outputs.manifest-sha256 }}",
	}
	expected := map[string]releaseJob{
		"producer": {
			RunsOn: "ubuntu-latest", TimeoutMinutes: 15,
			Outputs: map[string]string{"manifest-sha256": "${{ steps.dashboard.outputs.manifest_sha256 }}"},
			Steps: []releaseStep{
				checkout, run("Record control and admit accepted source"), goSetup, dependencies,
				action("Setup Node", "actions/setup-node@949feb2413d6458794dcd2491c4babbbce0c15c1",
					map[string]string{"node-version": "22", "cache": "npm", "cache-dependency-path": "web/package-lock.json"}),
				{Name: "Build genuine dashboard", WorkingDirectory: "web"},
				pack, upload, record,
			},
		},
		"consumer-bootstrap": {
			Needs: []string{"producer"}, RunsOn: "ubuntu-latest", TimeoutMinutes: 15,
			Steps: []releaseStep{
				checkout, run("Record control and admit accepted source"), goSetup, dependencies,
				action("Download same-run dashboard", "actions/download-artifact@9000827ccba6bdab643e8b6fd33ac0654aef8333",
					map[string]string{"name": acceptanceArtifact, "path": acceptancePath}),
				install,
				action("Setup original Azure Developer CLI", "Azure/setup-azd@0b7e3a35ab00f2eee7080c845eb39c3f0ebfa553", nil),
				run("Exercise original configured Kit bootstrap"),
			},
		},
	}
	// Freeze reviewed shell bodies as well as the parsed graph. This rejects added
	// commands, identity exports and error masking, not just missing substrings.
	shellDigests := map[string]string{
		"Record control and admit accepted source":   "2f9f726abf6d21a635125d4ff994dfa6c766530eb273538e7177bff8231fef3d",
		"Prepare offline checker dependencies":       "67d61c8e8a6f1e315d3b9377c5a69512f62e26c08e20bd1a34a675cba9a65075",
		"Build genuine dashboard":                    "9bfb1061990883c7d344a85f66e06d1ea0d0b596effe66fe9f0019534fd96b8e",
		"Pack accepted-source dashboard":             "52e753824abdf2f204c1ed6737ffcca5b1980d57c65ff80fc60a2f615d20648e",
		"Record observed producer evidence":          "a8b9e82e4f256935f79ac060cbeeadef5c6669812f19410987762442824f1329",
		"Install and verify actual hosted handoff":   "574907f9c2022d4ad120b0bd2cbcf08c8bcd57be0dbe7e3e4c9dd6401bd7bda9",
		"Exercise original configured Kit bootstrap": "1d45ae25834e235f47e5469e9f9489931915c12afc5ed8475ab0aaf477789819",
	}
	for key, job := range workflow.Jobs {
		for i, step := range job.Steps {
			if step.Run != "" {
				digest := fmt.Sprintf("%x", sha256.Sum256([]byte(step.Run)))
				if digest != shellDigests[step.Name] {
					return fmt.Errorf("%s/%s: unreviewed shell body", key, step.Name)
				}
				job.Steps[i].Run = ""
			} else if _, required := shellDigests[step.Name]; required {
				return fmt.Errorf("%s/%s: missing shell body", key, step.Name)
			}
		}
		workflow.Jobs[key] = job
	}
	if !reflect.DeepEqual(workflow.Jobs, expected) {
		return fmt.Errorf("exact source, pinned actions, independent digest, ordered two-job fail-closed graph required")
	}
	return nil
}

func TestDashboardAcceptanceContract(t *testing.T) {
	source := readReleaseWorkflow(t, "release-dashboard-acceptance.yml")
	mutations := []struct{ old, new string }{
		{"  workflow_dispatch:", "  push:"},
		{"  workflow_dispatch:", "  workflow_dispatch:\n  pull_request:"},
		{"  contents: read", "  contents: write"},
		{"  contents: read", "  contents: read\n  id-token: write"},
		{"  cancel-in-progress: false", "  cancel-in-progress: true"},
		{"CONTROL_WORKFLOW_SHA: ${{ github.workflow_sha }}", "CONTROL_WORKFLOW_SHA: ${{ github.sha }}"},
		{"CONTROL_EVENT_SHA: ${{ github.sha }}", "CONTROL_EVENT_SHA: stale"},
		{"ACCEPTED_SOURCE_SHA: " + acceptanceSource, "ACCEPTED_SOURCE_SHA: main"},
		{"ref: " + acceptanceSource, "ref: main"},
		{"persist-credentials: false", "persist-credentials: true"},
		{"needs: [producer]", "needs: []"},
		{"needs: [producer]", "needs: [producer]\n    if: always()"},
		{"  producer:\n", "  producer:\n    continue-on-error: true\n"},
		{"runs-on: ubuntu-latest", "runs-on: windows-latest"},
		{"timeout-minutes: 15", "timeout-minutes: 60"},
		{"${{ steps.dashboard.outputs.manifest_sha256 }}", "${{ steps.upload.outputs.artifact-digest }}"},
		{"${{ needs.producer.outputs.manifest-sha256 }}", "downloaded-manifest"},
		{"${{ github.run_id }}", "904"},
		{"${{ github.run_attempt }}", "1"},
		{"retention-days: 3", "retention-days: 90"},
		{"if-no-files-found: error", "if-no-files-found: warn"},
		{"name: Download same-run dashboard", "name: Download same-run dashboard\n        continue-on-error: true"},
		{"name: Download same-run dashboard", "name: Download same-run dashboard\n        if: 'false'"},
		{"path: " + acceptancePath, "path: unbound"},
		{"run: npm ci && npm run build", "run: npm ci && npm run build || true"},
		{"GITHUB_SHA=\"$ACCEPTED_SOURCE_SHA\" bash", "export GITHUB_SHA=\"$ACCEPTED_SOURCE_SHA\"\n          bash"},
		{"\"$GITHUB_RUN_ID\" \"$GITHUB_RUN_ATTEMPT\"", "\"904\" \"1\""},
		{"install microsoft.azd.extensions --source azd", "install microsoft.azd.extensions --source local"},
		{"test \"$kit_version\" = 0.14.0", "test \"$kit_version\" = 0.15.0"},
		{"azd x version)", "azd x version || true)"},
		{"azd version\n", "azd login\n          azd version\n"},
		{"azd version\n", "azd x publish\n          azd version\n"},
		{"go-version: '1.26'", "go-version: '1.27'"},
	}
	for _, newline := range []string{"\n", "\r\n"} {
		original := strings.ReplaceAll(string(source), "\n", newline)
		if err := validateDashboardAcceptance([]byte(original)); err != nil {
			t.Fatalf("valid %q workflow: %v", newline, err)
		}
		for i, mutation := range mutations {
			changed := strings.Replace(original, strings.ReplaceAll(mutation.old, "\n", newline),
				strings.ReplaceAll(mutation.new, "\n", newline), 1)
			if changed == original {
				t.Fatalf("mutation %d did not change actual workflow", i)
			}
			if _, err := acceptanceWorkflow([]byte(changed)); err != nil {
				t.Fatalf("mutation %d must reach semantic admission: %v", i, err)
			}
			if err := validateDashboardAcceptance([]byte(changed)); err == nil {
				t.Fatalf("mutation %d admitted: %s", i, mutation.old)
			}
		}
		workflow, err := acceptanceWorkflow(source)
		if err != nil {
			t.Fatal(err)
		}
		for _, jobName := range []string{"producer", "consumer-bootstrap"} {
			for _, field := range []string{"ref", "persist-credentials"} {
				original := workflow.Jobs[jobName].Steps[0].With[field]
				workflow.Jobs[jobName].Steps[0].With[field] = "untrusted"
				changed, err := yaml.Marshal(workflow)
				if err != nil {
					t.Fatal(err)
				}
				if err := validateDashboardAcceptance(changed); err == nil {
					t.Fatalf("%s checkout %s substitution admitted", jobName, field)
				}
				workflow.Jobs[jobName].Steps[0].With[field] = original
			}
		}
		workflow.Jobs["consumer-bootstrap"].Steps[4].With["run-id"] = "904"
		changed, err := yaml.Marshal(workflow)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateDashboardAcceptance(changed); err == nil {
			t.Fatal("cross-run download substitution admitted")
		}
	}
}

func TestDashboardAcceptanceShellBoundaries(t *testing.T) {
	workflow, err := acceptanceWorkflow(readReleaseWorkflow(t, "release-dashboard-acceptance.yml"))
	if err != nil {
		t.Fatal(err)
	}
	steps := releaseSteps(workflow.Jobs["consumer-bootstrap"])
	for _, test := range []struct {
		name, step, failure string
	}{
		{"admission-valid", "Record control and admit accepted source", ""},
		{"admission-wrong-source", "Record control and admit accepted source", "identity"},
		{"admission-error", "Record control and admit accepted source", "source"},
		{"pack-valid", "Pack accepted-source dashboard", ""},
		{"pack-error", "Pack accepted-source dashboard", "pack"},
		{"handoff-valid", "Install and verify actual hosted handoff", ""},
		{"install-error", "Install and verify actual hosted handoff", "install"},
		{"verification-error", "Install and verify actual hosted handoff", "verify-installed"},
		{"checker-error", "Install and verify actual hosted handoff", "checker"},
		{"source-error", "Install and verify actual hosted handoff", "source"},
		{"bootstrap-valid", "Exercise original configured Kit bootstrap", ""},
		{"bootstrap-install-error", "Exercise original configured Kit bootstrap", "bootstrap"},
		{"bootstrap-version-error", "Exercise original configured Kit bootstrap", "version"},
		{"bootstrap-wrong-kit", "Exercise original configured Kit bootstrap", "wrong-kit"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			scripts := filepath.Join(dir, ".github", "scripts")
			for _, path := range []string{bin, scripts, filepath.Join(dir, "web", "dist")} {
				if err := os.MkdirAll(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			write := func(path, text string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(text), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			write(filepath.Join(bin, "git"), "#!/bin/sh\nif [ \"$FAILURE\" = identity ]; then echo stale; else echo \"$ACCEPTED_SOURCE_SHA\"; fi\n")
			scoped := "#!/bin/sh\nset -eu\ntest \"$GITHUB_SHA\" = \"$ACCEPTED_SOURCE_SHA\"\ntest \"$CONTROL_EVENT_SHA\" = actual-control\ntest \"$GITHUB_RUN_ID\" = 123456\ntest \"$GITHUB_RUN_ATTEMPT\" = 7\n"
			write(filepath.Join(scripts, "release_source_check.sh"), scoped+"test \"$FAILURE\" != source\n")
			write(filepath.Join(scripts, "release_dashboard_handoff.sh"), scoped+"test \"$FAILURE\" != \"$1\"\ntest \"$RELEASE_DASHBOARD_MANIFEST_SHA256\" = independent-digest\n")
			write(filepath.Join(bin, "go"), "#!/bin/sh\nset -eu\ntest \"$GITHUB_SHA\" = actual-control\ntest -z \"$GOOS\"\ntest -z \"$GOARCH\"\ntest \"$GOPROXY\" = off\ntest \"$GOSUMDB\" = off\ntest \"$FAILURE\" != checker\n")
			write(filepath.Join(bin, "azd"), `#!/bin/sh
set -eu
test "$GITHUB_SHA" = actual-control
printf '%s\n' "$*" >> "$AZD_CALLS"
case "$*" in
  version|'config set alpha.extensions on'|'extension source list') ;;
  'extension install microsoft.azd.extensions --source azd') test "$FAILURE" != bootstrap ;;
  'x version')
    test "$FAILURE" != version
    if [ "$FAILURE" = wrong-kit ]; then echo 'Version: 0.15.0'; else echo 'Version: 0.14.0'; fi ;;
  *) exit 99 ;;
esac
`)
			step := steps[test.step]
			if test.step == "Pack accepted-source dashboard" {
				step = releaseSteps(workflow.Jobs["producer"])[test.step]
			}
			summary := filepath.Join(dir, "summary")
			jobOutput := filepath.Join(dir, "job-output")
			calls := filepath.Join(dir, "azd-calls")
			cmd := exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", step.Run)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"ACCEPTED_SOURCE_SHA="+acceptanceSource, "GITHUB_SHA=actual-control", "CONTROL_EVENT_SHA=actual-control",
				"CONTROL_WORKFLOW_SHA=actual-workflow", "GITHUB_REPOSITORY=microsoft/waza", "GITHUB_RUN_ID=123456",
				"GITHUB_RUN_ATTEMPT=7", "GITHUB_STEP_SUMMARY="+summary, "FAILURE="+test.failure,
				"RELEASE_DASHBOARD_MANIFEST_SHA256=independent-digest", "AZD_CALLS="+calls, "GITHUB_OUTPUT="+jobOutput)
			output, err := cmd.CombinedOutput()
			if (err != nil) != (test.failure != "") {
				t.Fatalf("failure=%q: %v\n%s", test.failure, err, output)
			}
			if test.step == "Exercise original configured Kit bootstrap" && test.failure == "" {
				actual, err := os.ReadFile(calls)
				expected := "version\nconfig set alpha.extensions on\nextension source list\nextension install microsoft.azd.extensions --source azd\nx version\n"
				if err != nil || string(actual) != expected {
					t.Fatalf("original bootstrap calls: %q, %v", actual, err)
				}
			}
			if test.failure != "" && test.step != "Record control and admit accepted source" {
				if _, err := os.Stat(summary); !os.IsNotExist(err) {
					t.Fatalf("failed boundary emitted success summary: %v", err)
				}
			}
			if test.failure == "pack" {
				content, err := os.ReadFile(jobOutput)
				if err != nil || len(content) != 0 {
					t.Fatalf("failed pack exposed a producer output: %q, %v", content, err)
				}
			}
		})
	}
	for jobName, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if step.Run == "" {
				continue
			}
			cmd := exec.CommandContext(t.Context(), "bash", "-n")
			cmd.Stdin = strings.NewReader(step.Run)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%s/%s syntax: %v\n%s", jobName, step.Name, err, output)
			}
		}
	}
}
