package scripts

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

var goReceiptSteps = map[string][]string{
	"lint":       {"checkout", "lint_controls", "workflow_controls", "lint"},
	"linux":      {"checkout", "verify_modules", "download", "node", "guard", "inventory", "web_build", "format", "vet", "ci_tests", "no_launch", "binary", "integration"},
	"windows":    {"checkout", "verify_modules", "download", "node", "guard", "vet", "ci_tests", "shell_controls", "no_launch", "binary", "integration"},
	"native":     {"checkout", "discovery_controls", "guard", "native_tests", "coverage"},
	"linux-gate": {"requirements", "checkout", "coverage", "inventory"},
}

func checkGoReceiptJob(job releaseJob, label, guard, unit string, contracts ...string) error {
	ids := map[string]releaseStep{}
	var receipt, upload *releaseStep
	for i, step := range job.Steps {
		if step.ID != "" {
			if _, duplicate := ids[step.ID]; duplicate {
				return fmt.Errorf("%s repeats step id %s", label, step.ID)
			}
			ids[step.ID] = step
		}
		if step.Name == "Capture PR completeness receipt" {
			receipt = &job.Steps[i]
		}
		if step.Name == "Upload PR completeness receipt" {
			upload = &job.Steps[i]
		}
	}
	for _, contract := range contracts {
		for _, id := range goReceiptSteps[contract] {
			step, ok := ids[id]
			if !ok {
				return fmt.Errorf("%s lacks receipt step %s", label, id)
			}
			if step.ContinueOnError {
				return fmt.Errorf("%s receipt step %s cannot mask failure", label, id)
			}
		}
	}
	record := ids["checkout"]
	if record.If != guard || record.Run != `echo "commit=$(git rev-parse HEAD)" >> "$GITHUB_OUTPUT"` {
		return fmt.Errorf("%s must record the tested checkout only for completeness callers", label)
	}
	if receipt == nil || upload == nil || job.Steps[len(job.Steps)-1].Name != upload.Name ||
		receipt.ID != "receipt" || receipt.If != "always() && "+guard || !receipt.ContinueOnError ||
		receipt.Env["WAZA_COMPLETENESS_STEPS"] != "${{ toJSON(steps) }}" ||
		!strings.Contains(receipt.Run, `pr-completeness.cjs receipt \`+"\n"+`  "$receipt_dir/plan.json" go "`+unit+`"`) {
		return fmt.Errorf("%s must capture its go/%s receipt after every step", label, unit)
	}
	if upload.If != "always() && steps.receipt.outcome == 'success'" ||
		upload.With["name"] != "${{ steps.receipt.outputs.artifact_name }}" ||
		upload.With["if-no-files-found"] != "error" {
		return fmt.Errorf("%s must upload its exact receipt", label)
	}
	return nil
}

func checkGoCompletenessWiring(goCI, native []byte) error {
	var workflow, shards releaseWorkflow
	if err := yaml.Unmarshal(goCI, &workflow); err != nil {
		return err
	}
	if err := yaml.Unmarshal(native, &shards); err != nil {
		return err
	}
	pr := "github.event_name == 'pull_request'"
	matrixUnit := "${{ matrix.os == 'windows-latest' && 'windows' || 'linux' }}"
	jobs := workflow.Jobs
	for _, check := range []error{
		checkGoReceiptJob(jobs["lint"], "lint", pr, "lint", "lint"),
		checkGoReceiptJob(jobs["build-and-test"], "linux", pr, matrixUnit, "linux", "windows"),
		checkGoReceiptJob(jobs["build-and-test-windows"], "windows", pr, matrixUnit, "linux", "windows"),
		checkGoReceiptJob(jobs["linux-gate"], "linux-gate", pr, "linux-gate", "linux-gate"),
		checkGoReceiptJob(shards.Jobs["native"], "native", "inputs.pr_completeness", "native-${{ matrix.shard }}", "native"),
	} {
		if check != nil {
			return check
		}
	}
	if jobs["native-test-shards"].With["pr_completeness"] != "${{ github.event_name == 'pull_request' }}" {
		return fmt.Errorf("native shards must capture receipts for pull requests")
	}
	if jobs["pr-scope"].If != pr+" && "+goValidationIf || jobs["pr-scope"].Uses != "./.github/workflows/pr-completeness-plan.yml" {
		return fmt.Errorf("pull requests must determine a source-bound plan")
	}
	plan := "${{ needs.pr-scope.outputs.plan_artifact }}"
	callers := map[string]string{
		"dashboard": "web-ci.yml", "documentation": "site-ci.yml", "assurance": "grader-assurance-ci.yml",
		"container": "docker-ci.yml", "tools": "squad-tools-ci.yml", "runtime": "evidence-judge-ci.yml",
	}
	for job, file := range callers {
		caller := jobs[job]
		if caller.Uses != "./.github/workflows/"+file || !slices.Equal(caller.Needs, []string{"pr-scope"}) ||
			caller.With["pr_completeness_plan"] != plan || !strings.HasPrefix(caller.If, "needs.pr-scope.outputs.") {
			return fmt.Errorf("%s must run %s only when the plan selects it", job, file)
		}
	}
	completeness := jobs["completeness"]
	required := []string{"pr-scope", "lint", "build-and-test", "build-and-test-windows", "native-test-shards", "linux-gate"}
	for job := range callers {
		required = append(required, job)
	}
	for _, job := range required {
		if !slices.Contains(completeness.Needs, job) {
			return fmt.Errorf("completeness must wait for %s", job)
		}
	}
	if completeness.If != "${{ always() && "+pr+" && "+goValidationIf+" }}" ||
		completeness.Uses != "./.github/workflows/pr-completeness-admission.yml" ||
		completeness.With["pr_completeness_plan"] != plan ||
		completeness.With["dependency_results"] != "${{ toJSON(needs) }}" {
		return fmt.Errorf("completeness must always admit every dependency result for pull requests")
	}
	return nil
}

func TestGoCompletenessReceiptWiring(t *testing.T) {
	read := func(file string) string {
		source, err := os.ReadFile(filepath.Join("..", "workflows", file))
		if err != nil {
			t.Fatal(err)
		}
		return string(source)
	}
	goCI, native := read("go-ci.yml"), read("go-native-shards.yml")
	if err := checkGoCompletenessWiring([]byte(goCI), []byte(native)); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct {
		native           bool
		old, replacement string
	}{
		{false, "        id: lint_controls\n", ""},
		{false, "        id: integration\n", ""},
		{false, "        id: requirements\n", ""},
		{true, "        id: discovery_controls\n", ""},
		{true, "        if: always() && inputs.pr_completeness\n", "        if: inputs.pr_completeness\n"},
		{false, "if: always() && github.event_name == 'pull_request'\n        id: receipt", "if: github.event_name == 'pull_request'\n        id: receipt"},
		{false, "      pr_completeness: ${{ github.event_name == 'pull_request' }}", "      pr_completeness: false"},
		{false, "      dependency_results: ${{ toJSON(needs) }}", "      dependency_results: '{}'"},
		{false, "      dashboard, documentation,", "      documentation,"},
		{false, "if: ${{ always() && github.event_name == 'pull_request' && " + goValidationIf + " }}", "if: ${{ always() && github.event_name == 'pull_request' }}"},
		{false, "    if: github.event_name == 'pull_request' && " + goValidationIf + "\n", "    if: github.event_name == 'pull_request'\n"},
		{false, "        id: receipt\n        # A stale-base receipt failure must not fail this required check;\n        # admission still fails closed on the missing receipt.\n        continue-on-error: true\n", "        id: receipt\n"},
		{true, "        continue-on-error: true\n", ""},
		{false, "    uses: ./.github/workflows/docker-ci.yml\n    with:\n      pr_completeness_plan: ${{ needs.pr-scope.outputs.plan_artifact }}", "    uses: ./.github/workflows/docker-ci.yml"},
		{false, `"$receipt_dir/plan.json" go "lint"`, `"$receipt_dir/plan.json" go "linux"`},
	} {
		source := &goCI
		if mutation.native {
			source = &native
		}
		mutated := strings.Replace(*source, mutation.old, mutation.replacement, 1)
		if mutated == *source {
			t.Fatalf("mutation did not change workflow: %q", mutation.old)
		}
		g, n := goCI, native
		if mutation.native {
			n = mutated
		} else {
			g = mutated
		}
		if checkGoCompletenessWiring([]byte(g), []byte(n)) == nil {
			t.Fatalf("mutation was admitted: %q", mutation.old)
		}
	}
}
