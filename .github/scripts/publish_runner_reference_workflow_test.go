package scripts

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Masterminds/semver/v3"
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
	Env            map[string]string     `yaml:"env"`
	Steps          []runnerReferenceStep `yaml:"steps"`
}

type runnerReferenceStep struct {
	ID    string            `yaml:"id"`
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
	if len(call.Inputs) != 5 {
		t.Fatalf("publisher inputs = %#v; want release identity, exact qualification artifact, and producer attempt", call.Inputs)
	}
	for _, name := range []string{"release_stage", "source_sha", "version", "qualification_artifact_name", "qualification_run_attempt"} {
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
		guard.Env["QUALIFICATION_ARTIFACT_NAME"] != "${{ inputs.qualification_artifact_name }}" ||
		guard.Env["QUALIFICATION_RUN_ATTEMPT"] != "${{ inputs.qualification_run_attempt }}" ||
		!strings.Contains(guard.Run, `test "$EVENT_NAME" = push`) ||
		!strings.Contains(guard.Run, `refs/tags/v`) ||
		!strings.Contains(guard.Run, `git rev-parse "${REF}^{commit}"`) ||
		!strings.Contains(guard.Run, `[[ "$QUALIFICATION_RUN_ATTEMPT" =~ ^[1-9][0-9]*$ ]]`) ||
		!strings.Contains(guard.Run, `-${QUALIFICATION_RUN_ATTEMPT}"`) ||
		!strings.Contains(guard.Run, `test "$QUALIFICATION_ARTIFACT_NAME" = "$expected_artifact"`) {
		t.Fatalf("publication must require the exact tag release source: %#v", guard)
	}
	if admitTag := findStep(publish.Steps, "Admit existing release image only at the qualified digest"); admitTag == nil ||
		admitTag.ID != "existing_image" ||
		!strings.Contains(admitTag.Run, "skopeo inspect --authfile") ||
		!strings.Contains(admitTag.Run, `"$remote_digest" != "$IMAGE_DIGEST"`) ||
		!strings.Contains(admitTag.Run, `printf '%s\n' "$IMAGE_DIGEST" > "$RUNNER_TEMP/published-image.digest"`) ||
		!strings.Contains(admitTag.Run, "Could not prove the release image tag is absent") {
		t.Fatal("release publication must allow only an exact-digest idempotent retry")
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
		download.With["name"] != "${{ inputs.qualification_artifact_name }}" ||
		download.With["path"] != "${{ runner.temp }}/qualification" {
		t.Fatalf("publisher must consume the exact same-run qualification artifact: %#v", download)
	}
	if qualified == nil || !strings.Contains(qualified.Run, "runner-image.oci.tar") ||
		!strings.Contains(qualified.Run, "runner-image.digest") ||
		!strings.Contains(qualified.Run, "runner-image.archive.sha256") ||
		!strings.Contains(qualified.Run, "--source-digest \"$SOURCE_SHA\"") ||
		!strings.Contains(qualified.Run, "--signer-digest \"$SOURCE_SHA\"") ||
		!strings.Contains(qualified.Run, `any(.[]; .verificationResult.statement.predicate == $inventory[0])`) {
		t.Fatalf("publisher must verify the qualification artifact and its source-bound image attestations: %#v", qualified)
	}
	if login == nil || login.Env["GHCR_TOKEN"] != "${{ secrets.GITHUB_TOKEN }}" {
		t.Fatal("only the publication job should expose its scoped GITHUB_TOKEN to GHCR login")
	}
	if publishImage == nil || publishImage.If != "steps.existing_image.outputs.already_published != 'true'" ||
		!strings.Contains(publishImage.Run, "skopeo copy --preserve-digests") ||
		!strings.Contains(publishImage.Run, "oci-archive:$RUNNER_TEMP/qualification/runner-image.oci.tar") ||
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
		!strings.Contains(guard.Run, `git rev-parse --verify 'FETCH_HEAD^{commit}'`) {
		t.Fatal("publisher must revalidate the exact release tag after publication")
	}
	if inventoryAttestation == nil || !strings.HasPrefix(inventoryAttestation.Uses, "actions/attest@") ||
		inventoryAttestation.With["subject-name"] != "ghcr.io/microsoft/waza-runner-reference" ||
		inventoryAttestation.With["subject-digest"] != "${{ steps.qualified.outputs.image_digest }}" ||
		inventoryAttestation.With["sbom-path"] != "${{ runner.temp }}/qualification/runner-inventory.cdx.json" ||
		inventoryAttestation.With["push-to-registry"] != "true" ||
		inventoryAttestation.With["create-storage-record"] != "false" {
		t.Fatalf("qualified component inventory must be published as an attestation for the exact digest: %#v", inventoryAttestation)
	}
	if verifyInventory == nil || !strings.Contains(verifyInventory.Run, "gh attestation verify \"oci://ghcr.io/microsoft/waza-runner-reference@$IMAGE_DIGEST\"") ||
		!strings.Contains(verifyInventory.Run, "--custom-trusted-root") ||
		!strings.Contains(verifyInventory.Run, `--bundle "$SBOM_BUNDLE"`) ||
		!strings.Contains(verifyInventory.Run, `--signer-digest "$SOURCE_SHA"`) ||
		!strings.Contains(verifyInventory.Run, `any(.[]; .verificationResult.statement.predicate == $inventory[0])`) {
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
	if stepIndex(publish.Steps, "Admit existing release image only at the qualified digest") >= stepIndex(publish.Steps, publishImage.Name) ||
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
		!strings.Contains(failureSummary.If, "steps.publish_image.outcome == 'failure'") ||
		!strings.Contains(failureSummary.If, "steps.existing_image.outputs.already_published == 'true'") ||
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
			wantCommand = "go test -count=1 -timeout=10m ./.github/scripts ./.github/scripts/attestation-policy ./.github/scripts/publish-runner-reference-guard ./.github/scripts/verify-runner-reference-guard"
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
	if qualifyJob.Outputs["evidence_artifact_name"] != "${{ steps.evidence.outputs.artifact_name }}" ||
		qualifyJob.Outputs["evidence_run_attempt"] != "${{ steps.evidence.outputs.run_attempt }}" ||
		qualifyJob.Outputs["package_artifact_name"] != "${{ steps.package.outputs.artifact_name }}" {
		t.Fatalf("qualification outputs = %#v; want concrete evidence name, producer attempt, and package name", qualifyJob.Outputs)
	}
	var qualificationCall struct {
		Outputs map[string]struct {
			Value string `yaml:"value"`
		} `yaml:"outputs"`
	}
	if err := workflow.On.Content[3].Decode(&qualificationCall); err != nil {
		t.Fatal(err)
	}
	if qualificationCall.Outputs["evidence_run_attempt"].Value != "${{ jobs.qualify.outputs.evidence_run_attempt }}" ||
		qualificationCall.Outputs["dashboard_artifact_name"].Value != "${{ jobs.validation.outputs.dashboard_artifact_name }}" ||
		qualificationCall.Outputs["dashboard_run_attempt"].Value != "${{ jobs.validation.outputs.dashboard_run_attempt }}" {
		t.Fatalf("qualification reusable outputs must expose producer-bound dashboard and evidence identities: %#v", qualificationCall.Outputs)
	}
	evidenceName := findStep(qualifyJob.Steps, "Expose exact evidence artifact name")
	if evidenceName == nil || evidenceName.ID != "evidence" ||
		!strings.Contains(evidenceName.Run, "$EVIDENCE_ARTIFACT_NAME") ||
		!strings.Contains(evidenceName.Run, "$GITHUB_RUN_ATTEMPT") ||
		!strings.Contains(qualifyJob.Env["EVIDENCE_ARTIFACT_NAME"], "${{ github.run_id }}-${{ github.run_attempt }}") {
		t.Fatalf("qualification must expose its exact uploaded evidence artifact name: %#v", evidenceName)
	}
	imageBuild := findStep(qualifyJob.Steps, "Build the release runner image")
	for _, required := range []string{
		`--output "type=oci,name=qualification-${SOURCE_SHA},dest=dist/runner-image.oci.tar"`,
		`--output "type=docker,name=${IMAGE_TAG}"`,
		`--label "org.opencontainers.image.revision=${SOURCE_SHA}"`,
		`--build-arg "WAZA_VERSION=${VERSION}"`,
		`docker image inspect --format '{{.Id}}' "$IMAGE_TAG"`,
		`test "$reported_version" = "waza version ${VERSION}"`,
	} {
		if imageBuild == nil || !strings.Contains(imageBuild.Run, required) {
			t.Errorf("runner image build must contain %q: %#v", required, imageBuild)
		}
	}
	if strings.Contains(imageBuild.Run, `--tag "$IMAGE_TAG"`) {
		t.Fatal("runner image must not use a global tag that overrides the OCI archive name")
	}
	for _, step := range []string{
		"Download exact-source dashboard",
		"Download dashboard checker dependency",
		"Install exact-source dashboard",
		"Require built dashboard output",
		"Require clean packaged source",
	} {
		if findStep(qualifyJob.Steps, step) == nil {
			t.Errorf("qualification must admit the validated exact-source dashboard before builds: %s", step)
		}
	}
	dashboardDownload := findStep(qualifyJob.Steps, "Download exact-source dashboard")
	if dashboardDownload == nil || dashboardDownload.Uses != "actions/download-artifact@9000827ccba6bdab643e8b6fd33ac0654aef8333" ||
		dashboardDownload.With["name"] != "${{ needs.validation.outputs.dashboard_artifact_name }}" ||
		dashboardDownload.With["path"] != "${{ runner.temp }}/release-dashboard" {
		t.Fatalf("qualification must consume the producer-attempt validation dashboard outside the checkout: %#v", dashboardDownload)
	}
	if qualifyJob.Env["RELEASE_DASHBOARD_MANIFEST_SHA256"] != "${{ needs.validation.outputs.dashboard_manifest_sha256 }}" ||
		qualifyJob.Env["RELEASE_DASHBOARD_RUN_ATTEMPT"] != "${{ needs.validation.outputs.dashboard_run_attempt }}" {
		t.Fatal("qualification must bind the dashboard digest and producer attempt handed off by exact-source validation")
	}
	if upload := findStep(qualifyJob.Steps, "Upload exact qualification evidence"); upload == nil ||
		upload.With["name"] != "${{ env.EVIDENCE_ARTIFACT_NAME }}" {
		t.Fatalf("qualification evidence artifact must use the exact run-attempt name: %#v", upload)
	}
	if upload := findStep(qualifyJob.Steps, "Upload workflow-linked assurance package"); upload == nil ||
		upload.With["name"] != "${{ env.PACKAGE_ARTIFACT_NAME }}" ||
		!strings.Contains(qualifyJob.Env["PACKAGE_ARTIFACT_NAME"], "${{ github.run_id }}-${{ github.run_attempt }}") {
		t.Fatalf("assurance package artifact must use the exact run-attempt name: %#v", upload)
	}
	imageAttestation := findStep(qualifyJob.Steps, "Attest the exact runner image digest")
	sbomAttestation := findStep(qualifyJob.Steps, "Attest the runner image digest and CycloneDX inventory")
	evidenceDirectories := findStep(qualifyJob.Steps, "Create evidence directories")
	inventoryVerification := findStep(qualifyJob.Steps, "Generate and verify inventory for the exact runner image digest")
	rootDownload := findStep(qualifyJob.Steps, "Download Sigstore trusted roots")
	offlineVerification := findStep(qualifyJob.Steps, "Verify all keyless attestations offline")
	if imageAttestation == nil || imageAttestation.With["subject-name"] != "ghcr.io/microsoft/waza-runner-reference" ||
		imageAttestation.With["subject-digest"] != "${{ steps.image.outputs.digest }}" ||
		sbomAttestation == nil || sbomAttestation.With["subject-name"] != "ghcr.io/microsoft/waza-runner-reference" ||
		sbomAttestation.With["subject-digest"] != "${{ steps.image.outputs.digest }}" ||
		sbomAttestation.With["sbom-path"] != "dist/runner-inventory.cdx.json" {
		t.Fatal("candidate image provenance and SBOM must bind to the exact OCI manifest digest")
	}
	if evidenceDirectories == nil ||
		evidenceDirectories.Run != "mkdir -p dist/attestations dist/execution-provenance dist/verification/attestations" ||
		inventoryVerification == nil ||
		!strings.Contains(inventoryVerification.Run, "> dist/verification/runner-inventory.json") ||
		strings.Contains(inventoryVerification.Run, "dist/verification/attestations/") {
		t.Fatal("non-attestation inventory verification must remain outside the attestation-report directory")
	}
	if offlineVerification == nil ||
		rootDownload == nil || rootDownload.Run != "gh attestation trusted-root > dist/trusted_root.jsonl" ||
		offlineVerification.Env["EXPECTED_REPOSITORY_ID"] != "${{ github.repository_id }}" ||
		offlineVerification.Env["EXPECTED_TRUSTED_ROOT_SHA256"] != "${{ vars.WAZA_SIGSTORE_TRUSTED_ROOT_SHA256 }}" ||
		!strings.Contains(offlineVerification.Run, "attestation-policy root") ||
		!strings.Contains(offlineVerification.Run, `"$EXPECTED_REPOSITORY_ID" "$EXPECTED_TRUSTED_ROOT_SHA256" dist/trusted_root.jsonl`) ||
		!strings.Contains(offlineVerification.Run, "gh attestation verify dist/runner-image.manifest.json") ||
		!strings.Contains(offlineVerification.Run, "--bundle dist/attestations/runner-image.json") ||
		!strings.Contains(offlineVerification.Run, "--bundle dist/attestations/runner-inventory.json") ||
		!strings.Contains(offlineVerification.Run, "attestation-policy verify") ||
		!strings.Contains(offlineVerification.Run, `"$EXPECTED_REPOSITORY_ID" "$EXPECTED_TRUSTED_ROOT_SHA256"`) ||
		!strings.Contains(offlineVerification.Run, "dist/trusted_root.jsonl dist/verification/attestations/*.json") ||
		strings.Contains(offlineVerification.Run, "dist/verification/*.json") ||
		strings.Count(offlineVerification.Run, "gh attestation verify") != 4 ||
		!strings.Contains(offlineVerification.Run, "for binary in dist/waza-linux-amd64 dist/waza-linux-arm64 dist/waza-darwin-amd64") ||
		!strings.Contains(offlineVerification.Run, "dist/waza-darwin-arm64 dist/waza-windows-amd64.exe dist/waza-windows-arm64.exe; do") ||
		!strings.Contains(offlineVerification.Run, `> "dist/verification/attestations/${name}.json"`) ||
		!strings.Contains(offlineVerification.Run, "> dist/verification/attestations/runner-image-archive-provenance.json") ||
		!strings.Contains(offlineVerification.Run, "> dist/verification/attestations/runner-image-provenance.json") ||
		!strings.Contains(offlineVerification.Run, "> dist/verification/attestations/runner-inventory-sbom.json") ||
		!strings.Contains(offlineVerification.Run, "dist/verification/attestations/runner-inventory-sbom.json >/dev/null") ||
		!strings.Contains(offlineVerification.Run, `any(.[]; .verificationResult.statement.predicate == $inventory[0])`) {
		t.Fatalf("offline policy must include every attestation report and exclude non-attestation verification output: %#v", offlineVerification)
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

func TestReleaseQualificationCandidateVersionUsesExactSource(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "workflows", "release-qualification.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow runnerReferenceWorkflow
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		t.Fatal(err)
	}
	step := findStep(workflow.Jobs["source"].Steps, "Resolve candidate identity")
	if step == nil {
		t.Fatal("missing candidate identity guard")
	}
	for _, test := range []struct {
		name    string
		version string
		mode    string
		wantErr bool
	}{
		{name: "hotfix version", version: "0.42.1"},
		{name: "future version", version: "1.2.3"},
		{name: "prerelease version", version: "1.2.3-rc.1"},
		{name: "prerelease with build metadata", version: "1.2.3-rc.1+build.2", wantErr: true},
		{name: "empty version", wantErr: true},
		{name: "prefixed version", version: "v0.42.1", wantErr: true},
		{name: "injected output", version: "0.42.1\nrelease_stage=released", wantErr: true},
		{name: "missing source version", mode: "missing", wantErr: true},
		{name: "stale main", version: "0.42.1", mode: "stale", wantErr: true},
		{name: "tag version unchanged", version: "9.9.9", mode: "tag"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir, err := os.MkdirTemp(".", ".release-qualification-test-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.RemoveAll(dir); err != nil {
					t.Error(err)
				}
			})
			dir, err = filepath.Abs(dir)
			if err != nil {
				t.Fatal(err)
			}
			check, err := os.ReadFile("release_version_check.sh")
			if err != nil {
				t.Fatal(err)
			}
			checkPath := filepath.Join(dir, ".github", "scripts", "release_version_check.sh")
			if err := os.MkdirAll(filepath.Dir(checkPath), 0o700); err != nil {
				t.Fatal(err)
			}
			writeReleaseFile(t, checkPath, check)
			envPath := filepath.Join(dir, "bash-env")
			outputPath := filepath.Join(dir, "github-output")
			if err := os.WriteFile(envPath, []byte(`git() {
  case "$*" in
    "rev-parse HEAD"|"rev-parse refs/tags/v0.42.1^{commit}") printf '%s\n' "$EVENT_SHA" ;;
    "fetch --no-tags origin refs/heads/main") return 0 ;;
    "rev-parse origin/main")
      if [[ "$FAKE_MODE" == stale ]]; then printf 'stale\n'; else printf '%s\n' "$EVENT_SHA"; fi ;;
    "show $EVENT_SHA:version.txt")
      [[ "$FAKE_MODE" != missing ]] || return 1
      printf '%s\n' "$FAKE_VERSION" ;;
    *) echo "Unexpected git command: $*" >&2; return 99 ;;
  esac
}
`), 0o600); err != nil {
				t.Fatal(err)
			}
			event, ref, stage := "workflow_dispatch", "refs/heads/main", ""
			wantStage, wantVersion := "candidate", test.version
			if test.mode == "tag" {
				event, ref, stage = "push", "refs/tags/v0.42.1", "released"
				wantStage, wantVersion = "released", "0.42.1"
			}
			cmd := exec.Command("bash", "-euo", "pipefail", "-c", step.Run)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(),
				"BASH_ENV="+envPath, "GITHUB_OUTPUT="+outputPath,
				"EVENT_NAME="+event, "EVENT_REF="+ref, "EVENT_SHA="+strings.Repeat("a", 40),
				"CANDIDATE_SHA="+strings.Repeat("a", 40), "SOURCE_SHA="+strings.Repeat("a", 40),
				"RELEASE_STAGE="+stage, "VERSION_INPUT=0.42.1",
				"FAKE_MODE="+test.mode, "FAKE_VERSION="+test.version,
			)
			output, err := cmd.CombinedOutput()
			if (err != nil) != test.wantErr {
				t.Fatalf("identity guard error = %v, wantErr %t\n%s", err, test.wantErr, output)
			}
			recorded, readErr := os.ReadFile(outputPath)
			if test.wantErr {
				if readErr == nil && len(recorded) != 0 {
					t.Fatalf("rejected candidate emitted identity: %s", recorded)
				}
				return
			}
			want := "source_sha=" + strings.Repeat("a", 40) + "\nversion=" + wantVersion + "\nrelease_stage=" + wantStage + "\n"
			if readErr != nil || string(recorded) != want {
				t.Fatalf("identity output = %q, error %v; want %q", recorded, readErr, want)
			}
		})
	}
}

func TestReleaseVersionAdmissionParity(t *testing.T) {
	check, err := os.ReadFile("release_version_check.sh")
	if err != nil {
		t.Fatal(err)
	}
	load := func(file, job, name string) runnerReferenceStep {
		t.Helper()
		source, err := os.ReadFile(filepath.Join("..", "workflows", file))
		if err != nil {
			t.Fatal(err)
		}
		var workflow runnerReferenceWorkflow
		if err := yaml.Unmarshal(source, &workflow); err != nil {
			t.Fatal(err)
		}
		step := findStep(workflow.Jobs[job].Steps, name)
		if step == nil {
			t.Fatalf("missing %s in %s", name, file)
		}
		if file == "release.yml" {
			actual := workflow.Jobs[job].Steps
			if len(actual) != 2 || actual[0].Name != "Check out version admission" || actual[1].Name != "Determine Version" {
				t.Fatal("version admission checkout must precede the shared validator")
			}
		}
		return *step
	}
	steps := []runnerReferenceStep{
		load("release-qualification.yml", "source", "Resolve candidate identity"),
		load("release-qualification.yml", "source", "Resolve candidate identity"),
		load("release.yml", "setup-version", "Determine Version"),
		load("publish-runner-reference.yml", "validate-release", "Validate event, source, and exact tag"),
	}
	paths := []string{"candidate", "tag-qualification", "tag-setup", "image-publication"}
	// The setup job previously had no checkout: its shared script must now
	// come from the event source, before version admission, without credentials.
	checkout := load("release.yml", "setup-version", "Check out version admission")
	if checkout.With["ref"] != "${{ github.sha }}" || checkout.With["persist-credentials"] != "false" ||
		!strings.HasPrefix(checkout.Uses, "actions/checkout@") {
		t.Fatal("version admission must check out the exact event source without persisted credentials")
	}
	versions := []struct {
		value string
		valid bool
	}{
		{"0.42.1", true},
		{"1.2.3", true},
		{"2.4.6-beta.2", true},
		{"1.2.3-rc.1", true},
		{"0.0.0", true},
		{"1.2.3-" + strings.Repeat("a", 121), true}, // v + version = 128 bytes.
		{"1.2.3-" + strings.Repeat("a", 122), false},
		{"1.2.3+build.2", false},
		{"1.2.3-rc.1+build.2", false},
		{"1.2.3-rc-1", false}, // Not admitted by the existing tag setup.
		{"01.2.3", false},     // Not admitted by the existing candidate path.
		{"1.2", false},
		{"v0.42.1", false},
		{"1.2.3.4", false},
		{"1.2.3-", false},
		{"1.2.3-rc..1", false},
		{"1.2.3-rc.", false},
		{"1.2.3-rc_1", false},
		{"1.2.3-α", false},
	}
	parserBoundaries := []struct {
		value string
		valid bool
	}{
		{"1.2.3-rc.0", true},
		{"1.2.3-rc.00", false},
		{"1.2.3-rc.01", false},
		{"1.2.3-rc.01a", true},
		{"1.2.3-18446744073709551616", true},
		{"18446744073709551615.2.3", true},
		{"18446744073709551616.2.3", false},
		{"1.18446744073709551615.3", true},
		{"1.18446744073709551616.3", false},
		{"1.2.18446744073709551615", true},
		{"1.2.18446744073709551616", false},
	}
	for _, boundary := range parserBoundaries {
		_, parseErr := semver.NewVersion(boundary.value)
		if (parseErr == nil) != boundary.valid {
			t.Fatalf("Waza version parser: %q accepted = %t, want %t: %v", boundary.value, parseErr == nil, boundary.valid, parseErr)
		}
	}
	versions = append(versions, parserBoundaries...)
	run := func(t *testing.T, path int, version string, valid bool, body, validator string) error {
		t.Helper()
		dir := t.TempDir()
		checkPath := filepath.Join(dir, ".github", "scripts", "release_version_check.sh")
		if err := os.MkdirAll(filepath.Dir(checkPath), 0o700); err != nil {
			t.Fatal(err)
		}
		writeReleaseFile(t, checkPath, []byte(validator))
		writeReleaseFile(t, filepath.Join(dir, "version.txt"), []byte(version+"\n"))
		publicationGit(t, dir, "init", "-q", "-b", "main")
		publicationGit(t, dir, "config", "core.autocrlf", "false")
		// Windows: 128-byte tag refs exceed MAX_PATH under the temp directory.
		publicationGit(t, dir, "config", "core.longpaths", "true")
		publicationGit(t, dir, "add", ".github", "version.txt")
		publicationGit(t, dir, "-c", "user.name=Release test", "-c", "user.email=release@example.invalid", "commit", "-qm", "version fixture")
		sha := publicationGit(t, dir, "rev-parse", "HEAD")
		if got := publicationGit(t, dir, "show", sha+":version.txt"); got != version {
			t.Fatalf("committed version = %q; want %q", got, version)
		}
		publicationGit(t, dir, "remote", "add", "origin", dir)
		publicationGit(t, dir, "update-ref", "refs/remotes/origin/main", sha)
		tag := "v" + version
		// Git forbids these malformed names too; no tag can exist, and the
		// production version guard must reject before trying to resolve it.
		if !strings.Contains(tag, "..") && !strings.HasSuffix(tag, ".") {
			publicationGit(t, dir, "tag", tag)
		}
		outputPath := filepath.Join(dir, "github-output")
		event, ref, stage := "push", "refs/tags/"+tag, "released"
		if path == 0 {
			event, ref, stage = "workflow_dispatch", "refs/heads/main", ""
		}
		cmd := exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", body)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GITHUB_OUTPUT="+outputPath, "GITHUB_REF_NAME="+tag,
			"EVENT_NAME="+event, "EVENT_REF="+ref, "REF="+ref, "EVENT_SHA="+sha,
			"CANDIDATE_SHA="+sha, "SOURCE_SHA="+sha, "RELEASE_STAGE="+stage,
			"VERSION_INPUT="+version, "REPOSITORY=microsoft/waza", "GITHUB_RUN_ID=123",
			"QUALIFICATION_RUN_ATTEMPT=1", "QUALIFICATION_ARTIFACT_NAME=release-qualification-evidence-"+sha+"-123-1",
		)
		output, execErr := cmd.CombinedOutput()
		recorded, readErr := os.ReadFile(outputPath)
		if !valid {
			if execErr == nil || !strings.Contains(string(output), "Unsupported release version:") ||
				(readErr == nil && len(recorded) != 0) {
				return fmt.Errorf("unsupported %q admitted or rejected for another reason: %v, output %s, identity %q", version, execErr, output, recorded)
			}
			return nil
		}
		want := "source_sha=" + sha + "\nversion=" + version + "\n"
		switch path {
		case 0:
			want += "release_stage=candidate\n"
		case 1:
			want += "release_stage=released\n"
		case 2:
			want = "version=" + version + "\ntag=" + tag + "\n"
		}
		if execErr != nil || readErr != nil || string(recorded) != want {
			return fmt.Errorf("publishable %q: %v, output %s, identity %q; want %q", version, execErr, output, recorded, want)
		}
		return nil
	}
	for path, step := range steps {
		for _, version := range versions {
			name := version.value
			if len(name) > 32 {
				name = fmt.Sprintf("length-%d", len(name))
			}
			t.Run(paths[path]+"/"+name, func(t *testing.T) {
				if err := run(t, path, version.value, version.valid, step.Run, string(check)); err != nil {
					t.Fatal(err)
				}
			})
		}
		t.Run(paths[path]+"/removed-admission-mutation", func(t *testing.T) {
			guard := `bash .github/scripts/release_version_check.sh "$version"`
			if path == 2 {
				guard = `bash .github/scripts/release_version_check.sh "$VERSION"`
			}
			if strings.Count(step.Run, guard) != 1 {
				t.Fatal("production workflow must invoke shared version admission exactly once")
			}
			mutated := strings.Replace(step.Run, guard, ":", 1)
			if err := run(t, path, "1.2.3+build.2", false, mutated, string(check)); err == nil {
				t.Fatal("removed version admission mutation was not detected")
			}
		})
	}
	t.Run("OCI-length-limit-mutation", func(t *testing.T) {
		if strings.Count(string(check), `-gt 127`) != 1 {
			t.Fatal("missing production OCI tag length limit")
		}
		mutated := strings.Replace(string(check), "-gt 127", "-gt 128", 1)
		if err := run(t, 0, "1.2.3-"+strings.Repeat("a", 122), false, steps[0].Run, mutated); err == nil {
			t.Fatal("OCI tag length mutation was not detected")
		}
	})
}

func TestExistingReleaseImageRetryRequiresExactDigest(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "workflows", "publish-runner-reference.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow runnerReferenceWorkflow
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		t.Fatal(err)
	}
	step := findStep(workflow.Jobs["publish"].Steps, "Admit existing release image only at the qualified digest")
	if step == nil {
		t.Fatal("missing existing image digest admission")
	}
	bashEnv := filepath.Join(t.TempDir(), "bash-env")
	if err := os.WriteFile(bashEnv, []byte(`skopeo() {
  case " $* " in
    *" --raw "*)
      case "$FAKE_MODE" in
        exact|different) printf '{"schemaVersion":2}\n' ;;
        missing) echo 'manifest unknown' >&2; return 1 ;;
        error) echo 'permission denied' >&2; return 1 ;;
      esac
      ;;
    *" --format "*)
      printf '%s\n' "$FAKE_DIGEST"
      ;;
    *) return 99 ;;
  esac
}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		mode       string
		digest     string
		wantErr    bool
		wantAdmit  string
		wantDigest bool
	}{
		{name: "exact digest retry", mode: "exact", digest: "sha256:qualified", wantAdmit: "already_published=true", wantDigest: true},
		{name: "different digest", mode: "different", digest: "sha256:other", wantErr: true},
		{name: "absent tag", mode: "missing", wantAdmit: "already_published=false"},
		{name: "unknown registry error", mode: "error", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			outputPath := filepath.Join(dir, "github-output")
			cmd := exec.Command("bash", "-euo", "pipefail", "-c", step.Run)
			cmd.Env = append(os.Environ(),
				"BASH_ENV="+bashEnv, "HOME="+dir, "RUNNER_TEMP="+dir,
				"GITHUB_OUTPUT="+outputPath, "VERSION=0.42.0",
				"IMAGE_DIGEST=sha256:qualified", "FAKE_MODE="+test.mode, "FAKE_DIGEST="+test.digest,
			)
			output, err := cmd.CombinedOutput()
			if (err != nil) != test.wantErr {
				t.Fatalf("admission error = %v, wantErr %t\n%s", err, test.wantErr, output)
			}
			if test.wantErr {
				return
			}
			recorded, err := os.ReadFile(outputPath)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(recorded), test.wantAdmit) {
				t.Fatalf("admission output = %q, want %q", recorded, test.wantAdmit)
			}
			_, err = os.Stat(filepath.Join(dir, "published-image.digest"))
			if (err == nil) != test.wantDigest {
				t.Fatalf("published digest present = %t, want %t (error %v)", err == nil, test.wantDigest, err)
			}
		})
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
