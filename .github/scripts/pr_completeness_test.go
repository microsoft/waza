package scripts

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
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
	for metric, count := range map[string]int{"tests": 18, "pass": 18, "fail": 0, "cancelled": 0, "skipped": 0} { //nolint:misspell // Node TAP field name.
		pattern := regexp.MustCompile(fmt.Sprintf(`(?m)^# %s %d\r?$`, metric, count))
		if !pattern.Match(output) {
			t.Fatalf("production Node controls lack expected %s=%d evidence:\n%s", metric, count, output)
		}
	}
}

func checkCompletenessControlSelection(source []byte) error {
	var workflow struct {
		Env      map[string]yaml.Node `yaml:"env"`
		Defaults ordinaryRunDefaults  `yaml:"defaults"`
		Jobs     map[string]struct {
			If       string               `yaml:"if"`
			RunsOn   string               `yaml:"runs-on"`
			Env      map[string]yaml.Node `yaml:"env"`
			Defaults ordinaryRunDefaults  `yaml:"defaults"`
			Steps    []struct {
				ordinaryExecutionStep `yaml:",inline"`
				Env                   map[string]yaml.Node `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		return err
	}
	job := workflow.Jobs["lint"]
	if job.If != goValidationIf || job.RunsOn != "ubuntu-latest" {
		return fmt.Errorf("Linux GoLint must select completeness controls on every validation event")
	}
	for _, step := range job.Steps {
		if step.Name != "Test Go workflow trigger selection" {
			continue
		}
		if step.If != "" || (step.ContinueOnError.Kind != 0 &&
			(step.ContinueOnError.Tag != "!!bool" || step.ContinueOnError.Value != "false")) {
			return fmt.Errorf("completeness control step must not skip or tolerate failure")
		}
		if _, err := ordinaryShellArguments("Linux", workflow.Defaults.Run.Shell, job.Defaults.Run.Shell, step.Shell); err != nil {
			return fmt.Errorf("GoLint completeness control shell: %w", err)
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
		packages := args[3:]
		if len(packages) > 0 && packages[0] == "-v" {
			packages = packages[1:]
		}
		selected := false
		for _, argument := range packages {
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

func effectiveWindowsEnv(name string, scopes ...map[string]yaml.Node) (yaml.Node, bool, error) {
	var effective yaml.Node
	present := false
	for _, scope := range scopes {
		var matched yaml.Node
		matches := 0
		for key, value := range scope {
			if strings.EqualFold(key, name) {
				matched = value
				matches++
			}
		}
		if matches > 1 {
			return yaml.Node{}, false, fmt.Errorf("Windows environment contains ambiguous case variants for %s", name)
		}
		if matches == 1 {
			effective, present = matched, true
		}
	}
	return effective, present, nil
}

func TestCompletenessControlsSelectedByGoLint(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "workflows", "go-ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	check := checkCompletenessControlSelection
	if err := check(source); err != nil {
		t.Fatal(err)
	}
	if err := check([]byte(strings.Replace(string(source), "go test -count=1 -v", "go test -count=1", 1))); err != nil {
		t.Fatalf("unfiltered nonverbose controls must remain valid: %v", err)
	}
	lintHeader := regexp.MustCompile(`(?m)^  lint:\n`).FindIndex(source)
	if lintHeader == nil {
		t.Fatal("actual production Lint block is missing")
	}
	lintEnd := len(source)
	if nextJob := regexp.MustCompile(`(?m)^  [A-Za-z0-9_-]+:\n`).FindIndex(source[lintHeader[1]:]); nextJob != nil {
		lintEnd = lintHeader[1] + nextJob[0]
	}
	mutateLint := func(old, replacement string) []byte {
		t.Helper()
		original := string(source[lintHeader[0]:lintEnd])
		block := strings.Replace(original, old, replacement, 1)
		if block == original {
			t.Fatalf("mutation did not change the actual Lint block: %q", old)
		}
		mutated := []byte(string(source[:lintHeader[0]]) + block + string(source[lintEnd:]))
		end := lintHeader[0] + len(block)
		if !bytes.Equal(mutated[:lintHeader[0]], source[:lintHeader[0]]) ||
			!bytes.Equal(mutated[end:], source[lintEnd:]) {
			t.Fatal("Lint mutation changed the Windows stanza or another consumer")
		}
		return mutated
	}
	t.Run("explicit-false-valid", func(t *testing.T) {
		mutated := mutateLint("run: go test -count=1", "continue-on-error: false\n        run: go test -count=1")
		if err := check(mutated); err != nil {
			t.Fatalf("explicit false must remain valid: %v", err)
		}
	})
	for _, value := range []string{"true", `"${{ inputs.allow_failure }}"`} {
		mutated := mutateLint("run: go test -count=1", "continue-on-error: "+value+"\n        run: go test -count=1")
		if check(mutated) == nil {
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
	withHelper := mutateLint(" ./.github/scripts/release-assets-helper", " ./.github/scripts/release-assets-helper ./.github/scripts/future-helper")
	if err := check(withHelper); err != nil {
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
		mutated := mutateLint(mutation.old, mutation.replacement)
		if check(mutated) == nil {
			t.Fatal("actual GoLint source mutation did not reject omitted, replaced or narrowed execution")
		}
	}
}

func TestCompletenessGoLintShellExecution(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "workflows", "go-ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	source = normalizeWorkflowLineEndings(source)
	setShell := func(input []byte, scope, value string) []byte {
		t.Helper()
		anchors := map[string]string{
			"workflow": "permissions:\n",
			"job":      "    defaults:\n      run:\n        working-directory: ./\n\n    steps:\n",
			"step":     "      - name: Test Go workflow trigger selection\n",
		}
		replacements := map[string]string{
			"workflow": "defaults:\n  run:\n    shell: " + value + "\n\npermissions:\n",
			"job":      "    defaults:\n      run:\n        working-directory: ./\n        shell: " + value + "\n\n    steps:\n",
			"step":     "      - name: Test Go workflow trigger selection\n        shell: " + value + "\n",
		}
		mutated := strings.Replace(string(input), anchors[scope], replacements[scope], 1)
		if mutated == string(input) {
			t.Fatalf("%s shell mutation did not change the actual workflow", scope)
		}
		var valid yaml.Node
		if err := yaml.Unmarshal([]byte(mutated), &valid); err != nil {
			t.Fatalf("%s shell mutation produced invalid YAML: %v", scope, err)
		}
		return []byte(mutated)
	}
	runSelected := func(input []byte, fail bool) {
		t.Helper()
		if err := checkCompletenessControlSelection(input); err != nil {
			t.Fatalf("valid selected shell was rejected: %v", err)
		}
		var workflow struct {
			Defaults ordinaryRunDefaults `yaml:"defaults"`
			Jobs     map[string]struct {
				Defaults ordinaryRunDefaults     `yaml:"defaults"`
				Steps    []ordinaryExecutionStep `yaml:"steps"`
			} `yaml:"jobs"`
		}
		if err := yaml.Unmarshal(input, &workflow); err != nil {
			t.Fatal(err)
		}
		job := workflow.Jobs["lint"]
		selected := 0
		for _, step := range job.Steps {
			if step.Name != "Test Go workflow trigger selection" {
				continue
			}
			selected++
			arguments, err := ordinaryShellArguments("Linux", workflow.Defaults.Run.Shell, job.Defaults.Run.Shell, step.Shell)
			if err != nil {
				t.Fatal(err)
			}
			command := step.Run
			if fail {
				command += "\nexit 27\n"
			}
			captured, err := ordinaryGoArguments(t, command, "Linux", nil, arguments)
			if fail {
				exit, ok := errors.AsType[*exec.ExitError](err)
				if !ok || exit.ExitCode() != 27 {
					t.Fatalf("runner-shaped shell must preserve exit 27: %v", err)
				}
			} else {
				expected := strings.Fields(step.Run)[2:]
				if err != nil || len(captured) == 0 || !reflect.DeepEqual(captured, expected) {
					t.Fatalf("actual selected control argv=%q want=%q err=%v", captured, expected, err)
				}
			}
		}
		if selected != 1 {
			t.Fatalf("actual control-step inventory=%d; want one", selected)
		}
	}
	t.Run("implicit-linux-shell", func(t *testing.T) {
		runSelected(source, false)
		runSelected(source, true)
	})
	supported := []string{"bash", "bash -e {0}", "bash -e -o pipefail {0}", "bash --noprofile --norc -e -o pipefail {0}"}
	unsafe := []string{`"bash -c true {0}"`, `"bash {0}"`, `"sh"`, `"pwsh"`, `"cmd"`,
		`"bash -e {0}; true"`, `"${{ inputs.shell }}"`, `""`, `" "`, "null", "false", "1", "[]", "{shell: bash}"}
	for _, scope := range []string{"workflow", "job", "step"} {
		for i, shell := range supported {
			t.Run(fmt.Sprintf("%s-supported-%d", scope, i), func(t *testing.T) {
				mutated := setShell(source, scope, strconv.Quote(shell))
				runSelected(mutated, false)
				runSelected(mutated, true)
			})
		}
		for i, value := range unsafe {
			t.Run(fmt.Sprintf("%s-unsafe-%d", scope, i), func(t *testing.T) {
				if err := checkCompletenessControlSelection(setShell(source, scope, value)); err == nil {
					t.Fatal("unsafe or unprovable effective shell was admitted")
				}
			})
		}
	}
	for _, override := range []struct{ lower, higher string }{
		{"workflow", "job"}, {"workflow", "step"}, {"job", "step"},
	} {
		t.Run(override.lower+"-overridden-by-"+override.higher, func(t *testing.T) {
			for _, lower := range unsafe {
				mutated := setShell(setShell(source, override.lower, lower), override.higher, `"bash"`)
				runSelected(mutated, false)
			}
			lower := setShell(source, override.lower, `"bash"`)
			for _, higher := range unsafe {
				if err := checkCompletenessControlSelection(setShell(lower, override.higher, higher)); err == nil {
					t.Fatal("higher unsafe or explicitly empty shell fell back to a lower safe shell")
				}
			}
		})
	}
	t.Run("ignored-script-counterexample", func(t *testing.T) {
		for _, scope := range []string{"workflow", "job", "step"} {
			if err := checkCompletenessControlSelection(setShell(source, scope, `"bash -c true {0}"`)); err == nil {
				t.Fatal("actual guard must reject the ignored-script shell")
			}
		}
		_, err := ordinaryGoArguments(t, "go test -count=1 ./.github/scripts\nexit 27\n", "Linux", nil, []string{"-c", "true"})
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("counterexample must exit zero without recording any Go invocation: %v", err)
		}
	})
	t.Run("safe-shell-does-not-admit-disabled-step", func(t *testing.T) {
		mutated := setShell(source, "step", `"bash"`)
		mutated = []byte(strings.Replace(string(mutated), "        shell: \"bash\"\n", "        shell: \"bash\"\n        if: false\n", 1))
		if err := checkCompletenessControlSelection(mutated); err == nil {
			t.Fatal("safe shell weakened the disabled-step rejection")
		}
	})
	t.Run("unrelated-shell-remains-outside-selection", func(t *testing.T) {
		anchor := "      - name: Test lint script failure handling\n"
		mutated := strings.Replace(string(source), anchor, anchor+"        shell: pwsh\n", 1)
		if mutated == string(source) {
			t.Fatal("unrelated step control did not change source")
		}
		if err := checkCompletenessControlSelection([]byte(mutated)); err != nil {
			t.Fatalf("selection guard must not impose Bash on unrelated steps: %v", err)
		}
	})
	t.Run("alias-is-not-a-literal-shell", func(t *testing.T) {
		mutated := setShell(setShell(source, "workflow", "&canonical bash"), "step", "*canonical")
		if err := checkCompletenessControlSelection(mutated); err == nil {
			t.Fatal("alias shell bypassed the documented literal-only admission")
		}
	})
	t.Run("windows-default-is-not-bash", func(t *testing.T) {
		if _, err := ordinaryShellArguments("Windows"); err == nil {
			t.Fatal("implicit Windows PowerShell was treated as Bash")
		}
		for _, shell := range supported {
			_, err := ordinaryShellArguments("Windows", yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: shell})
			if err != nil {
				t.Fatalf("existing explicit Windows Bash form was rejected: %v", err)
			}
		}
	})
}

func TestCompletenessWindowsControlsSelectedByNativeJob(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "workflows", "go-ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	source = normalizeWorkflowLineEndings(source)
	const command = `log="$(mktemp)"
go test -count=1 -v -timeout=5m -run '^Test(CompletenessGoLintShellExecution|OrdinaryShellOverridesAndDisabledPaths)$' ./.github/scripts | sed 's/\r$//' | tee "$log"
if grep -q -- '--- SKIP:' "$log"; then
  echo "Native Windows shell controls must not skip." >&2
  exit 1
fi
for control in TestCompletenessGoLintShellExecution TestOrdinaryShellOverridesAndDisabledPaths; do
  grep -Fxq "=== RUN   $control" "$log"
  grep -Eq "^--- PASS: $control \([0-9.]+s\)$" "$log"
done
rm "$log"
`
	controls := []string{"TestCompletenessGoLintShellExecution", "TestOrdinaryShellOverridesAndDisabledPaths"}
	for _, name := range []string{"pr_completeness_test.go", "go_ci_test.go"} {
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Recv != nil {
				continue
			}
			for i, control := range controls {
				if function.Name.Name == control {
					if function.Type.Results != nil || function.Type.Params.NumFields() != 1 {
						t.Fatalf("%s is not a runnable Go test", control)
					}
					pointer, ok := function.Type.Params.List[0].Type.(*ast.StarExpr)
					if !ok {
						t.Fatalf("%s must accept *testing.T", control)
					}
					selector, ok := pointer.X.(*ast.SelectorExpr)
					if !ok || selector.Sel.Name != "T" {
						t.Fatalf("%s must accept *testing.T", control)
					}
					packageName, ok := selector.X.(*ast.Ident)
					if !ok || packageName.Name != "testing" {
						t.Fatalf("%s must accept *testing.T", control)
					}
					controls[i] = ""
				}
			}
		}
	}
	if controls[0] != "" || controls[1] != "" {
		t.Fatalf("native selector names missing actual Go test declarations: %q", controls)
	}
	check := func(input []byte) error {
		var workflow struct {
			Env      map[string]yaml.Node `yaml:"env"`
			Defaults ordinaryRunDefaults  `yaml:"defaults"`
			Jobs     map[string]struct {
				If       string               `yaml:"if"`
				RunsOn   string               `yaml:"runs-on"`
				Env      map[string]yaml.Node `yaml:"env"`
				Defaults ordinaryRunDefaults  `yaml:"defaults"`
				Steps    []struct {
					ordinaryExecutionStep `yaml:",inline"`
					Env                   map[string]yaml.Node `yaml:"env"`
				} `yaml:"steps"`
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
		if job.If != goValidationIf || job.RunsOn != "windows-latest" ||
			!reflect.DeepEqual(job.Strategy.Matrix.OS, []string{"windows-latest"}) {
			return fmt.Errorf("native shell controls require the Windows job on every validation event")
		}
		positions := make(map[string]int)
		selected := 0
		for i, step := range job.Steps {
			positions[step.Name] = i
			if step.Name != "Test native Windows shell controls" {
				continue
			}
			selected++
			if step.If != "matrix.os == 'windows-latest'" || step.Run != command {
				return fmt.Errorf("Windows shell controls must retain the exact finite uncached selector and nonempty/no-skip evidence checks")
			}
			if step.ContinueOnError.Kind != 0 && (step.ContinueOnError.Tag != "!!bool" || step.ContinueOnError.Value != "false") {
				return fmt.Errorf("Windows shell controls must propagate failures")
			}
			arguments, err := ordinaryShellArguments("Windows", workflow.Defaults.Run.Shell, job.Defaults.Run.Shell, step.Shell)
			if err != nil {
				return err
			}
			if len(arguments) < 2 || arguments[len(arguments)-2] != "-o" || arguments[len(arguments)-1] != "pipefail" {
				return fmt.Errorf("Windows shell-control pipeline requires Bash pipefail")
			}
			flags, flagsPresent, err := effectiveWindowsEnv("GOFLAGS", workflow.Env, job.Env, step.Env)
			if err != nil {
				return err
			}
			if flagsPresent && (flags.Kind != yaml.ScalarNode || flags.Tag != "!!str" || flags.Value != "") {
				return fmt.Errorf("Windows shell-control GOFLAGS must be omitted or literal empty after workflow/job/step overrides")
			}
		}
		if selected != 1 {
			return fmt.Errorf("native Windows shell-control step count=%d; require one", selected)
		}
		for _, prerequisite := range []string{
			"Setup Node.js (web build and offline assertion oracle)", "Install and prove rejecting runtime guard", "Execute Tests",
		} {
			position, present := positions[prerequisite]
			if !present || position >= positions["Test native Windows shell controls"] {
				return fmt.Errorf("Windows shell controls must follow %s", prerequisite)
			}
		}
		marker, present := positions["Reject tolerated runtime launches"]
		if !present || marker <= positions["Test native Windows shell controls"] {
			return fmt.Errorf("runtime marker must be checked after Windows shell controls")
		}
		return nil
	}
	if err := check(source); err != nil {
		t.Fatal(err)
	}
	setFlags := func(input []byte, scope, name, value string) []byte {
		t.Helper()
		anchors := map[string]string{
			"workflow": "permissions:\n",
			"job":      "  build-and-test-windows:\n",
			"step":     "      - name: Test native Windows shell controls\n",
		}
		replacements := map[string]string{
			"workflow": "env:\n  " + name + ": " + value + "\n\npermissions:\n",
			"job":      "  build-and-test-windows:\n    env:\n      " + name + ": " + value + "\n",
			"step":     "      - name: Test native Windows shell controls\n        env:\n          " + name + ": " + value + "\n",
		}
		mutated := strings.Replace(string(input), anchors[scope], replacements[scope], 1)
		if mutated == string(input) {
			t.Fatalf("%s %s control did not change the actual workflow", scope, name)
		}
		return []byte(mutated)
	}
	for _, scope := range []string{"workflow", "job", "step"} {
		t.Run(scope+"-literal-empty-goflags-valid", func(t *testing.T) {
			if err := check(setFlags(source, scope, "GOFLAGS", `""`)); err != nil {
				t.Fatalf("literal empty GOFLAGS must remain valid: %v", err)
			}
		})
		for i, mutation := range []struct{ name, value string }{
			{"GOFLAGS", `"-skip=Test.*/.*"`}, {"goflags", `"-skip=Test.*/.*"`},
			{"GoFlags", `"-run=^$"`}, {"GOFLAGS", `"${{ inputs.go_flags }}"`},
			{"GOFLAGS", `" "`}, {"GOFLAGS", "null"}, {"GOFLAGS", "false"}, {"GOFLAGS", "1"},
		} {
			t.Run(fmt.Sprintf("%s-unsafe-goflags-%d", scope, i), func(t *testing.T) {
				if err := check(setFlags(source, scope, mutation.name, mutation.value)); err == nil {
					t.Fatal("nonempty or unprovable effective GOFLAGS must reject native control selection")
				}
			})
		}
		t.Run(scope+"-ambiguous-goflags-case", func(t *testing.T) {
			mutated := setFlags(source, scope, "GOFLAGS", `""`)
			anchors := map[string]string{
				"workflow": "  GOFLAGS: \"\"\n",
				"job":      "      GOFLAGS: \"\"\n",
				"step":     "          GOFLAGS: \"\"\n",
			}
			anchor := anchors[scope]
			mutated = []byte(strings.Replace(string(mutated), anchor,
				anchor+strings.Replace(anchor, "GOFLAGS: \"\"", `goflags: "-skip=Test.*/.*"`, 1), 1))
			if err := check(mutated); err == nil {
				t.Fatal("ambiguous same-scope GOFLAGS case variants must reject native control selection")
			}
		})
	}
	for _, override := range []struct{ lower, higher string }{
		{"workflow", "job"}, {"workflow", "step"}, {"job", "step"},
	} {
		t.Run(override.lower+"-goflags-overridden-by-"+override.higher, func(t *testing.T) {
			lower := setFlags(source, override.lower, "goflags", `"-skip=Test.*/.*"`)
			if err := check(setFlags(lower, override.higher, "GoFlags", `""`)); err != nil {
				t.Fatalf("higher literal empty GOFLAGS must override lower unsafe value: %v", err)
			}
		})
	}
	t.Run("step-unsafe-goflags-overrides-empty-job", func(t *testing.T) {
		lower := setFlags(setFlags(source, "workflow", "goflags", `"-skip=Test.*/.*"`), "job", "GOFLAGS", `""`)
		if err := check(setFlags(lower, "step", "GoFlags", `"-run=^$"`)); err == nil {
			t.Fatal("higher unsafe GOFLAGS must not inherit lower empty admission")
		}
	})
	for _, mutation := range []struct{ old, replacement string }{
		{"      - name: Test native Windows shell controls", "      - name: Missing Windows shell controls"},
		{"        if: matrix.os == 'windows-latest'", "        if: false"},
		{"        if: matrix.os == 'windows-latest'", "        if: matrix.os == 'ubuntu-latest'"},
		{"        if: matrix.os == 'windows-latest'", "        if: matrix.os == 'windows-latest'\n        continue-on-error: true"},
		{"        if: matrix.os == 'windows-latest'", "        if: matrix.os == 'windows-latest'\n        shell: bash -c true {0}"},
		{"        if: matrix.os == 'windows-latest'", "        if: matrix.os == 'windows-latest'\n        shell: bash -e {0}"},
		{"        os: [windows-latest]", "        os: [ubuntu-latest]"},
		{"-timeout=5m", "-timeout=0"},
		{"-count=1 -v -timeout=5m", "-count=1 -timeout=5m"},
		{"OrdinaryShellOverridesAndDisabledPaths)$", "MissingControl)$"},
		{`          if grep -q -- '--- SKIP:' "$log"; then`, "          if false; then"},
		{`            grep -Fxq "=== RUN   $control" "$log"`, ":"},
		{`            grep -Eq "^--- PASS: $control \([0-9.]+s\)$" "$log"`, ":"},
	} {
		mutated := strings.Replace(string(source), mutation.old, mutation.replacement, 1)
		if mutated == string(source) {
			t.Fatalf("native-selection mutation did not change production source: %q", mutation.old)
		}
		if err := check([]byte(mutated)); err == nil {
			t.Fatalf("native-selection bypass admitted: %q", mutation.replacement)
		}
	}
	for _, declaration := range []string{
		"        continue-on-error: false",
		"        shell: bash -e -o pipefail {0}",
	} {
		mutated := strings.Replace(string(source), "        if: matrix.os == 'windows-latest'",
			"        if: matrix.os == 'windows-latest'\n"+declaration, 1)
		if mutated == string(source) {
			t.Fatal("alternative-valid native-selection mutation did not change production source")
		}
		if err := check([]byte(mutated)); err != nil {
			t.Fatalf("alternative-valid native selection rejected: %s: %v", declaration, err)
		}
	}
	for _, fixture := range []struct {
		name   string
		output string
		status int
		valid  bool
	}{
		{"complete", "=== RUN   TestCompletenessGoLintShellExecution\n--- PASS: TestCompletenessGoLintShellExecution (0.01s)\n=== RUN   TestOrdinaryShellOverridesAndDisabledPaths\n--- PASS: TestOrdinaryShellOverridesAndDisabledPaths (0.01s)\n", 0, true},
		{"complete-crlf", "=== RUN   TestCompletenessGoLintShellExecution\r\n--- PASS: TestCompletenessGoLintShellExecution (0.01s)\r\n=== RUN   TestOrdinaryShellOverridesAndDisabledPaths\r\n--- PASS: TestOrdinaryShellOverridesAndDisabledPaths (0.01s)\r\n", 0, true},
		{"empty", "testing: warning: no tests to run\nPASS\n", 0, false},
		{"missing-family", "=== RUN   TestCompletenessGoLintShellExecution\n--- PASS: TestCompletenessGoLintShellExecution (0.01s)\n", 0, false},
		{"skipped", "=== RUN   TestCompletenessGoLintShellExecution\n--- SKIP: TestCompletenessGoLintShellExecution (0.01s)\n=== RUN   TestOrdinaryShellOverridesAndDisabledPaths\n--- PASS: TestOrdinaryShellOverridesAndDisabledPaths (0.01s)\n", 0, false},
		{"skipped-subtest", "=== RUN   TestCompletenessGoLintShellExecution\n=== RUN   TestCompletenessGoLintShellExecution/required-native-case\n    --- SKIP: TestCompletenessGoLintShellExecution/required-native-case (0.01s)\n--- PASS: TestCompletenessGoLintShellExecution (0.01s)\n=== RUN   TestOrdinaryShellOverridesAndDisabledPaths\n--- PASS: TestOrdinaryShellOverridesAndDisabledPaths (0.01s)\n", 0, false},
		{"failed-command", "=== RUN   TestCompletenessGoLintShellExecution\n--- PASS: TestCompletenessGoLintShellExecution (0.01s)\n=== RUN   TestOrdinaryShellOverridesAndDisabledPaths\n--- PASS: TestOrdinaryShellOverridesAndDisabledPaths (0.01s)\n", 27, false},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			scratch := t.TempDir()
			probe := "#!/usr/bin/env bash\nprintf '%s' \"$WAZA_WINDOWS_CONTROL_OUTPUT\"\nexit \"$WAZA_WINDOWS_CONTROL_STATUS\"\n"
			if err := os.WriteFile(filepath.Join(scratch, "go"), []byte(probe), 0o700); err != nil {
				t.Fatal(err)
			}
			script := filepath.Join(scratch, "native selection.sh")
			if err := os.WriteFile(script, []byte(command), 0o600); err != nil {
				t.Fatal(err)
			}
			arguments, err := ordinaryShellArguments("Windows", yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "bash"})
			if err != nil {
				t.Fatal(err)
			}
			probeCommand := exec.CommandContext(t.Context(), "bash", append(arguments, script)...)
			probeCommand.Env = append(os.Environ(),
				"PATH="+scratch+string(os.PathListSeparator)+os.Getenv("PATH"),
				"TMPDIR="+filepath.ToSlash(scratch),
				"WAZA_WINDOWS_CONTROL_OUTPUT="+fixture.output,
				"WAZA_WINDOWS_CONTROL_STATUS="+strconv.Itoa(fixture.status))
			output, err := probeCommand.CombinedOutput()
			if (err == nil) != fixture.valid {
				t.Fatalf("actual workflow output admission %s: err=%v\n%s", fixture.name, err, output)
			}
			if fixture.status != 0 {
				exit, ok := errors.AsType[*exec.ExitError](err)
				if !ok || exit.ExitCode() != fixture.status {
					t.Fatalf("actual workflow pipeline lost status %d: %v", fixture.status, err)
				}
			}
		})
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

// receiptJob identifies one receipt-producing job. An empty job means the
// workflow must contain exactly one job. Guard is an extra condition conjunct
// for jobs that also run when their family is not selected.
type receiptJob struct {
	family string
	job    string
	units  []string
	runner string
	guard  string
}

func checkAuxiliaryReceiptJob(source []byte, family string, spec receiptJob) error {
	units := spec.units
	label := family + "/" + strings.Join(units, ",")
	triggers, err := readTriggers(source)
	if err != nil {
		return err
	}
	input := triggers["workflow_call"].Inputs["pr_completeness_plan"]
	if input.Type != "string" ||
		input.Default != (workflowLiteral{Present: true, Tag: "!!str", Value: ""}) ||
		(input.Required.Present && input.Required.Value != "false") {
		return fmt.Errorf("%s completeness plan input must remain optional and inactive", label)
	}
	if _, ok := triggers["pull_request"]; ok {
		return fmt.Errorf("%s pull requests must run only through the Go completeness caller", label)
	}
	if _, ok := triggers["push"]; !ok {
		return fmt.Errorf("%s lost its direct push trigger", label)
	}

	type workflowStep struct {
		ID              string               `yaml:"id"`
		Uses            string               `yaml:"uses"`
		If              string               `yaml:"if"`
		Run             string               `yaml:"run"`
		ContinueOnError yaml.Node            `yaml:"continue-on-error"`
		With            map[string]yaml.Node `yaml:"with"`
	}
	type workflowJob struct {
		RunsOn   string `yaml:"runs-on"`
		Strategy struct {
			Matrix struct {
				Include []struct {
					OS   string `yaml:"os"`
					Unit string `yaml:"unit"`
				} `yaml:"include"`
			} `yaml:"matrix"`
		} `yaml:"strategy"`
		Steps []workflowStep `yaml:"steps"`
	}
	var workflow struct {
		Jobs map[string]workflowJob `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		return err
	}
	var job workflowJob
	if spec.job == "" {
		if len(workflow.Jobs) != 1 {
			return fmt.Errorf("%s must retain one receipt-producing job", label)
		}
		for _, candidate := range workflow.Jobs {
			job = candidate
		}
	} else {
		candidate, ok := workflow.Jobs[spec.job]
		if !ok {
			return fmt.Errorf("%s receipt-producing job %s is missing", label, spec.job)
		}
		job = candidate
	}
	if len(units) > 1 {
		actual := make([]string, 0, len(job.Strategy.Matrix.Include))
		expectedRunners := map[string]string{
			"linux":   "ubuntu-latest",
			"windows": "windows-latest",
			"macos":   "macos-latest",
		}
		if job.RunsOn != "${{ matrix.os }}" {
			return fmt.Errorf("%s matrix job must run on its declared OS", label)
		}
		for _, member := range job.Strategy.Matrix.Include {
			if member.OS == "" || member.Unit == "" {
				return fmt.Errorf("%s matrix member lacks an OS or stable unit", label)
			}
			if member.OS != expectedRunners[member.Unit] {
				return fmt.Errorf("%s unit %s runs on %s; want %s", label, member.Unit, member.OS, expectedRunners[member.Unit])
			}
			actual = append(actual, member.Unit)
		}
		if !reflect.DeepEqual(actual, units) {
			return fmt.Errorf("%s matrix units %v; want %v", label, actual, units)
		}
	} else if job.RunsOn != spec.runner {
		return fmt.Errorf("%s receipt must retain its %s runner", label, spec.runner)
	}

	steps := map[string]workflowStep{}
	for _, step := range job.Steps {
		if step.ID == "" {
			continue
		}
		if _, duplicate := steps[step.ID]; duplicate {
			return fmt.Errorf("%s duplicate step id %s", label, step.ID)
		}
		if step.ContinueOnError.Kind != 0 &&
			(step.ContinueOnError.Tag != "!!bool" || step.ContinueOnError.Value != "false") {
			return fmt.Errorf("%s step %s tolerates failure", label, step.ID)
		}
		steps[step.ID] = step
	}
	planCondition := "inputs.pr_completeness_plan != ''" + spec.guard
	sourceCheckout := false
	planDownload := false
	for _, step := range job.Steps {
		switch {
		case strings.HasPrefix(step.Uses, "actions/checkout@"):
			sourceCheckout = step.With["fetch-depth"].Value == "2"
		case strings.HasPrefix(step.Uses, "actions/download-artifact@"):
			planDownload = step.If == planCondition &&
				step.With["name"].Value == "${{ inputs.pr_completeness_plan }}" &&
				step.With["path"].Value == "${{ runner.temp }}/pr-completeness-plan"
		}
	}
	if !sourceCheckout || !planDownload {
		return fmt.Errorf("%s must fetch both merge parents and the exact current-run plan artifact", label)
	}
	required := map[string][]string{
		"assurance":           {"checkout", "build", "custody", "guard", "qualification", "export", "preserved_export", "consent", "reader", "preserved_reader", "browser"},
		"tools":               {"checkout", "integrity", "tests"},
		"runtime":             {"checkout", "tests"},
		"dashboard":           {"checkout", "node", "install", "go", "lint", "build", "chromium", "tests"},
		"site":                {"checkout", "node", "install", "build", "metadata", "chromium", "tests", "chat-tests"},
		"examples":            {"checkout", "build", "examples"},
		"container/image":     {"checkout", "build", "version"},
		"container/reference": {"checkout", "controls", "subprocess", "native"},
	}
	ids, ok := required[family]
	if !ok {
		ids = required[family+"/"+units[0]]
	}
	if len(ids) == 0 {
		return fmt.Errorf("%s has no required receipt step contract", label)
	}
	for _, id := range ids {
		if _, ok := steps[id]; !ok {
			return fmt.Errorf("%s receipt omits required step id %s", label, id)
		}
	}
	checkout := steps["checkout"]
	if checkout.If != planCondition ||
		!strings.Contains(checkout.Run, `git rev-parse HEAD`) ||
		!strings.Contains(checkout.Run, `GITHUB_OUTPUT`) {
		return fmt.Errorf("%s checkout evidence is not conditionally source-bound", label)
	}
	receipt, ok := steps["receipt"]
	if !ok {
		return fmt.Errorf("%s receipt capture step is missing", label)
	}
	unit := units[0]
	if len(units) > 1 {
		unit = `"${{ matrix.unit }}"`
	}
	if receipt.If != "always() && "+planCondition ||
		!strings.Contains(receipt.Run, `pr-completeness.cjs receipt`) ||
		!strings.Contains(receipt.Run, `pr-completeness-plan/plan.json`) ||
		!strings.Contains(receipt.Run, " "+family+" "+unit) {
		return fmt.Errorf("%s receipt capture is not fail-closed or family-bound", label)
	}
	upload := false
	expectedReceiptPath := "${{ runner.temp }}/pr-completeness-receipt/receipt.json"
	if len(units) > 1 {
		expectedReceiptPath = "${{ runner.temp }}/pr-completeness-receipt-${{ matrix.unit }}/receipt.json"
	}
	for _, step := range job.Steps {
		if !strings.HasPrefix(step.Uses, "actions/upload-artifact@") ||
			(!strings.Contains(step.With["name"].Value, "steps.receipt") &&
				!strings.Contains(step.With["path"].Value, "pr-completeness-receipt") &&
				!strings.Contains(step.If, "steps.receipt")) {
			continue
		}
		upload = true
		if step.If != "always() && steps.receipt.outcome == 'success'" ||
			step.With["name"].Value != "${{ steps.receipt.outputs.artifact_name }}" ||
			step.With["path"].Value != expectedReceiptPath ||
			step.With["if-no-files-found"].Value != "error" {
			return fmt.Errorf("%s receipt upload does not require successful capture and an exact artifact", label)
		}
	}
	if !upload {
		return fmt.Errorf("%s receipt artifact upload is missing", label)
	}
	browserSteps := map[string][]string{"assurance": {"browser"}, "dashboard": {"tests"}, "site": {"tests", "chat-tests"}}[family]
	for _, id := range browserSteps {
		reported := 0
		for _, command := range strings.Split(steps[id].Run, "&&") {
			if !strings.Contains(command, "--reporter=github,line,json") {
				continue
			}
			reported++
			if !strings.Contains(command, "--project=chromium") || !strings.Contains(command, "--retries=0") {
				return fmt.Errorf("%s browser receipt step %s lacks explicit zero-retry Chromium evidence", label, id)
			}
		}
		if reported == 0 {
			return fmt.Errorf("%s browser receipt step %s lacks JSON evidence", label, id)
		}
	}
	return nil
}

func TestAuxiliaryValidationReceiptWiring(t *testing.T) {
	siteGuard := " && needs.changes.outputs.site == 'true'"
	workflows := []struct {
		file   string
		family string
		jobs   []receiptJob
	}{
		{"grader-assurance-ci.yml", "assurance", []receiptJob{{units: []string{"assurance"}, runner: "ubuntu-latest"}}},
		{"squad-tools-ci.yml", "tools", []receiptJob{{units: []string{"linux", "windows"}}}},
		{"evidence-judge-ci.yml", "runtime", []receiptJob{{units: []string{"linux", "macos"}}}},
		{"web-ci.yml", "dashboard", []receiptJob{{units: []string{"dashboard"}, runner: "ubuntu-latest"}}},
		{"site-ci.yml", "site", []receiptJob{
			{job: "build", units: []string{"site"}, runner: "ubuntu-latest", guard: siteGuard},
			{family: "examples", job: "examples", units: []string{"examples"}, runner: "ubuntu-latest"},
		}},
		{"docker-ci.yml", "container", []receiptJob{
			{job: "docker-build", units: []string{"image"}, runner: "ubuntu-latest"},
			{job: "runner-reference", units: []string{"reference"}, runner: "ubuntu-24.04"},
		}},
	}
	for _, workflow := range workflows {
		t.Run(workflow.family, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join("..", "workflows", workflow.file))
			if err != nil {
				t.Fatal(err)
			}
			check := func(source []byte) error {
				for _, job := range workflow.jobs {
					family := workflow.family
					if job.family != "" {
						family = job.family
					}
					if err := checkAuxiliaryReceiptJob(source, family, job); err != nil {
						return err
					}
				}
				return nil
			}
			if err := check(source); err != nil {
				t.Fatal(err)
			}
			mutations := []struct {
				old         string
				replacement string
			}{
				{"        default: ''", "        default: active"},
				{"if: always() && inputs.pr_completeness_plan != ''", "if: inputs.pr_completeness_plan != ''"},
				{"id: receipt", "id: omitted_receipt"},
				{"pr-completeness.cjs receipt", "pr-completeness.cjs evaluate"},
				{"name: ${{ inputs.pr_completeness_plan }}", "name: unrelated-plan"},
				{"path: ${{ runner.temp }}/pr-completeness-receipt", "path: ${{ runner.temp }}/unrelated/steps.json #"},
				{"id: checkout", "id: unrecorded_checkout"},
				{"fetch-depth: 2", "fetch-depth: 1"},
			}
			if workflow.family == "site" {
				mutations = append(mutations,
					struct {
						old         string
						replacement string
					}{"          lfs: true\n          fetch-depth: 2", "          lfs: true\n          fetch-depth: 1"},
					struct {
						old         string
						replacement string
					}{"examples examples", "examples site"},
					struct {
						old         string
						replacement string
					}{"--project=chromium --retries=0 --reporter=github,line,json --output=test-results/chat-unavailable", "--project=chromium --reporter=github,line,json --output=test-results/chat-unavailable"},
					struct {
						old         string
						replacement string
					}{"if: always() && inputs.pr_completeness_plan != '' && needs.changes.outputs.site == 'true'", "if: always() && inputs.pr_completeness_plan != ''"},
				)
			}
			if workflow.family == "container" {
				mutations = append(mutations, struct {
					old         string
					replacement string
				}{"        id: native\n", "\n"})
			}
			mutations = append(mutations, struct {
				old         string
				replacement string
			}{"on:\n", "on:\n  pull_request:\n"})
			for _, mutation := range mutations {
				mutated := strings.Replace(string(source), mutation.old, mutation.replacement, 1)
				if mutated == string(source) {
					t.Fatalf("mutation did not change %s: %q", workflow.file, mutation.old)
				}
				if check([]byte(mutated)) == nil {
					t.Fatalf("%s mutation was admitted: %q", workflow.file, mutation.old)
				}
			}
			nativeRunner := map[string]string{"tools": "windows-latest", "runtime": "macos-latest"}[workflow.family]
			if nativeRunner != "" {
				mutated := strings.Replace(string(source), nativeRunner, "ubuntu-latest", 1)
				if mutated == string(source) {
					t.Fatalf("native runner mutation did not change %s", workflow.file)
				}
				if check([]byte(mutated)) == nil {
					t.Fatalf("%s admitted Linux in place of %s", workflow.file, nativeRunner)
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
