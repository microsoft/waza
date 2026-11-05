package controlledcomparison_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/microsoft/waza/internal/controlledcomparison"
	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/stretchr/testify/require"
)

// Frozen pre-native policy member oracle from b64c6bb10. Keep these original
// members independent of the current Policy type. The actual old executable
// additionally exercises full legacy assessment in the optional smoke.
type frozenPolicyMembersB64 struct {
	Kind         string          `json:"kind"`
	Version      string          `json:"version"`
	Digest       json.RawMessage `json:"digest"`
	Design       json.RawMessage `json:"design"`
	Arms         json.RawMessage `json:"arms"`
	Changes      json.RawMessage `json:"allowed_changes"`
	GoldenIDs    json.RawMessage `json:"required_golden_task_ids"`
	GoldenRule   string          `json:"golden_rule"`
	FamilyRule   string          `json:"family_rule"`
	Requirements json.RawMessage `json:"requirements"`
}

func TestNativeInnerProfileRejectsFrozenPolicyMemberOracle(t *testing.T) {
	raw := []byte(`{"kind":"waza.release-policy","version":"1.0","digest":{},"design":{},"arms":{},"allowed_changes":[],"required_golden_task_ids":[],"golden_rule":"both_arms_first_attempt_pass","family_rule":"single_contrast_external_family_bound","requirements":{}}`)
	decode := func(raw []byte) error {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		var frozen frozenPolicyMembersB64
		return decoder.Decode(&frozen)
	}
	require.NoError(t, decode(raw))
	var document map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &document))
	document["collection_profile"] = json.RawMessage(`"` + releasepolicy.NativeCollectionProfile + `"`)
	native, err := json.Marshal(document)
	require.NoError(t, err)
	require.ErrorContains(t, decode(native), `unknown field "collection_profile"`)
}

func TestNativeArtifactsAreRequiredAndConfined(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("native durable creation requires Linux or macOS")
	}
	source := nativeFixture(t, "fixed-model")
	policy, err := controlledcomparison.PlanNative(t.Context(), source, source, nativeDesign(), nativeRequirements(), nil)
	require.NoError(t, err)
	directory := filepath.Join(t.TempDir(), "native")
	calls := 0
	require.NoError(t, controlledcomparison.CollectNativeWithOptions(t.Context(), policy, source, source, directory,
		controlledcomparison.NativeCollectorOptions{NewCopilotClient: nativeFactory(t, &calls)}))
	for _, relative := range []string{"native-capture.json", "native/native-task-events.ndjson", "native/native-task-rows.ndjson"} {
		t.Run(relative, func(t *testing.T) {
			path := filepath.Join(directory, relative)
			bytes, err := os.ReadFile(path)
			require.NoError(t, err)
			require.NoError(t, os.Remove(path))
			decision, err := controlledcomparison.ReadSelectedDecision(t.Context(), directory, policy)
			require.Error(t, err)
			require.False(t, decision.Accepted)
			outside := filepath.Join(t.TempDir(), "outside")
			require.NoError(t, os.WriteFile(outside, bytes, 0o600))
			require.NoError(t, os.Symlink(outside, path))
			decision, err = controlledcomparison.ReadSelectedDecision(t.Context(), directory, policy)
			require.Error(t, err)
			require.False(t, decision.Accepted)
			require.NoError(t, os.Remove(path))
			require.NoError(t, os.WriteFile(path, bytes, 0o600))
		})
	}
	require.NoError(t, os.Rename(filepath.Join(directory, "native"), filepath.Join(directory, "retained-native")))
	require.NoError(t, os.Symlink("retained-native", filepath.Join(directory, "native")))
	decision, err := controlledcomparison.ReadSelectedDecision(t.Context(), directory, policy)
	require.ErrorContains(t, err, "directory")
	require.False(t, decision.Accepted)
}
