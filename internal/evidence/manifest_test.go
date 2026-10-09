package evidence

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func sampleManifest(t *testing.T) *models.EvidenceManifest {
	t.Helper()
	digest, err := JSONDigest(map[string]any{"count": json.Number("9007199254740993"), "a/b": []any{"ok"}})
	require.NoError(t, err)
	manifest := &models.EvidenceManifest{
		Origin:  models.EvidenceOrigin{EvalID: "eval", TaskID: "task", RunNumber: 1, AttemptCount: 1},
		Runtime: models.EvidenceRuntime{ExecutionMode: "unknown", NativeSkillControl: "unknown"},
		Artifacts: []models.EvidenceArtifact{{
			ID: "receipt", Kind: "tool_events", Availability: "captured", Completeness: "complete",
			Document: "snapshot", Pointer: "/toolEvents", ContentDigest: digest,
		}},
	}
	require.NoError(t, Seal(manifest))
	require.Equal(t, "ae787d745a6a9350aff0908f9bfb76ccdaf889b874927d3e5067392db310c003", digest.SHA256)
	require.Equal(t, "df1ce4895c69cd6c14ffdee6e48df023927a0430c23d0f652962a9a8c4acbf8f", manifest.SHA256)
	return manifest
}

func TestManifestIdentityAndScopedReferences(t *testing.T) {
	manifest := sampleManifest(t)
	require.NoError(t, Validate(manifest))
	reference, err := Reference(manifest, "receipt")
	require.NoError(t, err)
	artifact, err := Resolve(manifest, reference)
	require.NoError(t, err)
	require.Equal(t, "captured", artifact.Availability)
	reference.Origin.TaskID = "another-task"
	_, err = Resolve(manifest, reference)
	require.ErrorContains(t, err, "different")
	_, err = Reference(manifest, "missing")
	require.ErrorContains(t, err, "absent")
	_, err = Reference(nil, "receipt")
	require.Error(t, err)
	manifest.Artifacts[0].Completeness = "partial"
	require.ErrorContains(t, Validate(manifest), "identity")
}

func TestVerifyContentAndPointers(t *testing.T) {
	manifest := sampleManifest(t)
	reference, err := Reference(manifest, "receipt")
	require.NoError(t, err)
	reference.Pointer = "/a~1b/0"
	require.NoError(t, VerifyContent(manifest, reference, []byte(`{"a/b":["ok"],"count":9007199254740993}`), true, true))
	for name, content := range map[string]string{
		"tampered":  `{"a/b":["bad"],"count":9007199254740993}`,
		"rounded":   `{"a/b":["ok"],"count":9007199254740992}`,
		"invalid":   `{`,
		"trailing":  `{"a/b":["ok"],"count":9007199254740993} {}`,
		"duplicate": `{"a/b":["ok"],"count":1,"count":9007199254740993}`,
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, VerifyContent(manifest, reference, []byte(content), true, true))
		})
	}
	for _, pointer := range []string{"/missing", "/a~1b/99", "/a~1b/00", "/count/next", "/bad~2"} {
		reference.Pointer = pointer
		require.Error(t, VerifyContent(manifest, reference, []byte(`{"a/b":["ok"],"count":9007199254740993}`), true, true))
	}
}

func TestManifestConditionalFields(t *testing.T) {
	for name, mutate := range map[string]func(*models.EvidenceManifest){
		"duplicate ID":             func(m *models.EvidenceManifest) { m.Artifacts = append(m.Artifacts, m.Artifacts[0]) },
		"empty ID":                 func(m *models.EvidenceManifest) { m.Artifacts[0].ID = "" },
		"bad completeness":         func(m *models.EvidenceManifest) { m.Artifacts[0].Completeness = "verified" },
		"bad availability":         func(m *models.EvidenceManifest) { m.Artifacts[0].Availability = "assured" },
		"bad SHA":                  func(m *models.EvidenceManifest) { m.Artifacts[0].ContentDigest.SHA256 = "abc" },
		"bad encoding":             func(m *models.EvidenceManifest) { m.Artifacts[0].ContentDigest.Encoding = "secret" },
		"no locator":               func(m *models.EvidenceManifest) { m.Artifacts[0].Pointer = "" },
		"no content":               func(m *models.EvidenceManifest) { m.Artifacts[0].ContentDigest = nil },
		"unknown without reason":   func(m *models.EvidenceManifest) { m.Artifacts[0].Completeness = "unknown" },
		"unavailable with content": func(m *models.EvidenceManifest) { m.Artifacts[0].Availability = "unavailable" },
		"digest only with locator": func(m *models.EvidenceManifest) { m.Artifacts[0].Availability = "digest_only" },
	} {
		t.Run(name, func(t *testing.T) {
			manifest := sampleManifest(t)
			mutate(manifest)
			require.Error(t, Seal(manifest))
		})
	}
	require.Error(t, Seal(nil))
	require.Error(t, Validate(nil))
	_, err := JSONDigest(func() {})
	require.Error(t, err)
	manifest := sampleManifest(t)
	manifest.Version = "2.0"
	require.ErrorContains(t, Validate(manifest), "version")
}

func TestRequiredEvidenceRejectsMissingPartialAndChangedInputs(t *testing.T) {
	manifest := sampleManifest(t)
	reference, err := Reference(manifest, "receipt")
	require.NoError(t, err)
	manifest.Artifacts[0].Completeness = "partial"
	manifest.Artifacts[0].Reason = "Interrupted."
	require.NoError(t, Seal(manifest))
	require.ErrorContains(t, VerifyContent(manifest, reference, nil, true, false), "incomplete")
	manifest.Artifacts[0].Completeness, manifest.Artifacts[0].Redacted = "complete", true
	require.NoError(t, Seal(manifest))
	require.ErrorContains(t, VerifyContent(manifest, reference, nil, false, true), "redaction")
	manifest.Artifacts[0] = models.EvidenceArtifact{
		ID: "receipt", Availability: "digest_only", Completeness: "unknown",
		SourceDigest: &models.EvidenceDigest{SHA256: strings.Repeat("a", 64), Encoding: "source-bytes"},
		Reason:       "No file contents preserved.",
	}
	require.NoError(t, Seal(manifest))
	require.ErrorContains(t, VerifyContent(manifest, reference, nil, false, false), "not preserved")
}

func TestRegradePreservesOriginalWithoutReusingAssessmentIdentity(t *testing.T) {
	original := sampleManifest(t)
	original.Artifacts[0].ID = "validations"
	require.NoError(t, Seal(original))
	updated, err := Regrade(original)
	require.NoError(t, err)
	require.NotEqual(t, original.SHA256, updated.SHA256)
	require.Equal(t, original.SHA256, updated.SourceManifestSHA256)
	require.Equal(t, "captured", original.Artifacts[0].Availability)
	require.Equal(t, "unavailable", updated.Artifacts[0].Availability)
	require.NoError(t, Validate(updated))
	again, err := Regrade(updated)
	require.NoError(t, err)
	require.Equal(t, original.SHA256, again.SourceManifestSHA256)
	_, err = Regrade(nil)
	require.Error(t, err)
}

func TestCanonicalTypedAndParsedContentIdentity(t *testing.T) {
	type payload struct {
		Z string      `json:"z"`
		A json.Number `json:"a"`
	}
	digest, err := JSONDigest(payload{Z: "ok", A: "9007199254740993"})
	require.NoError(t, err)
	parsed, err := JSONDigest(map[string]any{"a": json.Number("9007199254740993"), "z": "ok"})
	require.NoError(t, err)
	require.Equal(t, digest, parsed)
	require.Equal(t, "7b80b3c931fd14341f4f635196e2a8004ddd3b98f1d9b5cb2788a78353e4c90e", digest.SHA256)
	manifest := sampleManifest(t)
	encoded, err := json.Marshal(manifest)
	require.NoError(t, err)
	var projection map[string]any
	require.NoError(t, json.Unmarshal(encoded, &projection))
	delete(projection, "sha256")
	expected, err := JSONDigest(projection)
	require.NoError(t, err)
	require.Equal(t, expected.SHA256, manifest.SHA256)
}

func TestUnknownManifestFieldsAreRetainedButNeverValidated(t *testing.T) {
	manifest := sampleManifest(t)
	data, err := json.Marshal(manifest)
	require.NoError(t, err)
	data = append([]byte(`{"unhashed":{"assertion":"complete"},`), data[1:]...)
	var loaded models.EvidenceManifest
	require.NoError(t, json.Unmarshal(data, &loaded))
	require.Error(t, Validate(&loaded))
	rewritten, err := json.Marshal(loaded)
	require.NoError(t, err)
	require.Contains(t, string(rewritten), `"unhashed"`)
	require.Error(t, json.Unmarshal([]byte(`{"version":"1.0","version":"1.0"}`), &loaded))
	manifest.SourceManifestSHA256 = "bad"
	require.Error(t, Seal(manifest))
}

func TestIncompleteOriginCannotResolveAndCachedOriginIsNotRelabeled(t *testing.T) {
	manifest := sampleManifest(t)
	require.NoError(t, Bind(manifest, "new-eval", "task", 1, 1, true))
	require.Error(t, Bind(manifest, "new-eval", "task", 1, 1, false))
	require.Error(t, Bind(manifest, "eval", "wrong-task", 1, 1, true))
	require.Error(t, Bind(manifest, "eval", "task", 2, 1, true))
	require.Error(t, Bind(manifest, "eval", "task", 1, 2, true))
	manifest.Origin.EvalID = ""
	require.NoError(t, Seal(manifest))
	_, err := Reference(manifest, "receipt")
	require.ErrorContains(t, err, "incomplete")
}
