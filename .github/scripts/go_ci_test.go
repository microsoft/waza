package scripts

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type workflowTriggers map[string]map[string][]string

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
	filters, ok := triggers[event]
	if !ok {
		return false
	}
	if branches, ok := filters["branches"]; ok && !slices.Contains(branches, branch) {
		return false
	}
	if event == "pull_request" {
		types, ok := filters["types"]
		if !ok {
			types = []string{"opened", "synchronize", "reopened"}
		}
		return slices.Contains(types, action)
	}
	return event == "push"
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
	if workflow.Concurrency.Group != "${{ github.workflow }}-${{ github.event_name }}-${{ github.ref }}" ||
		workflow.Concurrency.Cancel != "${{ github.event_name == 'pull_request' }}" {
		return fmt.Errorf("cancel only superseded PR runs, scoped to workflow/event/ref")
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
		{"${{ github.workflow }}-${{ github.event_name }}-${{ github.ref }}", "${{ github.ref }}"},
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
				"push":         {"branches": tc.pushBranches},
				"pull_request": tc.prFilters,
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
