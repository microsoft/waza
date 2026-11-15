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
	Needs          yaml.Node             `yaml:"needs"`
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

func TestRunnerReferencePublisherIsReleaseOnlyAndLeastPrivilege(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "workflows", "publish-runner-reference.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow runnerReferenceWorkflow
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		t.Fatal(err)
	}
	if workflow.On.Kind != yaml.MappingNode || len(workflow.On.Content) != 2 ||
		workflow.On.Content[0].Value != "workflow_call" {
		t.Fatalf("publisher triggers = %v; want workflow_call only", workflow.On.Content)
	}
	var call struct {
		Inputs map[string]struct {
			Required bool   `yaml:"required"`
			Type     string `yaml:"type"`
		} `yaml:"inputs"`
	}
	if err := workflow.On.Content[1].Decode(&call); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"release_stage", "source_sha", "version"} {
		input, ok := call.Inputs[name]
		if !ok || !input.Required || input.Type != "string" {
			t.Fatalf("%s input = %#v; want required string", name, input)
		}
	}
	if len(workflow.Jobs) != 2 {
		t.Fatalf("publisher jobs = %d; want validation and publication jobs", len(workflow.Jobs))
	}
	if workflow.Permissions["contents"] != "read" || len(workflow.Permissions) != 1 {
		t.Fatalf("workflow permissions = %#v; want contents: read only", workflow.Permissions)
	}

	validate, ok := workflow.Jobs["validate-release"]
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
	if publish.Needs.Kind != yaml.ScalarNode || publish.Needs.Value != "validate-release" {
		t.Fatalf("publish needs %v; want validate-release", publish.Needs)
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

	if strings.Contains(string(source), "workflow_dispatch") || strings.Contains(string(source), "candidate_sha") {
		t.Fatal("the public image publisher must not expose a manual candidate-publish path")
	}
	if guard := findStep(validate.Steps, "Validate event, source, and exact tag"); guard == nil ||
		guard.Env["EVENT_NAME"] != "${{ github.event_name }}" ||
		guard.Env["REF"] != "${{ github.ref }}" ||
		guard.Env["RELEASE_STAGE"] != "${{ inputs.release_stage }}" ||
		guard.Env["SOURCE_SHA"] != "${{ inputs.source_sha }}" ||
		guard.Env["VERSION_INPUT"] != "${{ inputs.version }}" ||
		!strings.Contains(guard.Run, `test "$EVENT_NAME" = push`) ||
		!strings.Contains(guard.Run, `refs/tags/v`) ||
		!strings.Contains(guard.Run, `git rev-parse "${REF}^{commit}"`) {
		t.Fatalf("publication must require the exact tag release source: %#v", guard)
	}
	if missingTag := findStep(publish.Steps, "Refuse to replace an existing release image tag"); missingTag == nil ||
		!strings.Contains(missingTag.Run, "skopeo inspect --authfile") ||
		!strings.Contains(missingTag.Run, "Could not prove the release image tag is absent") {
		t.Fatal("release publication must refuse to replace a versioned image tag")
	}
	if !hasRun(publish.Steps, "git status --porcelain --untracked-files=all") {
		t.Fatal("publication must reject a non-clean source checkout")
	}
	if runner := findStep(publish.Steps, "Require native Linux AMD64 runner"); runner == nil ||
		runner.If != "runner.os != 'Linux' || runner.arch != 'X64'" {
		t.Fatalf("publication must reject a non-native Linux AMD64 runner: %#v", runner)
	}
	for _, step := range []*runnerReferenceStep{
		findStep(validate.Steps, "Check out release tag"),
		findStep(publish.Steps, "Check out validated release source"),
	} {
		if step == nil || step.With["persist-credentials"] != "false" {
			if step == nil {
				t.Fatal("release jobs must check out source without persisted credentials")
			}
			t.Errorf("checkout %q must not persist a token in the build tree", step.Name)
		}
	}

	var download, qualified, login, publishImage, imageAttestation, verifyProvenance, inventoryAttestation, verifyInventory *runnerReferenceStep
	for i := range publish.Steps {
		step := &publish.Steps[i]
		switch step.Name {
		case "Download exact qualification evidence":
			download = step
		case "Validate exact qualified runner image and inventory":
			qualified = step
		case "Log in to GHCR":
			login = step
		case "Publish the exact qualified OCI image digest":
			publishImage = step
		case "Attest published runner image provenance":
			imageAttestation = step
		case "Verify published runner image provenance offline":
			verifyProvenance = step
		case "Attest published runner image inventory":
			inventoryAttestation = step
		case "Verify published image inventory attestation offline":
			verifyInventory = step
		}
		if strings.HasPrefix(step.Uses, "docker/build-push-action@") {
			t.Fatal("release publication must not rebuild an image after qualification")
		}
	}
	if download == nil || download.Uses != "actions/download-artifact@9000827ccba6bdab643e8b6fd33ac0654aef8333" ||
		download.With["name"] != "release-qualification-evidence-${{ needs.validate-release.outputs.source_sha }}" ||
		download.With["path"] != "qualification" {
		t.Fatalf("publisher must consume the exact same-run qualification artifact: %#v", download)
	}
	if qualified == nil || !strings.Contains(qualified.Run, "runner-image.oci.tar") ||
		!strings.Contains(qualified.Run, "runner-image.digest") ||
		!strings.Contains(qualified.Run, "runner-image.archive.sha256") ||
		!strings.Contains(qualified.Run, "--source-digest \"$SOURCE_SHA\"") {
		t.Fatalf("publisher must verify the qualification artifact and its source-bound image attestations: %#v", qualified)
	}
	if login == nil || login.Env["GHCR_TOKEN"] != "${{ secrets.GITHUB_TOKEN }}" {
		t.Fatal("only the publication job should expose its scoped GITHUB_TOKEN to GHCR login")
	}
	if publishImage == nil || !strings.Contains(publishImage.Run, "skopeo copy --preserve-digests") ||
		!strings.Contains(publishImage.Run, "oci-archive:qualification/runner-image.oci.tar") ||
		!strings.Contains(publishImage.Run, "test \"$published_digest\" = \"$IMAGE_DIGEST\"") ||
		!strings.Contains(publishImage.Run, "test \"$remote_digest\" = \"$IMAGE_DIGEST\"") {
		t.Fatalf("publication must preserve and verify the qualified image digest: %#v", publishImage)
	}
	if imageAttestation == nil || !strings.HasPrefix(imageAttestation.Uses, "actions/attest@") ||
		imageAttestation.With["subject-name"] != "ghcr.io/microsoft/waza-runner-reference" ||
		imageAttestation.With["subject-digest"] != "${{ steps.qualified.outputs.image_digest }}" ||
		imageAttestation.With["push-to-registry"] != "true" ||
		imageAttestation.With["create-storage-record"] != "false" ||
		imageAttestation.With["sbom-path"] != "" {
		t.Fatalf("published image provenance must be signed for the exact digest without widening permissions: %#v", imageAttestation)
	}
	if verifyProvenance == nil ||
		!strings.Contains(verifyProvenance.Run, "gh attestation verify \"oci://ghcr.io/microsoft/waza-runner-reference@$IMAGE_DIGEST\"") ||
		!strings.Contains(verifyProvenance.Run, "--signer-workflow \"$SIGNER_WORKFLOW\"") ||
		!strings.Contains(verifyProvenance.Run, "--signer-digest \"$SOURCE_SHA\"") ||
		!strings.Contains(verifyProvenance.Run, "--source-digest \"$SOURCE_SHA\"") ||
		!strings.Contains(verifyProvenance.Run, "--custom-trusted-root") ||
		!strings.Contains(verifyProvenance.Run, `--bundle "$PROVENANCE_BUNDLE"`) {
		t.Fatalf("publisher must verify its source-bound image provenance offline: %#v", verifyProvenance)
	}
	if guard := findStep(publish.Steps, "Revalidate release tag after publication"); guard == nil ||
		!strings.Contains(guard.Run, `git fetch --no-tags origin "$GITHUB_REF"`) ||
		!strings.Contains(guard.Run, `git rev-parse "${GITHUB_REF}^{commit}"`) {
		t.Fatal("publisher must revalidate the exact release tag after publication")
	}
	if inventoryAttestation == nil || !strings.HasPrefix(inventoryAttestation.Uses, "actions/attest@") ||
		inventoryAttestation.With["subject-name"] != "ghcr.io/microsoft/waza-runner-reference" ||
		inventoryAttestation.With["subject-digest"] != "${{ steps.qualified.outputs.image_digest }}" ||
		inventoryAttestation.With["sbom-path"] != "qualification/runner-inventory.cdx.json" ||
		inventoryAttestation.With["push-to-registry"] != "true" ||
		inventoryAttestation.With["create-storage-record"] != "false" {
		t.Fatalf("qualified component inventory must be published as an attestation for the exact digest: %#v", inventoryAttestation)
	}
	if verifyInventory == nil || !strings.Contains(verifyInventory.Run, "gh attestation verify \"oci://ghcr.io/microsoft/waza-runner-reference@$IMAGE_DIGEST\"") ||
		!strings.Contains(verifyInventory.Run, "--custom-trusted-root") ||
		!strings.Contains(verifyInventory.Run, `--bundle "$SBOM_BUNDLE"`) {
		t.Fatalf("published inventory attestation must be verified offline against the registry digest: %#v", verifyInventory)
	}
	stepIndex := func(steps []runnerReferenceStep, name string) int {
		for i, step := range steps {
			if step.Name == name {
				return i
			}
		}
		return -1
	}
	if stepIndex(publish.Steps, "Refuse to replace an existing release image tag") >= stepIndex(publish.Steps, publishImage.Name) ||
		stepIndex(publish.Steps, publishImage.Name) >= stepIndex(publish.Steps, imageAttestation.Name) ||
		stepIndex(publish.Steps, imageAttestation.Name) >= stepIndex(publish.Steps, verifyProvenance.Name) ||
		stepIndex(publish.Steps, verifyProvenance.Name) >= stepIndex(publish.Steps, inventoryAttestation.Name) ||
		stepIndex(publish.Steps, inventoryAttestation.Name) >= stepIndex(publish.Steps, "Revalidate release tag after publication") {
		t.Fatal("release tag verification must bracket image publication and attestation")
	}
	summary := findStep(publish.Steps, "Record published release digest")
	if summary == nil || summary.Shell != "bash" ||
		!strings.Contains(summary.Run, `>> "$GITHUB_STEP_SUMMARY"`) ||
		!strings.Contains(summary.Run, "Published the exact qualified image digest and verified its signed provenance and inventory attestations") ||
		summary.Env["SOURCE_SHA"] != "${{ needs.validate-release.outputs.source_sha }}" ||
		summary.Env["IMAGE_DIGEST"] != "${{ steps.qualified.outputs.image_digest }}" {
		t.Fatalf("release summary must record the exact source and immutable image digest: %#v", summary)
	}
	failureSummary := findStep(publish.Steps, "Mark incomplete release image publication")
	if failureSummary == nil || !strings.Contains(failureSummary.If, "steps.publish_image.outcome == 'success'") ||
		!strings.Contains(failureSummary.If, "steps.image_attestation.outcome != 'success'") ||
		!strings.Contains(failureSummary.If, "steps.verify_provenance.outcome != 'success'") ||
		!strings.Contains(failureSummary.If, "steps.inventory_attestation.outcome != 'success'") ||
		!strings.Contains(failureSummary.If, "steps.verify_inventory.outcome != 'success'") ||
		failureSummary.Env["IMAGE_DIGEST"] != "${{ steps.qualified.outputs.image_digest }}" ||
		!strings.Contains(failureSummary.Run, "No rollback is claimed") {
		t.Fatalf("partial push or attestation failure must be explicitly unaccepted: %#v", failureSummary)
	}

	for _, workflowName := range []string{
		"go-ci.yml",
		"release-validation.yml",
	} {
		workflow, err := os.ReadFile(filepath.Join("..", "workflows", workflowName))
		if err != nil {
			t.Fatal(err)
		}
		wantCommand := "go test -count=1 -v ./.github/scripts ./.github/scripts/publish-runner-reference-guard ./.github/scripts/verify-runner-reference-guard ./.github/scripts/release-assets-helper"
		if strings.Contains(workflowName, "release") {
			wantCommand = "go test -count=1 -timeout=10m ./.github/scripts ./.github/scripts/publish-runner-reference-guard ./.github/scripts/verify-runner-reference-guard"
		}
		if !strings.Contains(string(workflow), wantCommand) {
			t.Errorf("%s must explicitly select both hidden image guard packages' negative-path tests", workflowName)
		}
	}
}

func TestReleaseQualificationSeparatesCandidateAndTagPublication(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "workflows", "release-qualification.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow runnerReferenceWorkflow
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		t.Fatal(err)
	}
	if workflow.On.Kind != yaml.MappingNode || len(workflow.On.Content) != 4 ||
		workflow.On.Content[0].Value != "workflow_dispatch" ||
		workflow.On.Content[2].Value != "workflow_call" {
		t.Fatalf("qualification triggers = %v; want manual candidate and reusable tag entrypoints", workflow.On.Content)
	}
	if workflow.Permissions["contents"] != "read" || len(workflow.Permissions) != 1 {
		t.Fatalf("qualification workflow permissions = %#v; want contents: read only", workflow.Permissions)
	}
	sourceJob, ok := workflow.Jobs["source"]
	if !ok {
		t.Fatal("missing source identity job")
	}
	guard := findStep(sourceJob.Steps, "Resolve candidate identity")
	for _, required := range []string{
		`if [[ "$EVENT_NAME" == "workflow_dispatch" ]]; then`,
		`test "$EVENT_REF" = "refs/heads/main"`,
		`test "$CANDIDATE_SHA" = "$EVENT_SHA"`,
		`test "$(git rev-parse origin/main)" = "$EVENT_SHA"`,
		`test "$EVENT_NAME" = "push"`,
		`test "$RELEASE_STAGE" = released`,
		`test "$SOURCE_SHA" = "$EVENT_SHA"`,
		`test "$(git rev-parse "${EVENT_REF}^{commit}")" = "$EVENT_SHA"`,
	} {
		if guard == nil || !strings.Contains(guard.Run, required) {
			t.Errorf("exact source guard must contain %q: %#v", required, guard)
		}
	}
	qualifyJob, ok := workflow.Jobs["qualify"]
	if !ok {
		t.Fatal("missing qualification job")
	}
	imageBuild := findStep(qualifyJob.Steps, "Build the release runner image")
	for _, required := range []string{
		`--output "type=oci,name=qualification-${SOURCE_SHA},dest=dist/runner-image.oci.tar"`,
		`--output type=docker`,
		`--label "org.opencontainers.image.revision=${SOURCE_SHA}"`,
		`docker image inspect --format '{{.Id}}' "$IMAGE_TAG"`,
	} {
		if imageBuild == nil || !strings.Contains(imageBuild.Run, required) {
			t.Errorf("runner image build must contain %q: %#v", required, imageBuild)
		}
	}
	imageAttestation := findStep(qualifyJob.Steps, "Attest the exact runner image digest")
	sbomAttestation := findStep(qualifyJob.Steps, "Attest the runner image digest and CycloneDX inventory")
	offlineVerification := findStep(qualifyJob.Steps, "Verify all keyless attestations offline")
	if imageAttestation == nil || imageAttestation.With["subject-name"] != "ghcr.io/microsoft/waza-runner-reference" ||
		imageAttestation.With["subject-digest"] != "${{ steps.image.outputs.digest }}" ||
		sbomAttestation == nil || sbomAttestation.With["subject-name"] != "ghcr.io/microsoft/waza-runner-reference" ||
		sbomAttestation.With["subject-digest"] != "${{ steps.image.outputs.digest }}" ||
		sbomAttestation.With["sbom-path"] != "dist/runner-inventory.cdx.json" {
		t.Fatal("candidate image provenance and SBOM must bind to the exact OCI manifest digest")
	}
	if offlineVerification == nil ||
		!strings.Contains(offlineVerification.Run, "gh attestation verify dist/runner-image.manifest.json") ||
		!strings.Contains(offlineVerification.Run, "--bundle dist/attestations/runner-image.json") ||
		!strings.Contains(offlineVerification.Run, "--bundle dist/attestations/runner-inventory.json") {
		t.Fatalf("candidate image provenance and inventory must be verified offline against the exact manifest: %#v", offlineVerification)
	}
	if strings.Contains(string(source), "packages: write") ||
		strings.Contains(string(source), "push-to-registry: true") ||
		strings.Contains(string(source), "gh release create") {
		t.Fatal("candidate and qualification workflow must not publish to GHCR or create GitHub releases")
	}
	if qualifyJob.Permissions["contents"] != "read" ||
		qualifyJob.Permissions["attestations"] != "write" || qualifyJob.Permissions["id-token"] != "write" ||
		len(qualifyJob.Permissions) != 3 {
		t.Fatalf("qualification permissions = %#v; candidate evidence requires read and keyless-attestation scopes only", qualifyJob.Permissions)
	}
	pinnedAction := regexp.MustCompile(`^[^@]+@[0-9a-f]{40}$`)
	for _, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if step.Uses != "" && !pinnedAction.MatchString(step.Uses) {
				t.Errorf("action %q is not pinned to a full commit SHA", step.Uses)
			}
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
	if !ok || job.Needs.Kind != yaml.ScalarNode || job.Needs.Value != "validate-public-digest" || job.TimeoutMinutes <= 0 ||
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
		strings.Contains(attestation.Run, "--source-ref") ||
		!strings.Contains(attestation.Run, `--format json > "$RUNNER_TEMP/verified-runner-provenance.json"`) ||
		!strings.Contains(attestation.Run, "--signer-workflow microsoft/waza/.github/workflows/publish-runner-reference.yml") {
		t.Fatalf("verifier must validate the exact source-bound publisher attestation: %#v", attestation)
	}
	inventory := findStep(attestationJob.Steps, "Verify signed component inventory for the exact digest")
	if inventory == nil || inventory.Env["IMAGE_DIGEST"] != "${{ steps.guard.outputs.image_digest }}" ||
		!strings.Contains(inventory.Run, "--predicate-type https://cyclonedx.org/bom") ||
		!strings.Contains(inventory.Run, "--signer-workflow microsoft/waza/.github/workflows/publish-runner-reference.yml") ||
		!strings.Contains(inventory.Run, "--source-digest \"$SOURCE_REVISION\"") ||
		strings.Contains(inventory.Run, "--source-ref") ||
		!strings.Contains(inventory.Run, "go run ./cmd/waza runner inventory verify") ||
		!strings.Contains(inventory.Run, `--image-digest "$IMAGE_DIGEST"`) {
		t.Fatalf("verifier must check the signed inventory is bound to the exact digest: %#v", inventory)
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
