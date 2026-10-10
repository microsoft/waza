package evidence_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/assurance"
	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/snapshot"
	"github.com/stretchr/testify/require"
)

func referenceFixture(t *testing.T) *assurance.AuthoredOutput {
	t.Helper()
	text := "finite"
	input, err := assurance.NewAuthoredOutput("subject", "case", "task", 1, &text)
	require.NoError(t, err)
	return input
}

func TestReferenceInputSharedProfileAndHistoricalGuards(t *testing.T) {
	input := referenceFixture(t)
	manifest := input.Manifest()
	require.NoError(t, evidence.Validate(manifest))
	require.NoError(t, evidence.SealReferenceInput(manifest))
	before, err := json.Marshal(manifest)
	require.NoError(t, err)
	require.Error(t, evidence.Seal(manifest))
	after, err := json.Marshal(manifest)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Error(t, evidence.ValidateNative(manifest))
	require.Error(t, evidence.Bind(manifest, manifest.Origin.EvalID, "task", 1, 1, false))
	require.Error(t, evidence.Bind(manifest, "native-eval", "task", 1, 1, true))
	_, err = evidence.Regrade(manifest)
	require.Error(t, err)
	native, err := snapshot.Capture(snapshot.CaptureInput{
		EvalID: "native-eval", Task: &models.TestCase{TestID: "task"}, ExecutionMode: "mock",
		Run: &models.RunResult{RunNumber: 1, Attempts: 1, Status: models.StatusPassed, FinalOutput: "native"},
	})
	require.NoError(t, err)
	nativeBytes, err := json.Marshal(native.Evidence)
	require.NoError(t, err)
	require.Error(t, evidence.SealReferenceInput(native.Evidence))
	unchanged, err := json.Marshal(native.Evidence)
	require.NoError(t, err)
	require.Equal(t, nativeBytes, unchanged)
	require.NoError(t, evidence.Bind(native.Evidence, "native-eval", "task", 1, 1, false))
	regraded, err := evidence.Regrade(native.Evidence)
	require.NoError(t, err)
	require.Equal(t, "1.0", regraded.Version)
	require.NoError(t, evidence.ValidateNative(regraded))
	native.Evidence.Origin.EvalID = "reference:subject"
	data, err := json.Marshal(native.Evidence)
	require.NoError(t, err)
	value, err := jsonutil.Parse(data)
	require.NoError(t, err)
	object, ok := value.(map[string]any)
	require.True(t, ok)
	delete(object, "sha256")
	digest, err := evidence.JSONDigest(object)
	require.NoError(t, err)
	native.Evidence.SHA256 = digest.SHA256
	require.NoError(t, evidence.Validate(native.Evidence))
	require.Error(t, evidence.Bind(native.Evidence, "native-eval", "task", 1, 1, true))
	require.Error(t, evidence.Seal(native.Evidence))
	require.Error(t, evidence.SealReferenceInput(nil))
	require.Error(t, evidence.ValidateNative(nil))
}

func TestReferenceInputRejectsMalformedProfile(t *testing.T) {
	for name, mutate := range map[string]func(*models.EvidenceManifest){
		"native origin":        func(m *models.EvidenceManifest) { m.Origin.EvalID = "native-eval" },
		"empty subject":        func(m *models.EvidenceManifest) { m.Origin.EvalID = "reference:" },
		"unsafe subject":       func(m *models.EvidenceManifest) { m.Origin.EvalID = "reference:../subject" },
		"unsafe task":          func(m *models.EvidenceManifest) { m.Origin.TaskID = "task/other" },
		"ordinal":              func(m *models.EvidenceManifest) { m.Origin.RunNumber = 0 },
		"attempt count":        func(m *models.EvidenceManifest) { m.Origin.AttemptCount = 2 },
		"prior history":        func(m *models.EvidenceManifest) { m.Origin.PriorAttempts = "unknown" },
		"live runtime":         func(m *models.EvidenceManifest) { m.Runtime.ExecutionMode = "live" },
		"model inference":      func(m *models.EvidenceManifest) { m.Runtime.RequestedModel = "model" },
		"skill inference":      func(m *models.EvidenceManifest) { m.Runtime.NativeSkillControl = "sdk_default" },
		"enforcement":          func(m *models.EvidenceManifest) { m.Runtime.VerifiedEnforcement = "enforced" },
		"sdk inference":        func(m *models.EvidenceManifest) { value := "sdk"; m.Runtime.SDKVersion = &value },
		"source history":       func(m *models.EvidenceManifest) { m.SourceManifestSHA256 = strings.Repeat("a", 64) },
		"diagnostic history":   func(m *models.EvidenceManifest) { m.Diagnostics = []models.EvidenceDiagnostic{{Code: "runtime"}} },
		"redaction":            func(m *models.EvidenceManifest) { m.Redaction.MatchCount = 1 },
		"redaction policy":     func(m *models.EvidenceManifest) { m.Redaction.Policy = "unknown" },
		"redaction rules":      func(m *models.EvidenceManifest) { m.Redaction.AppliedRules = []string{"rule"} },
		"limitations":          func(m *models.EvidenceManifest) { m.Redaction.Limitations = []string{"history"} },
		"extra artifact":       func(m *models.EvidenceManifest) { m.Artifacts = append(m.Artifacts, m.Artifacts[0]) },
		"missing artifact":     func(m *models.EvidenceManifest) { m.Artifacts = nil },
		"artifact id":          func(m *models.EvidenceManifest) { m.Artifacts[0].ID = "tool-events" },
		"artifact kind":        func(m *models.EvidenceManifest) { m.Artifacts[0].Kind = "tool_events" },
		"snapshot document":    func(m *models.EvidenceManifest) { m.Artifacts[0].Document = "snapshot" },
		"relative root":        func(m *models.EvidenceManifest) { m.Artifacts[0].Pointer = "/output" },
		"partial":              func(m *models.EvidenceManifest) { m.Artifacts[0].Completeness = "partial" },
		"redacted artifact":    func(m *models.EvidenceManifest) { m.Artifacts[0].Redacted = true },
		"captured reason":      func(m *models.EvidenceManifest) { m.Artifacts[0].Reason = "changed" },
		"digest only":          func(m *models.EvidenceManifest) { m.Artifacts[0].Availability = "digest_only" },
		"absent with locator":  func(m *models.EvidenceManifest) { m.Artifacts[0].Availability = "unavailable" },
		"missing digest":       func(m *models.EvidenceManifest) { m.Artifacts[0].ContentDigest = nil },
		"invalid digest":       func(m *models.EvidenceManifest) { m.Artifacts[0].ContentDigest.SHA256 = "invalid" },
		"uppercase digest":     func(m *models.EvidenceManifest) { m.Artifacts[0].ContentDigest.SHA256 = strings.Repeat("A", 64) },
		"utf8 encoding":        func(m *models.EvidenceManifest) { m.Artifacts[0].ContentDigest.Encoding = "utf8" },
		"source digest":        func(m *models.EvidenceManifest) { m.Artifacts[0].SourceDigest = m.Artifacts[0].ContentDigest },
		"future version":       func(m *models.EvidenceManifest) { m.Version = "1.2" },
		"native version":       func(m *models.EvidenceManifest) { m.Version = "1.0" },
		"implicit new version": func(m *models.EvidenceManifest) { m.Version = "" },
	} {
		t.Run(name, func(t *testing.T) {
			manifest := referenceFixture(t).Manifest()
			mutate(manifest)
			before, err := json.Marshal(manifest)
			require.NoError(t, err)
			require.Error(t, evidence.Validate(manifest))
			require.Error(t, evidence.SealReferenceInput(manifest))
			after, err := json.Marshal(manifest)
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestReferenceInputContentAndPointerContract(t *testing.T) {
	input := referenceFixture(t)
	manifest := input.Manifest()
	ref, err := evidence.Reference(manifest, evidence.ReferenceInputArtifactID)
	require.NoError(t, err)
	for _, pointer := range []string{"", "/payload", "/output/child", "/missing", "/bad~2"} {
		wrong := ref
		wrong.Pointer = pointer
		_, err := evidence.Resolve(manifest, wrong)
		require.Error(t, err)
	}
	wrong := ref
	wrong.Origin.TaskID = "other"
	_, err = evidence.Resolve(manifest, wrong)
	require.Error(t, err)
	for _, raw := range []string{`"finite"`, `{"output":"finite"}`, `{"output":"finite","output":"finite"}`, string(input.ProjectionBytes()) + `{}`, strings.Replace(string(input.ProjectionBytes()), "finite", "tampered", 1)} {
		require.Error(t, evidence.VerifyContent(manifest, ref, []byte(raw), true, true))
	}
	_, err = evidence.Reference(manifest, "missing")
	require.Error(t, err)
}

func TestReferenceInputUnavailableProfile(t *testing.T) {
	input, err := assurance.NewAuthoredOutput("subject", "case", "task", 1, nil)
	require.NoError(t, err)
	for _, mutate := range []func(*models.EvidenceManifest){
		func(m *models.EvidenceManifest) { m.Artifacts[0].Reason = "" },
		func(m *models.EvidenceManifest) { m.Artifacts[0].Completeness = "complete" },
		func(m *models.EvidenceManifest) { m.Artifacts[0].Document = evidence.ReferenceInputDocument },
		func(m *models.EvidenceManifest) { m.Artifacts[0].Pointer = "/payload" },
		func(m *models.EvidenceManifest) { m.Artifacts[0].Availability = "not_requested" },
	} {
		manifest := input.Manifest()
		mutate(manifest)
		require.Error(t, evidence.SealReferenceInput(manifest))
	}

}

func TestReferenceInputUnknownFieldsAreNeverResealed(t *testing.T) {
	input := referenceFixture(t)
	data, err := json.Marshal(input.Manifest())
	require.NoError(t, err)
	data = []byte(strings.TrimSuffix(string(data), "}") + `,"history":{}}`)
	var manifest models.EvidenceManifest
	require.NoError(t, json.Unmarshal(data, &manifest))
	require.True(t, manifest.HasUnknownFields())
	require.Error(t, evidence.Validate(&manifest))
	require.Error(t, evidence.SealReferenceInput(&manifest))
	require.Error(t, evidence.Seal(&manifest))
}
