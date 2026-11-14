package runnerassurance

import (
	"bytes"
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
	require.Contains(t, string(result.Markdown), "Evidence links are pinned to the `v0.42.0` release tag.")

	published, err := os.ReadFile(filepath.Join(root, "site", "src", "content", "docs", "guides", "production-integration-assurance.mdx"))
	require.NoError(t, err)
	require.True(t, bytes.Equal(published, result.Markdown), "generated site page is stale; regenerate it with `waza runner assurance build --format markdown`")

	roundTrip, err := Build(result.JSON, root)
	require.NoError(t, err)
	require.Equal(t, result.JSON, roundTrip.JSON)
	require.Equal(t, result.Markdown, roundTrip.Markdown)
}

func TestBuildRejectsUnknownStateAndStaleEvidence(t *testing.T) {
	root, err := FindRepositoryRoot(".")
	require.NoError(t, err)
	input, err := os.ReadFile(filepath.Join(root, "docs", "PRODUCTION_INTEGRATION_ASSURANCE-1.0.json"))
	require.NoError(t, err)

	unknownState := bytes.Replace(input, []byte(`"state": "supported"`), []byte(`"state": "unverified"`), 1)
	_, err = Build(unknownState, root)
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
