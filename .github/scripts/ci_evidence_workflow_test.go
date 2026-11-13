package scripts

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func checkColdInput(declaration yaml.Node) error {
	var input struct {
		Inputs map[string]struct {
			Description string
			Type        string
			Default     yaml.Node
			Required    yaml.Node
		}
	}
	if err := declaration.Decode(&input); err != nil {
		return err
	}
	cold, exists := input.Inputs["cold_observation"]
	// Completeness callers may also pass their plan; it does not affect cold state.
	delete(input.Inputs, "pr_completeness_plan")
	delete(input.Inputs, "pr_completeness")
	if declaration.Kind != yaml.MappingNode || len(declaration.Content) != 2 ||
		!exists || len(input.Inputs) != 1 || cold.Description == "" || cold.Type != "boolean" ||
		cold.Default.Tag != "!!bool" || cold.Default.Value != "false" ||
		cold.Required.Tag != "!!bool" || cold.Required.Value != "false" {
		return fmt.Errorf("cold observation must be an optional typed boolean with explicit default false")
	}
	return nil
}

func checkColdWorkflow(source []byte, file string) error {
	var workflow struct {
		On   map[string]yaml.Node `yaml:"on"`
		Env  map[string]string
		Jobs map[string]releaseJob
	}
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		return err
	}
	events, jobID, requested := []string{"workflow_dispatch"}, "build-and-test",
		"${{ github.event_name == 'workflow_dispatch' && inputs.cold_observation }}"
	cache, nodeCache, prepareIf, scope := "${{ env.CI_EVIDENCE_COLD_REQUESTED != 'true' }}",
		"${{ env.CI_EVIDENCE_COLD_REQUESTED != 'true' && 'npm' || '' }}",
		"env.CI_EVIDENCE_COLD_REQUESTED == 'true'", "go-npm web"
	switch file {
	case "go-native-shards.yml":
		events, jobID, requested = []string{"workflow_call"}, "native", "${{ inputs.cold_observation }}"
		cache, nodeCache, prepareIf, scope = "${{ !inputs.cold_observation }}", "", "inputs.cold_observation", "go"
	case "web-ci.yml":
		events, jobID, requested = []string{"workflow_dispatch", "workflow_call"}, "build", "${{ inputs.cold_observation == true }}"
		prepareIf += " && (steps.filter.outputs.web == 'true' || github.event_name != 'pull_request' || inputs.pr_completeness_plan != '')"
	case "go-ci.yml":
		if workflow.Jobs["native-test-shards"].With["cold_observation"] != requested ||
			workflow.Jobs["build-and-test-windows"].Env["CI_EVIDENCE_COLD_REQUESTED"] != "" {
			return fmt.Errorf("cold mode must propagate to native Linux only, without changing Windows")
		}
	default:
		return fmt.Errorf("unsupported cold workflow")
	}
	for _, event := range events {
		if err := checkColdInput(workflow.On[event]); err != nil {
			return err
		}
	}
	job := workflow.Jobs[jobID]
	if file == "go-native-shards.yml" {
		steps := releaseSteps(job)
		fixture := steps["Test shard discovery and failure handling"]
		execute := steps["Execute exhaustive native test shard"]
		_, executeOverride := execute.Env["CI_EVIDENCE_COLD_REQUESTED"]
		if fixture.Run != "bash .github/scripts/go_test_shard_test.sh" ||
			fixture.If != "" || fixture.ContinueOnError || len(fixture.Env) != 1 ||
			fixture.Env["CI_EVIDENCE_COLD_REQUESTED"] != "false" ||
			executeOverride {
			return fmt.Errorf("only the synthetic shard fixture may disable inherited cold observation")
		}
	}
	if workflow.Env["CI_EVIDENCE_COLD_REQUESTED"] != "" ||
		job.Env["CI_EVIDENCE_COLD_REQUESTED"] != requested ||
		job.Env["CI_EVIDENCE_COLD_SETUP_CACHE_DISABLED"] != requested {
		return fmt.Errorf("cold state must be explicitly requested in its owning Linux job")
	}
	prepare, goSetup, nodeSetup := -1, -1, -1
	for i, step := range job.Steps {
		if strings.HasPrefix(step.Uses, "actions/cache") {
			return fmt.Errorf("cold workflow cannot restore or save ambient dependency caches")
		}
		if strings.HasPrefix(step.Uses, "actions/setup-go@") {
			goSetup = i
			if step.With["cache"] != cache {
				return fmt.Errorf("Go cache restore/save must be disabled only for explicit opt-in")
			}
		}
		if strings.HasPrefix(step.Uses, "actions/setup-node@") {
			nodeSetup = i
			if step.With["cache"] != nodeCache || step.With["package-manager-cache"] != cache {
				return fmt.Errorf("Node explicit and automatic caching must both be disabled only for opt-in")
			}
		}
		if step.Name == "Observe initially empty isolated stores" {
			_, prepareOverride := step.Env["CI_EVIDENCE_COLD_REQUESTED"]
			if prepare >= 0 || step.Run != "node .github/scripts/ci-evidence-cache.cjs prepare "+scope ||
				step.If != prepareIf || step.ContinueOnError ||
				prepareOverride {
				return fmt.Errorf("cold preparation must run once with the actual applicable stores and fail explicitly")
			}
			prepare = i
		}
	}
	if goSetup < 0 || nodeSetup < 0 || prepare <= goSetup || prepare <= nodeSetup {
		return fmt.Errorf("both toolchains must be ready before observing isolated stores")
	}
	for _, step := range job.Steps[:prepare] {
		// Completeness receipt bookkeeping runs only for PR completeness callers, never cold dispatches.
		if step.Run != "" && !map[string]bool{"inputs.pr_completeness_plan != ''": true,
			"github.event_name == 'pull_request'": true, "inputs.pr_completeness": true}[step.If] {
			return fmt.Errorf("initial empty observation must precede dependencies, offline guard, discovery and tests")
		}
	}
	return nil
}

func TestCIEvidenceColdWorkflowContracts(t *testing.T) {
	for _, file := range []string{"go-ci.yml", "go-native-shards.yml", "web-ci.yml"} {
		t.Run(file, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join("..", "workflows", file))
			if err != nil {
				t.Fatal(err)
			}
			if err := checkColdWorkflow(source, file); err != nil {
				t.Fatal(err)
			}
			cache := "${{ env.CI_EVIDENCE_COLD_REQUESTED != 'true' }}"
			if file == "go-native-shards.yml" {
				cache = "${{ !inputs.cold_observation }}"
			}
			for _, mutation := range [][2]string{
				{"default: false", "default: true"},
				{"type: boolean", "type: string"},
				{"package-manager-cache: " + cache, "package-manager-cache: true"},
				{"          cache: " + cache, "          cache: true"},
				{"node .github/scripts/ci-evidence-cache.cjs prepare", "echo cache-disabled"},
				{"      - name: Observe initially empty isolated stores\n", "      - name: Warm before observation\n        run: go mod download\n      - name: Observe initially empty isolated stores\n"},
				{"      - name: Observe initially empty isolated stores\n", "      - name: Observe initially empty isolated stores\n        continue-on-error: true\n"},
				{"      - name: Observe initially empty isolated stores\n", "      - name: Observe initially empty isolated stores\n        env:\n          CI_EVIDENCE_COLD_REQUESTED: ''\n"},
				{"      - name: Observe initially empty isolated stores\n", "      - name: Observe initially empty isolated stores\n        env:\n          CI_EVIDENCE_COLD_REQUESTED: null\n"},
			} {
				changed := strings.Replace(string(source), mutation[0], mutation[1], 1)
				if changed == string(source) || checkColdWorkflow([]byte(changed), file) == nil {
					t.Fatalf("unsafe cold workflow mutation admitted: %s", mutation[0])
				}
			}
			if file != "go-native-shards.yml" {
				changed := strings.Replace(string(source),
					"${{ env.CI_EVIDENCE_COLD_REQUESTED != 'true' && 'npm' || '' }}",
					"${{ inputs.cold_observation && '' || 'npm' }}", 1)
				if changed == string(source) || checkColdWorkflow([]byte(changed), file) == nil {
					t.Fatal("falsy cold-cache fallback admitted")
				}
			} else {
				for _, mutation := range [][2]string{
					{"        env:\n          CI_EVIDENCE_COLD_REQUESTED: 'false'\n", ""},
					{"          CI_EVIDENCE_COLD_REQUESTED: 'false'", "          CI_EVIDENCE_COLD_REQUESTED: 'true'"},
					{"          SHARD: ${{ matrix.shard }}", "          CI_EVIDENCE_COLD_REQUESTED: 'false'\n          SHARD: ${{ matrix.shard }}"},
					{"          SHARD: ${{ matrix.shard }}", "          CI_EVIDENCE_COLD_REQUESTED: ''\n          SHARD: ${{ matrix.shard }}"},
					{"          SHARD: ${{ matrix.shard }}", "          CI_EVIDENCE_COLD_REQUESTED: null\n          SHARD: ${{ matrix.shard }}"},
				} {
					changed := strings.Replace(string(source), mutation[0], mutation[1], 1)
					if changed == string(source) || checkColdWorkflow([]byte(changed), file) == nil {
						t.Fatalf("cold fixture isolation mutation admitted: %s", mutation[0])
					}
				}
			}
		})
	}
}

func TestCIEvidenceLintVisibilityContract(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "workflows", "go-ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	const command = "go test -count=1 -v ./.github/scripts ./.github/scripts/publish-runner-reference-guard ./.github/scripts/verify-runner-reference-guard ./.github/scripts/release-assets-helper"
	check := func(source []byte) error {
		var workflow releaseWorkflow
		if err := yaml.Unmarshal(source, &workflow); err != nil {
			return err
		}
		job := workflow.Jobs["lint"]
		if job.Name != goRequiredName("Lint", "'Lint'") || job.If != goValidationIf {
			return fmt.Errorf("existing lint job must run for every validation event")
		}
		selected := 0
		for _, step := range job.Steps {
			if step.Name != "Test Go workflow trigger selection" {
				continue
			}
			selected++
			if step.Run != command || step.If != "" || step.ContinueOnError || step.Uses != "" {
				return fmt.Errorf("lint must execute the same complete uncached package selection once with visible test output")
			}
		}
		if selected != 1 {
			return fmt.Errorf("lint must retain exactly one selected production contract step")
		}
		return nil
	}
	if err := check(source); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range [][2]string{
		{command, strings.Replace(command, " -v", "", 1)},
		{command, strings.Replace(command, " -v", " -v -run TestOnly", 1)},
		{command, strings.Replace(command, " -v", " -v -list .", 1)},
		{command, strings.Replace(command, " -count=1", "", 1)},
		{command, "go test -count=1 -v ./.github/scripts"},
		{"      - name: Test Go workflow trigger selection\n", "      - name: Test Go workflow trigger selection\n        if: false\n"},
		{"      - name: Test Go workflow trigger selection\n", "      - name: Test Go workflow trigger selection\n        continue-on-error: true\n"},
		{"'Lint' }}\n    if: " + goValidationIf + "\n", "'Lint' }}\n    if: false\n"},
		{"      - name: Test Go workflow trigger selection\n", "      # " + command + "\n      - name: Unselected contract decoy\n"},
		{"      - name: Test Go workflow trigger selection\n", "      - name: Test Go workflow trigger selection\n        run: " + command + "\n      - name: Test Go workflow trigger selection\n"},
	} {
		changed := strings.Replace(string(source), mutation[0], mutation[1], 1)
		if changed == string(source) || check([]byte(changed)) == nil {
			t.Fatalf("lint visibility mutation admitted: %s", mutation[1])
		}
	}
}

func TestCIEvidenceColdNativeFixtureCaller(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "workflows", "go-native-shards.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow releaseWorkflow
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		t.Fatal(err)
	}
	step := releaseSteps(workflow.Jobs["native"])["Test shard discovery and failure handling"]
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, isolated := range []bool{false, true} {
		cmd := exec.CommandContext(t.Context(), "bash", "-c", step.Run)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "CI_EVIDENCE_COLD_REQUESTED=true", "CI_EVIDENCE_COLD_SETUP_CACHE_DISABLED=true")
		if isolated {
			for name, value := range step.Env {
				cmd.Env = append(cmd.Env, name+"="+value)
			}
		}
		output, err := cmd.CombinedOutput()
		if !isolated {
			if err == nil {
				t.Fatal("unisolated synthetic fixture unexpectedly admitted cold observation")
			}
			continue
		}
		if err != nil {
			t.Fatalf("isolated production fixture caller: %v\n%s", err, output)
		}
		if !strings.Contains(string(output), "Native shard discovery, selection, failure propagation, and required artifact verification: PASS") {
			t.Fatalf("fixture caller did not reach its exhaustive assertions:\n%s", output)
		}
	}
}

func checkNativeEvidence(source, script []byte) error {
	var workflow releaseWorkflow
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		return err
	}
	job := workflow.Jobs["native"]
	steps := releaseSteps(job)
	if job.TimeoutMinutes != 40 || workflow.Permissions["contents"] != "read" {
		return fmt.Errorf("native evidence must preserve deadline and permissions")
	}
	setup := steps["Setup Node for sanitized evidence"]
	execute := steps["Execute exhaustive native test shard"]
	upload := steps["Retain sanitized native execution evidence"]
	coverage := steps["Retain coverage and test-selection evidence"]
	if setup.With["node-version"] != "22" || setup.If != "" ||
		execute.ID != "native_tests" || execute.If != "" || execute.ContinueOnError ||
		execute.Env["GO_CACHE_HIT"] != "${{ steps.go.outputs.cache-hit }}" ||
		!strings.Contains(execute.Run, `bash .github/scripts/go_test_shard.sh "$SHARD" "coverage-native-$SHARD.txt"`) ||
		!strings.Contains(execute.Run, `"$COPILOT_CLI_PATH.invoked"`) {
		return fmt.Errorf("native execution, runtime guard and observed cache must remain")
	}
	if upload.If != "always() && steps.native_tests.outcome != 'skipped' && steps.native_tests.outcome != ''" ||
		!strings.HasPrefix(upload.Uses, "actions/upload-artifact@") ||
		upload.With["name"] != "ci-evidence-go-native-${{ github.run_id }}-${{ github.run_attempt }}-${{ matrix.shard }}" ||
		upload.With["path"] != "coverage-native-${{ matrix.shard }}.txt.evidence.json" ||
		upload.With["retention-days"] != "14" {
		return fmt.Errorf("sanitized artifacts must retain failure/attempt/shard identity and bounded retention")
	}
	if coverage.With["path"] != "coverage-native-${{ matrix.shard }}.txt\ncoverage-native-${{ matrix.shard }}.txt.all\ncoverage-native-${{ matrix.shard }}.txt.selected\n" {
		return fmt.Errorf("coverage must retain exact coverage/discovery/selection without duplicating sanitized artifacts")
	}
	for _, required := range []string{
		`node "$scripts/ci-evidence.cjs" init-go "$profile.evidence.json"`,
		`go test -race -covermode=atomic -list . "$package"`,
		`go test -count=1 -json -v -race -timeout=30m -coverprofile="$profile" -covermode=atomic`,
		`-run "^($pattern)$" "$package" |`,
		`CI_EVIDENCE_INVENTORY_SCOPE=names node "$scripts/ci-evidence.cjs" go`,
		`"$profile.evidence.json" "$profile.all" "$profile.selected"`,
	} {
		if !strings.Contains(string(script), required) {
			return fmt.Errorf("native capture missing %s", required)
		}
	}
	if strings.Count(string(script), "go test ") != 2 {
		return fmt.Errorf("native must have one discovery and one execution, not duplicate suites")
	}
	return nil
}

func checkBrowserEvidence(source []byte) error {
	var workflow releaseWorkflow
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		return err
	}
	job := workflow.Jobs["build"]
	steps := releaseSteps(job)
	condition := "steps.filter.outputs.web == 'true' || github.event_name != 'pull_request' || inputs.pr_completeness_plan != ''"
	discover, execute := steps["Discover dashboard tests without execution"], steps["Test dashboard"]
	sanitize, upload := steps["Sanitize dashboard execution evidence"], steps["Retain sanitized dashboard execution evidence"]
	if workflow.Permissions["contents"] != "read" || workflow.Permissions["pull-requests"] != "read" || len(workflow.Permissions) != 2 || job.TimeoutMinutes != 15 {
		return fmt.Errorf("browser capture must not change permissions or deadline")
	}
	if discover.If != condition || discover.ID != "discovery" || discover.Run != "npx --no-install playwright test --project=chromium --retries=0 --list --reporter=json" ||
		discover.Env["PLAYWRIGHT_JSON_OUTPUT_NAME"] != ".ci-evidence-discovery.json" ||
		execute.If != condition || execute.ID != "tests" ||
		execute.Env["PLAYWRIGHT_JSON_OUTPUT_NAME"] != ".ci-evidence-report.json" ||
		execute.Run != "npx --no-install playwright test --project=chromium --retries=0 --reporter=github,line,json" {
		return fmt.Errorf("browser requires nonexecuting discovery and one unchanged retry-disabled execution")
	}
	always := "always() && steps.discovery.outcome != 'skipped' && steps.discovery.outcome != ''"
	if sanitize.If != always || sanitize.Run != "node .github/scripts/ci-evidence.cjs browser web/.ci-evidence-report.json ci-evidence-browser.json web/.ci-evidence-discovery.json" ||
		sanitize.Env["BROWSER_DISCOVERY_OUTCOME"] != "${{ steps.discovery.outcome }}" ||
		sanitize.Env["BROWSER_EXECUTION_OUTCOME"] != "${{ steps.tests.outcome }}" ||
		upload.If != always || !strings.HasPrefix(upload.Uses, "actions/upload-artifact@") ||
		upload.With["name"] != "ci-evidence-browser-${{ github.run_id }}-${{ github.run_attempt }}" ||
		upload.With["path"] != "ci-evidence-browser.json" || upload.With["retention-days"] != "14" {
		return fmt.Errorf("browser evidence must sanitize actual report, retain failure identity and bounded storage")
	}
	ordinaryUpload := steps["Upload dashboard test evidence"]
	if ordinaryUpload.If != "always() && steps.tests.outcome != 'skipped' && steps.tests.outcome != ''" ||
		ordinaryUpload.With["path"] != "web/test-results/\n" ||
		ordinaryUpload.With["include-hidden-files"] != "true" || ordinaryUpload.With["if-no-files-found"] != "error" {
		return fmt.Errorf("ordinary browser artifacts must retain only current-run Playwright outputs, fail closed, and exclude raw reporter inputs")
	}
	producerTest := steps["Test current browser reporter evidence contract"]
	if producerTest.If != condition || producerTest.Run != "node --test .github/scripts/ci-evidence-playwright.test.cjs" {
		return fmt.Errorf("current Playwright producer contract must execute after install")
	}
	budget := steps["Check dashboard wall-time budget"]
	if budget.If != "steps.tests.outcome == 'success'" || budget.Run != "node .github/scripts/ci-evidence-dashboard-budget.cjs web/.ci-evidence-report.json" {
		return fmt.Errorf("successful dashboard runs must enforce the wall-time budget from the actual report")
	}
	var commands string
	for _, step := range job.Steps {
		if step.ContinueOnError {
			return fmt.Errorf("browser evidence must not tolerate failing validation")
		}
		commands += step.Run + "\n"
	}
	if strings.Count(commands, "playwright test ") != 2 {
		return fmt.Errorf("browser must have one discovery and one suite execution")
	}
	if strings.Count(string(source), "'"+".github/scripts/ci-evidence*"+"'") != 2 {
		return fmt.Errorf("collector changes must select push and PR browser coverage")
	}
	return nil
}

func checkOrdinaryEvidence(source []byte, name string) error {
	var workflow releaseWorkflow
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		return err
	}
	isRelease := name == "release-validation.yml"
	if len(workflow.Permissions) != 1 || workflow.Permissions["contents"] != "read" {
		return fmt.Errorf("%s: evidence must preserve existing read contents permissions", name)
	}
	jobs := []string{"validate"}
	if name == "go-ci.yml" {
		jobs = []string{"build-and-test", "build-and-test-windows"}
	} else if isRelease {
		jobs = []string{"build-test", "windows-test"}
	}
	for _, id := range jobs {
		job := workflow.Jobs[id]
		steps := releaseSteps(job)
		execute, upload := steps["Execute Tests"], steps["Retain sanitized Go execution evidence"]
		if isRelease && id == "windows-test" {
			execute = steps["Execute complete Windows tests"]
		}
		if job.TimeoutMinutes != 60 || execute.ID != "ci_tests" ||
			execute.Env["GO_CACHE_HIT"] != "${{ steps.go.outputs.cache-hit }}" ||
			execute.Env["NODE_CACHE_HIT"] != "${{ steps.node.outputs.cache-hit }}" {
			return fmt.Errorf("%s: capture must bind actual execution and observed caches", id)
		}
		goSetup, nodeSetup := false, false
		for _, step := range job.Steps {
			if strings.HasPrefix(step.Uses, "actions/setup-go@") && step.ID == "go" {
				goSetup = true
			}
			if strings.HasPrefix(step.Uses, "actions/setup-node@") && step.ID == "node" {
				nodeSetup = true
			}
		}
		if !goSetup || !nodeSetup {
			return fmt.Errorf("%s: observed cache outputs must refer to setup steps", id)
		}
		if upload.If != "always() && steps.ci_tests.outcome != 'skipped' && steps.ci_tests.outcome != ''" ||
			!strings.HasPrefix(upload.Uses, "actions/upload-artifact@") ||
			upload.With["name"] != "ci-evidence-go-run-${{ github.run_id }}-${{ github.run_attempt }}-${{ github.job }}" ||
			upload.With["path"] != "coverage.txt.evidence.json" || upload.With["retention-days"] != "14" ||
			upload.ContinueOnError {
			return fmt.Errorf("%s: retain sanitized attempt/job evidence after failures", id)
		}
	}
	return nil
}

func TestCIEvidenceWorkflowContracts(t *testing.T) {
	native, err := os.ReadFile(filepath.Join("..", "workflows", "go-native-shards.yml"))
	if err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile("go_test_shard.sh")
	if err != nil {
		t.Fatal(err)
	}
	browser, err := os.ReadFile(filepath.Join("..", "workflows", "web-ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := checkNativeEvidence(native, script); err != nil {
		t.Fatal(err)
	}
	if err := checkBrowserEvidence(browser); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"go-ci.yml", "squad-preview.yml", "release-validation.yml"} {
		source, err := os.ReadFile(filepath.Join("..", "workflows", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := checkOrdinaryEvidence(source, name); err != nil {
			t.Fatal(err)
		}
		for _, mutation := range [][2]string{
			{"        id: ci_tests\n", ""},
			{"        id: node\n", ""},
			{"GO_CACHE_HIT: ${{ steps.go.outputs.cache-hit }}", "GO_CACHE_HIT: false"},
			{"if: always() && steps.ci_tests.outcome", "if: success() && steps.ci_tests.outcome"},
			{"${{ github.run_attempt }}-${{ github.job }}", "1-${{ github.job }}"},
			{"${{ github.job }}", "fixed-job"},
			{"path: coverage.txt.evidence.json", "path: coverage.txt*"},
			{"retention-days: 14", "retention-days: 90"},
		} {
			changed := strings.Replace(string(source), mutation[0], mutation[1], 1)
			if changed == string(source) || checkOrdinaryEvidence([]byte(changed), name) == nil {
				t.Fatalf("%s: ordinary evidence mutation admitted: %s", name, mutation[0])
			}
		}
		if name == "release-validation.yml" {
			start := strings.Index(string(source), "\n  windows-test:")
			if start < 0 {
				t.Fatal("canonical release Windows branch is missing")
			}
			for _, mutation := range [][2]string{
				{"        id: ci_tests\n", ""},
				{"        id: node\n", ""},
				{"GO_CACHE_HIT: ${{ steps.go.outputs.cache-hit }}", "GO_CACHE_HIT: false"},
				{"if: always() && steps.ci_tests.outcome", "if: success() && steps.ci_tests.outcome"},
				{"path: coverage.txt.evidence.json", "path: coverage.txt*"},
			} {
				changed := string(source[:start]) + strings.Replace(string(source[start:]), mutation[0], mutation[1], 1)
				if changed == string(source) || checkOrdinaryEvidence([]byte(changed), name) == nil {
					t.Fatalf("Windows release evidence mutation admitted: %s", mutation[0])
				}
			}
		}
	}
	for _, mutation := range [][2]string{
		{"if: always() && steps.native_tests.outcome", "if: success() && steps.native_tests.outcome"},
		{"${{ github.run_attempt }}", "1"},
		{"retention-days: 14", "retention-days: 90"},
		{"node-version: '22'", "node-version: '20'"},
		{"contents: read", "contents: write"},
		{"            coverage-native-${{ matrix.shard }}.txt.selected\n", ""},
	} {
		changed := strings.Replace(string(native), mutation[0], mutation[1], 1)
		if changed == string(native) || checkNativeEvidence([]byte(changed), script) == nil {
			t.Fatalf("native workflow mutation admitted: %s", mutation[0])
		}
	}
	for _, mutation := range [][2]string{
		{"-count=1 -json", "-json"},
		{"-count=1 -json", "-count=1"},
		{`-run "^($pattern)$"`, `-run "^TestOnly$"`},
		{`"$profile.evidence.json" "$profile.all" "$profile.selected"`, `"$profile.evidence.json" "$profile.selected" "$profile.selected"`},
	} {
		changed := strings.Replace(string(script), mutation[0], mutation[1], 1)
		if changed == string(script) || checkNativeEvidence(native, []byte(changed)) == nil {
			t.Fatalf("native script mutation admitted: %s", mutation[0])
		}
	}
	for _, mutation := range [][2]string{
		{"--list --reporter=json", "--reporter=json"},
		{"--retries=0 --reporter=github,line,json", "--retries=1 --reporter=github,line,json"},
		{"if: always() && steps.discovery.outcome", "if: success() && steps.discovery.outcome"},
		{"if: always() && steps.discovery.outcome", "if: always() && steps.tests.outcome"},
		{"BROWSER_DISCOVERY_OUTCOME: ${{ steps.discovery.outcome }}", "BROWSER_DISCOVERY_OUTCOME: success"},
		{"BROWSER_EXECUTION_OUTCOME: ${{ steps.tests.outcome }}", "BROWSER_EXECUTION_OUTCOME: success"},
		{"ci-evidence-browser.json web/.ci-evidence-discovery.json", "ci-evidence-browser.json"},
		{"${{ github.run_attempt }}", "1"},
		{"          path: ci-evidence-browser.json", "          path: web/test-results/report.json"},
		{"PLAYWRIGHT_JSON_OUTPUT_NAME: .ci-evidence-report.json", "PLAYWRIGHT_JSON_OUTPUT_NAME: test-results/report.json"},
		{"            web/test-results/\n", "            web/\n"},
		{"            web/test-results/\n", "            web/test-results/\n            web/.ci-evidence-report.json\n"},
		{"            web/test-results/\n", "            web/test-results/\n            web/dist/\n"},
		{"          include-hidden-files: true\n", ""},
		{"include-hidden-files: true\n          if-no-files-found: error", "include-hidden-files: true\n          if-no-files-found: warn"},
		{"retention-days: 14", "retention-days: 90"},
		{"if: steps.tests.outcome == 'success'", "if: false"},
		{"ci-evidence-dashboard-budget.cjs web/.ci-evidence-report.json", "ci-evidence-dashboard-budget.cjs /dev/null"},
	} {
		changed := strings.Replace(string(browser), mutation[0], mutation[1], 1)
		if changed == string(browser) || checkBrowserEvidence([]byte(changed)) == nil {
			t.Fatalf("browser workflow mutation admitted: %s", mutation[0])
		}
	}
}

func TestCIEvidenceCollectorProductionContracts(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "node", "--test", "ci-evidence.test.cjs", "ci-evidence-dashboard-budget.test.cjs")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("structured evidence production contracts: %v\n%s", err, output)
	}
	t.Logf("production Node contracts:\n%s", output)
}
