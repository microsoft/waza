package scripts

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type admissionWorkflow struct {
	On struct {
		Call struct {
			Inputs map[string]struct {
				Type     string `yaml:"type"`
				Required bool   `yaml:"required"`
			} `yaml:"inputs"`
			Outputs map[string]struct {
				Value string `yaml:"value"`
			} `yaml:"outputs"`
		} `yaml:"workflow_call"`
	} `yaml:"on"`
	Permissions map[string]string `yaml:"permissions"`
	Jobs        map[string]struct {
		Name       string            `yaml:"name"`
		If         string            `yaml:"if"`
		RunsOn     string            `yaml:"runs-on"`
		Timeout    int               `yaml:"timeout-minutes"`
		Outputs    map[string]string `yaml:"outputs"`
		ContinueOn yaml.Node         `yaml:"continue-on-error"`
		Steps      []struct {
			Name       string            `yaml:"name"`
			ID         string            `yaml:"id"`
			If         string            `yaml:"if"`
			Uses       string            `yaml:"uses"`
			Run        string            `yaml:"run"`
			Shell      string            `yaml:"shell"`
			With       map[string]string `yaml:"with"`
			Env        map[string]string `yaml:"env"`
			ContinueOn yaml.Node         `yaml:"continue-on-error"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func checkAdmissionWorkflow(source []byte, mode string) error {
	var workflow admissionWorkflow
	// The workflow name is descriptive, not an admission input.
	var document yaml.Node
	if err := yaml.Unmarshal(source, &document); err != nil {
		return err
	}
	mapping := document.Content[0]
	filtered := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for i := 0; i < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value != "name" {
			filtered.Content = append(filtered.Content, mapping.Content[i], mapping.Content[i+1])
		}
	}
	encoded, err := yaml.Marshal(filtered)
	if err != nil {
		return err
	}
	decoder := yaml.NewDecoder(strings.NewReader(string(encoded)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&workflow); err != nil {
		return err
	}
	expectedPermissions := map[string]string{"contents": "read"}
	if mode == "admission" {
		expectedPermissions["pull-requests"] = "read"
	}
	if !reflect.DeepEqual(workflow.Permissions, expectedPermissions) || len(workflow.Jobs) != 1 {
		return fmt.Errorf("admission boundary requires exact read-only permissions and one job")
	}
	job, ok := workflow.Jobs[mode]
	if !ok || job.If != "" || job.ContinueOn.Kind != 0 || job.RunsOn != "ubuntu-latest" ||
		job.Timeout <= 0 || job.Timeout > 10 {
		return fmt.Errorf("required %s job can skip, tolerate failure, or lacks bounded native execution", mode)
	}
	if mode == "admission" {
		if job.Name != "PR completeness" {
			return fmt.Errorf("admission context must be stable")
		}
		inputs := workflow.On.Call.Inputs
		if len(inputs) != 2 || inputs["pr_completeness_plan"].Type != "string" ||
			!inputs["pr_completeness_plan"].Required || inputs["dependency_results"].Type != "string" ||
			!inputs["dependency_results"].Required {
			return fmt.Errorf("admission needs required opaque artifact and dependency inputs")
		}
	} else {
		for _, output := range []string{"plan_artifact", "go", "dashboard", "site", "examples", "assurance", "container", "tools", "runtime"} {
			if job.Outputs[output] != "${{ steps.plan.outputs."+output+" }}" ||
				workflow.On.Call.Outputs[output].Value != "${{ jobs.plan.outputs."+output+" }}" {
				return fmt.Errorf("source-bound plan output %s is missing or remapped", output)
			}
		}
	}
	var checkout, node, caller, planDownload, receiptDownload, upload bool
	for _, step := range job.Steps {
		if step.ContinueOn.Kind != 0 || (step.If != "" && step.Name != "Retain admission result including rejected evidence") {
			return fmt.Errorf("required boundary step %s can skip or tolerate failure", step.Name)
		}
		switch step.Name {
		case "Checkout tested merge and both parents":
			checkout = strings.HasPrefix(step.Uses, "actions/checkout@") &&
				reflect.DeepEqual(step.With, map[string]string{"fetch-depth": "2", "persist-credentials": "false"})
		case "Setup Node":
			node = strings.HasPrefix(step.Uses, "actions/setup-node@") &&
				reflect.DeepEqual(step.With, map[string]string{"node-version": "24"})
		case "Discover complete tested-merge applicability":
			caller = mode == "plan" && step.ID == "plan" && step.Shell == "bash" &&
				step.Run == `node .github/scripts/pr-completeness-admission.cjs plan "$RUNNER_TEMP/pr-completeness-plan"`
		case "Download exact plan from this run":
			planDownload = strings.HasPrefix(step.Uses, "actions/download-artifact@") &&
				reflect.DeepEqual(step.With, map[string]string{
					"name": "${{ inputs.pr_completeness_plan }}", "path": "${{ runner.temp }}/pr-completeness-plan",
				})
		case "Download individual receipts from this attempt":
			receiptDownload = strings.HasPrefix(step.Uses, "actions/download-artifact@") &&
				reflect.DeepEqual(step.With, map[string]string{
					"pattern":        "pr-completeness-receipt-*-${{ github.run_id }}-${{ github.run_attempt }}",
					"merge-multiple": "false", "path": "${{ runner.temp }}/pr-completeness-receipts",
				})
		case "Admit current source and all applicable evidence":
			caller = mode == "admission" && step.ID == "admit" && step.Shell == "bash" &&
				strings.TrimSpace(step.Run) == `node .github/scripts/pr-completeness-admission.cjs admit \
  "$RUNNER_TEMP/pr-completeness-plan" "$RUNNER_TEMP/pr-completeness-receipts" \
  "$RUNNER_TEMP/pr-completeness-result"` &&
				reflect.DeepEqual(step.Env, map[string]string{
					"GH_TOKEN":                "${{ github.token }}",
					"WAZA_COMPLETENESS_PLAN":  "${{ inputs.pr_completeness_plan }}",
					"WAZA_COMPLETENESS_NEEDS": "${{ inputs.dependency_results }}",
				})
		case "Upload source-bound plan":
			upload = mode == "plan" && strings.HasPrefix(step.Uses, "actions/upload-artifact@") &&
				reflect.DeepEqual(step.With, map[string]string{
					"name": "${{ steps.plan.outputs.plan_artifact }}", "path": "${{ runner.temp }}/pr-completeness-plan/plan.json",
					"if-no-files-found": "error",
				})
		case "Retain admission result including rejected evidence":
			upload = mode == "admission" && strings.HasPrefix(step.Uses, "actions/upload-artifact@") &&
				step.If == "always() && steps.admit.outcome != 'skipped' && steps.admit.outcome != ''" &&
				reflect.DeepEqual(step.With, map[string]string{
					"name": "pr-completeness-result-${{ github.run_id }}-${{ github.run_attempt }}",
					"path": "${{ runner.temp }}/pr-completeness-result/result.json", "if-no-files-found": "error",
				})
		default:
			return fmt.Errorf("unreviewed boundary step: %s", step.Name)
		}
	}
	if !checkout || !node || !caller || !upload || (mode == "admission" && (!planDownload || !receiptDownload)) {
		return fmt.Errorf("missing or invalid source/transport/caller/result step")
	}
	return nil
}

func TestCompletenessAdmissionWorkflowSource(t *testing.T) {
	for _, mode := range []string{"plan", "admission"} {
		source, err := os.ReadFile("../workflows/pr-completeness-" + mode + ".yml")
		if err != nil {
			t.Fatal(err)
		}
		if err := checkAdmissionWorkflow(source, mode); err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		for _, mutation := range []struct{ old, replacement string }{
			{"fetch-depth: 2", "fetch-depth: 1"},
			{"fetch-depth: 2", "fetch-depth: 2\n          ref: main"},
			{"persist-credentials: false", "persist-credentials: true"},
			{"persist-credentials: false", "# credential persistence guard removed"},
			{"timeout-minutes:", "if: false\n    timeout-minutes:"},
			{"shell: bash", "shell: pwsh"},
			{"shell: bash", "if: false\n        shell: bash"},
			{"shell: bash", "continue-on-error: true\n        shell: bash"},
			{"run: node .github/scripts/pr-completeness-admission.cjs plan", "run: echo node .github/scripts/pr-completeness-admission.cjs plan"},
			{"admission.cjs admit", "admission.cjs plan"},
			{"contents: read", "contents: write"},
			{"if-no-files-found: error", "if-no-files-found: ignore"},
			{"plan.json", "steps.json"},
			{"steps.plan.outputs.dashboard", "steps.plan.outputs.tools"},
			{"merge-multiple: false", "merge-multiple: true"},
			{"github.run_attempt", "github.run_number"},
			{"name: ${{ inputs.pr_completeness_plan }}", "name: unrelated-plan"},
			{"name: ${{ inputs.pr_completeness_plan }}", "run-id: 123\n          name: ${{ inputs.pr_completeness_plan }}"},
			{"${{ inputs.dependency_results }}", "'{}'"},
			{"pull-requests: read", "pull-requests: none"},
			{"if: always() &&", "if:"},
		} {
			if !strings.Contains(string(source), mutation.old) {
				continue
			}
			mutated := strings.Replace(string(source), mutation.old, mutation.replacement, 1)
			if mutated == string(source) {
				t.Fatal("mutation did not change production workflow")
			}
			if err := checkAdmissionWorkflow([]byte(mutated), mode); err == nil {
				t.Errorf("%s accepted mutation %q -> %q", mode, mutation.old, mutation.replacement)
			}
		}
		alternative := strings.Replace(string(source), "fetch-depth: 2", "fetch-depth: '2'", 1)
		if err := checkAdmissionWorkflow([]byte(alternative), mode); err != nil {
			t.Fatalf("alternative-valid quoted checkout depth: %v", err)
		}
	}
}

func TestCompletenessAdmissionProductionCallers(t *testing.T) {
	output := runCompletenessNode(t, "--test", "--test-reporter=tap", "pr-completeness-admission.test.cjs")
	for _, metric := range []string{"# tests 6", "# pass 6", "# fail 0", "# cancelled 0", "# skipped 0"} { //nolint:misspell // Node TAP field name.
		if !strings.Contains(string(output), metric+"\n") && !strings.Contains(string(output), metric+"\r\n") {
			t.Fatalf("production admission callers lack %s evidence:\n%s", metric, output)
		}
	}
}
