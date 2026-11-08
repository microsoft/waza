package scripts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func runCompletenessNode(t *testing.T, args ...string) []byte {
	t.Helper()
	run := func(args ...string) []byte {
		t.Helper()
		var stderr bytes.Buffer
		command := exec.CommandContext(t.Context(), "node", args...)
		command.Stderr = &stderr
		output, err := command.Output()
		if err != nil {
			t.Fatalf("completeness controls require working Node 22.18+ or 24+: %v\nstdout:\n%s\nstderr:\n%s", err, output, stderr.Bytes())
		}
		if stderr.Len() > 0 {
			t.Logf("completeness Node stderr: %s", stderr.Bytes())
		}
		return output
	}
	version := run("--version")
	if err := checkCompletenessNodeVersion(string(version)); err != nil {
		t.Fatal(err)
	}
	return run(args...)
}

func checkCompletenessNodeVersion(version string) error {
	matches := regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`).FindStringSubmatch(strings.TrimSpace(version))
	if len(matches) != 4 {
		return fmt.Errorf("invalid Node release %q: completeness controls require Node 22.18+ or 24+", version)
	}
	var parts [3]int
	for i, value := range matches[1:] {
		part, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("parse Node release %q: %w", version, err)
		}
		parts[i] = part
	}
	if (parts[0] == 22 && parts[1] >= 18) || parts[0] >= 24 {
		return nil
	}
	return fmt.Errorf("unsupported Node release %q: completeness controls require Node 22.18+ or 24+", strings.TrimSpace(version))
}

func TestCompletenessNodeRuntimeAdmission(t *testing.T) {
	for _, version := range []string{"v22.18.0", "v22.22.1\n", "v24.0.0", "v24.15.0", "v25.0.0"} {
		if err := checkCompletenessNodeVersion(version); err != nil {
			t.Errorf("supported Node release %q: %v", version, err)
		}
	}
	for _, version := range []string{"", "v20.19.0", "v22.17.9", "v23.0.0", "22.18.0",
		"v22.18", "v022.18.0", "v24.0.0-next", "v24.0.0\nunexpected",
		"v999999999999999999999999.0.0", "v24.999999999999999999999999.0",
		"v24.0.999999999999999999999999"} {
		if err := checkCompletenessNodeVersion(version); err == nil {
			t.Errorf("invalid or unsupported Node release %q was admitted", version)
		}
	}
}

func TestCompletenessNodeWarningsRetainStructuredOutput(t *testing.T) {
	output := runCompletenessNode(t, "-e", `
process.emitWarning("type-stripping control", {type: "ExperimentalWarning"});
process.stdout.write(JSON.stringify(require("./pr-completeness.cjs").applicability(["AGENTS.md"])));
`)
	var selected map[string]bool
	if err := json.Unmarshal(output, &selected); err != nil {
		t.Fatalf("decode selector stdout despite runtime warning: %v\n%s", err, output)
	}
	if !reflect.DeepEqual(selected, map[string]bool{
		"go": true, "dashboard": false, "site": false, "examples": false,
		"assurance": false, "container": false, "tools": false, "runtime": false,
	}) {
		t.Fatalf("unexpected selector output: %v", selected)
	}
}

func TestCompletenessProductionCLIControls(t *testing.T) {
	output := runCompletenessNode(t, "--test", "--test-reporter=tap", "pr-completeness.test.cjs")
	for metric, count := range map[string]int{"tests": 16, "pass": 16, "fail": 0, "cancelled": 0, "skipped": 0} { //nolint:misspell // Node TAP field name.
		pattern := regexp.MustCompile(fmt.Sprintf(`(?m)^# %s %d\r?$`, metric, count))
		if !pattern.Match(output) {
			t.Fatalf("production Node controls lack expected %s=%d evidence:\n%s", metric, count, output)
		}
	}
}

func TestCompletenessControlsSelectedByGoLint(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "workflows", "go-ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	check := func(source []byte) error {
		var workflow struct {
			Env  map[string]yaml.Node `yaml:"env"`
			Jobs map[string]struct {
				If     string               `yaml:"if"`
				RunsOn string               `yaml:"runs-on"`
				Env    map[string]yaml.Node `yaml:"env"`
				Steps  []struct {
					ordinaryValidationStep `yaml:",inline"`
					Env                    map[string]yaml.Node `yaml:"env"`
				} `yaml:"steps"`
			} `yaml:"jobs"`
		}
		if err := yaml.Unmarshal(source, &workflow); err != nil {
			return err
		}
		job := workflow.Jobs["lint"]
		if job.If != "" || job.RunsOn != "ubuntu-latest" {
			return fmt.Errorf("Linux GoLint must select completeness controls unconditionally")
		}
		for _, step := range job.Steps {
			if step.Name != "Test Go workflow trigger selection" {
				continue
			}
			if step.If != "" || (step.ContinueOnError.Kind != 0 &&
				(step.ContinueOnError.Tag != "!!bool" || step.ContinueOnError.Value != "false")) {
				return fmt.Errorf("completeness control step must not skip or tolerate failure")
			}
			var flags yaml.Node
			flagsPresent := false
			for _, env := range []map[string]yaml.Node{workflow.Env, job.Env, step.Env} {
				if value, present := env["GOFLAGS"]; present {
					flags, flagsPresent = value, true
				}
			}
			if flagsPresent && (flags.Kind != yaml.ScalarNode || flags.Tag != "!!str" || flags.Value != "") {
				return fmt.Errorf("GoLint GOFLAGS must be omitted or literal empty after workflow/job/step overrides")
			}
			args := strings.Fields(step.Run)
			if len(args) < 4 || args[0] != "go" || args[1] != "test" || args[2] != "-count=1" {
				return fmt.Errorf("GoLint must execute uncached Go controls")
			}
			selected := false
			for _, argument := range args[3:] {
				if !regexp.MustCompile(`^\./[A-Za-z0-9_./-]+$`).MatchString(argument) {
					return fmt.Errorf("GoLint must not narrow or replace control execution")
				}
				selected = selected || argument == "./.github/scripts"
			}
			if !selected {
				return fmt.Errorf("GoLint omits the completeness production bridge package")
			}
			return nil
		}
		return fmt.Errorf("GoLint completeness control step is missing")
	}
	if err := check(source); err != nil {
		t.Fatal(err)
	}
	t.Run("explicit-false-valid", func(t *testing.T) {
		mutated := strings.Replace(string(source), "run: go test -count=1", "continue-on-error: false\n        run: go test -count=1", 1)
		if mutated == string(source) {
			t.Fatal("explicit-false control did not change the actual step")
		}
		if err := check([]byte(mutated)); err != nil {
			t.Fatalf("explicit false must remain valid: %v", err)
		}
	})
	for _, value := range []string{"true", `"${{ inputs.allow_failure }}"`} {
		mutated := strings.Replace(string(source), "run: go test -count=1", "continue-on-error: "+value+"\n        run: go test -count=1", 1)
		if mutated == string(source) || check([]byte(mutated)) == nil {
			t.Fatal("actual step must reject true or dynamic continue-on-error")
		}
	}
	setFlags := func(source []byte, scope, value string) []byte {
		t.Helper()
		anchors := map[string]string{
			"workflow": "permissions:\n",
			"job":      "  lint:\n",
			"step":     "      - name: Test Go workflow trigger selection\n",
		}
		replacements := map[string]string{
			"workflow": "env:\n  GOFLAGS: " + value + "\n\npermissions:\n",
			"job":      "  lint:\n    env:\n      GOFLAGS: " + value + "\n",
			"step":     "      - name: Test Go workflow trigger selection\n        env:\n          GOFLAGS: " + value + "\n",
		}
		mutated := strings.Replace(string(source), anchors[scope], replacements[scope], 1)
		if mutated == string(source) {
			t.Fatalf("%s GOFLAGS control did not change the actual workflow", scope)
		}
		return []byte(mutated)
	}
	for _, scope := range []string{"workflow", "job", "step"} {
		t.Run(scope+"-literal-empty-valid", func(t *testing.T) {
			if err := check(setFlags(source, scope, `""`)); err != nil {
				t.Fatalf("literal empty GOFLAGS must remain valid: %v", err)
			}
		})
		for i, value := range []string{`"-run=TestWorkflowTriggerTypedInputs"`, `"-v"`,
			`"${{ inputs.go_flags }}"`, `" "`, "null", "false", "1"} {
			t.Run(fmt.Sprintf("%s-unsafe-flags-%d", scope, i), func(t *testing.T) {
				if err := check(setFlags(source, scope, value)); err == nil {
					t.Fatal("nonempty or unprovable effective GOFLAGS must reject control selection")
				}
			})
		}
	}
	for _, override := range []struct{ lower, higher string }{
		{"workflow", "job"}, {"workflow", "step"}, {"job", "step"},
	} {
		t.Run(override.lower+"-overridden-by-"+override.higher, func(t *testing.T) {
			for _, value := range []string{`"-run=TestWorkflowTriggerTypedInputs"`, `"${{ inputs.go_flags }}"`} {
				lower := setFlags(source, override.lower, value)
				if err := check(setFlags(lower, override.higher, `""`)); err != nil {
					t.Fatalf("higher literal empty GOFLAGS must override lower unsafe value: %v", err)
				}
			}
		})
	}
	t.Run("step-unsafe-overrides-empty-job", func(t *testing.T) {
		lower := setFlags(setFlags(source, "workflow", `"-run=TestWorkflowTriggerTypedInputs"`), "job", `""`)
		if err := check(setFlags(lower, "step", `"-run=TestWorkflowTriggerTypedInputs"`)); err == nil {
			t.Fatal("higher unsafe GOFLAGS must not inherit lower empty admission")
		}
	})
	withHelper := strings.Replace(string(source), "run: go test -count=1", "run: go test -count=1 ./.github/scripts/release-assets-helper", 1)
	if withHelper == string(source) {
		t.Fatal("alternative-valid helper package did not change source")
	}
	if err := check([]byte(withHelper)); err != nil {
		t.Fatalf("literal future helper package must remain valid: %v", err)
	}
	for _, mutation := range []struct{ old, replacement string }{
		{" ./.github/scripts ", " "},
		{"run: go test -count=1", "run: echo go test -count=1"},
		{" ./.github/scripts/verify-runner-reference-guard", " ./.github/scripts/verify-runner-reference-guard;true"},
		{" ./.github/scripts/verify-runner-reference-guard", " ./.github/scripts/verify-runner-reference-guard||true"},
		{"run: go test -count=1", "if: false\n        run: go test -count=1"},
		{"run: go test -count=1", "run: go test -count=1 -run TestWorkflowTriggerTypedInputs"},
	} {
		mutated := strings.Replace(string(source), mutation.old, mutation.replacement, 1)
		if mutated == string(source) || check([]byte(mutated)) == nil {
			t.Fatal("actual GoLint source mutation did not reject omitted, replaced or narrowed execution")
		}
	}
}

func TestCompletenessRuntimeImportClosure(t *testing.T) {
	output := runCompletenessNode(t, "-e", "process.stdout.write(JSON.stringify(require('./pr-completeness.cjs').runtimeInputs))")
	var selectors []string
	if err := json.Unmarshal(output, &selectors); err != nil {
		t.Fatalf("decode runtime selectors: %v", err)
	}
	if len(selectors) == 0 {
		t.Fatal("runtime selector inventory is empty")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, platform := range []struct{ goos, goarch string }{{"linux", "amd64"}, {"darwin", "arm64"}} {
		t.Run(platform.goos+"-"+platform.goarch, func(t *testing.T) {
			context := build.Default
			context.GOOS, context.GOARCH = platform.goos, platform.goarch
			context.BuildTags = []string{"race"}
			context.CgoEnabled = true
			queue := []string{"internal/execution", "internal/graders", "internal/judgeevidence"}
			visited := map[string]bool{}
			for len(queue) > 0 {
				directory := queue[0]
				queue = queue[1:]
				if visited[directory] {
					continue
				}
				visited[directory] = true
				covered := false
				for _, selector := range selectors {
					if directory == selector || strings.HasPrefix(directory, selector+"/") {
						covered = true
					}
				}
				if !covered {
					t.Errorf("pinned execution test-binary dependency %s is not a runtime input", directory)
				}
				pkg, err := context.ImportDir(filepath.Join(root, filepath.FromSlash(directory)), 0)
				if err != nil {
					t.Fatalf("read selected dependency %s: %v", directory, err)
				}
				files := append(append([]string{}, pkg.GoFiles...), pkg.CgoFiles...)
				if directory == "internal/execution" {
					files = append(files, pkg.TestGoFiles...)
					files = append(files, pkg.XTestGoFiles...)
				}
				for _, name := range files {
					file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, filepath.FromSlash(directory), name), nil, parser.ImportsOnly)
					if err != nil {
						t.Fatalf("parse dependency imports %s/%s: %v", directory, name, err)
					}
					for _, declaration := range file.Imports {
						importPath, err := strconv.Unquote(declaration.Path.Value)
						if err != nil {
							t.Fatalf("decode import %s/%s: %v", directory, name, err)
						}
						if dependency, local := strings.CutPrefix(importPath, "github.com/microsoft/waza/"); local {
							queue = append(queue, dependency)
						}
					}
				}
			}
			for _, selector := range selectors {
				if !visited[selector] {
					t.Errorf("runtime selector %s has no demonstrated pinned test-binary import", selector)
				}
			}
		})
	}
}

func TestWorkflowTriggerTypedInputs(t *testing.T) {
	source := `on:
  push:
    branches: [main]
    paths: &paths [Dockerfile, 'internal/**']
  pull_request:
    branches: [main]
    paths: *paths
    types: [opened, synchronize, reopened, edited]
  workflow_call:
    inputs:
      cold_observation:
        type: boolean
        required: false
        default: false
      pr_completeness_plan:
        type: string
        required: false
        default: ''
  workflow_dispatch:
    inputs:
      cold_observation:
        type: boolean
        required: false
        default: false
`
	triggers, err := readTriggers([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(triggers["push"].Filters["paths"], triggers["pull_request"].Filters["paths"]) {
		t.Fatal("typed input parsing lost anchored path filters")
	}
	if !selects(triggers, "workflow_dispatch", "feature/stack-parent", "") ||
		selects(triggers, "workflow_call", "feature/stack-parent", "") {
		t.Fatal("declared dispatch must select; reusable calls are not standalone events")
	}
	undeclared, err := readTriggers([]byte("on:\n  pull_request:\n"))
	if err != nil {
		t.Fatal(err)
	}
	if selects(undeclared, "workflow_dispatch", "feature/stack-parent", "") {
		t.Fatal("dispatch was invented without a declaration")
	}
	cold := triggers["workflow_call"].Inputs["cold_observation"]
	if cold.Type != "boolean" ||
		cold.Default != (workflowLiteral{Present: true, Tag: "!!bool", Value: "false"}) ||
		cold.Required != (workflowLiteral{Present: true, Tag: "!!bool", Value: "false"}) {
		t.Fatalf("optional cold interface was not preserved: %+v", cold)
	}
	plan := triggers["workflow_call"].Inputs["pr_completeness_plan"]
	if plan.Default != (workflowLiteral{Present: true, Tag: "!!str", Value: ""}) {
		t.Fatalf("explicit empty callable mode was not preserved: %+v", plan)
	}
	checkOptionalDefaults := func(source string) error {
		parsed, err := readTriggers([]byte(source))
		if err != nil {
			return err
		}
		for name, expected := range map[string]workflowLiteral{
			"cold_observation":     {Present: true, Tag: "!!bool", Value: "false"},
			"pr_completeness_plan": {Present: true, Tag: "!!str", Value: ""},
		} {
			input := parsed["workflow_call"].Inputs[name]
			if input.Default != expected || (input.Required.Present && input.Required.Value != "false") {
				return fmt.Errorf("input %s must remain optional with its explicit inactive default", name)
			}
		}
		return nil
	}
	if err := checkOptionalDefaults(source); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct{ old, replacement string }{
		{"default: false", "default: true"},
		{"required: false", "required: true"},
		{"default: ''", "default: 'active'"},
		{"        default: false\n", ""},
	} {
		mutated := strings.Replace(source, mutation.old, mutation.replacement, 1)
		if mutated == source {
			t.Fatal("inactive-default mutation did not change source")
		}
		if _, err := readTriggers([]byte(mutated)); err != nil {
			t.Fatalf("semantic-default mutation must retain valid typed YAML: %v", err)
		}
		if err := checkOptionalDefaults(mutated); err == nil {
			t.Fatal("active or missing default weakened callable admission")
		}
	}
	if err := checkOptionalDefaults(strings.Replace(source, "        required: false\n", "", 1)); err != nil {
		t.Fatalf("omitted optional required flag is alternative-valid: %v", err)
	}
	for _, branch := range []string{"main", "main-near-miss", "feature/stack-parent"} {
		if selects(triggers, "pull_request", branch, "synchronize") != (branch == "main") {
			t.Fatalf("typed input parsing weakened branch selection: %s", branch)
		}
	}
	for _, mutation := range []struct{ name, old, replacement string }{
		{"null-default", "default: false", "default: null"},
		{"string-default", "default: false", "default: 'false'"},
		{"numeric-default", "default: false", "default: 0"},
		{"string-required", "required: false", "required: 'false'"},
		{"unknown-type", "type: boolean", "type: unknown"},
		{"duplicate-input-default", "default: false", "default: false\n        default: false"},
		{"null-inputs", "inputs:\n      cold_observation:", "inputs: null\n    unexpected:\n      cold_observation:"},
		{"numeric-branch", "branches: [main]", "branches: [123]"},
		{"boolean-branch", "branches: [main]", "branches: [false]"},
		{"null-path-entry", "paths: &paths [Dockerfile, 'internal/**']", "paths: &paths [null, 'internal/**']"},
		{"mapping-action-entry", "types: [opened, synchronize, reopened, edited]", "types: [{opened: true}]"},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			mutated := strings.Replace(source, mutation.old, mutation.replacement, 1)
			if mutated == source {
				t.Fatal("mutation did not change source")
			}
			if _, err := readTriggers([]byte(mutated)); err == nil {
				t.Fatal("invalid typed interface was accepted")
			}
		})
	}
	omitted := strings.Replace(source, "        default: false\n", "", 1)
	parsed, err := readTriggers([]byte(omitted))
	if err != nil {
		t.Fatal(err)
	}
	if parsed["workflow_call"].Inputs["cold_observation"].Default.Present {
		t.Fatal("omitted default repaired into false")
	}
	emptyBranches := strings.Replace(source, "branches: [main]", "branches: []", 1)
	parsed, err = readTriggers([]byte(emptyBranches))
	if err != nil {
		t.Fatal(err)
	}
	if selects(parsed, "push", "main", "") {
		t.Fatal("explicit empty branch filter repaired into omitted")
	}
	if parsed["push"].Filters["branches"] == nil {
		t.Fatal("explicit empty branch list repaired into null")
	}
	nullBranches := strings.Replace(source, "branches: [main]", "branches: null", 1)
	parsed, err = readTriggers([]byte(nullBranches))
	if err != nil {
		t.Fatal(err)
	}
	if selects(parsed, "push", "main", "") {
		t.Fatal("explicit null branch filter repaired into omitted")
	}
	if parsed["push"].Filters["branches"] != nil {
		t.Fatal("explicit null branch filter repaired into empty")
	}
	anchored, err := readTriggers([]byte("on:\n  push:\n    branches: [&branch main]\n  pull_request:\n    branches: [*branch]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !selects(anchored, "pull_request", "main", "synchronize") ||
		selects(anchored, "pull_request", "main-near-miss", "synchronize") {
		t.Fatal("anchored scalar branch did not retain exact branch selection")
	}
	for _, shape := range []struct {
		source  string
		mapping bool
	}{
		{"on:\n  pull_request:\n", false},
		{"on:\n  pull_request: {}\n", true},
	} {
		parsed, err := readTriggers([]byte(shape.source))
		if err != nil {
			t.Fatal(err)
		}
		if parsed["pull_request"].Mapping != shape.mapping {
			t.Fatal("empty mapping and null event declaration lost their distinction")
		}
	}
}
