package controlledcomparison_test

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/controlledcomparison"
	"github.com/microsoft/waza/internal/nativesnapshot"
	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/stretchr/testify/require"
)

// These controls also run against the frozen pre-fast-path reader at
// 08b384f3679c67e5a1d09fa5acfb181ad204cbfa, not just two new implementations.
func TestNativeReadSelectedDecisionCaptureVerification(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("native durable creation requires Linux or macOS")
	}
	collect := func(source controlledcomparison.Source) (string, []byte, []byte) {
		t.Helper()
		policy, err := controlledcomparison.PlanNative(t.Context(), source, source, nativeDesign(), nativeRequirements(), nil)
		require.NoError(t, err)
		policy = fixedNativePolicy(t, policy)
		directory := filepath.Join(t.TempDir(), "native")
		calls := 0
		require.NoError(t, controlledcomparison.CollectNativeWithOptions(t.Context(), policy, source, source, directory,
			controlledcomparison.NativeCollectorOptions{NewCopilotClient: nativeFactory(t, &calls)}))
		require.Equal(t, 2, calls)
		raw, err := os.ReadFile(filepath.Join(directory, "native-capture.json"))
		require.NoError(t, err)
		native, _, _, err := controlledcomparison.DecodeNativePolicy(policy)
		require.NoError(t, err)
		require.Equal(t, native.CaptureDigest, releasepolicy.SourceDigest(raw), "collector must persist exactly the committed writer bytes")
		return directory, policy, raw
	}
	directory, policy, original := collect(nativeFixture(t, "fixed-model"))
	// A second actual producer capture is independently valid but belongs to
	// another source path/collection. Do not manufacture a contradictory blob.
	otherSource := nativeFixture(t, "fixed-model")
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(otherSource.EvalPath), "task.yaml"),
		[]byte("id: task-0\nname: independent-source\ngolden: true\ninputs:\n  prompt: say hello\n"), 0o600))
	_, _, other := collect(otherSource)
	require.NotEqual(t, releasepolicy.SourceDigest(original), releasepolicy.SourceDigest(other))
	_, err := nativesnapshot.ReadCollectionCapture(t.Context(), other)
	require.NoError(t, err)

	write := func(name string, raw []byte) {
		t.Helper()
		require.NoError(t, os.WriteFile(filepath.Join(directory, name), raw, 0o600))
	}
	reseal := func(mutate func(*controlledcomparison.NativePolicy)) []byte {
		t.Helper()
		native, _, _, err := controlledcomparison.DecodeNativePolicy(policy)
		require.NoError(t, err)
		mutate(native)
		raw, err := releasepolicy.SealJSON(native)
		require.NoError(t, err)
		_, _, _, err = controlledcomparison.DecodeNativePolicy(raw)
		require.NoError(t, err, "negative policy must remain independently admissible")
		return raw
	}
	assertVerified := func(t *testing.T, decision releasepolicy.Decision, err error) {
		t.Helper()
		require.NoError(t, err)
		require.NotNil(t, decision.Native)
		require.Len(t, decision.Native.Attempts, 2)
		require.Equal(t, "passed", decision.Golden.State)
		require.Equal(t, "observed", decision.Operations.State)
		require.NotEqual(t, "invalid", decision.Compatibility.State)
		require.False(t, decision.Accepted, "the deterministic unjustified single cluster is not quality evidence")
		for _, attempt := range decision.Native.Attempts {
			require.Equal(t, "passed", attempt.Status)
			require.NotNil(t, attempt.Output)
			require.Equal(t, "hello", *attempt.Output)
			require.True(t, attempt.UsageComplete)
		}
	}
	t.Run("original-writer-bytes", func(t *testing.T) {
		decision, err := controlledcomparison.ReadSelectedDecision(t.Context(), directory, policy)
		assertVerified(t, decision, err)
	})
	t.Run("canonical-equivalent-wire", func(t *testing.T) {
		// Alter only leading JSON whitespace, avoiding another copy of every
		// large base64 blob through a generic JSON map.
		noncanonical := append([]byte(" \n\t"), original...)
		require.False(t, bytes.Equal(original, noncanonical))
		require.NotEqual(t, releasepolicy.SourceDigest(original), releasepolicy.SourceDigest(noncanonical))
		capture, err := nativesnapshot.ReadCollectionCapture(t.Context(), noncanonical)
		require.NoError(t, err)
		canonical, err := capture.Marshal(t.Context())
		require.NoError(t, err)
		require.Equal(t, releasepolicy.SourceDigest(original), releasepolicy.SourceDigest(canonical))
		write("native-capture.json", noncanonical)
		t.Cleanup(func() { write("native-capture.json", original) })
		decision, err := controlledcomparison.ReadSelectedDecision(t.Context(), directory, policy)
		assertVerified(t, decision, err)
	})
	t.Run("independently-valid-mismatched-capture", func(t *testing.T) {
		write("native-capture.json", other)
		t.Cleanup(func() { write("native-capture.json", original) })
		decision, err := controlledcomparison.ReadSelectedDecision(t.Context(), directory, policy)
		require.ErrorContains(t, err, "differs from precollection capture")
		require.False(t, decision.Accepted)
		require.Equal(t, "invalid", decision.Compatibility.State)
		require.Empty(t, decision.Native.Attempts, "mismatched capture must not yield verified terminal evidence")
		require.NotEqual(t, "passed", decision.Golden.State)
	})
	t.Run("valid-collection-selected-policy-mismatch", func(t *testing.T) {
		selected := reseal(func(native *controlledcomparison.NativePolicy) {
			_, _, profile, err := controlledcomparison.DecodeNativePolicy(policy)
			require.NoError(t, err)
			profile.Nonce = strings.Repeat("cd", 32)
			native.Profile, err = releasepolicy.SealJSON(profile)
			require.NoError(t, err)
		})
		require.NotEqual(t, policy, selected)
		decision, err := controlledcomparison.ReadSelectedDecision(t.Context(), directory, selected)
		require.ErrorContains(t, err, "selected native policy differs from collection")
		require.False(t, decision.Accepted)
		require.Equal(t, "mismatched", decision.Compatibility.State)
		require.Nil(t, decision.Native)
		decision, err = controlledcomparison.ReadSelectedDecision(t.Context(), directory, policy)
		assertVerified(t, decision, err)
	})
	t.Run("matching-wire-digest-does-not-bypass-admission", func(t *testing.T) {
		invalid := bytes.Replace(original, []byte(`"kind":"waza.release-native-capture"`), []byte(`"kind":"not-a-native-capture"`), 1)
		require.False(t, bytes.Equal(original, invalid))
		_, err := nativesnapshot.ReadCollectionCapture(t.Context(), invalid)
		require.ErrorContains(t, err, "unsupported native capture kind/version")
		selected := reseal(func(native *controlledcomparison.NativePolicy) {
			native.CaptureDigest = releasepolicy.SourceDigest(invalid)
		})
		write("native-capture.json", invalid)
		write("policy.json", selected)
		t.Cleanup(func() {
			write("native-capture.json", original)
			write("policy.json", policy)
		})
		decision, err := controlledcomparison.ReadSelectedDecision(t.Context(), directory, selected)
		require.ErrorContains(t, err, "unsupported native capture kind/version")
		require.False(t, decision.Accepted)
		require.Equal(t, "invalid", decision.Compatibility.State)
		require.Empty(t, decision.Native.Attempts)
	})
	t.Run("matching-wire-digest-does-not-bypass-profile-join", func(t *testing.T) {
		selected := reseal(func(native *controlledcomparison.NativePolicy) {
			_, _, profile, err := controlledcomparison.DecodeNativePolicy(policy)
			require.NoError(t, err)
			requests := profile.Arms[releasepolicy.Baseline].Requests
			require.Len(t, requests, 1)
			require.NotEqual(t, strings.Repeat("0", 64), requests[0].RequestDigest.SHA256)
			requests[0].RequestDigest.SHA256 = strings.Repeat("0", 64)
			native.Profile, err = releasepolicy.SealJSON(profile)
			require.NoError(t, err)
		})
		write("policy.json", selected)
		t.Cleanup(func() { write("policy.json", policy) })
		decision, err := controlledcomparison.ReadSelectedDecision(t.Context(), directory, selected)
		require.ErrorContains(t, err, "native profile request differs from retained source declaration")
		require.False(t, decision.Accepted)
		require.Equal(t, "invalid", decision.Compatibility.State)
		require.Empty(t, decision.Native.Attempts)
		require.NotEqual(t, "passed", decision.Golden.State)
	})
	t.Run("matching-wire-digest-does-not-bypass-projection-join", func(t *testing.T) {
		selected := reseal(func(native *controlledcomparison.NativePolicy) {
			native.CaptureDigest = releasepolicy.SourceDigest(other)
		})
		write("native-capture.json", other)
		write("policy.json", selected)
		t.Cleanup(func() {
			write("native-capture.json", original)
			write("policy.json", policy)
		})
		decision, err := controlledcomparison.ReadSelectedDecision(t.Context(), directory, selected)
		require.ErrorContains(t, err, "native source projection differs from selected policy")
		require.False(t, decision.Accepted)
		require.Equal(t, "invalid", decision.Compatibility.State)
		require.Empty(t, decision.Native.Attempts)
		require.NotEqual(t, "passed", decision.Golden.State)
	})
	// All artifact mutations restore the actual producer bytes before the final
	// control; no subtest is allowed to make a later negative vacuously fail.
	decision, err := controlledcomparison.ReadSelectedDecision(t.Context(), directory, policy)
	assertVerified(t, decision, err)
}
