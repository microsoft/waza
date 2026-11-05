package nativetask

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/stretchr/testify/require"
)

func collectionFixture(t *testing.T) (*Prepared, *Admitted, *execution.ExecutionRequest, []CapturedSource, Record) {
	t.Helper()
	_, profile, old, record := fixture(t)
	req := &execution.ExecutionRequest{ModelID: "observed-test-model", Message: "synthetic prompt", NoSkills: true,
		SkipWorkspaceCapture: true, ToolPolicy: &execution.ToolPolicy{Mode: execution.ToolPolicyDenyAll},
		Resources: []execution.ResourceFile{}, Instructions: []execution.InstructionFile{}, Context: map[string]any{}}
	sources := []CapturedSource{{Path: "evaluator", Bytes: "retained evaluator bytes", Digest: releasepolicy.SourceDigest([]byte("retained evaluator bytes"))}}
	prepared, err := PrepareCaptured(t.Context(), releasepolicy.Baseline, "task", req, sources)
	require.NoError(t, err)
	profile.Version, profile.Selection.Version = "2.0", "2.0"
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		plan := profile.Arms[arm]
		plan.Requests[0].RequestDigest = prepared.RequestDigest()
		profile.Arms[arm] = plan
	}
	raw, err := releasepolicy.SealJSON(profile)
	require.NoError(t, err)
	profile, err = DecodeProfile(raw)
	require.NoError(t, err)
	binding, err := old.Binding()
	require.NoError(t, err)
	admitted, err := Admit(t.Context(), prepared, profile, binding.Key)
	require.NoError(t, err)
	binding, err = admitted.Binding()
	require.NoError(t, err)
	record.Version, record.CollectionID, record.ProfileDigest = "2.0", binding.CollectionID, binding.ProfileDigest
	record.RequestDigest = binding.RequestDigest
	return prepared, admitted, req, sources, record
}

func TestCollectionPreparationReferencesLargeExecutableWithoutExpandingAttempts(t *testing.T) {
	_, admitted, req, sources, _ := collectionFixture(t)
	source := strings.Repeat("x", 16<<20+1)
	sources[0].Bytes, sources[0].Digest = source, releasepolicy.SourceDigest([]byte(source))
	sources[0].Executable = true
	prepared, err := PrepareCaptured(t.Context(), releasepolicy.Baseline, "task", req, sources)
	require.NoError(t, err, "the separately selected profile retains an evaluator larger than the private primitive cap")
	require.Less(t, len(prepared.data), 2048, "per-attempt preparation must not encode executable bytes")
	data, err := prepared.view()
	require.NoError(t, err)
	require.Empty(t, data.Sources[0].Bytes)
	profile := &Profile{Kind: profileKind, Version: "2.0", Selection: Selection{"native_text_task", "2.0"},
		Nonce: strings.Repeat("a", 64), PermissionMode: "deny_all", PolicyDigest: digest(t, "policy"), Arms: map[releasepolicy.Arm]ArmPlan{}}
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		profile.Arms[arm] = ArmPlan{digest(t, string(arm)), []RequestPlan{{"task", prepared.RequestDigest()}}}
	}
	raw, err := releasepolicy.SealJSON(profile)
	require.NoError(t, err)
	profile, err = DecodeProfile(raw)
	require.NoError(t, err)
	binding, err := admitted.Binding()
	require.NoError(t, err)
	admitted, err = Admit(t.Context(), prepared, profile, binding.Key)
	require.NoError(t, err)
	require.NoError(t, MatchCollectionSnapshot(t.Context(), admitted, binding.Key, req, sources))
	require.Error(t, MatchSnapshot(t.Context(), admitted, binding.Key, req, sources), "private admission stays separate")
	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()
	file, err := root.Create("evaluator")
	require.NoError(t, err)
	_, err = file.WriteString(source)
	require.NoError(t, err)
	require.NoError(t, file.Close())
	_, err = Prepare(t.Context(), releasepolicy.Baseline, "task", req, []Source{{root, "evaluator"}})
	require.Error(t, err, "private 1.0 cap must not be loosened")
}

func TestVerifiedCollectionSourcesFreezeValidatedDescriptors(t *testing.T) {
	_, admitted, req, sources, _ := collectionFixture(t)
	original := sources[0].Digest
	verified, err := VerifyCollectionSources(t.Context(), sources)
	require.NoError(t, err)

	sources[0].Bytes = "mutated after verification"
	sources[0].Digest = releasepolicy.SourceDigest([]byte(sources[0].Bytes))
	prepared, err := PrepareVerifiedCaptured(t.Context(), releasepolicy.Baseline, "task", req, verified)
	require.NoError(t, err)
	data, err := prepared.view()
	require.NoError(t, err)
	require.Equal(t, original, data.Sources[0].Digest)

	binding, err := admitted.Binding()
	require.NoError(t, err)
	require.NoError(t, MatchVerifiedCollectionSnapshot(t.Context(), admitted, binding.Key, req, verified))

	sources[0].Digest.SHA256 = strings.Repeat("a", 64)
	_, err = VerifyCollectionSources(t.Context(), sources)
	require.ErrorContains(t, err, "invalid retained native source inventory")
}

func BenchmarkCollectionDescriptorAdmission81MiB(b *testing.B) {
	benchmarkCollectionDescriptorAdmission(b, false)
}

func BenchmarkVerifiedCollectionDescriptorAdmission81MiB(b *testing.B) {
	benchmarkCollectionDescriptorAdmission(b, true)
}

func benchmarkCollectionDescriptorAdmission(b *testing.B, useVerified bool) {
	b.Helper()
	request := &execution.ExecutionRequest{ModelID: "observed-test-model", Message: "synthetic prompt", NoSkills: true,
		SkipWorkspaceCapture: true, ToolPolicy: &execution.ToolPolicy{Mode: execution.ToolPolicyDenyAll},
		Resources: []execution.ResourceFile{}, Instructions: []execution.InstructionFile{}, Context: map[string]any{}}
	content := strings.Repeat("x", 81<<20)
	sources := []CapturedSource{{Executable: true, Path: "evaluator", Bytes: content, Digest: releasepolicy.SourceDigest([]byte(content))}}
	verified, err := VerifyCollectionSources(b.Context(), sources)
	require.NoError(b, err)

	prepare := func() (*Prepared, error) {
		if useVerified {
			return PrepareVerifiedCaptured(b.Context(), releasepolicy.Baseline, "task", request, verified)
		}
		return PrepareCaptured(b.Context(), releasepolicy.Baseline, "task", request, sources)
	}
	initial, err := prepare()
	require.NoError(b, err)
	profile := benchmarkCollectionProfile(b, initial.RequestDigest())
	key := releasepolicy.AttemptKey{SampleKey: releasepolicy.SampleKey{ClusterID: "cluster", TaskID: "task", Trial: 1},
		Arm: releasepolicy.Baseline, EvalID: "eval", Attempt: 1}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		prepared, err := prepare()
		require.NoError(b, err)
		admitted, err := Admit(b.Context(), prepared, profile, key)
		require.NoError(b, err)
		if useVerified {
			err = MatchVerifiedCollectionSnapshot(b.Context(), admitted, key, request, verified)
		} else {
			err = MatchCollectionSnapshot(b.Context(), admitted, key, request, sources)
		}
		require.NoError(b, err)
	}
}

func benchmarkCollectionProfile(b *testing.B, requestDigest models.EvidenceDigest) *Profile {
	b.Helper()
	digest := func(value string) models.EvidenceDigest {
		result, err := evidence.JSONDigest(value)
		require.NoError(b, err)
		return *result
	}
	profile := Profile{Kind: profileKind, Version: "2.0", Selection: Selection{"native_text_task", "2.0"},
		Nonce: strings.Repeat("a", 64), PermissionMode: "deny_all", PolicyDigest: digest("policy"),
		Arms: map[releasepolicy.Arm]ArmPlan{}}
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		profile.Arms[arm] = ArmPlan{PlanDigest: digest(string(arm)), Requests: []RequestPlan{{TaskID: "task", RequestDigest: requestDigest}}}
	}
	raw, err := releasepolicy.SealJSON(profile)
	require.NoError(b, err)
	decoded, err := DecodeProfile(raw)
	require.NoError(b, err)
	return decoded
}

func TestCollectionPreparationRawInventoryBoundary(t *testing.T) {
	_, _, req, _, _ := collectionFixture(t)
	unit := strings.Repeat("x", 1<<20)
	digest := releasepolicy.SourceDigest([]byte(unit))
	sources := []CapturedSource{}
	for i := 0; i < 512; i++ {
		sources = append(sources, CapturedSource{Path: fmt.Sprintf("source-%03d", i), Bytes: unit, Digest: digest})
	}
	_, err := PrepareCaptured(t.Context(), releasepolicy.Baseline, "task", req, sources)
	require.NoError(t, err, "exact 512 MiB aggregate boundary")
	sources = append(sources, CapturedSource{Path: "source-512", Bytes: "x", Digest: releasepolicy.SourceDigest([]byte("x"))})
	_, err = PrepareCaptured(t.Context(), releasepolicy.Baseline, "task", req, sources)
	require.ErrorContains(t, err, "bounded inventory")
	_, err = PrepareCaptured(t.Context(), releasepolicy.Baseline, "task", req, make([]CapturedSource, collectionSourceEntries+1))
	require.Error(t, err)
}

func TestCollectionPreparationOrdinarySourceLimit(t *testing.T) {
	_, _, req, sources, _ := collectionFixture(t)
	content := strings.Repeat("x", 16<<20+1)
	sources[0].Bytes, sources[0].Digest = content, releasepolicy.SourceDigest([]byte(content))
	_, err := PrepareCaptured(t.Context(), releasepolicy.Baseline, "task", req, sources)
	require.ErrorContains(t, err, "invalid retained native source")
	sources[0].Executable = true
	_, err = PrepareCaptured(t.Context(), releasepolicy.Baseline, "task", req, sources)
	require.NoError(t, err, "only evaluator-role sources select the executable allowance")
}

func TestCollectionAmbiguousAssistantContentDoesNotBecomeObservedEmptyText(t *testing.T) {
	for _, content := range []string{`{}`, `{"content":null}`, `{"content":""}`, `{"content":false}`, `{"content":0}`} {
		t.Run(content, func(t *testing.T) {
			_, admitted, _, _, record := collectionFixture(t)
			record = operational(record, "Unambiguous typed content was not observed.")
			record.Output = Output{Availability: "unavailable", Reason: "Missing typed content.",
				Messages: []Message{{EventOrdinal: 1, ContentPresence: "unknown"}}}
			record.Response.FinalOutput, record.ActualRow.Run.FinalOutput = "", ""
			record.Response.CanonicalEvents = []json.RawMessage{json.RawMessage(`{"type":"assistant.message","data":` + content + `}`)}
			_, err := DecodeRecord(encoded(t, record), admitted)
			require.NoError(t, err)
			record.Output.Availability, record.Output.Value = "available", new(string)
			record.Output.Reason = ""
			_, err = DecodeRecord(encoded(t, record), admitted)
			require.Error(t, err, "unknown/null/empty must not be repaired into observed empty text")
		})
	}
}

func TestCollectionPreparationInvalidInputsAndVersionIsolation(t *testing.T) {
	prepared, admitted, req, sources, _ := collectionFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := PrepareCaptured(ctx, releasepolicy.Baseline, "task", req, sources)
	require.ErrorIs(t, err, context.Canceled)
	for _, change := range []func([]CapturedSource) []CapturedSource{
		func(s []CapturedSource) []CapturedSource { return append(s, s[0]) },
		func(s []CapturedSource) []CapturedSource { s[0].Digest.SHA256 = strings.Repeat("a", 64); return s },
		func(s []CapturedSource) []CapturedSource { s[0].Path = filepath.Join("..", "escape"); return s },
	} {
		_, err := PrepareCaptured(t.Context(), releasepolicy.Baseline, "task", req, change(append([]CapturedSource{}, sources...)))
		require.Error(t, err)
	}
	_, oldProfile, _, _ := fixture(t)
	key, err := admitted.Binding()
	require.NoError(t, err)
	_, err = Admit(t.Context(), prepared, oldProfile, key.Key)
	require.Error(t, err)
	require.Equal(t, models.EvidenceDigest{SHA256: prepared.requestDigest.SHA256, Encoding: "json-v1"}, prepared.RequestDigest())
}
