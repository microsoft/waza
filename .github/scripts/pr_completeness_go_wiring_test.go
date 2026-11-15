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
	"lint":                 {"checkout", "lint_controls", "workflow_controls", "lint"},
	"linux":                {"checkout", "verify_modules", "download", "node", "guard", "inventory", "web_build", "format", "vet", "ci_tests", "no_launch", "binary", "integration"},
	"windows":              {"checkout", "verify_modules", "download", "node", "guard", "vet", "ci_tests", "shell_controls", "no_launch", "binary", "integration"},
	"native":               {"checkout", "discovery_controls", "guard", "native_tests", "coverage"},
	"linux-remaining":      {"checkout", "verify_modules", "download", "guard", "web_dependencies", "discovery_controls", "remaining_tests", "coverage_sources", "no_launch", "coverage", "observation"},
	"linux-coverage-merge": {"requirements", "checkout", "setup_go", "node", "sources", "download", "merge", "coverage"},
	"linux-gate":           {"requirements", "checkout", "coverage", "inventory"},
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

func checkGoCompletenessWiring(goCI, native, remaining []byte) error {
	var workflow, shards, remainingShards releaseWorkflow
	if err := yaml.Unmarshal(goCI, &workflow); err != nil {
		return err
	}
	if err := yaml.Unmarshal(native, &shards); err != nil {
		return err
	}
	if err := yaml.Unmarshal(remaining, &remainingShards); err != nil {
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
		checkGoReceiptJob(jobs["linux-coverage-merge"], "linux-coverage-merge", pr, "linux-coverage-merge", "linux-coverage-merge"),
		checkGoReceiptJob(shards.Jobs["native"], "native", "inputs.pr_completeness", "native-${{ matrix.shard }}", "native"),
		checkGoReceiptJob(remainingShards.Jobs["remaining"], "linux-remaining", "inputs.pr_completeness",
			"linux-remaining-${{ matrix.shard }}", "linux-remaining"),
	} {
		if check != nil {
			return check
		}
	}
	if jobs["native-test-shards"].With["pr_completeness"] != "${{ github.event_name == 'pull_request' }}" {
		return fmt.Errorf("native shards must capture receipts for pull requests")
	}
	if jobs["linux-remaining-shards"].With["pr_completeness"] != "${{ github.event_name == 'pull_request' }}" {
		return fmt.Errorf("remaining Linux shards must capture receipts for pull requests")
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
		return string(normalizeWorkflowLineEndings(source))
	}
	goCI, native, remaining := read("go-ci.yml"), read("go-native-shards.yml"), read("go-remaining-shards.yml")
	if err := checkGoCompletenessWiring([]byte(goCI), []byte(native), []byte(remaining)); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct {
		native, remaining bool
		old, replacement  string
	}{
		{old: "        id: lint_controls\n"},
		{old: "        id: integration\n"},
		{old: "        id: requirements\n"},
		{native: true, old: "        id: discovery_controls\n"},
		{native: true, old: "        if: always() && inputs.pr_completeness\n", replacement: "        if: inputs.pr_completeness\n"},
		{old: "if: always() && github.event_name == 'pull_request'\n        id: receipt", replacement: "if: github.event_name == 'pull_request'\n        id: receipt"},
		{old: "      pr_completeness: ${{ github.event_name == 'pull_request' }}", replacement: "      pr_completeness: false"},
		{old: "      dependency_results: ${{ toJSON(needs) }}", replacement: "      dependency_results: '{}'"},
		{old: "      dashboard, documentation,", replacement: "      documentation,"},
		{old: "if: ${{ always() && github.event_name == 'pull_request' && " + goValidationIf + " }}", replacement: "if: ${{ always() && github.event_name == 'pull_request' }}"},
		{old: "    if: github.event_name == 'pull_request' && " + goValidationIf + "\n", replacement: "    if: github.event_name == 'pull_request'\n"},
		{old: "        id: receipt\n        # A stale-base receipt failure must not fail this required check;\n        # admission still fails closed on the missing receipt.\n        continue-on-error: true\n", replacement: "        id: receipt\n"},
		{native: true, old: "        continue-on-error: true\n"},
		{remaining: true, old: "        id: remaining_tests\n"},
		{old: "    uses: ./.github/workflows/docker-ci.yml\n    with:\n      pr_completeness_plan: ${{ needs.pr-scope.outputs.plan_artifact }}", replacement: "    uses: ./.github/workflows/docker-ci.yml"},
		{old: `"$receipt_dir/plan.json" go "lint"`, replacement: `"$receipt_dir/plan.json" go "linux"`},
	} {
		source := &goCI
		if mutation.native {
			source = &native
		} else if mutation.remaining {
			source = &remaining
		}
		mutated := strings.Replace(*source, mutation.old, mutation.replacement, 1)
		if mutated == *source {
			t.Fatalf("mutation did not change workflow: %q", mutation.old)
		}
		g, n, r := goCI, native, remaining
		if mutation.native {
			n = mutated
		} else if mutation.remaining {
			r = mutated
		} else {
			g = mutated
		}
		if checkGoCompletenessWiring([]byte(g), []byte(n), []byte(r)) == nil {
			t.Fatalf("mutation was admitted: %q", mutation.old)
		}
	}
}
