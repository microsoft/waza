package scripts

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func validateCheckpointDeliveryPhases(source, spec []byte) error {
	var workflow struct {
		On struct {
			Dispatch struct {
				Inputs map[string]struct {
					Type     string
					Default  string
					Required bool
					Options  []string
				}
			} `yaml:"workflow_dispatch"`
		}
		Jobs map[string]releaseJob
	}
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		return err
	}
	phase := workflow.On.Dispatch.Inputs["delivery_phase"]
	if phase.Type != "choice" || phase.Default != "preview" || !phase.Required ||
		!reflect.DeepEqual(phase.Options, []string{"preview", "final"}) {
		return fmt.Errorf("delivery requires explicit preview/final phases with preview default")
	}
	job := workflow.Jobs["build"]
	if job.TimeoutMinutes != 30 || job.Env["WAZA_CHECKPOINT_DELIVERY"] != "${{ inputs.delivery_phase || 'preview' }}" {
		return fmt.Errorf("delivery must retain a bounded job and explicit test phase")
	}
	steps := releaseSteps(job)
	previous := -1
	for _, name := range []string{
		"Build clean-stamped CLI for checkpoint API browser coverage",
		"Test compiled checkpoint interruption and failed restore",
		"Build dashboard", "Build matched embedded checkpoint CLI",
		"Test matched embedded checkpoint interruption and failed restore", "Test dashboard",
		"Require executed embedded checkpoint browser control",
	} {
		found := false
		for index, step := range job.Steps {
			if step.Name == name {
				if found || index <= previous || step.ContinueOnError {
					return fmt.Errorf("delivery gate %q must execute once in production order", name)
				}
				found, previous = true, index
			}
		}
		if !found {
			return fmt.Errorf("missing delivery gate %q", name)
		}
	}
	preview := "steps.filter.outputs.web == 'true' || github.event_name != 'pull_request'"
	for _, name := range []string{"Build dashboard", "Test compiled checkpoint interruption and failed restore", "Test dashboard"} {
		if steps[name].If != preview {
			return fmt.Errorf("preview must not be blocked by final-only gate %q", name)
		}
		before := steps["Build clean-stamped CLI for checkpoint API browser coverage"]
		if !strings.Contains(before.Run, "vcs.modified=false") ||
			!strings.Contains(before.Run, `vcs.revision=%s' "$(git rev-parse HEAD)")`) ||
			steps["Test compiled checkpoint interruption and failed restore"].Run != "node --test web/e2e/checkpoint-cli-process.test.mjs" {
			return fmt.Errorf("preview must preserve genuine owning API/process controls")
		}
	}
	gate := steps["Build matched embedded checkpoint CLI"]
	process := steps["Test matched embedded checkpoint interruption and failed restore"]
	if gate.If != "inputs.delivery_phase == 'final'" || gate.ID != "embedded" ||
		process.If != gate.If ||
		process.Run != "node --test web/e2e/checkpoint-cli-process.test.mjs" ||
		process.Env["WAZA_E2E_CLI"] != "${{ runner.temp }}/waza-checkpoint-embedded-e2e" {
		return fmt.Errorf("final must execute the real process controls with its postfrontend binary")
	}
	if !strings.HasPrefix(gate.Run, "git diff --exit-code -- web/dist/index.html web/dist/favicon.svg\n") ||
		!strings.Contains(gate.Run, "go build -o \"${RUNNER_TEMP}/waza-checkpoint-embedded-e2e\" ./cmd/waza") ||
		!strings.Contains(gate.Run, `go version -m "${RUNNER_TEMP}/waza-checkpoint-embedded-e2e"`) ||
		!strings.Contains(gate.Run, `vcs.revision=%s' "$(git rev-parse HEAD)")`) ||
		!strings.Contains(gate.Run, "vcs.modified=false") {
		return fmt.Errorf("final must require committed generated index and genuine owning VCS metadata")
	}
	for _, forbidden := range []string{"-buildvcs=false", "git restore", "git checkout", "git reset", "npm run build"} {
		if strings.Contains(gate.Run, forbidden) {
			return fmt.Errorf("final must not waive, restore, or rebuild its admitted source: %s", forbidden)
		}
	}
	if steps["Test dashboard"].Env["WAZA_E2E_CLI"] !=
		"${{ inputs.delivery_phase == 'final' && format('{0}/waza-checkpoint-embedded-e2e', runner.temp) || format('{0}/waza-checkpoint-e2e', runner.temp) }}" {
		return fmt.Errorf("browser must consume its actual phase-specific owning binary")
	}
	if steps["Require executed embedded checkpoint browser control"].If != "${{ !cancelled() && inputs.delivery_phase == 'final' }}" { //nolint:misspell // GitHub's status function.
		return fmt.Errorf("final browser receipt must be checked after failure without running after cancellation")
	}
	for _, assertion := range []string{
		`test.skip(process.env.WAZA_CHECKPOINT_DELIVERY !== "final"`,
		`await page.goto(` + "`${base}/#/lab/runner`",
		`expect(await index.body()).toEqual(readFileSync(new URL("index.html", dist)))`,
		`const favicon = await request.get(` + "`${base}/favicon.svg`)",
		`expect(favicon.status()).toBe(200)`,
		`expect(await favicon.body()).toEqual(readFileSync(new URL("favicon.svg", dist)))`,
		`expect(await response.body(), asset).toEqual(readFileSync(physical))`,
		`expect(resumed.submission.engine_calls).toBe(0)`,
	} {
		if !strings.Contains(string(spec), assertion) {
			return fmt.Errorf("embedded browser control is missing actual assertion %q", assertion)
		}
	}
	return nil
}

func TestCheckpointFinalReceiptRequiresActualUnskippedExecution(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "workflows", "web-ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow siteWorkflow
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		t.Fatal(err)
	}
	run := releaseSteps(workflow.Jobs["build"])["Require executed embedded checkpoint browser control"].Run
	script, found := strings.CutPrefix(run, "node --input-type=module <<'NODE'\n")
	if !found {
		t.Fatal("receipt control must run the actual Node script")
	}
	script, found = strings.CutSuffix(script, "\nNODE\n")
	if !found {
		t.Fatal("receipt script terminator is missing")
	}
	valid := `{"suites":[{"specs":[{"file":"checkpoint-embedded.spec.ts","tests":[{"status":"expected","results":[{"status":"passed"}]}]}]}]}`
	for _, control := range []struct {
		name, input string
		pass        bool
	}{
		{"actual-pass", valid, true},
		{"not-selected", `{"suites":[]}`, false},
		{"skipped", strings.Replace(valid, `"expected"`, `"skipped"`, 1), false},
		{"failed", strings.Replace(valid, `"passed"`, `"failed"`, 1), false},
		{"no-execution", strings.Replace(valid, `[{"status":"passed"}]`, `[]`, 1), false},
		{"retried", strings.Replace(valid, `[{"status":"passed"}]`, `[{"status":"failed"},{"status":"passed"}]`, 1), false},
	} {
		t.Run(control.name, func(t *testing.T) {
			directory := t.TempDir()
			resultDir := filepath.Join(directory, "web", "test-results")
			if err := os.MkdirAll(resultDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(resultDir, "report.json"), []byte(control.input), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "node", "--input-type=module", "-e", script)
			command.Dir = directory
			output, err := command.CombinedOutput()
			if (err == nil) != control.pass {
				t.Fatalf("actual receipt script pass=%t, want %t: %v\n%s", err == nil, control.pass, err, output)
			}
		})
	}
}

func TestCheckpointPreviewAndFinalDeliverySelectActualControls(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "workflows", "web-ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	spec, err := os.ReadFile(filepath.Join("..", "..", "web", "e2e", "checkpoint-embedded.spec.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCheckpointDeliveryPhases(source, spec); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct{ name, before, after string }{
		{"missing-clean-stamp", "vcs.modified=false", "vcs.modified=true"},
		{"wrong-owning-revision", "git rev-parse HEAD", "git rev-parse HEAD~1"},
		{"preview-deadlock", "if: inputs.delivery_phase == 'final'", "if: inputs.delivery_phase != 'final'"},
		{"ignored-index-change", "git diff --exit-code -- web/dist/index.html web/dist/favicon.svg", "git diff -- web/dist/index.html"},
		{"wrong-process-binary", "WAZA_E2E_CLI: ${{ runner.temp }}/waza-checkpoint-embedded-e2e", "WAZA_E2E_CLI: ${{ runner.temp }}/waza-checkpoint-e2e"},
		{"missing-final-process", "run: node --test web/e2e/checkpoint-cli-process.test.mjs", "run: echo skipped"},
		{"browser-stale-binary", "inputs.delivery_phase == 'final' && format", "inputs.delivery_phase == 'preview' && format"},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			changed := strings.Replace(string(source), mutation.before, mutation.after, 1)
			if changed == string(source) {
				t.Fatal("mutation did not change actual production workflow")
			}
			if err := validateCheckpointDeliveryPhases([]byte(changed), spec); err == nil {
				t.Fatal("changed delivery gate must fail closed")
			}
		})
	}
	for _, assertion := range []string{
		`test.skip(process.env.WAZA_CHECKPOINT_DELIVERY !== "final"`,
		`expect(await response.body(), asset).toEqual(readFileSync(physical))`,
		`const favicon = await request.get(` + "`${base}/favicon.svg`)",
		`expect(favicon.status()).toBe(200)`,
		`expect(await favicon.body()).toEqual(readFileSync(new URL("favicon.svg", dist)))`,
	} {
		changed := strings.Replace(string(spec), assertion, "", 1)
		if changed == string(spec) {
			t.Fatal("mutation did not change actual embedded spec")
		}
		if err := validateCheckpointDeliveryPhases(source, []byte(changed)); err == nil {
			t.Fatal("missing phase or physical embedding assertion must be rejected")
		}
	}
}
