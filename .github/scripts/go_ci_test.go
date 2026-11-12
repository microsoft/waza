package scripts

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestGoLintExecutesReleaseAssetHelper(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "workflows", "go-ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	check := func(source []byte) error {
		var workflow struct {
			Env  map[string]yaml.Node `yaml:"env"`
			Jobs map[string]struct {
				Name  string               `yaml:"name"`
				Env   map[string]yaml.Node `yaml:"env"`
				Steps []struct {
					ordinaryValidationStep `yaml:",inline"`
					Env                    map[string]yaml.Node `yaml:"env"`
				} `yaml:"steps"`
			} `yaml:"jobs"`
		}
		if err := yaml.Unmarshal(source, &workflow); err != nil {
			return err
		}
		job := workflow.Jobs["lint"]
		if job.Name != "Lint" {
			return fmt.Errorf("helper tests require the required Lint job")
		}
		for _, step := range job.Steps {
			if step.Name != "Test Go workflow trigger selection" {
				continue
			}
			if step.If != "" || (step.ContinueOnError.Kind != 0 &&
				(step.ContinueOnError.Tag != "!!bool" || step.ContinueOnError.Value != "false")) {
				return fmt.Errorf("helper test selection must run without conditional or tolerated failures")
			}
			var goflags *yaml.Node
			for _, env := range []map[string]yaml.Node{workflow.Env, job.Env, step.Env} {
				if value, present := env["GOFLAGS"]; present {
					goflags = &value
				}
			}
			if goflags != nil && (goflags.Kind != yaml.ScalarNode || goflags.Tag != "!!str" || goflags.Value != "") {
				return fmt.Errorf("effective declared GOFLAGS must be omitted or a literal empty string")
			}
			literal := strings.Fields(step.Run)
			if len(literal) < 4 || !slices.Equal(literal[:3], []string{"go", "test", "-count=1"}) {
				return fmt.Errorf("expected literal uncached Go test command")
			}
			packages := literal[3:]
			if len(packages) > 0 && packages[0] == "-v" {
				packages = packages[1:]
			}
			for _, arg := range packages {
				if !strings.HasPrefix(arg, "./") {
					return fmt.Errorf("expected only literal package arguments after -count=1, got %q", arg)
				}
				for _, char := range arg {
					if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._-", char) {
						return fmt.Errorf("nonliteral package argument %q", arg)
					}
				}
			}
			scratch := t.TempDir()
			captured := filepath.Join(scratch, "arguments")
			capturedEnv := filepath.Join(scratch, "environment")
			probe := "#!/usr/bin/env bash\nset -euo pipefail\ntest ! -e \"$WAZA_HELPER_TEST_ARGUMENTS\"\nprintf '%s\\n' \"$@\" > \"$WAZA_HELPER_TEST_ARGUMENTS\"\nprintf 'GOFLAGS=%s\\n' \"$GOFLAGS\" > \"$WAZA_HELPER_TEST_ENVIRONMENT\"\n"
			if err := os.WriteFile(filepath.Join(scratch, "go"), []byte(probe), 0o700); err != nil {
				return err
			}
			cmd := exec.CommandContext(t.Context(), "bash", "-e", "-o", "pipefail", "-c", step.Run)
			cmd.Env = append(os.Environ(),
				"PATH="+scratch+string(os.PathListSeparator)+os.Getenv("PATH"),
				"WAZA_HELPER_TEST_ARGUMENTS="+captured,
				"WAZA_HELPER_TEST_ENVIRONMENT="+capturedEnv,
				"GOFLAGS=",
			)
			if output, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("execute actual Lint test-selection command: %w\n%s", err, output)
			}
			// This guards declarative workflow configuration, not ambient go env -w or runner tool state.
			envData, err := os.ReadFile(capturedEnv)
			if err != nil {
				return fmt.Errorf("record effective GOFLAGS: %w", err)
			}
			if string(envData) != "GOFLAGS=\n" {
				return fmt.Errorf("actual Go environment differs from the admitted empty GOFLAGS")
			}
			data, err := os.ReadFile(captured)
			if err != nil {
				return fmt.Errorf("Lint must invoke Go, not merely mention the package: %w", err)
			}
			args := strings.Split(strings.TrimSpace(string(data)), "\n")
			if !slices.Equal(args, literal[1:]) {
				return fmt.Errorf("actual Go arguments differ from literal unfiltered command: %q", args)
			}
			count := 0
			for _, arg := range args {
				if arg == "./.github/scripts/release-assets-helper" {
					count++
				}
			}
			if count != 1 {
				return fmt.Errorf("actual Lint Go invocation selects helper %d times; want exactly once", count)
			}
			return nil
		}
		return fmt.Errorf("required Lint helper test-selection step is missing")
	}
	if err := check(source); err != nil {
		t.Fatal(err)
	}
	if err := check([]byte(strings.Replace(string(source), "go test -count=1 -v", "go test -count=1", 1))); err != nil {
		t.Fatalf("helper selection must also admit an unfiltered nonverbose command: %v", err)
	}
	for _, tc := range []struct{ name, before, after string }{
		{"missing-package", " ./.github/scripts/release-assets-helper", ""},
		{"echo-only", "run: go test -count=1 -v ./.github/scripts", "run: echo go test -count=1 -v ./.github/scripts"},
		{"skipped-step", "- name: Test Go workflow trigger selection\n        run:", "- name: Test Go workflow trigger selection\n        if: false\n        run:"},
		{"run-value", "-count=1 -v ./.github/scripts", "-count=1 -v -run '^$' ./.github/scripts"},
		{"run-equals", "-count=1 -v ./.github/scripts", "-count=1 -v -run=^$ ./.github/scripts"},
		{"list-value", "-count=1 -v ./.github/scripts", "-count=1 -v -list '.' ./.github/scripts"},
		{"list-equals", "-count=1 -v ./.github/scripts", "-count=1 -v -list=. ./.github/scripts"},
		{"skip-value", "-count=1 -v ./.github/scripts", "-count=1 -v -skip '.' ./.github/scripts"},
		{"skip-equals", "-count=1 -v ./.github/scripts", "-count=1 -v -skip=. ./.github/scripts"},
		{"args-value", "-count=1 -v ./.github/scripts", "-count=1 -v -args -test.run '^$' ./.github/scripts"},
		{"args-equals", "-count=1 -v ./.github/scripts", "-count=1 -v -args=-test.run=^$ ./.github/scripts"},
		{"exec-value", "-count=1 -v ./.github/scripts", "-count=1 -v -exec echo ./.github/scripts"},
		{"exec-equals", "-count=1 -v ./.github/scripts", "-count=1 -v -exec=echo ./.github/scripts"},
		{"late-run", "./.github/scripts/release-assets-helper", "./.github/scripts/release-assets-helper -run=^$"},
		{"count-override", "-count=1 -v ./.github/scripts", "-count=1 -v -count=0 ./.github/scripts"},
		{"failure-mask", "./.github/scripts/release-assets-helper", "./.github/scripts/release-assets-helper || true"},
		{"pipeline-mask", "./.github/scripts/release-assets-helper", "./.github/scripts/release-assets-helper | true"},
		{"duplicate-helper", "./.github/scripts/release-assets-helper", "./.github/scripts/release-assets-helper ./.github/scripts/release-assets-helper"},
		{"workflow-goflags", "jobs:\n", "env:\n  GOFLAGS: '-run=^$'\njobs:\n"},
		{"job-goflags", "  lint:\n", "  lint:\n    env:\n      GOFLAGS: '-list=.'\n"},
		{"step-goflags", "- name: Test Go workflow trigger selection\n", "- name: Test Go workflow trigger selection\n        env:\n          GOFLAGS: '-skip=.'\n"},
		{"dynamic-goflags", "  lint:\n", "  lint:\n    env:\n      GOFLAGS: '${{ vars.GOFLAGS }}'\n"},
		{"null-goflags", "  lint:\n", "  lint:\n    env:\n      GOFLAGS: null\n"},
		{"boolean-goflags", "  lint:\n", "  lint:\n    env:\n      GOFLAGS: false\n"},
		{"numeric-goflags", "  lint:\n", "  lint:\n    env:\n      GOFLAGS: 0\n"},
		{"sequence-goflags", "  lint:\n", "  lint:\n    env:\n      GOFLAGS: []\n"},
		{"mapping-goflags", "  lint:\n", "  lint:\n    env:\n      GOFLAGS: {}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mutated := strings.Replace(string(source), tc.before, tc.after, 1)
			if mutated == string(source) || check([]byte(mutated)) == nil {
				t.Fatalf("actual helper selection mutation %q was not rejected", tc.name)
			}
		})
	}
	for _, tc := range []struct{ name, before, after string }{
		{"package-order", "./.github/scripts ./.github/scripts/publish-runner-reference-guard", "./.github/scripts/publish-runner-reference-guard ./.github/scripts"},
		{"additional-package", "-count=1 -v ./.github/scripts", "-count=1 -v ./internal/models ./.github/scripts"},
		{"workflow-empty-goflags", "jobs:\n", "env:\n  GOFLAGS: ''\njobs:\n"},
		{"job-empty-overrides-workflow", "  lint:\n", "  lint:\n    env:\n      GOFLAGS: ''\n"},
		{"step-empty-overrides-job", "- name: Test Go workflow trigger selection\n", "- name: Test Go workflow trigger selection\n        env:\n          GOFLAGS: ''\n"},
		{"step-empty-overrides-workflow", "- name: Test Go workflow trigger selection\n", "- name: Test Go workflow trigger selection\n        env:\n          GOFLAGS: ''\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mutated := strings.Replace(string(source), tc.before, tc.after, 1)
			switch tc.name {
			case "job-empty-overrides-workflow":
				mutated = strings.Replace(mutated, "jobs:\n", "env:\n  GOFLAGS: '-run=^$'\njobs:\n", 1)
			case "step-empty-overrides-job":
				mutated = strings.Replace(mutated, "  lint:\n", "  lint:\n    env:\n      GOFLAGS: '-list=.'\n", 1)
			case "step-empty-overrides-workflow":
				mutated = strings.Replace(mutated, "jobs:\n", "env:\n  GOFLAGS: '${{ vars.GOFLAGS }}'\njobs:\n", 1)
			}
			if mutated == string(source) {
				t.Fatal("alternative-valid command was not changed")
			}
			if err := check([]byte(mutated)); err != nil {
				t.Fatalf("alternative-valid command rejected: %v", err)
			}
		})
	}
}

type workflowTriggers map[string]workflowTrigger

type workflowLiteral struct {
	Present bool
	Tag     string
	Value   string
}

type workflowInput struct {
	Type        string
	Default     workflowLiteral
	Required    workflowLiteral
	Description string
}

type workflowTrigger struct {
	Mapping bool
	Filters map[string][]string
	Inputs  map[string]workflowInput
	Other   map[string]yaml.Node
}

func (trigger *workflowTrigger) UnmarshalYAML(node *yaml.Node) error {
	if node.Tag == "!!null" {
		return nil
	}
	trigger.Mapping = true
	var fields map[string]yaml.Node
	if err := node.Decode(&fields); err != nil {
		return err
	}
	for key, value := range fields {
		switch key {
		case "inputs":
			if value.Kind != yaml.MappingNode {
				return fmt.Errorf("workflow inputs must be a mapping")
			}
			var definitions map[string]map[string]yaml.Node
			if err := value.Decode(&definitions); err != nil {
				return err
			}
			trigger.Inputs = make(map[string]workflowInput, len(definitions))
			for name, definition := range definitions {
				var input workflowInput
				for field, raw := range definition {
					if raw.Kind != yaml.ScalarNode {
						return fmt.Errorf("input %s: %s must be scalar", name, field)
					}
					switch field {
					case "type", "description":
						if raw.Tag != "!!str" {
							return fmt.Errorf("input %s: %s must be a string", name, field)
						}
						if field == "type" {
							input.Type = raw.Value
						} else {
							input.Description = raw.Value
						}
					case "default":
						input.Default = workflowLiteral{Present: true, Tag: raw.Tag, Value: raw.Value}
					case "required":
						if raw.Tag != "!!bool" {
							return fmt.Errorf("input %s: required must be boolean", name)
						}
						input.Required = workflowLiteral{Present: true, Tag: raw.Tag, Value: strings.ToLower(raw.Value)}
					default:
						return fmt.Errorf("input %s: unsupported field %s", name, field)
					}
				}
				defaultTag := map[string][]string{
					"boolean": {"!!bool"}, "string": {"!!str"}, "number": {"!!int", "!!float"},
				}[input.Type]
				if defaultTag == nil || (input.Default.Present && !slices.Contains(defaultTag, input.Default.Tag)) {
					return fmt.Errorf("input %s: invalid type or default", name)
				}
				if input.Default.Tag == "!!bool" {
					input.Default.Value = strings.ToLower(input.Default.Value)
				}
				trigger.Inputs[name] = input
			}
		case "branches", "branches-ignore", "paths", "paths-ignore", "types":
			var filter []string
			var entries []yaml.Node
			if err := value.Decode(&entries); err != nil {
				return err
			}
			if entries != nil {
				filter = make([]string, 0, len(entries))
			}
			for _, entry := range entries {
				if entry.Kind == yaml.AliasNode && entry.Alias != nil {
					entry = *entry.Alias
				}
				if entry.Kind != yaml.ScalarNode || entry.Tag != "!!str" {
					return fmt.Errorf("workflow %s entries must be strings", key)
				}
				filter = append(filter, entry.Value)
			}
			if trigger.Filters == nil {
				trigger.Filters = make(map[string][]string)
			}
			trigger.Filters[key] = filter
		default:
			if trigger.Other == nil {
				trigger.Other = make(map[string]yaml.Node)
			}
			trigger.Other[key] = value
		}
	}
	return nil
}

func checkOfflineAssertionOracleRuntime(source []byte) error {
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string            `yaml:"name"`
				Uses string            `yaml:"uses"`
				If   string            `yaml:"if"`
				With map[string]string `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		return err
	}
	for _, name := range []string{"build-and-test", "build-and-test-windows"} {
		found := false
		for _, step := range workflow.Jobs[name].Steps {
			if strings.HasPrefix(step.Uses, "actions/setup-node@") {
				if step.If != "" || step.With["node-version"] != "22" {
					return fmt.Errorf("%s: offline TypeScript oracle requires unconditional Node 22 setup", name)
				}
				found = true
			}
			if step.Name == "Execute Tests" && !found {
				return fmt.Errorf("%s: tests precede offline oracle runtime setup", name)
			}
		}
		if !found {
			return fmt.Errorf("%s: missing offline oracle Node setup", name)
		}
	}
	return nil
}

func TestGoWorkflowsInstallOfflineAssertionOracleRuntimeOnEveryPlatform(t *testing.T) {
	for _, name := range []string{"go-ci.yml"} {
		t.Run(name, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join("..", "workflows", name))
			if err != nil {
				t.Fatal(err)
			}
			if err := checkOfflineAssertionOracleRuntime(source); err != nil {
				t.Fatal(err)
			}
			for _, replacement := range []string{
				"      - name: Setup Node.js (web build and offline assertion oracle)\n        if: matrix.os == 'ubuntu-latest'",
				"      - name: Setup Node.js (web build and offline assertion oracle)\n        if: false",
			} {
				mutated := strings.Replace(string(source), "      - name: Setup Node.js (web build and offline assertion oracle)", replacement, 1)
				if mutated == string(source) || checkOfflineAssertionOracleRuntime([]byte(mutated)) == nil {
					t.Fatal("mutation failed to reject conditional/skipped Node setup")
				}
			}
			mutated := strings.Replace(string(source), "node-version: '22'", "node-version: '20'", 1)
			if mutated == string(source) || checkOfflineAssertionOracleRuntime([]byte(mutated)) == nil {
				t.Fatal("mutation failed to reject unsupported TypeScript runtime")
			}
		})
	}
}

type ordinaryValidationStep struct {
	Name            string    `yaml:"name"`
	If              string    `yaml:"if"`
	Run             string    `yaml:"run"`
	ContinueOnError yaml.Node `yaml:"continue-on-error"`
}

type ordinaryExecutionStep struct {
	ordinaryValidationStep `yaml:",inline"`
	Shell                  yaml.Node `yaml:"shell"`
}

type ordinaryRunDefaults struct {
	Run struct {
		Shell yaml.Node `yaml:"shell"`
	} `yaml:"run"`
}

func normalizeWorkflowLineEndings(source []byte) []byte {
	return []byte(strings.ReplaceAll(string(source), "\r\n", "\n"))
}

func ordinaryShellArguments(platform string, declarations ...yaml.Node) ([]string, error) {
	var effective yaml.Node
	for _, declaration := range declarations {
		if declaration.Kind != 0 {
			effective = declaration
		}
	}
	if effective.Kind == 0 {
		if platform == "Windows" {
			return nil, fmt.Errorf("Windows validation requires explicit Bash for this Bash-script contract; the implicit shell is PowerShell")
		}
		return []string{"-e"}, nil
	}
	if effective.Kind != yaml.ScalarNode || effective.Tag != "!!str" {
		return nil, fmt.Errorf("effective validation shell must be a literal supported Bash string")
	}
	switch effective.Value {
	case "bash", "bash --noprofile --norc -e -o pipefail {0}":
		return []string{"--noprofile", "--norc", "-e", "-o", "pipefail"}, nil
	case "bash -e {0}":
		return []string{"-e"}, nil
	case "bash -e -o pipefail {0}":
		return []string{"-e", "-o", "pipefail"}, nil
	default:
		return nil, fmt.Errorf("unsupported validation shell %q: require a canonical failure-propagating Bash shell", effective.Value)
	}
}

func ordinaryGoArguments(t *testing.T, command, platform string, packages, shellArguments []string) ([]string, error) {
	args, _, err := ordinaryGoProbe(t, command, platform, packages, shellArguments)
	return args, err
}

func ordinaryGoProbe(t *testing.T, command, platform string, packages, shellArguments []string, captureSource ...[]byte) ([]string, string, error) {
	t.Helper()
	scratch := t.TempDir()
	inventory := filepath.Join(scratch, "packages")
	arguments := filepath.Join(scratch, "arguments")
	discovery := filepath.Join(scratch, "discovery")
	listed := filepath.Join(scratch, "listed")
	scripts := filepath.Join(scratch, ".github", "scripts")
	copyCICaptureScripts(t, scripts)
	if len(captureSource) > 0 {
		if err := os.WriteFile(filepath.Join(scripts, "ci_go_test_capture.sh"), captureSource[0], 0o600); err != nil {
			return nil, "", err
		}
	}
	if err := os.WriteFile(inventory, []byte(strings.Join(packages, "\n")+"\n"), 0o600); err != nil {
		return nil, "", err
	}
	// Execute the workflow shell, but record Go invocations instead of rerunning tests.
	probe := `#!/usr/bin/env bash
set -euo pipefail
case "$1" in
  version)
    test "$#" -eq 1
    echo 'go version go1.26.4 linux/amd64'
    ;;
  list)
    test "$#" -eq 2
    test "$2" = ./...
    test ! -e "$WAZA_PACKAGE_LISTED"
    : > "$WAZA_PACKAGE_LISTED"
    cat "$WAZA_PACKAGE_INVENTORY"
    ;;
  test)
    shift
    phase=execution
    for arg in "$@"; do
      if [ "$arg" = -list ]; then phase=discovery; fi
    done
    captured="$WAZA_TEST_ARGUMENTS"
    if [ "$phase" = discovery ]; then captured="$WAZA_DISCOVERY_ARGUMENTS"; fi
    test ! -e "$captured"
    printf '%s\n' "$@" > "$captured"
    while [ "$#" -gt 0 ] && [[ "$1" = -* ]]; do
      if [ "$1" = -list ]; then shift 2; else shift; fi
    done
    if [ "$*" = ./... ]; then
      selected=()
      while IFS= read -r package; do selected+=("$package"); done < "$WAZA_PACKAGE_INVENTORY"
    else
      selected=("$@")
    fi
    for package in "${selected[@]}"; do
      printf '{"Action":"start","Package":"%s"}\n' "$package"
      if [ "$phase" = discovery ]; then
        printf '{"Action":"output","Package":"%s","Output":"TestObserved\\n"}\n' "$package"
      else
        printf '{"Action":"run","Package":"%s","Test":"TestObserved"}\n' "$package"
        printf '{"Action":"pass","Package":"%s","Test":"TestObserved","Elapsed":1}\n' "$package"
      fi
      printf '{"Action":"pass","Package":"%s","Elapsed":2}\n' "$package"
    done
    ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(scratch, "go"), []byte(probe), 0o700); err != nil {
		return nil, "", err
	}
	script := filepath.Join(scratch, "selection script.sh")
	if err := os.WriteFile(script, []byte(command), 0o600); err != nil {
		return nil, "", fmt.Errorf("write test-selection script: %w", err)
	}
	cmd := exec.CommandContext(t.Context(), "bash", append(slices.Clone(shellArguments), script)...)
	cmd.Dir = scratch
	cmd.Env = append(os.Environ(),
		"PATH="+scratch+string(os.PathListSeparator)+os.Getenv("PATH"),
		"RUNNER_OS="+platform,
		"CI_EVIDENCE_COLD_REQUESTED=false",
		"WAZA_PACKAGE_INVENTORY="+inventory,
		"WAZA_TEST_ARGUMENTS="+arguments,
		"WAZA_DISCOVERY_ARGUMENTS="+discovery,
		"WAZA_PACKAGE_LISTED="+listed,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		return nil, "", fmt.Errorf("%s: executing workflow test-selection probe: %w\n%s", platform, err, output)
	}
	captured, err := os.ReadFile(arguments)
	if err != nil {
		return nil, "", fmt.Errorf("%s: workflow must execute Go tests: %w", platform, err)
	}
	return strings.Split(strings.TrimSuffix(string(captured), "\n"), "\n"), scratch, nil
}

func checkOrdinaryTestSelection(t *testing.T, command, platform string, packages []string, sharded bool, shellArguments []string, captureSource ...[]byte) error {
	t.Helper()
	args, scratch, err := ordinaryGoProbe(t, command, platform, packages, shellArguments, captureSource...)
	if err != nil {
		return err
	}
	requiredFlags := []string{"-count=1", "-json", "-v", "-race", "-timeout=30m", "-coverprofile=coverage.txt", "-covermode=atomic"}
	var flags, selected []string
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			flags = append(flags, arg)
		} else if arg == "./..." {
			selected = append(selected, packages...)
		} else {
			selected = append(selected, arg)
		}
	}
	slices.Sort(flags)
	slices.Sort(requiredFlags)
	if !slices.Equal(flags, requiredFlags) {
		return fmt.Errorf("%s: Go test flags = %q; want %q without narrowing selectors", platform, flags, requiredFlags)
	}
	expected := slices.Clone(packages)
	if sharded {
		expected = slices.DeleteFunc(expected, func(name string) bool {
			return name == "github.com/microsoft/waza/internal/controlledcomparison"
		})
	}
	slices.Sort(selected)
	slices.Sort(expected)
	if !slices.Equal(selected, expected) {
		return fmt.Errorf("%s: Go test packages = %q; want exhaustive ordinary inventory %q", platform, selected, expected)
	}
	discovered, err := os.ReadFile(filepath.Join(scratch, "discovery"))
	if err != nil {
		return fmt.Errorf("%s: missing separate discovery invocation: %w", platform, err)
	}
	discoveryArgs := strings.Split(strings.TrimSuffix(string(discovered), "\n"), "\n")
	prefix := []string{"-race", "-covermode=atomic", "-json", "-list", "."}
	if len(discoveryArgs) < len(prefix) || !slices.Equal(discoveryArgs[:len(prefix)], prefix) {
		return fmt.Errorf("%s: discovery flags = %q; want nonexecuting %q", platform, discoveryArgs, prefix)
	}
	discoveryPackages := discoveryArgs[len(prefix):]
	if slices.Equal(discoveryPackages, []string{"./..."}) {
		discoveryPackages = slices.Clone(packages)
	}
	slices.Sort(discoveryPackages)
	if !slices.Equal(discoveryPackages, expected) {
		return fmt.Errorf("%s: discovery packages = %q; want %q", platform, discoveryPackages, expected)
	}
	_, listErr := os.Stat(filepath.Join(scratch, "listed"))
	if sharded && listErr != nil {
		return fmt.Errorf("%s: remaining selection must use authoritative go list ./...: %w", platform, listErr)
	}
	if !sharded && !os.IsNotExist(listErr) {
		return fmt.Errorf("%s: full execution must preserve root ./... without separate package selection", platform)
	}
	reportData, err := os.ReadFile(filepath.Join(scratch, "coverage.txt.evidence.json"))
	if err != nil {
		return err
	}
	var report struct {
		Complete      bool
		DiscoveryExit *int `json:"discovery_exit"`
		ExecutionExit *int `json:"execution_exit"`
	}
	if err := json.Unmarshal(reportData, &report); err != nil {
		return err
	}
	if !report.Complete || report.DiscoveryExit == nil || *report.DiscoveryExit != 0 || report.ExecutionExit == nil || *report.ExecutionExit != 0 {
		return fmt.Errorf("%s: workflow capture must finalize actual discovery and execution evidence", platform)
	}
	return nil
}

func checkOrdinaryRuntimeMarker(t *testing.T, command string, shellArguments []string) error {
	t.Helper()
	guard := filepath.Join(t.TempDir(), "offline runtime")
	script := guard + ".sh"
	if err := os.WriteFile(script, []byte(command), 0o600); err != nil {
		return fmt.Errorf("write runtime-marker script: %w", err)
	}
	for _, present := range []bool{false, true} {
		if present {
			if err := os.WriteFile(guard+".invoked", nil, 0o600); err != nil {
				return err
			}
		}
		cmd := exec.CommandContext(t.Context(), "bash", append(slices.Clone(shellArguments), script)...)
		cmd.Env = append(os.Environ(), "COPILOT_CLI_PATH="+filepath.ToSlash(guard))
		output, err := cmd.CombinedOutput()
		status := 0
		if err != nil {
			exit, ok := err.(*exec.ExitError)
			if !ok {
				return fmt.Errorf("execute runtime marker check: %w", err)
			}
			status = exit.ExitCode()
		}
		expected := 0
		if present {
			expected = 1
		}
		if status != expected {
			return fmt.Errorf("runtime marker present=%t: exit %d; want %d\n%s", present, status, expected, output)
		}
	}
	return nil
}

func checkOrdinaryOfflineValidation(t *testing.T, source []byte, workflowName string, packages []string) error {
	t.Helper()
	var workflow struct {
		Defaults ordinaryRunDefaults `yaml:"defaults"`
		Jobs     map[string]struct {
			Defaults ordinaryRunDefaults     `yaml:"defaults"`
			Steps    []ordinaryExecutionStep `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		return err
	}
	jobs := []string{"validate"}
	if workflowName == "go-ci.yml" {
		jobs = []string{"build-and-test", "build-and-test-windows"}
	}
	for _, jobName := range jobs {
		job := workflow.Jobs[jobName]
		steps := job.Steps
		platform := "Linux"
		if jobName == "build-and-test-windows" {
			platform = "Windows"
		}
		positions := make(map[string]int, len(steps))
		commands := make(map[string]string, len(steps))
		conditions := make(map[string]string, len(steps))
		shells := make(map[string][]string, len(steps))
		for i, step := range steps {
			positions[step.Name] = i
			commands[step.Name] = step.Run
			conditions[step.Name] = step.If
		}
		required := []string{
			"Install and prove rejecting runtime guard",
			"Verify uncached compatibility inventory",
			"Execute Tests",
			"Reject tolerated runtime launches",
			"Integration Test",
		}
		for _, name := range required {
			if _, ok := positions[name]; !ok {
				return fmt.Errorf("%s: missing %s", jobName, name)
			}
			continueOnError := steps[positions[name]].ContinueOnError
			if continueOnError.Kind != 0 &&
				(continueOnError.Tag != "!!bool" || !strings.EqualFold(continueOnError.Value, "false")) {
				return fmt.Errorf("%s: %s must not tolerate failure", jobName, name)
			}
			if name != required[1] && conditions[name] != "" {
				return fmt.Errorf("%s: %s must execute unconditionally", jobName, name)
			}
			if platform == "Windows" && name == required[1] {
				continue
			}
			shellArguments, err := ordinaryShellArguments(platform, workflow.Defaults.Run.Shell, job.Defaults.Run.Shell, steps[positions[name]].Shell)
			if err != nil {
				return fmt.Errorf("%s: %s: %w", jobName, name, err)
			}
			shells[name] = shellArguments
		}
		for i := 1; i < len(required); i++ {
			if positions[required[i-1]] >= positions[required[i]] {
				return fmt.Errorf("%s: %s must precede %s", jobName, required[i-1], required[i])
			}
		}
		if commands[required[0]] != `bash .github/scripts/go_offline_env.sh >> "$GITHUB_ENV"` {
			return fmt.Errorf("%s: rejecting runtime guard must use the proven installer", jobName)
		}
		if commands[required[1]] != "make test-compat-inventory" {
			return fmt.Errorf("%s: compatibility inventory must use the canonical Make target", jobName)
		}
		testCommand := commands[required[2]]
		if !strings.Contains(testCommand, "bash .github/scripts/ci_go_test_capture.sh ") {
			return fmt.Errorf("%s: broad Go tests must use the verified uncached capture caller", jobName)
		}
		if err := checkOrdinaryTestSelection(t, testCommand, platform, packages, workflowName == "go-ci.yml" && jobName == "build-and-test", shells[required[2]]); err != nil {
			return fmt.Errorf("%s: %w", jobName, err)
		}
		if !strings.Contains(commands[required[3]], `"$COPILOT_CLI_PATH.invoked"`) {
			return fmt.Errorf("%s: tolerated runtime launches must fail validation", jobName)
		}
		if err := checkOrdinaryRuntimeMarker(t, commands[required[3]], shells[required[3]]); err != nil {
			return fmt.Errorf("%s: %w", jobName, err)
		}
		integration := commands[required[4]]
		if !strings.Contains(integration, "bash .github/scripts/release_integration_test.sh") {
			return fmt.Errorf("%s: integration must use the production artifact gate", jobName)
		}
		if conditions[required[4]] != "" {
			return fmt.Errorf("%s: integration must execute on every job platform", jobName)
		}
		if workflowName == "go-ci.yml" {
			if conditions[required[1]] != "matrix.os == 'ubuntu-latest'" {
				return fmt.Errorf("%s: compatibility inventory should execute once without duplicating Windows", jobName)
			}
			if !strings.Contains(integration, `if [ "$RUNNER_OS" = Windows ]`) ||
				!strings.Contains(integration, "BINARY_NAME=waza.exe") {
				return fmt.Errorf("%s: Windows integration must invoke the native executable", jobName)
			}
		} else if conditions[required[1]] != "" {
			return fmt.Errorf("%s: preview compatibility inventory must execute", jobName)
		}
	}
	return nil
}

func TestOrdinaryWorkflowsUseFailClosedOfflineValidation(t *testing.T) {
	list := exec.Command("go", "list", "./...")
	list.Dir = filepath.Join("..", "..")
	output, err := list.Output()
	if err != nil {
		t.Fatalf("discover ordinary package inventory: %v", err)
	}
	packages := strings.Fields(string(output))
	if !slices.Contains(packages, "github.com/microsoft/waza/cmd/waza") ||
		!slices.Contains(packages, "github.com/microsoft/waza/internal/controlledcomparison") {
		t.Fatalf("package discovery omitted required CLI/native packages: %v", packages)
	}
	for _, name := range []string{"go-ci.yml", "squad-preview.yml"} {
		t.Run(name, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join("..", "workflows", name))
			if err != nil {
				t.Fatal(err)
			}
			if err := checkOrdinaryOfflineValidation(t, source, name, packages); err != nil {
				t.Fatal(err)
			}
			for _, step := range []string{
				"Install and prove rejecting runtime guard", "Verify uncached compatibility inventory",
				"Execute Tests", "Reject tolerated runtime launches", "Integration Test",
			} {
				anchor := "      - name: " + step + "\n"
				mutated := strings.Replace(string(source), anchor, anchor+"        shell: bash -c true {0}\n", 1)
				if mutated == string(source) {
					t.Fatalf("selected-step shell mutation did not change %s", step)
				}
				if err := checkOrdinaryOfflineValidation(t, []byte(mutated), name, packages); err == nil {
					t.Fatalf("%s shell bypass was admitted", step)
				}
			}
			for _, step := range []string{
				"Install and prove rejecting runtime guard",
				"Verify uncached compatibility inventory",
				"Execute Tests",
				"Reject tolerated runtime launches",
				"Integration Test",
			} {
				anchor := "      - name: " + step + "\n"
				if !strings.Contains(string(source), anchor) {
					t.Fatalf("required step mutation anchor missing: %s", step)
				}
				for offset := 0; ; {
					position := strings.Index(string(source)[offset:], anchor)
					if position < 0 {
						break
					}
					offset += position + len(anchor)
					for _, value := range []string{"false", "FALSE", "true", "null", "''", "\"false\"", "[]", "{}", "${{ always() }}"} { //nolint:misspell // GitHub expression.
						mutated := string(source[:offset]) + "        continue-on-error: " + value + "\n" + string(source[offset:])
						err := checkOrdinaryOfflineValidation(t, []byte(mutated), name, packages)
						if (err == nil) != strings.EqualFold(value, "false") {
							t.Fatalf("%s at offset %d: continue-on-error=%s: validation error = %v", step, offset, value, err)
						}
					}
				}
			}
			mutations := [][2]string{
				{`bash .github/scripts/go_offline_env.sh >> "$GITHUB_ENV"`, ":"},
				{"make test-compat-inventory", ":"},
				{"bash .github/scripts/ci_go_test_capture.sh", ":"},
				{"      - name: Execute Tests\n", "      - name: Execute Tests\n        if: false\n"},
				{`"$COPILOT_CLI_PATH.invoked"`, `"$COPILOT_CLI_PATH.unchecked"`},
				{"bash .github/scripts/release_integration_test.sh", "./waza run"},
			}
			if name == "go-ci.yml" {
				mutations = append(mutations,
					[2]string{"ci_go_test_capture.sh remaining", "ci_go_test_capture.sh all"},
					[2]string{"ci_go_test_capture.sh all", "ci_go_test_capture.sh remaining"},
					[2]string{"      - name: Integration Test\n        run:", "      - name: Integration Test\n        if: matrix.os == 'ubuntu-latest'\n        run:"},
					[2]string{"BINARY_NAME=waza.exe", "BINARY_NAME=waza"},
				)
			}
			mutations = append(mutations,
				[2]string{"ci_go_test_capture.sh all coverage.txt", "ci_go_test_capture.sh all unexpected.txt"},
				[2]string{`if [ -e "$COPILOT_CLI_PATH.invoked" ]; then`, "if true; then"},
			)
			message := "Preview Go tests attempted to launch the Copilot runtime."
			if name == "go-ci.yml" {
				message = "Ordinary Go tests attempted to launch the Copilot runtime."
			}
			mutations = append(mutations, [2]string{
				`echo "` + message + "\" >&2\n            exit 1",
				`echo "` + message + "\" >&2\n            :",
			})
			for _, mutation := range mutations {
				mutated := strings.Replace(string(source), mutation[0], mutation[1], 1)
				if mutated == string(source) {
					t.Fatalf("mutation did not change workflow: %s", mutation[0])
				}
				if err := checkOrdinaryOfflineValidation(t, []byte(mutated), name, packages); err == nil {
					t.Fatalf("mutation was accepted: %s", mutation[0])
				}
			}
		})
	}
}

func TestOrdinaryCICaptureRejectsWeakenedDiscoveryExecutionAndInventory(t *testing.T) {
	packages := []string{
		"github.com/microsoft/waza/cmd/waza",
		"github.com/microsoft/waza/internal/controlledcomparison",
		"github.com/microsoft/waza/internal/models",
	}
	source, err := os.ReadFile("ci_go_test_capture.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range [][2]string{
		{"-count=1 -json", "-json"},
		{"-count=1 -json", "-count=2 -json"},
		{"-count=1 -json", "-count=1"},
		{"-count=1 -json", "-count=1 -run '^$' -json"},
		{"-count=1 -json", "-count=1 -skip '.' -json"},
		{"-count=1 -json", "-count=1 -list '.' -json"},
		{"-race -covermode=atomic -json -list .", "-json -list ."},
		{"go list ./...", "go list ./internal/models"},
		{"github.com/microsoft/waza/internal/controlledcomparison", "github.com/microsoft/waza/cmd/waza"},
		{`"${selected[@]}"`, "./internal/models"},
	} {
		t.Run(mutation[0], func(t *testing.T) {
			changed := strings.Replace(string(source), mutation[0], mutation[1], 1)
			if changed == string(source) {
				t.Fatalf("capture mutation did not change source: %s", mutation[0])
			}
			command := "bash .github/scripts/ci_go_test_capture.sh remaining coverage.txt"
			if err := checkOrdinaryTestSelection(t, command, "Linux", packages, true, []string{"-e"}, []byte(changed)); err == nil {
				t.Fatalf("weakened capture mutation admitted: %s", mutation[0])
			}
		})
	}
}

func TestOrdinaryShellOverridesAndDisabledPaths(t *testing.T) {
	packages := []string{"github.com/microsoft/waza/cmd/waza", "github.com/microsoft/waza/internal/controlledcomparison"}
	for _, name := range []string{"go-ci.yml", "squad-preview.yml"} {
		t.Run(name, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join("..", "workflows", name))
			if err != nil {
				t.Fatal(err)
			}
			source = normalizeWorkflowLineEndings(source)
			rootUnsafe := strings.Replace(string(source), "permissions:\n",
				"defaults:\n  run:\n    shell: bash -c true {0}\n\npermissions:\n", 1)
			if rootUnsafe == string(source) {
				t.Fatal("workflow-default mutation did not change actual source")
			}
			err = checkOrdinaryOfflineValidation(t, []byte(rootUnsafe), name, packages)
			if (err == nil) != (name == "go-ci.yml") {
				t.Fatalf("explicit Go job Bash must override root shell; implicit preview must not: %v", err)
			}
			var document map[string]yaml.Node
			if err := yaml.Unmarshal(source, &document); err != nil {
				t.Fatal(err)
			}
			var jobs map[string]yaml.Node
			jobsNode := document["jobs"]
			if err := jobsNode.Decode(&jobs); err != nil {
				t.Fatal(err)
			}
			jobName := "validate"
			if name == "go-ci.yml" {
				jobName = "build-and-test-windows"
			}
			var job map[string]yaml.Node
			jobNode := jobs[jobName]
			if err := jobNode.Decode(&job); err != nil {
				t.Fatal(err)
			}
			var steps []yaml.Node
			stepNode := job["steps"]
			if err := stepNode.Decode(&steps); err != nil {
				t.Fatal(err)
			}
			selected := 0
			for i := range steps {
				var step ordinaryValidationStep
				if err := steps[i].Decode(&step); err != nil {
					t.Fatal(err)
				}
				if step.Name != "Verify uncached compatibility inventory" {
					continue
				}
				selected++
				steps[i].Content = append(steps[i].Content,
					&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "shell"},
					&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "pwsh"})
			}
			if selected != 1 {
				t.Fatalf("actual inventory-step count=%d; want one", selected)
			}
			sequence := yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
			for i := range steps {
				sequence.Content = append(sequence.Content, &steps[i])
			}
			job["steps"] = sequence
			if err := jobNode.Encode(job); err != nil {
				t.Fatal(err)
			}
			jobs[jobName] = jobNode
			jobsNode = document["jobs"]
			if err := jobsNode.Encode(jobs); err != nil {
				t.Fatal(err)
			}
			document["jobs"] = jobsNode
			mutated, err := yaml.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if string(mutated) == string(source) {
				t.Fatal("inventory shell control did not change actual source")
			}
			err = checkOrdinaryOfflineValidation(t, mutated, name, packages)
			if (err == nil) != (name == "go-ci.yml") {
				t.Fatalf("disabled Windows inventory must stay outside Bash policy; selected preview inventory must reject: %v", err)
			}
		})
	}
}

func readTriggers(source []byte) (workflowTriggers, error) {
	var workflow struct {
		On workflowTriggers `yaml:"on"`
	}
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		return nil, err
	}
	return workflow.On, nil
}

func checkTriggers(source []byte, want workflowTriggers) error {
	got, err := readTriggers(source)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("workflow triggers = %#v; want %#v", got, want)
	}
	return nil
}

// Evaluate only the literal expressions admitted by checkCanonicalGoWorkflow,
// rather than approximating arbitrary GitHub Actions expression semantics.
func goConcurrencyGroup(group, workflow, event, ref, runID string) string {
	scope := runID
	if event == "pull_request" {
		scope = ref
	}
	return strings.NewReplacer(
		"${{ github.workflow }}", workflow,
		"${{ github.event_name }}", event,
		"${{ github.event_name == 'pull_request' && github.ref || github.run_id }}", scope,
	).Replace(group)
}

func TestGoWorkflowConcurrency(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "workflows", "go-ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := checkCanonicalGoWorkflow(source); err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Name        string `yaml:"name"`
		Concurrency struct {
			Group  string `yaml:"group"`
			Cancel string `yaml:"cancel-in-progress"`
		} `yaml:"concurrency"`
	}
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		t.Fatal(err)
	}
	if workflow.Name != "Go Build and Test" {
		t.Fatalf("unexpected workflow identity %q", workflow.Name)
	}
	runs := []struct {
		name, workflow, event, ref, runID, wantGroup string
	}{
		{"PR old commit", workflow.Name, "pull_request", "refs/pull/829/merge", "100", "Go Build and Test-pull_request-refs/pull/829/merge"},
		{"PR new commit", workflow.Name, "pull_request", "refs/pull/829/merge", "101", "Go Build and Test-pull_request-refs/pull/829/merge"},
		{"different PR", workflow.Name, "pull_request", "refs/pull/830/merge", "102", "Go Build and Test-pull_request-refs/pull/830/merge"},
		{"other PR workflow", "Web CI", "pull_request", "refs/pull/829/merge", "103", "Web CI-pull_request-refs/pull/829/merge"},
		{"push old run", workflow.Name, "push", "refs/heads/main", "104", "Go Build and Test-push-104"},
		{"push same commit again", workflow.Name, "push", "refs/heads/main", "105", "Go Build and Test-push-105"},
		{"push same ref as PR", workflow.Name, "push", "refs/pull/829/merge", "106", "Go Build and Test-push-106"},
		{"release publication", "Release", "push", "refs/heads/main", "107", "Release-push-107"},
		{"manual publication", "Publish Runner Reference Image", "workflow_dispatch", "refs/heads/main", "108", "Publish Runner Reference Image-workflow_dispatch-108"},
	}
	for i, older := range runs {
		t.Run(older.name, func(t *testing.T) {
			group := goConcurrencyGroup(workflow.Concurrency.Group, older.workflow, older.event, older.ref, older.runID)
			if group != older.wantGroup {
				t.Fatalf("group = %q; want %q", group, older.wantGroup)
			}
			for j, newer := range runs {
				if i == j {
					continue
				}
				incomingGroup := goConcurrencyGroup(workflow.Concurrency.Group, newer.workflow, newer.event, newer.ref, newer.runID)
				cancel := newer.event == "pull_request"
				wantCancel := (i == 0 && j == 1) || (i == 1 && j == 0)
				if got := cancel && group == incomingGroup; got != wantCancel {
					t.Errorf("%s supersedes %s = %v; want %v", newer.name, older.name, got, wantCancel)
				}
			}
		})
	}
}

// Selection is deliberately limited to the exact, literal branch/type filters
// admitted by checkTriggers, not a general GitHub Actions glob interpreter.
func selects(triggers workflowTriggers, event, branch, action string) bool {
	trigger, ok := triggers[event]
	if !ok {
		return false
	}
	if branches, ok := trigger.Filters["branches"]; ok && !slices.Contains(branches, branch) {
		return false
	}
	if event == "pull_request" {
		types, ok := trigger.Filters["types"]
		if !ok {
			types = []string{"opened", "synchronize", "reopened"}
		}
		return slices.Contains(types, action)
	}
	return event == "push" || event == "workflow_dispatch"
}

func checkCanonicalGoWorkflow(source []byte) error {
	var workflow struct {
		Permissions map[string]string `yaml:"permissions"`
		Concurrency struct {
			Group  string `yaml:"group"`
			Cancel string `yaml:"cancel-in-progress"`
		} `yaml:"concurrency"`
		Jobs map[string]struct {
			Name  string   `yaml:"name"`
			Uses  string   `yaml:"uses"`
			Needs []string `yaml:"needs"`
			If    string   `yaml:"if"`
			Steps []struct {
				Run string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		return err
	}
	if workflow.Concurrency.Group != "${{ github.workflow }}-${{ github.event_name }}-${{ github.event_name == 'pull_request' && github.ref || github.run_id }}" ||
		workflow.Concurrency.Cancel != "${{ github.event_name == 'pull_request' }}" {
		return fmt.Errorf("cancel only superseded PR runs, scoped to workflow/event/ref; non-PR runs require independent groups")
	}
	if !reflect.DeepEqual(workflow.Permissions, map[string]string{"contents": "read"}) {
		return fmt.Errorf("validation must remain read-only")
	}
	if workflow.Jobs["lint"].Name != "Lint" ||
		workflow.Jobs["build-and-test-windows"].Name != "${{ matrix.os }}" ||
		workflow.Jobs["native-test-shards"].Uses != "./.github/workflows/go-native-shards.yml" {
		return fmt.Errorf("required native and lint jobs must be retained")
	}
	gate := workflow.Jobs["linux-gate"]
	if gate.Name != "ubuntu-latest" || gate.If != "${{ always() }}" ||
		!reflect.DeepEqual(gate.Needs, []string{"build-and-test", "native-test-shards"}) {
		return fmt.Errorf("required Linux gate must inspect both validation branches even after failure")
	}
	var commands string
	for _, step := range gate.Steps {
		commands += step.Run
	}
	for _, required := range []string{
		`test "$BUILD_RESULT" = success`,
		`test "$NATIVE_RESULT" = success`,
		"bash .github/scripts/go_test_shard_verify.sh coverage-reports",
	} {
		if !strings.Contains(commands, required) {
			return fmt.Errorf("Linux gate missing %s", required)
		}
	}
	return nil
}

func TestCanonicalGoWorkflow(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "workflows", "go-ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := checkCanonicalGoWorkflow(source); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range [][2]string{
		{"${{ github.workflow }}-${{ github.event_name }}-${{ github.event_name == 'pull_request' && github.ref || github.run_id }}", "${{ github.ref }}"},
		{"${{ github.workflow }}-", ""},
		{"${{ github.event_name }}-", ""},
		{"${{ github.workflow }}-${{ github.event_name }}-${{ github.event_name == 'pull_request' && github.ref || github.run_id }}", "go-ci-${{ github.event_name == 'pull_request' && github.event.pull_request.number || github.run_id }}"},
		{"${{ github.event_name == 'pull_request' && github.ref || github.run_id }}", "${{ github.ref }}"},
		{"${{ github.event_name == 'pull_request' && github.ref || github.run_id }}", "${{ github.run_id }}"},
		{"${{ github.event_name == 'pull_request' && github.ref || github.run_id }}", "${{ github.event_name == 'pull_request' && github.ref || github.sha }}"},
		{"cancel-in-progress: ${{ github.event_name == 'pull_request' }}", "cancel-in-progress: true"},
		{"cancel-in-progress: ${{ github.event_name == 'pull_request' }}", "cancel-in-progress: false"},
		{"name: Lint", "name: Lint renamed"},
		{"name: ubuntu-latest", "name: Linux renamed"},
		{"needs: [build-and-test, native-test-shards]", "needs: [build-and-test]"},
		{"if: ${{ always() }}", "if: ${{ success() }}"},
		{`test "$NATIVE_RESULT" = success`, ":"},
		{"bash .github/scripts/go_test_shard_verify.sh coverage-reports", ":"},
	} {
		mutated := strings.Replace(string(source), mutation[0], mutation[1], 1)
		if mutated == string(source) || checkCanonicalGoWorkflow([]byte(mutated)) == nil {
			t.Fatalf("mutation not rejected: %s", mutation[0])
		}
	}
	files, err := filepath.Glob(filepath.Join("..", "workflows", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var validators []string
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var workflow struct {
			Jobs map[string]yaml.Node `yaml:"jobs"`
		}
		if err := yaml.Unmarshal(raw, &workflow); err != nil {
			t.Fatal(err)
		}
		if _, ok := workflow.Jobs["build-and-test-windows"]; ok {
			validators = append(validators, filepath.Base(file))
		}
	}
	if !reflect.DeepEqual(validators, []string{"go-ci.yml"}) {
		t.Fatalf("duplicate native validation workflows: %v", validators)
	}
	if _, err := os.Stat(filepath.Join("..", "workflows", "squad-ci.yml")); !os.IsNotExist(err) {
		t.Fatalf("obsolete Squad CI workflow must be removed: %v", err)
	}
}

func TestGoCIRequiresNativeWindowsReleaseDashboardChecker(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "workflows", "go-ci.yml"))
	if err != nil {
		t.Fatal(err)
	}

	const stepName = "Test native Windows release dashboard checker"
	const command = `log="$(mktemp)"
trap 'rm -f "$log"' EXIT
go test -v -race -count=1 -timeout=5m ./.github/scripts/release-dashboard-check | sed 's/\r$//' | tee "$log"
if grep -q -- '--- SKIP:' "$log"; then
  echo "Native Windows release dashboard tests must not skip." >&2
  exit 1
fi
for control in TestReadFileOpensOrdinaryWindowsFile TestReadFileRejectsWindowsLeafReparseReplacement TestReadFileRetainsOpenedWindowsParentDuringReplacement; do
  grep -Fxq "=== RUN   $control" "$log"
  grep -Eq "^--- PASS: $control \([0-9.]+s\)$" "$log"
done
`

	check := func(input []byte) error {
		var workflow struct {
			Defaults ordinaryRunDefaults `yaml:"defaults"`
			Jobs     map[string]struct {
				If       string                  `yaml:"if"`
				RunsOn   string                  `yaml:"runs-on"`
				Defaults ordinaryRunDefaults     `yaml:"defaults"`
				Steps    []ordinaryExecutionStep `yaml:"steps"`
				Strategy struct {
					Matrix struct {
						OS []string `yaml:"os"`
					} `yaml:"matrix"`
				} `yaml:"strategy"`
			} `yaml:"jobs"`
		}
		if err := yaml.Unmarshal(input, &workflow); err != nil {
			return err
		}
		job := workflow.Jobs["build-and-test-windows"]
		if job.If != "" || job.RunsOn != "windows-latest" ||
			!reflect.DeepEqual(job.Strategy.Matrix.OS, []string{"windows-latest"}) {
			return fmt.Errorf("dashboard checker requires the unconditional native Windows job")
		}

		positions := make(map[string]int, len(job.Steps))
		selected := 0
		for i, step := range job.Steps {
			positions[step.Name] = i
			if step.Name != stepName {
				continue
			}
			selected++
			if step.If != "matrix.os == 'windows-latest'" || step.Run != command {
				return fmt.Errorf("dashboard checker must run the uncached native Windows tests and reject skips")
			}
			if step.ContinueOnError.Kind != 0 &&
				(step.ContinueOnError.Tag != "!!bool" || step.ContinueOnError.Value != "false") {
				return fmt.Errorf("dashboard checker must propagate failures")
			}
			arguments, err := ordinaryShellArguments("Windows", workflow.Defaults.Run.Shell, job.Defaults.Run.Shell, step.Shell)
			if err != nil {
				return err
			}
			if len(arguments) < 2 || arguments[len(arguments)-2] != "-o" || arguments[len(arguments)-1] != "pipefail" {
				return fmt.Errorf("dashboard checker output pipeline requires Bash pipefail")
			}
		}
		if selected != 1 {
			return fmt.Errorf("native Windows dashboard checker step count=%d; require one", selected)
		}
		for _, prerequisite := range []string{
			"Install and prove rejecting runtime guard", "Execute Tests", "Test native Windows shell controls",
		} {
			position, present := positions[prerequisite]
			if !present || position >= positions[stepName] {
				return fmt.Errorf("dashboard checker must follow %s", prerequisite)
			}
		}
		marker, present := positions["Reject tolerated runtime launches"]
		if !present || marker <= positions[stepName] {
			return fmt.Errorf("runtime marker must be checked after dashboard checker tests")
		}
		return nil
	}
	if err := check(source); err != nil {
		t.Fatal(err)
	}

	for _, mutation := range []struct{ old, replacement string }{
		{
			"      - name: Test native Windows release dashboard checker\n",
			"      - name: Missing native Windows release dashboard checker\n",
		},
		{
			"      - name: Test native Windows release dashboard checker\n        if: matrix.os == 'windows-latest'",
			"      - name: Test native Windows release dashboard checker\n        if: false",
		},
		{
			"      - name: Test native Windows release dashboard checker\n        if: matrix.os == 'windows-latest'\n",
			"      - name: Test native Windows release dashboard checker\n        if: matrix.os == 'windows-latest'\n        continue-on-error: true\n",
		},
		{
			"          go test -v -race -count=1 -timeout=5m ./.github/scripts/release-dashboard-check",
			"          go test -v -race -count=1 -timeout=5m -run '^$' ./.github/scripts/release-dashboard-check",
		},
		{
			"          if grep -q -- '--- SKIP:' \"$log\"; then\n            echo \"Native Windows release dashboard tests must not skip.\" >&2",
			"          if false; then\n            echo \"Native Windows release dashboard tests must not skip.\" >&2",
		},
		{
			"          for control in TestReadFileOpensOrdinaryWindowsFile TestReadFileRejectsWindowsLeafReparseReplacement TestReadFileRetainsOpenedWindowsParentDuringReplacement; do\n            grep -Fxq",
			"          for control in TestReadFileOpensOrdinaryWindowsFile; do\n            grep -Fxq",
		},
	} {
		mutated := strings.Replace(string(source), mutation.old, mutation.replacement, 1)
		if mutated == string(source) || check([]byte(mutated)) == nil {
			t.Fatalf("dashboard checker workflow mutation admitted: %q", mutation.replacement)
		}
	}
}

func TestGoWorkflowTriggers(t *testing.T) {
	for _, tc := range []struct {
		file         string
		pushBranches []string
		prFilters    map[string][]string
	}{
		{"go-ci.yml", []string{"main", "develop", "dev", "insider"}, nil},
	} {
		t.Run(tc.file, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join("..", "workflows", tc.file))
			if err != nil {
				t.Fatal(err)
			}
			want := workflowTriggers{
				"push":         {Mapping: true, Filters: map[string][]string{"branches": tc.pushBranches}},
				"pull_request": {Filters: tc.prFilters},
				"workflow_dispatch": {Mapping: true, Inputs: map[string]workflowInput{
					"cold_observation": {
						Type: "boolean", Description: "Observe initially empty isolated Linux dependency stores",
						Default:  workflowLiteral{Present: true, Tag: "!!bool", Value: "false"},
						Required: workflowLiteral{Present: true, Tag: "!!bool", Value: "false"},
					},
				}},
			}
			if err := checkTriggers(source, want); err != nil {
				t.Fatal(err)
			}
			triggers, err := readTriggers(source)
			if err != nil {
				t.Fatal(err)
			}
			for _, branch := range []string{
				"main", "develop", "dev", "preview", "insider",
				"feature/stack-base", "squad/782-release", "spboyer-release-layer",
				"release/0.40.0", "main-near-miss",
			} {
				for _, action := range []string{"opened", "synchronize", "reopened", "closed", "edited", "labeled", "ready_for_review"} {
					want := slices.Contains([]string{"opened", "synchronize", "reopened"}, action)
					if got := selects(triggers, "pull_request", branch, action); got != want {
						t.Errorf("pull_request base=%q action=%q selected=%v; want %v", branch, action, got, want)
					}
				}
				if got, want := selects(triggers, "push", branch, ""), slices.Contains(tc.pushBranches, branch); got != want {
					t.Errorf("push branch=%q selected=%v; want %v", branch, got, want)
				}
			}
			if !selects(triggers, "workflow_dispatch", "feature/frozen-source", "") {
				t.Fatal("declared feature-only opt-in dispatch missing; default-branch activation is a separate prerequisite")
			}
			for _, event := range []string{"pull_request_target", "release"} {
				if selects(triggers, event, "main", "opened") {
					t.Errorf("unexpected event selected: %s", event)
				}
			}
			for name, replacement := range map[string]string{
				"base allowlist": "  pull_request:\n    branches: [main]",
				"base exclusion": "  pull_request:\n    branches-ignore: ['feature/**']",
				"path filter":    "  pull_request:\n    paths: ['**.go']",
				"path exclusion": "  pull_request:\n    paths-ignore: ['docs/**']",
				"event widening": "  pull_request:\n    types: [opened, synchronize, reopened, closed]",
				"trust boundary": "  pull_request_target:",
			} {
				t.Run("reject "+name, func(t *testing.T) {
					mutated := strings.Replace(string(source), "  pull_request:", replacement, 1)
					if mutated == string(source) {
						t.Fatal("mutation did not change workflow source")
					}
					if name == "event widening" {
						mutated = strings.Replace(mutated, "    types: [opened, synchronize, reopened]\n", "", 1)
					}
					if _, err := readTriggers([]byte(mutated)); err != nil {
						t.Fatalf("mutation must remain valid YAML: %v", err)
					}
					if err := checkTriggers([]byte(mutated), want); err == nil {
						t.Fatal("trigger regression was accepted")
					}
				})
			}
			t.Run("reject broadened pushes", func(t *testing.T) {
				lines := strings.Split(string(source), "\n")
				for i, line := range lines {
					if strings.HasPrefix(line, "    branches:") {
						lines[i] = "    branches: ['**']"
						break
					}
				}
				mutated := strings.Join(lines, "\n")
				if mutated == string(source) {
					t.Fatal("mutation did not change push branches")
				}
				if err := checkTriggers([]byte(mutated), want); err == nil {
					t.Fatal("push trigger regression was accepted")
				}
			})
		})
	}
}
