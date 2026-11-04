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
	if guard := findStep(publish.Steps, "Revalidate protected main immediately before push"); guard == nil ||
		!hasRun([]runnerReferenceStep{*guard}, "go run ./.github/scripts/publish-runner-reference-guard") {
		t.Fatal("publisher must revalidate current main immediately before the image push")
	}
	if guard := findStep(publish.Steps, "Revalidate protected main after push and attestation"); guard == nil ||
		!hasRun([]runnerReferenceStep{*guard}, "go run ./.github/scripts/publish-runner-reference-guard") {
		t.Fatal("publisher must reject an image if main moves during push or attestation")
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
	stepIndex := func(steps []runnerReferenceStep, name string) int {
		for i, step := range steps {
			if step.Name == name {
				return i
			}
		}
		return -1
	}
	if stepIndex(publish.Steps, "Revalidate protected main immediately before push") >= stepIndex(publish.Steps, build.Name) ||
		stepIndex(publish.Steps, attestation.Name) >= stepIndex(publish.Steps, "Revalidate protected main after push and attestation") {
		t.Fatal("main must be revalidated around publication and attestation")
	}
	summary := findStep(publish.Steps, "Record pushed candidate digest")
	if summary == nil || summary.Shell != "bash" ||
		!strings.Contains(summary.Run, `>> "$GITHUB_STEP_SUMMARY"`) ||
		!strings.Contains(summary.Run, "not accepted for use") ||
		summary.Env["CANDIDATE_SHA"] != "${{ needs.validate-candidate.outputs.candidate_sha }}" ||
		summary.Env["IMAGE_DIGEST"] != "${{ steps.build.outputs.digest }}" {
		t.Fatalf("push summary must defer acceptance until public digest verification: %#v", summary)
	}
	failureSummary := findStep(publish.Steps, "Mark pushed digest as unaccepted when finalization fails")
	if failureSummary == nil || !strings.Contains(failureSummary.If, "steps.build.outcome == 'success'") ||
		!strings.Contains(failureSummary.Run, "No rollback is claimed") {
		t.Fatalf("partial push or attestation failure must be explicitly unaccepted: %#v", failureSummary)
	}

	for _, workflowName := range []string{
		"go-ci.yml",
		"squad-release.yml",
		"squad-insider-release.yml",
	} {
		workflow, err := os.ReadFile(filepath.Join("..", "workflows", workflowName))
		if err != nil {
			t.Fatal(err)
		}
		wantCommand := "go test -count=1 ./.github/scripts ./.github/scripts/publish-runner-reference-guard ./.github/scripts/verify-runner-reference-guard"
		if strings.Contains(workflowName, "release") {
			wantCommand = "go test -count=1 -timeout=10m ./.github/scripts ./.github/scripts/publish-runner-reference-guard ./.github/scripts/verify-runner-reference-guard"
		}
		if !strings.Contains(string(workflow), wantCommand) {
			t.Errorf("%s must explicitly select both hidden image guard packages' negative-path tests", workflowName)
		}
	}
}

func TestRunnerReferenceVerifierIsManualAnonymousAndUsesPublishedDigest(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "workflows", "verify-runner-reference.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow runnerReferenceWorkflow
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		t.Fatal(err)
	}
	if workflow.On.Kind != yaml.MappingNode || len(workflow.On.Content) != 2 ||
		workflow.On.Content[0].Value != "workflow_dispatch" {
		t.Fatalf("verifier triggers = %v; want workflow_dispatch only", workflow.On.Content)
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
	if len(dispatch.Inputs) != 2 ||
		!dispatch.Inputs["source_revision"].Required || dispatch.Inputs["source_revision"].Type != "string" ||
		!dispatch.Inputs["image_digest"].Required || dispatch.Inputs["image_digest"].Type != "string" {
		t.Fatalf("verifier inputs = %#v; want required source revision and immutable digest", dispatch.Inputs)
	}
	if workflow.Permissions["contents"] != "read" || len(workflow.Permissions) != 1 {
		t.Fatalf("verifier workflow permissions = %#v; want contents: read only", workflow.Permissions)
	}
	if len(workflow.Jobs) != 2 {
		t.Fatalf("verifier jobs = %d; want isolated provenance and container-test jobs", len(workflow.Jobs))
	}
	attestationJob, ok := workflow.Jobs["validate-public-digest"]
	if !ok || attestationJob.TimeoutMinutes <= 0 || attestationJob.RunsOn != "ubuntu-24.04" ||
		attestationJob.Permissions["contents"] != "read" || attestationJob.Permissions["attestations"] != "read" ||
		attestationJob.Permissions["actions"] != "read" ||
		attestationJob.Permissions["packages"] != "" || len(attestationJob.Permissions) != 3 {
		t.Fatalf("provenance job must be bounded and read-only, with no package credentials: %#v", attestationJob)
	}
	job, ok := workflow.Jobs["verify-public-image"]
	if !ok || job.Needs != "validate-public-digest" || job.TimeoutMinutes <= 0 ||
		job.RunsOn != "ubuntu-24.04" || job.Permissions["contents"] != "read" ||
		job.Permissions["attestations"] != "" || job.Permissions["packages"] != "" ||
		len(job.Permissions) != 1 {
		t.Fatalf("container test must be isolated from provenance credentials and bounded: %#v", job)
	}

	pinnedAction := regexp.MustCompile(`^[^@]+@[0-9a-f]{40}$`)
	for _, step := range append(append([]runnerReferenceStep{}, attestationJob.Steps...), job.Steps...) {
		if step.Uses != "" && !pinnedAction.MatchString(step.Uses) {
			t.Errorf("action %q is not pinned to a full commit SHA", step.Uses)
		}
		if strings.Contains(step.Run, "${{") {
			t.Errorf("shell command in %q interpolates an Actions expression; pass values through env", step.Name)
		}
	}
	if !hasRun(attestationJob.Steps, "git fetch --no-tags origin refs/heads/main") ||
		!hasRun(attestationJob.Steps, "go run ./.github/scripts/verify-runner-reference-guard") ||
		!hasRun(job.Steps, "node --test examples/runner/container/published-image-test.mjs") {
		t.Fatal("verifier must validate inputs/main ancestry and run the published-image production caller")
	}
	if runner := findStep(job.Steps, "Require native Linux AMD64 runner"); runner == nil ||
		runner.If != "runner.os != 'Linux' || runner.arch != 'X64'" {
		t.Fatalf("verifier must reject non-native Linux AMD64 runners: %#v", runner)
	}
	guard := findStep(attestationJob.Steps, "Validate source and immutable digest inputs")
	if guard == nil || guard.Env["SOURCE_SHA"] != "${{ inputs.source_revision }}" ||
		guard.Env["IMAGE_DIGEST"] != "${{ inputs.image_digest }}" {
		t.Fatalf("verifier must pass untrusted inputs through environment variables: %#v", guard)
	}
	sourceCheckout := findStep(job.Steps, "Check out validated source fixtures and probes")
	if sourceCheckout == nil || sourceCheckout.With["ref"] != "${{ needs.validate-public-digest.outputs.source_sha }}" ||
		sourceCheckout.With["path"] != "candidate" || sourceCheckout.With["persist-credentials"] != "false" {
		t.Fatalf("production callers must come from the validated source without persisted credentials: %#v", sourceCheckout)
	}
	goSetup := findStep(job.Steps, "Set up Go")
	if goSetup == nil || goSetup.With["go-version-file"] != "candidate/go.mod" ||
		goSetup.With["cache"] != "false" {
		t.Fatalf("native probe binaries must use the validated source's Go toolchain without cache writes: %#v", goSetup)
	}
	if findStep(job.Steps, "Log in to GHCR") != nil {
		t.Fatal("anonymous verification must not authenticate to GHCR")
	}
	attestation := findStep(attestationJob.Steps, "Verify signed provenance for the exact digest")
	if attestation == nil || attestation.Env["GH_TOKEN"] != "${{ secrets.GITHUB_TOKEN }}" ||
		!strings.Contains(attestation.Run, "gh attestation verify") ||
		!strings.Contains(attestation.Run, "--source-digest \"$SOURCE_REVISION\"") ||
		!strings.Contains(attestation.Run, "--signer-digest \"$SOURCE_REVISION\"") ||
		!strings.Contains(attestation.Run, `--format json > "$RUNNER_TEMP/verified-runner-provenance.json"`) ||
		!strings.Contains(attestation.Run, "--signer-workflow microsoft/waza/.github/workflows/publish-runner-reference.yml") {
		t.Fatalf("verifier must validate the exact source-bound publisher attestation: %#v", attestation)
	}
	finalization := findStep(attestationJob.Steps, "Require successful publisher run and finalization")
	if finalization == nil || finalization.Env["GH_TOKEN"] != "${{ secrets.GITHUB_TOKEN }}" ||
		finalization.Env["SOURCE_SHA"] != "${{ steps.guard.outputs.source_sha }}" ||
		finalization.Env["IMAGE_DIGEST"] != "${{ steps.guard.outputs.image_digest }}" ||
		finalization.Run != `go run ./.github/scripts/verify-runner-reference-guard --finalized-publication "$RUNNER_TEMP/verified-runner-provenance.json"` {
		t.Fatalf("verifier must bind the verified digest to its successful publisher attempt: %#v", finalization)
	}
	callers := findStep(job.Steps, "Verify anonymous immutable digest and production callers")
	if callers == nil || callers.Env["GH_TOKEN"] != "" || callers.Env["GITHUB_TOKEN"] != "" {
		t.Fatalf("anonymous Docker pull must not receive the attestation API token: %#v", callers)
	}
	if !hasRun(job.Steps, "Anonymous public pull and production-caller smoke passed") {
		t.Fatal("success summary must record anonymous pull and caller evidence")
	}

	testSource, err := os.ReadFile(filepath.Join("..", "..", "examples", "runner", "container", "published-image-test.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"ghcr.io/microsoft/waza-runner-reference",
		"command(['pull'",
		"--platform=linux/amd64",
		"DOCKER_CONFIG",
		"org.opencontainers.image.revision",
		"org.opencontainers.image.source",
		"linux/amd64",
		"native-bundle-probe",
		"reply-after-positive",
		"hang_stopped",
	} {
		if !strings.Contains(string(testSource), required) {
			t.Errorf("published-image test does not assert %q", required)
		}
	}
	for _, secret := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GHCR_TOKEN", "DOCKER_AUTH_CONFIG"} {
		if !strings.Contains(string(testSource), secret) {
			t.Errorf("published-image test must remove %s from the anonymous Docker environment", secret)
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
