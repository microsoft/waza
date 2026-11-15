package runnerassurance

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildMatchesPublishedPackagePage(t *testing.T) {
	root, err := FindRepositoryRoot(".")
	require.NoError(t, err)
	input, err := os.ReadFile(filepath.Join(root, "docs", "PRODUCTION_INTEGRATION_ASSURANCE-1.0.json"))
	require.NoError(t, err)

	result, err := Build(input, root)
	require.NoError(t, err)
	require.Equal(t, Kind, result.Package.Kind)
	require.Equal(t, Version, result.Package.PackageVersion)
	require.Contains(t, string(result.Markdown), "## Operator conformance matrix")
	require.Contains(t, string(result.Markdown), "## Threat model")
	require.Contains(t, string(result.Markdown), "## Production-readiness checklist")
	require.Contains(t, string(result.Markdown), "## Verification limits")
	require.Contains(t, string(result.Markdown), "blob/v0.42.0/docs/EXECUTION_PROVENANCE.md")
	require.NotContains(t, string(result.Markdown), "blob/main/docs/EXECUTION_PROVENANCE.md")
	require.Contains(t, string(result.Markdown), "Source evidence links are pinned to the `v0.42.0` release tag.")

	published, err := os.ReadFile(filepath.Join(root, "site", "src", "content", "docs", "guides", "production-integration-assurance.mdx"))
	require.NoError(t, err)
	normalizedPublished := bytes.ReplaceAll(published, []byte("\r\n"), []byte("\n"))
	require.True(t, bytes.Equal(normalizedPublished, result.Markdown), "generated site page is stale; regenerate it with `waza runner assurance build --format markdown`")

	roundTrip, err := Build(result.JSON, root)
	require.NoError(t, err)
	require.Equal(t, result.JSON, roundTrip.JSON)
	require.Equal(t, result.Markdown, roundTrip.Markdown)
}

func TestBuildWithWorkflowEvidenceBindsExactArtifactFiles(t *testing.T) {
	root, err := FindRepositoryRoot(".")
	require.NoError(t, err)
	input, err := os.ReadFile(filepath.Join(root, "docs", "PRODUCTION_INTEGRATION_ASSURANCE-1.0.json"))
	require.NoError(t, err)
	evidenceRoot := writeQualificationFixture(t, root)
	files, err := hashEvidenceFiles(evidenceRoot)
	require.NoError(t, err)
	workflowEvidence, err := json.Marshal(WorkflowEvidenceInput{
		Repository: "microsoft/waza", SourceSHA: strings.Repeat("a", 40), ReleaseStage: "candidate",
		Workflow: "Release qualification", RunID: 12345, RunAttempt: 1,
		RunURL:       "https://github.com/microsoft/waza/actions/runs/12345",
		ArtifactName: "release-qualification", ArtifactID: 67890,
		ArtifactURL:    "https://github.com/microsoft/waza/actions/runs/12345/artifacts/67890",
		ArtifactSHA256: strings.Repeat("b", 64), Files: files,
	})
	require.NoError(t, err)

	result, err := BuildWithWorkflowEvidence(input, root, workflowEvidence, evidenceRoot)
	require.NoError(t, err)
	require.Equal(t, "candidate", result.Package.ReleaseStage)
	require.Equal(t, int64(12345), result.Package.WorkflowEvidence.RunID)
	require.Equal(t, files, result.Package.WorkflowEvidence.Files)
	require.Contains(t, string(result.Markdown), "actions/runs/12345")
	require.Contains(t, string(result.Markdown), "runner-inventory.cdx.json")

	require.NoError(t, os.Remove(filepath.Join(evidenceRoot, "verification", "runner-inventory.json")))
	_, err = BuildWithWorkflowEvidence(input, root, workflowEvidence, evidenceRoot)
	require.ErrorIs(t, err, ErrInvalid, "missing inventory verification evidence must fail closed")

	require.NoError(t, os.WriteFile(filepath.Join(evidenceRoot, "verification", "runner-inventory.json"), []byte("verification evidence"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(evidenceRoot, "contract-conformance.json"), []byte("tampered"), 0o600))
	_, err = BuildWithWorkflowEvidence(input, root, workflowEvidence, evidenceRoot)
	require.ErrorIs(t, err, ErrInvalid)
}

func writeQualificationFixture(t *testing.T, root string) string {
	t.Helper()
	evidenceRoot, err := os.MkdirTemp(root, ".workflow-evidence-test-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(evidenceRoot)) })
	files := []string{
		"contract-conformance.json", "offline-verification.json", "runner-image.oci.tar",
		"runner-image.archive.sha256", "runner-image.config.digest", "runner-image.digest",
		"runner-image.manifest.json", "runner-inventory.cdx.json", "trusted_root.jsonl",
		"attestations/binaries.json", "attestations/runner-image.json",
		"attestations/runner-inventory.json", "attestations/runner-image-archive.json",
		"execution-provenance/statement.json", "execution-provenance/envelope.json",
		"execution-provenance/verification.json", "execution-provenance/public-key.pem",
		"execution-provenance/bundle/bundle.json", "execution-provenance/bundle/statement.dsse.json",
		"verification/runner-image-archive-provenance.json",
		"verification/runner-image-provenance.json", "verification/runner-inventory-sbom.json",
		"verification/runner-inventory.json",
	}
	for _, binary := range []string{
		"waza-linux-amd64", "waza-linux-arm64", "waza-darwin-amd64",
		"waza-darwin-arm64", "waza-windows-amd64.exe", "waza-windows-arm64.exe",
	} {
		files = append(files, binary, binary+".source-sha.txt", binary+".sha256",
			"verification/"+binary+".json")
	}
	for _, name := range files {
		fullPath := filepath.Join(evidenceRoot, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(fullPath), 0o700))
		require.NoError(t, os.WriteFile(fullPath, []byte("qualification evidence: "+name), 0o600))
	}
	return evidenceRoot
}

func TestBuildRejectsUnknownStateAndStaleEvidence(t *testing.T) {
	root, err := FindRepositoryRoot(".")
	require.NoError(t, err)
	input, err := os.ReadFile(filepath.Join(root, "docs", "PRODUCTION_INTEGRATION_ASSURANCE-1.0.json"))
	require.NoError(t, err)

	unknownState := bytes.Replace(input, []byte(`"state": "supported"`), []byte(`"state": "unverified"`), 1)
	_, err = Build(unknownState, root)
	require.ErrorIs(t, err, ErrInvalid)

	var promotedState Package
	require.NoError(t, json.Unmarshal(input, &promotedState))
	for i := range promotedState.ConformanceMatrix {
		if promotedState.ConformanceMatrix[i].ID == "provenance-keyless-kms" {
			promotedState.ConformanceMatrix[i].State = "supported"
		}
	}
	promotedBytes, err := json.Marshal(promotedState)
	require.NoError(t, err)
	_, err = Build(promotedBytes, root)
	require.ErrorIs(t, err, ErrInvalid)

	staleReference := bytes.Replace(input, []byte("internal/provenance/provenance_test.go"), []byte("internal/provenance/missing_test.go"), 1)
	_, err = Build(staleReference, root)
	require.ErrorIs(t, err, ErrInvalid)
}

func TestBuildRejectsMissingLabViewEvidenceAndUnsafePaths(t *testing.T) {
	root, err := FindRepositoryRoot(".")
	require.NoError(t, err)
	input, err := os.ReadFile(filepath.Join(root, "docs", "PRODUCTION_INTEGRATION_ASSURANCE-1.0.json"))
	require.NoError(t, err)

	missingView := bytes.Replace(input, []byte(`"panel": "Execution provenance"`), []byte(`"panel": "Unpublished panel"`), 1)
	_, err = Build(missingView, root)
	require.ErrorIs(t, err, ErrInvalid)

	unsafePath := bytes.Replace(input, []byte("docs/EXECUTION_PROVENANCE.md"), []byte("../outside.md"), 1)
	_, err = Build(unsafePath, root)
	require.ErrorIs(t, err, ErrInvalid)

	nonGuideLabEvidence := bytes.Replace(input,
		[]byte(`"guide": "site/src/content/docs/guides/execution-provenance.mdx"`),
		[]byte(`"guide": "docs/EXECUTION_PROVENANCE.md"`), 1)
	_, err = Build(nonGuideLabEvidence, root)
	require.ErrorIs(t, err, ErrInvalid)
}

func TestCompileSchemaResourcesRejectsInvalidSchemasAndUnresolvedReferences(t *testing.T) {
	const rootID = "https://microsoft.github.io/waza/schemas/assurance-test-1.0.schema.json"
	const childID = "https://microsoft.github.io/waza/schemas/assurance-child-1.0.schema.json"
	resources := map[string][]byte{
		"schemas/assurance-test-1.0.schema.json": []byte(`{
			"$schema": "https://json-schema.org/draft/2020-12/schema",
			"$id": "` + rootID + `",
			"type": "object",
			"properties": {"value": {"$ref": "assurance-child-1.0.schema.json"}}
		}`),
		"schemas/assurance-child-1.0.schema.json": []byte(`{
			"$schema": "https://json-schema.org/draft/2020-12/schema",
			"$id": "` + childID + `",
			"type": "string"
		}`),
	}
	require.NoError(t, compileSchemaResources(resources, []string{"schemas/assurance-test-1.0.schema.json"}))

	resources["schemas/assurance-child-1.0.schema.json"] = []byte(`{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"$id": "` + childID + `",
		"type": "not-a-json-schema-type"
	}`)
	require.ErrorIs(t, compileSchemaResources(resources, []string{"schemas/assurance-test-1.0.schema.json"}), ErrInvalid)

	resources["schemas/assurance-child-1.0.schema.json"] = []byte(`{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"$id": "` + childID + `",
		"type": "string"
	}`)
	resources["schemas/assurance-test-1.0.schema.json"] = []byte(`{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"$id": "` + rootID + `",
		"type": "object",
		"properties": {"value": {"$ref": "unregistered.schema.json"}}
	}`)
	require.ErrorIs(t, compileSchemaResources(resources, []string{"schemas/assurance-test-1.0.schema.json"}), ErrInvalid)
}

func TestBuildReportsAllConformanceStatesAndExistingLabLinks(t *testing.T) {
	root, err := FindRepositoryRoot(".")
	require.NoError(t, err)
	input, err := os.ReadFile(filepath.Join(root, "docs", "PRODUCTION_INTEGRATION_ASSURANCE-1.0.json"))
	require.NoError(t, err)

	result, err := Build(input, root)
	require.NoError(t, err)
	for _, issue := range []string{"#769", "#770", "#771", "#772", "#773", "#774"} {
		require.Contains(t, string(result.Markdown), issue)
	}
	for _, state := range []string{"`supported`", "`unsupported`", "`unavailable`"} {
		require.Contains(t, string(result.Markdown), state)
	}
	require.Contains(t, string(result.Markdown), "no new Lab dashboard view")
	require.NotContains(t, strings.ToLower(string(result.Markdown)), "production ready")
}
