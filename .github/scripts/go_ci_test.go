package scripts

import (
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

func checkOrdinaryTestSelection(t *testing.T, command, platform string, packages []string, sharded bool) error {
	t.Helper()
	scratch := t.TempDir()
	inventory := filepath.Join(scratch, "packages")
	arguments := filepath.Join(scratch, "arguments")
	if err := os.WriteFile(inventory, []byte(strings.Join(packages, "\n")+"\n"), 0o600); err != nil {
		return err
	}
	// Execute the workflow shell, but record Go invocations instead of rerunning tests.
	probe := `#!/usr/bin/env bash
set -euo pipefail
case "$1" in
  list)
    test "$#" -eq 2
    test "$2" = ./...
    cat "$WAZA_PACKAGE_INVENTORY"
    ;;
  test)
    test ! -e "$WAZA_TEST_ARGUMENTS"
    shift
    printf '%s\n' "$@" > "$WAZA_TEST_ARGUMENTS"
    ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(scratch, "go"), []byte(probe), 0o700); err != nil {
		return err
	}
	cmd := exec.Command("bash", "-e", "-o", "pipefail", "-c", command)
	cmd.Env = append(os.Environ(),
		"PATH="+scratch+string(os.PathListSeparator)+os.Getenv("PATH"),
		"RUNNER_OS="+platform,
		"WAZA_PACKAGE_INVENTORY="+inventory,
		"WAZA_TEST_ARGUMENTS="+arguments,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: executing workflow test-selection probe: %w\n%s", platform, err, output)
	}
	captured, err := os.ReadFile(arguments)
	if err != nil {
		return fmt.Errorf("%s: workflow must execute Go tests: %w", platform, err)
	}
	args := strings.Split(strings.TrimSuffix(string(captured), "\n"), "\n")
	requiredFlags := []string{"-count=1", "-v", "-race", "-timeout=30m", "-coverprofile=coverage.txt", "-covermode=atomic"}
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
	return nil
}

func checkOrdinaryRuntimeMarker(t *testing.T, command string) error {
	t.Helper()
	guard := filepath.Join(t.TempDir(), "offline runtime")
	for _, present := range []bool{false, true} {
		if present {
			if err := os.WriteFile(guard+".invoked", nil, 0o600); err != nil {
				return err
			}
		}
		cmd := exec.Command("bash", "-e", "-o", "pipefail", "-c", command)
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
		Jobs map[string]struct {
			Steps []ordinaryValidationStep `yaml:"steps"`
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
		steps := workflow.Jobs[jobName].Steps
		positions := make(map[string]int, len(steps))
		commands := make(map[string]string, len(steps))
		conditions := make(map[string]string, len(steps))
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
		if strings.Count(testCommand, "go test ") == 0 ||
			strings.Count(testCommand, "go test -count=1 ") != strings.Count(testCommand, "go test ") {
			return fmt.Errorf("%s: broad Go tests must be uncached", jobName)
		}
		platform := "Linux"
		if jobName == "build-and-test-windows" {
			platform = "Windows"
		}
		if err := checkOrdinaryTestSelection(t, testCommand, platform, packages, workflowName == "go-ci.yml" && jobName == "build-and-test"); err != nil {
			return fmt.Errorf("%s: %w", jobName, err)
		}
		if !strings.Contains(commands[required[3]], `"$COPILOT_CLI_PATH.invoked"`) {
			return fmt.Errorf("%s: tolerated runtime launches must fail validation", jobName)
		}
		if err := checkOrdinaryRuntimeMarker(t, commands[required[3]]); err != nil {
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
				{"go test -count=1 ", "go test "},
				{"go test -count=1 ", "go test -count=1 -run '^$' "},
				{"go test -count=1 ", "go test -count=1 -skip '.' "},
				{"go test -count=1 ", "go test -count=1 -list '.' "},
				{"      - name: Execute Tests\n        run:", "      - name: Execute Tests\n        if: false\n        run:"},
				{`"$COPILOT_CLI_PATH.invoked"`, `"$COPILOT_CLI_PATH.unchecked"`},
				{"bash .github/scripts/release_integration_test.sh", "./waza run"},
			}
			if name == "go-ci.yml" {
				mutations = append(mutations,
					[2]string{`"${selected[@]}"`, "./internal/models"},
					[2]string{"go list ./...", "go list ./internal/models"},
					[2]string{"github.com/microsoft/waza/internal/controlledcomparison", "github.com/microsoft/waza/cmd/waza"},
					[2]string{"      - name: Integration Test\n        run:", "      - name: Integration Test\n        if: matrix.os == 'ubuntu-latest'\n        run:"},
					[2]string{"BINARY_NAME=waza.exe", "BINARY_NAME=waza"},
				)
			}
			mutations = append(mutations,
				[2]string{"-covermode=atomic ./...", "-covermode=atomic ./internal/models"},
				[2]string{"-covermode=atomic ./...", "-covermode=atomic ./cmd/waza"},
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
			for _, event := range []string{"pull_request_target", "workflow_dispatch", "release"} {
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
