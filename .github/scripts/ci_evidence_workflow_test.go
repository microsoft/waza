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
	condition := "steps.filter.outputs.web == 'true' || github.event_name != 'pull_request'"
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
		ordinaryUpload.With["path"] != "web/test-results/\nweb/dist/\ndocs/images/\nsite/public/images/explore/\n" {
		return fmt.Errorf("ordinary browser artifacts must not include new raw reporter inputs")
	}
	producerTest := steps["Test current browser reporter evidence contract"]
	if producerTest.If != condition || producerTest.Run != "node --test .github/scripts/ci-evidence-playwright.test.cjs" {
		return fmt.Errorf("current Playwright producer contract must execute after install")
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
	if len(workflow.Permissions) != 1 || workflow.Permissions["contents"] != "read" {
		return fmt.Errorf("ordinary evidence must preserve read-only permissions")
	}
	jobs := []string{"validate"}
	if name == "go-ci.yml" {
		jobs = []string{"build-and-test", "build-and-test-windows"}
	}
	for _, id := range jobs {
		job := workflow.Jobs[id]
		steps := releaseSteps(job)
		execute, upload := steps["Execute Tests"], steps["Retain sanitized Go execution evidence"]
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
	for _, name := range []string{"go-ci.yml", "squad-preview.yml"} {
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
			{"${{ github.run_attempt }}", "1"},
			{"${{ github.job }}", "fixed-job"},
			{"path: coverage.txt.evidence.json", "path: coverage.txt*"},
			{"retention-days: 14", "retention-days: 90"},
		} {
			changed := strings.Replace(string(source), mutation[0], mutation[1], 1)
			if changed == string(source) || checkOrdinaryEvidence([]byte(changed), name) == nil {
				t.Fatalf("%s: ordinary evidence mutation admitted: %s", name, mutation[0])
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
		{"retention-days: 14", "retention-days: 90"},
	} {
		changed := strings.Replace(string(browser), mutation[0], mutation[1], 1)
		if changed == string(browser) || checkBrowserEvidence([]byte(changed)) == nil {
			t.Fatalf("browser workflow mutation admitted: %s", mutation[0])
		}
	}
}

func TestCIEvidenceCollectorProductionContracts(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "node", "--test", "ci-evidence.test.cjs")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("structured evidence production contracts: %v\n%s", err, output)
	}
}
