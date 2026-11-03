package scripts

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type runnerReferenceWorkflow struct {
	On          yaml.Node                     `yaml:"on"`
	Permissions map[string]string             `yaml:"permissions"`
	Jobs        map[string]runnerReferenceJob `yaml:"jobs"`
}

type runnerReferenceJob struct {
	RunsOn         string                `yaml:"runs-on"`
	TimeoutMinutes int                   `yaml:"timeout-minutes"`
	Needs          string                `yaml:"needs"`
	Permissions    map[string]string     `yaml:"permissions"`
	Outputs        map[string]string     `yaml:"outputs"`
	Steps          []runnerReferenceStep `yaml:"steps"`
}

type runnerReferenceStep struct {
	Name  string            `yaml:"name"`
	Uses  string            `yaml:"uses"`
	Run   string            `yaml:"run"`
	If    string            `yaml:"if"`
	Shell string            `yaml:"shell"`
	With  map[string]string `yaml:"with"`
	Env   map[string]string `yaml:"env"`
}

func TestRunnerReferencePublisherIsManualAndLeastPrivilege(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "workflows", "publish-runner-reference.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow runnerReferenceWorkflow
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		t.Fatal(err)
	}
	if workflow.On.Kind != yaml.MappingNode || len(workflow.On.Content) != 2 ||
		workflow.On.Content[0].Value != "workflow_dispatch" {
		t.Fatalf("publisher triggers = %v; want workflow_dispatch only", workflow.On.Content)
	}
	var dispatch struct {
		Inputs map[string]struct {
			Required bool   `yaml:"required"`
			Type     string `yaml:"type"`
		} `yaml:"inputs"`
	}
	if err := workflow.On.Content[1].Decode(&dispatch); err != nil {
		t.Fatal(err)
	}
	input, ok := dispatch.Inputs["candidate_sha"]
	if !ok || !input.Required || input.Type != "string" {
		t.Fatalf("candidate_sha input = %#v; want required string", input)
	}
	if len(workflow.Jobs) != 2 {
		t.Fatalf("publisher jobs = %d; want validation and publication jobs", len(workflow.Jobs))
	}
	if workflow.Permissions["contents"] != "read" || len(workflow.Permissions) != 1 {
		t.Fatalf("workflow permissions = %#v; want contents: read only", workflow.Permissions)
	}

	validate, ok := workflow.Jobs["validate-candidate"]
	if !ok {
		t.Fatal("missing validation job")
	}
	publish, ok := workflow.Jobs["publish"]
	if !ok {
		t.Fatal("missing publication job")
	}
	if validate.TimeoutMinutes <= 0 || publish.TimeoutMinutes <= 0 ||
		validate.RunsOn != "ubuntu-24.04" || publish.RunsOn != "ubuntu-24.04" {
		t.Fatal("every publisher job must have an explicit timeout and native Linux AMD64 runner")
	}
	if validate.Permissions["contents"] != "read" || len(validate.Permissions) != 1 {
		t.Fatalf("validation permissions = %#v; want contents: read only", validate.Permissions)
	}
	if publish.Needs != "validate-candidate" {
		t.Fatalf("publish needs %q; want validate-candidate", publish.Needs)
	}
	if publish.Permissions["contents"] != "read" || publish.Permissions["packages"] != "write" ||
		publish.Permissions["attestations"] != "write" || publish.Permissions["id-token"] != "write" ||
		len(publish.Permissions) != 4 {
		t.Fatalf("publication permissions = %#v; want only the read, package-publish and signed-attestation scopes", publish.Permissions)
	}

	allSteps := append(append([]runnerReferenceStep{}, validate.Steps...), publish.Steps...)
	pinnedAction := regexp.MustCompile(`^[^@]+@[0-9a-f]{40}$`)
	for _, step := range allSteps {
		if step.Uses != "" && !pinnedAction.MatchString(step.Uses) {
			t.Errorf("action %q is not pinned to a full commit SHA", step.Uses)
		}
		if strings.Contains(step.Run, "${{") {
			t.Errorf("shell command in %q interpolates an Actions expression; pass values through env", step.Name)
		}
	}
	for _, step := range validate.Steps {
		if strings.Contains(step.Env["GHCR_TOKEN"], "GITHUB_TOKEN") ||
			strings.Contains(step.Env["TOKEN"], "GITHUB_TOKEN") {
			t.Errorf("validation step %q must not receive a package token", step.Name)
		}
	}

	if !hasRun(validate.Steps, "go run ./.github/scripts/publish-runner-reference-guard") ||
		!hasRun(publish.Steps, "go run ./.github/scripts/publish-runner-reference-guard") {
		t.Fatal("both jobs must fail closed on the candidate guard")
	}
	if guard := findStep(validate.Steps, "Validate event, source and exact candidate"); guard == nil ||
		guard.Env["EVENT_SHA"] != "${{ github.sha }}" || guard.Env["CANDIDATE_SHA"] != "${{ inputs.candidate_sha }}" {
		t.Fatalf("validation must compare the requested candidate with the dispatch source: %#v", guard)
	}
	if guard := findStep(publish.Steps, "Revalidate exact candidate before publishing"); guard == nil ||
		guard.Env["EVENT_SHA"] != "${{ github.sha }}" ||
		guard.Env["CANDIDATE_SHA"] != "${{ needs.validate-candidate.outputs.candidate_sha }}" {
		t.Fatalf("publication must revalidate the candidate against the dispatch source: %#v", guard)
	}
	if !hasRun(validate.Steps, "git fetch --no-tags origin refs/heads/main") ||
		!hasRun(publish.Steps, "git fetch --no-tags origin refs/heads/main") {
		t.Fatal("both jobs must compare against current main")
	}
	if !hasRun(publish.Steps, "git status --porcelain --untracked-files=all") {
		t.Fatal("publication must reject a non-clean source checkout")
	}
	if runner := findStep(publish.Steps, "Require native Linux AMD64 runner"); runner == nil ||
		runner.If != "runner.os != 'Linux' || runner.arch != 'X64'" {
		t.Fatalf("publication must reject a non-native Linux AMD64 runner: %#v", runner)
	}
	for _, step := range []*runnerReferenceStep{
		findStep(validate.Steps, "Check out workflow candidate"),
		findStep(publish.Steps, "Check out validated candidate"),
	} {
		if step == nil || step.With["persist-credentials"] != "false" {
			if step == nil {
				t.Fatal("candidate jobs must check out source without persisted credentials")
			}
			t.Errorf("checkout %q must not persist a token in the build tree", step.Name)
		}
	}

	var build *runnerReferenceStep
	var login *runnerReferenceStep
	for i := range publish.Steps {
		step := &publish.Steps[i]
		if strings.HasPrefix(step.Uses, "docker/build-push-action@") {
			build = step
		}
		if step.Name == "Log in to GHCR" {
			login = step
		}
	}
	if build == nil {
		t.Fatal("publication must use the pinned Docker build-push action")
	}
	if build.With["target"] != "native-reference" || build.With["platforms"] != "linux/amd64" ||
		build.With["push"] != "true" || build.With["provenance"] != "mode=max" ||
		build.With["sbom"] != "true" {
		t.Fatalf("build settings do not describe the required native AMD64 provenance build: %#v", build.With)
	}
	if build.With["context"] != "." {
		t.Fatalf("build must use the LFS-hydrated, validated checkout: %q", build.With["context"])
	}
	if !strings.Contains(build.With["tags"], "${{ needs.validate-candidate.outputs.candidate_sha }}") ||
		!strings.Contains(build.With["tags"], "${{ github.run_id }}") ||
		!strings.Contains(build.With["tags"], "${{ github.run_attempt }}") ||
		strings.Contains(build.With["tags"], "latest") {
		t.Fatalf("publication tag is not unique to the validated source/run: %q", build.With["tags"])
	}
	if !strings.Contains(build.With["labels"], "org.opencontainers.image.source=https://github.com/microsoft/waza") ||
		!strings.Contains(build.With["labels"], "org.opencontainers.image.revision=${{ needs.validate-candidate.outputs.candidate_sha }}") {
		t.Fatalf("build labels do not bind source and revision: %q", build.With["labels"])
	}
	if login == nil || login.Env["GHCR_TOKEN"] != "${{ secrets.GITHUB_TOKEN }}" {
		t.Fatal("only the publication job should expose its scoped GITHUB_TOKEN to GHCR login")
	}
	var attestation *runnerReferenceStep
	for i := range publish.Steps {
		step := &publish.Steps[i]
		if strings.HasPrefix(step.Uses, "actions/attest-build-provenance@") {
			attestation = step
		}
	}
	if attestation == nil || attestation.With["subject-name"] != "ghcr.io/microsoft/waza-runner-reference" ||
		attestation.With["subject-digest"] != "${{ steps.build.outputs.digest }}" ||
		attestation.With["push-to-registry"] != "true" {
		t.Fatalf("signed provenance must bind the image package to the exact build digest: %#v", attestation)
	}
	summary := findStep(publish.Steps, "Record published digest")
	if summary == nil || summary.Shell != "bash" ||
		!strings.Contains(summary.Run, `>> "$GITHUB_STEP_SUMMARY"`) ||
		summary.Env["CANDIDATE_SHA"] != "${{ needs.validate-candidate.outputs.candidate_sha }}" ||
		summary.Env["IMAGE_DIGEST"] != "${{ steps.build.outputs.digest }}" {
		t.Fatalf("publication summary must record the validated source and immutable build digest: %#v", summary)
	}

	for _, workflowName := range []string{
		"go-ci.yml",
		"squad-ci.yml",
		"squad-release.yml",
		"squad-insider-release.yml",
	} {
		workflow, err := os.ReadFile(filepath.Join("..", "workflows", workflowName))
		if err != nil {
			t.Fatal(err)
		}
		wantCommand := "go test -count=1 ./.github/scripts ./.github/scripts/publish-runner-reference-guard"
		if strings.Contains(workflowName, "release") {
			wantCommand = "go test -count=1 -timeout=10m ./.github/scripts ./.github/scripts/publish-runner-reference-guard"
		}
		if !strings.Contains(string(workflow), wantCommand) {
			t.Errorf("%s must explicitly select the hidden publisher guard package's negative-path tests", workflowName)
		}
	}
}

func findStep(steps []runnerReferenceStep, name string) *runnerReferenceStep {
	for i := range steps {
		if steps[i].Name == name {
			return &steps[i]
		}
	}
	return nil
}

func hasRun(steps []runnerReferenceStep, text string) bool {
	for _, step := range steps {
		if strings.Contains(step.Run, text) {
			return true
		}
	}
	return false
}
