package scripts

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

var siteFilterPaths = []string{
	"site/**", "docs/**", "README.md", "CHANGELOG.md", "version.txt", "registry.json", "go.mod",
	"cmd/waza/cmd_serve.go",
	"internal/embedded/**", "internal/models/outcome.go", "internal/models/schema_version.go",
	"internal/runneridentity/testdata/**", "internal/runnerunit/testdata/**", "internal/runnerterminal/testdata/**",
	"schemas/**", ".github/workflows/site-ci.yml", ".github/workflows/docker-ci.yml",
	".github/workflows/publish-runner-reference.yml", ".github/workflows/verify-runner-reference.yml", ".github/workflows/pages.yml",
}

var exampleFilterPaths = []string{
	"site/**", "docs/**", "examples/interop/**", "README.md", "registry.json", "cmd/waza/**", "internal/**",
	"schemas/**", "go.mod", "go.sum", "Makefile", "web/**", ".github/workflows/site-ci.yml",
}

var sitePushPaths = []string{
	"site/**", "docs/**", "examples/interop/**", "README.md", "CHANGELOG.md", "version.txt", "registry.json",
	"cmd/waza/**", "internal/**", "schemas/**", "go.mod", "go.sum", "Makefile", "web/**",
	".github/workflows/site-ci.yml", ".github/workflows/docker-ci.yml", ".github/workflows/publish-runner-reference.yml",
	".github/workflows/verify-runner-reference.yml", ".github/workflows/pages.yml",
}

type siteWorkflow struct {
	On map[string]struct {
		Branches []string
		Paths    []string
	}
	Permissions map[string]string
	Concurrency struct {
		Group            string
		CancelInProgress bool `yaml:"cancel-in-progress"`
	}
	Jobs map[string]releaseJob
}

func validateSiteWorkflow(source []byte) error {
	var workflow siteWorkflow
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		return err
	}
	if !reflect.DeepEqual(workflow.Permissions, map[string]string{"contents": "read", "pull-requests": "read"}) ||
		workflow.Concurrency.Group != "site-ci-${{ github.ref }}" || !workflow.Concurrency.CancelInProgress {
		return fmt.Errorf("site workflow must retain permissions and cancellation policy")
	}
	if len(workflow.On) != 2 || !reflect.DeepEqual(workflow.On["push"].Branches, []string{"main"}) ||
		!reflect.DeepEqual(workflow.On["push"].Paths, sitePushPaths) ||
		!reflect.DeepEqual(workflow.On["pull_request"].Branches, []string{"main"}) || len(workflow.On["pull_request"].Paths) != 0 {
		return fmt.Errorf("site push filters and unconditional main PR checks must remain unchanged")
	}
	changes, build, examples := workflow.Jobs["changes"], workflow.Jobs["build"], workflow.Jobs["examples"]
	if len(workflow.Jobs) != 3 || len(changes.Needs) != 0 || changes.If != "" || changes.TimeoutMinutes != 5 ||
		len(changes.Steps) != 2 || !strings.HasPrefix(changes.Steps[0].Uses, "actions/checkout@") {
		return fmt.Errorf("change detection must be a short independent checkout/filter prerequisite")
	}
	filter := changes.Steps[1]
	if filter.ID != "filter" || !strings.HasPrefix(filter.Uses, "dorny/paths-filter@") ||
		filter.If != "" || filter.ContinueOnError ||
		!reflect.DeepEqual(changes.Outputs, map[string]string{"site": "${{ steps.filter.outputs.site }}", "examples": "${{ steps.filter.outputs.examples }}"}) {
		return fmt.Errorf("change detection must expose both genuine filter outputs")
	}
	var paths map[string][]string
	if err := yaml.Unmarshal([]byte(filter.With["filters"]), &paths); err != nil {
		return err
	}
	if len(paths) != 2 || !reflect.DeepEqual(paths["site"], siteFilterPaths) || !reflect.DeepEqual(paths["examples"], exampleFilterPaths) {
		return fmt.Errorf("site and offline example change filters must retain their exact independent scopes")
	}
	if build.Name != "Build site" || examples.Name != "Offline documentation examples" ||
		!reflect.DeepEqual(build.Needs, []string{"changes"}) || !reflect.DeepEqual(examples.Needs, []string{"changes"}) ||
		build.If != "${{ !cancelled() }}" || examples.If != "needs.changes.outputs.examples == 'true' || github.event_name == 'push'" || //nolint:misspell // GitHub's exact status function.
		build.TimeoutMinutes != 15 || examples.TimeoutMinutes != 20 {
		return fmt.Errorf("stable site and example checks must run independently after successful filters")
	}
	steps := releaseSteps(build)
	gate := steps["Require successful change detection"]
	if len(build.Steps) == 0 || build.Steps[0].Name != gate.Name || gate.Name == "" || gate.If != "" || gate.ContinueOnError ||
		gate.Env["CHANGES_RESULT"] != "${{ needs.changes.result }}" || gate.Run != `test "$CHANGES_RESULT" = success` {
		return fmt.Errorf("required Build site check must fail, not skip or succeed, when change detection fails")
	}
	condition := "needs.changes.outputs.site == 'true' || github.event_name == 'push'"
	for _, name := range []string{"Setup Node", "Install dependencies", "Build site", "Typecheck chat and test public metadata",
		"Install Chromium for site regression tests", "Test site", "Test optional chat with mocked service and bot protection"} {
		step, ok := steps[name]
		if !ok || step.If != condition || step.ContinueOnError {
			return fmt.Errorf("site validation %q must retain PR/push selection and fail on error", name)
		}
	}
	if steps["Checkout"].Uses == "" || steps["Setup Node"].With["node-version"] != "24" ||
		steps["Install dependencies"].Run != "cd site && npm ci" ||
		steps["Build site"].Run != "cd site && npm run build" ||
		steps["Typecheck chat and test public metadata"].Run != "cd site && npm run test:chat-types && npm run test:chat-metadata && npm run test:chat-contract" ||
		steps["Install Chromium for site regression tests"].Run != "cd site && npx --no-install playwright install --with-deps chromium" ||
		steps["Test site"].Run != "cd site && npm run test:e2e -- --project=chromium --retries=0 --reporter=github,line,json --output=test-results/site" ||
		steps["Test site"].ID != "tests" || steps["Test site"].Env["CI"] != "true" ||
		steps["Test site"].Env["PLAYWRIGHT_JSON_OUTPUT_NAME"] != "test-results/site-report.json" ||
		steps["Test optional chat with mocked service and bot protection"].Run != "cd site && npm run test:private-chat && PLAYWRIGHT_JSON_OUTPUT_NAME=test-results/chat-report.json npm run test:chat -- --project=chromium --retries=0 --reporter=github,line,json --output=test-results/chat && PLAYWRIGHT_JSON_OUTPUT_NAME=test-results/chat-unavailable-report.json npm run test:chat-unavailable -- --project=chromium --retries=0 --reporter=github,line,json --output=test-results/chat-unavailable" ||
		steps["Test optional chat with mocked service and bot protection"].ID != "chat-tests" ||
		steps["Test optional chat with mocked service and bot protection"].Env["CI"] != "true" ||
		steps["Skip (no site changes)"].If != "needs.changes.outputs.site != 'true' && github.event_name == 'pull_request'" {
		return fmt.Errorf("site job must retain every build, chat, Chromium and non-site PR path")
	}
	evidence := steps["Upload site test evidence"]
	if evidence.If != "always() && (steps.tests.outcome != 'skipped' && steps.tests.outcome != '' || steps.chat-tests.outcome != 'skipped' && steps.chat-tests.outcome != '')" ||
		!strings.HasPrefix(evidence.Uses, "actions/upload-artifact@") ||
		evidence.With["path"] != "site/test-results/" || evidence.With["if-no-files-found"] != "error" {
		return fmt.Errorf("site browser evidence must survive failing browser tests")
	}
	exampleSteps := releaseSteps(examples)
	if len(examples.Steps) != 5 || exampleSteps["Checkout source and embedded runtime assets"].With["lfs"] != "true" ||
		exampleSteps["Setup Go"].With["go-version"] != "1.26" || exampleSteps["Setup Node"].With["node-version"] != "24" ||
		exampleSteps["Build current CLI with genuine dashboard assets"].Run != "make build" ||
		exampleSteps["Run extracted examples and fail-closed isolation regressions"].Run != `WAZA_BIN="$GITHUB_WORKSPACE/waza" npm --prefix site run test:examples` ||
		!reflect.DeepEqual(examples.Env, map[string]string{
			"CI": "1", "NO_COLOR": "1", "GOMAXPROCS": "2", "WAZA_NO_UPDATE_CHECK": "1",
			"ENABLE_COPILOT_TESTS": "false", "ENABLE_COPILOT_SDK_TESTS": "false", "ENABLE_LIVE_COPILOT_TESTS": "false",
			"COPILOT_CLI_PATH": "${{ github.workspace }}",
		}) {
		return fmt.Errorf("offline examples must retain the actual CLI build, extraction and isolation environment")
	}
	for _, step := range examples.Steps {
		if step.If != "" || step.ContinueOnError {
			return fmt.Errorf("offline example step %q must not skip or tolerate failures", step.Name)
		}
	}
	return nil
}

func TestSiteWorkflowParallelChecks(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "workflows", "site-ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateSiteWorkflow(source); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct{ name, old, new string }{
		{"examples-wait-on-browser", "    needs: [changes]\n    if: needs.changes.outputs.examples", "    needs: [build]\n    if: needs.changes.outputs.examples"},
		{"build-waits-on-examples", "    needs: [changes]\n    if: ${{ !cancelled() }}", "    needs: [changes, examples]\n    if: ${{ !cancelled() }}"}, //nolint:misspell // GitHub expression.
		{"missing-example-output", "examples: ${{ steps.filter.outputs.examples }}", "examples: 'true'"},
		{"stale-filter-step", "if: needs.changes.outputs.site", "if: steps.filter.outputs.site"},
		{"push-path-regression", "      - 'examples/interop/**'\n", ""},
		{"example-filter-regression", "              - 'examples/interop/**'\n", ""},
		{"site-filter-regression", "              - 'internal/models/outcome.go'\n", ""},
		{"missing-push-build", "needs.changes.outputs.site == 'true' || github.event_name == 'push'", "needs.changes.outputs.site == 'true'"},
		{"missing-push-examples", "needs.changes.outputs.examples == 'true' || github.event_name == 'push'", "needs.changes.outputs.examples == 'true'"},
		{"unstable-check-name", "name: Build site\n    needs:", "name: Browser build\n    needs:"},
		{"missing-browser-test", "npm run test:e2e -- --project=chromium --retries=0", "true"},
		{"ignored-browser-failure", "      - name: Test site\n", "      - name: Test site\n        continue-on-error: true\n"},
		{"missing-browser-report", "          PLAYWRIGHT_JSON_OUTPUT_NAME: test-results/site-report.json\n", ""},
		{"colliding-chat-reports", "PLAYWRIGHT_JSON_OUTPUT_NAME=test-results/chat-unavailable-report.json", "PLAYWRIGHT_JSON_OUTPUT_NAME=test-results/chat-report.json"},
		{"colliding-chat-output", "--output=test-results/chat-unavailable", "--output=test-results/chat"},
		{"ignored-private-chat-failure", "npm run test:private-chat &&", "npm run test:private-chat;"},
		{"tracked-media-as-evidence", "path: site/test-results/", "path: docs/images/"},
		{"missing-example-isolation", "      ENABLE_LIVE_COPILOT_TESTS: 'false'\n", ""},
		{"failed-filter-bypass", `test "$CHANGES_RESULT" = success`, ":"},
		{"unbound-filter-result", "${{ needs.changes.result }}", "success"},
		{"missing-required-check-gate", "    if: ${{ !cancelled() }}\n", ""}, //nolint:misspell // GitHub expression.
	} {
		t.Run(mutation.name, func(t *testing.T) {
			changed := strings.Replace(string(source), mutation.old, mutation.new, 1)
			if changed == string(source) {
				t.Fatal("mutation did not alter production site workflow")
			}
			if err := validateSiteWorkflow([]byte(changed)); err == nil {
				t.Fatal("unsafe site workflow mutation was admitted")
			}
		})
	}
	var workflow siteWorkflow
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		t.Fatal(err)
	}
	gate := releaseSteps(workflow.Jobs["build"])["Require successful change detection"]
	for _, result := range []string{"success", "failure", "cancelled", "skipped", ""} { //nolint:misspell // GitHub job conclusion.
		cmd := exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", gate.Run)
		cmd.Env = append(os.Environ(), "CHANGES_RESULT="+result)
		if err := cmd.Run(); (err == nil) != (result == "success") {
			t.Fatalf("change detection %q: required site check gate error = %v", result, err)
		}
	}
	testSiteFilterSelection(t, source)
}

func TestSiteChatEvidenceFailurePropagation(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "workflows", "site-ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow siteWorkflow
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		t.Fatal(err)
	}
	step := releaseSteps(workflow.Jobs["build"])["Test optional chat with mocked service and bot protection"]
	for _, failure := range []string{"", "test:private-chat", "test:chat", "test:chat-unavailable"} {
		t.Run("failure="+failure, func(t *testing.T) {
			dir := t.TempDir()
			for _, name := range []string{"bin", "site"} {
				if err := os.Mkdir(filepath.Join(dir, name), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			fixture := `#!/usr/bin/env bash
set -euo pipefail
printf '%s|%s\n' "$2" "${PLAYWRIGHT_JSON_OUTPUT_NAME:-none}" >> "$CALLS"
if [ "$2" = "$FAILURE" ]; then exit 29; fi
`
			if err := os.WriteFile(filepath.Join(dir, "bin", "npm"), []byte(fixture), 0o700); err != nil {
				t.Fatal(err)
			}
			calls := filepath.Join(dir, "calls")
			cmd := exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", step.Run)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "PATH="+filepath.Join(dir, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
				"CALLS="+calls, "FAILURE="+failure, "PLAYWRIGHT_JSON_OUTPUT_NAME=")
			output, err := cmd.CombinedOutput()
			if (err != nil) != (failure != "") {
				t.Fatalf("chat failure=%q: %v\n%s", failure, err, output)
			}
			data, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			expected := []string{"test:private-chat|none", "test:chat|test-results/chat-report.json",
				"test:chat-unavailable|test-results/chat-unavailable-report.json"}
			for i, line := range expected {
				if strings.HasPrefix(line, failure+"|") && failure != "" {
					expected = expected[:i+1]
					break
				}
			}
			if string(data) != strings.Join(expected, "\n")+"\n" {
				t.Fatalf("wrong chat commands/report identities after failure=%q:\n%s", failure, data)
			}
		})
	}
}

func testSiteFilterSelection(t *testing.T, source []byte) {
	t.Helper()
	var workflow siteWorkflow
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		t.Fatal(err)
	}
	var filters map[string][]string
	if err := yaml.Unmarshal([]byte(workflow.Jobs["changes"].Steps[1].With["filters"]), &filters); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path           string
		site, examples bool
	}{
		{"site/src/content/docs/index.mdx", true, true},
		{"docs/GUIDE.md", true, true},
		{"internal/models/outcome.go", true, true},
		{"cmd/waza/cmd_serve.go", true, true},
		{"internal/embedded/assets/runner", true, true},
		{"internal/execution/copilot.go", false, true},
		{"examples/interop/tasks/example.yaml", false, true},
		{"web/src/App.tsx", false, true},
		{"CHANGELOG.md", true, false},
		{".github/workflows/pages.yml", true, false},
		{"AGENTS.md", false, false},
		{"site-near-miss/file", false, false},
	} {
		for _, event := range []string{"pull_request", "push"} {
			for _, kind := range []string{"site", "examples"} {
				selected := false
				for _, pattern := range filters[kind] {
					selected = selected || tc.path == pattern ||
						(strings.HasSuffix(pattern, "/**") && strings.HasPrefix(tc.path, strings.TrimSuffix(pattern, "**")))
				}
				expected := tc.site
				if kind == "examples" {
					expected = tc.examples
				}
				if selected != expected {
					t.Fatalf("%s filter for %q: selected=%t, want=%t", kind, tc.path, selected, expected)
				}
				expression := releaseSteps(workflow.Jobs["build"])["Build site"].If
				if kind == "examples" {
					expression = workflow.Jobs["examples"].If
				}
				expression = strings.ReplaceAll(expression, "needs.changes.outputs."+kind, `"$SELECTED"`)
				expression = strings.ReplaceAll(expression, "github.event_name", `"$EVENT"`)
				cmd := exec.CommandContext(t.Context(), "bash", "-c", "[[ "+expression+" ]]")
				cmd.Env = append(os.Environ(), fmt.Sprintf("SELECTED=%t", selected), "EVENT="+event)
				if err := cmd.Run(); (err == nil) != (expected || event == "push") {
					t.Fatalf("%s %s path=%q: selection error=%v", event, kind, tc.path, err)
				}
			}
		}
	}
}
