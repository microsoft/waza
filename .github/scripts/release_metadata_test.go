package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestReleaseCommittedVersionAdmission(t *testing.T) {
	var workflow releaseWorkflow
	if err := yaml.Unmarshal(readReleaseWorkflow(t, "release.yml"), &workflow); err != nil {
		t.Fatal(err)
	}
	step := releaseSteps(workflow.Jobs["release-extension"])["Sync Version Files"]
	script := step.Run
	for _, tc := range []struct {
		name, version, manifest string
		ok                      bool
	}{
		{"canonical", "1.2.3\n", "version: 1.2.3\n", true},
		{"quoted", "1.2.3\n", "version: \"1.2.3\" # release\n", true},
		{"single-quoted-crlf", "1.2.3\n", "version: '1.2.3'\r\n", true},
		{"wrong-file", "9.9.9\n", "version: 1.2.3\n", false},
		{"wrong-manifest", "1.2.3\n", "version: 9.9.9\n", false},
		{"empty-file", "", "version: 1.2.3\n", false},
		{"missing-field", "1.2.3\n", "id: microsoft.azd.waza\n", false},
		{"duplicate-field", "1.2.3\n", "version: 1.2.3\nversion: 9.9.9\n", false},
		{"matching-output-error", "1.2.3\n", "version: 1.2.3\n", false},
		{"matching-manifest-read-error", "1.2.3\n", "version: 1.2.3\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeReleaseFile(t, filepath.Join(dir, "version.txt"), []byte(tc.version))
			writeReleaseFile(t, filepath.Join(dir, "extension.yaml"), []byte(tc.manifest))
			run := script + "\nprintf admitted > metadata-admitted\n"
			if tc.name == "matching-output-error" {
				run = failingReleaseCommand("cat", "version.txt") + run
			}
			if tc.name == "matching-manifest-read-error" {
				run = `
sed() {
  command sed "$@" || return
  for arg in "$@"; do
    if [ "$arg" = extension.yaml ]; then
      printf failed > manifest-read-failed
      return 73
    fi
  done
}
` + run
			}
			shell := []string{"-e", "-c"}
			if step.Shell == "bash" {
				shell = []string{"--noprofile", "--norc", "-eo", "pipefail", "-c"}
			}
			cmd := exec.CommandContext(t.Context(), "bash", append(shell, run)...)
			cmd.Dir, cmd.Env = dir, append(os.Environ(), "VERSION=1.2.3")
			output, err := cmd.CombinedOutput()
			if (err == nil) != tc.ok {
				t.Fatalf("committed metadata %s: %v\n%s", tc.name, err, output)
			}
			for path, expected := range map[string]string{"version.txt": tc.version, "extension.yaml": tc.manifest} {
				content, err := os.ReadFile(filepath.Join(dir, path))
				if err != nil || string(content) != expected {
					t.Fatalf("publication repaired source %s: %q, %v", path, content, err)
				}
			}
			_, err = os.Stat(filepath.Join(dir, "metadata-admitted"))
			if tc.ok && err != nil || !tc.ok && !os.IsNotExist(err) {
				t.Fatalf("metadata failure reached later production steps: %v", err)
			}
			if tc.name == "matching-output-error" {
				assertReleaseFailureObserved(t, dir)
			}
			if tc.name == "matching-manifest-read-error" {
				if _, err := os.Stat(filepath.Join(dir, "manifest-read-failed")); err != nil {
					t.Fatalf("production manifest sed failure was not exercised: %v", err)
				}
			}
		})
	}
}
