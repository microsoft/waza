package faultfixture

import (
	"encoding/json"
	"testing"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func nativeReferenceManifest(t *testing.T, artifact string, content []byte) *models.EvidenceManifest {
	t.Helper()
	value, err := jsonutil.Parse(content)
	require.NoError(t, err)
	digest, err := evidence.JSONDigest(value)
	require.NoError(t, err)
	manifest := &models.EvidenceManifest{
		Origin: models.EvidenceOrigin{EvalID: "eval", TaskID: "task", RunNumber: 1, AttemptCount: 1, PriorAttempts: "none"},
		Runtime: models.EvidenceRuntime{
			ExecutionMode: "mock", NativeSkillControl: "mock_not_applicable", VerifiedEnforcement: "unknown",
		},
		Artifacts: []models.EvidenceArtifact{{
			ID: artifact, Kind: artifact, Availability: "captured", Completeness: "unknown",
			Document: "snapshot", Pointer: "/native", ContentDigest: digest, Reason: "native tape completeness is unassessed",
		}},
	}
	require.NoError(t, evidence.Seal(manifest))
	return manifest
}

func TestNativeReferenceUsesCapturedOrdinalAndFullTape(t *testing.T) {
	tape, err := json.Marshal([]models.CommandInvocation{
		{Command: "inspect", Args: []string{"first"}, ResponseIndex: 0, ExitCode: 1},
		{Command: "inspect", Args: []string{"second"}, ResponseIndex: 0, ExitCode: 0},
	})
	require.NoError(t, err)
	for _, artifact := range []string{"command-invocations", "tool-events"} {
		manifest := nativeReferenceManifest(t, artifact, tape)
		reference, err := NativeReference(manifest, manifest.Origin, artifact, 1, tape, false, false)
		require.NoError(t, err)
		require.Equal(t, "/1", reference.Pointer)
		require.Equal(t, manifest.Origin, reference.Origin)
		_, err = NativeReference(manifest, manifest.Origin, artifact, 2, tape, false, false)
		require.Error(t, err)
		_, err = NativeReference(manifest, manifest.Origin, artifact, 1, []byte(`[{"command":"inspect"},{"command":"inspect"}]`), false, false)
		require.ErrorContains(t, err, "digest mismatch", "names and matcher indices do not authenticate the tape")
		_, err = NativeReference(manifest, manifest.Origin, artifact, 1, []byte(`[{"command":"other"},{"command":"inspect"}]`), false, false)
		require.Error(t, err, "the entire tape, not just selected ordinal, must match")
	}
}

func TestNativeReferenceNeverFabricatesMissingOrUnassessedEvidence(t *testing.T) {
	tape := []byte(`[{"native_id":"request"}]`)
	manifest := nativeReferenceManifest(t, "tool-events", tape)
	for _, origin := range []models.EvidenceOrigin{
		{},
		{EvalID: "other", TaskID: "task", RunNumber: 1, AttemptCount: 1, PriorAttempts: "none"},
		{EvalID: "eval", TaskID: "other", RunNumber: 1, AttemptCount: 1, PriorAttempts: "none"},
		{EvalID: "eval", TaskID: "task", RunNumber: 2, AttemptCount: 1, PriorAttempts: "none"},
		{EvalID: "eval", TaskID: "task", RunNumber: 1, AttemptCount: 2, PriorAttempts: "none"},
	} {
		_, err := NativeReference(manifest, origin, "tool-events", 0, tape, false, false)
		require.Error(t, err)
	}
	_, err := NativeReference(manifest, manifest.Origin, "tool-events", -1, tape, false, false)
	require.Error(t, err)
	_, err = NativeReference(manifest, manifest.Origin, "unknown", 0, tape, false, false)
	require.Error(t, err)
	_, err = NativeReference(manifest, manifest.Origin, "tool-events", 0, tape, true, false)
	require.ErrorContains(t, err, "unassessed")
	manifest.Artifacts[0].Redacted = true
	require.NoError(t, evidence.Seal(manifest))
	_, err = NativeReference(manifest, manifest.Origin, "tool-events", 0, tape, false, true)
	require.ErrorContains(t, err, "redaction")
	manifest.Artifacts[0].Availability = "unavailable"
	manifest.Artifacts[0].ContentDigest = nil
	manifest.Artifacts[0].Document = ""
	manifest.Artifacts[0].Pointer = ""
	require.NoError(t, evidence.Seal(manifest))
	_, err = NativeReference(manifest, manifest.Origin, "tool-events", 0, tape, false, false)
	require.ErrorContains(t, err, "not preserved")
	_, err = NativeReference(nil, models.EvidenceOrigin{}, "tool-events", 0, tape, false, false)
	require.Error(t, err)
	for _, invalid := range []string{`{"0":{}}`, `null`, `[]`, `[null]`, `[1]`, `invalid`} {
		_, err := NativeReference(manifest, manifest.Origin, "tool-events", 0, []byte(invalid), false, false)
		require.Error(t, err)
	}
}
