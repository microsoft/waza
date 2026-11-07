package manifestaggregate

import (
	"bytes"
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runneridentity"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/stretchr/testify/require"
)

type memoryBackend struct {
	objects map[string][]byte
	creates int
	gets    int
	before  func(string)
}

func (b *memoryBackend) Create(_ context.Context, key string, data []byte) (bool, error) {
	b.creates++
	if _, exists := b.objects[key]; exists {
		return false, nil
	}
	b.objects[key] = bytes.Clone(data)
	return true, nil
}

func (b *memoryBackend) Get(ctx context.Context, key string, limit int64) ([]byte, error) {
	b.gets++
	if b.before != nil {
		b.before(key)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, ok := b.objects[key]
	if !ok {
		return nil, artifactmanifest.ErrMissing
	}
	if int64(len(data)) > limit {
		return nil, artifactmanifest.ErrCorrupt
	}
	return bytes.Clone(data), nil
}

func fixture(t *testing.T, trial, attempt int, outcome runnerterminal.Outcome) (artifactmanifest.Manifest, []byte) {
	t.Helper()
	data, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	request, err := runnercontract.Parse(data)
	require.NoError(t, err)
	request.Selection.Trial = trial
	request.Selection.Attempt = attempt
	identity, err := runneridentity.Derive(request)
	require.NoError(t, err)
	binding := artifactmanifest.Binding{
		Identity: *identity, Selection: request.Selection, ExecutionMode: "mock", GraderIDs: []string{"check"},
	}
	exit, known := outcome.ExitCode()
	require.True(t, known)
	envelope := runnerterminal.Envelope{
		Kind: "waza.runner-terminal", SchemaVersion: runnerterminal.Version,
		Outcome: outcome, ExitCode: exit, FailureBoundary: "none", Validation: "valid",
		Selection: &binding.Selection, Identity: &binding.Identity, Diagnostics: []runnerterminal.Diagnostic{},
	}
	switch outcome {
	case runnerterminal.Success, runnerterminal.EvaluationFailure:
		status := "passed"
		if outcome == runnerterminal.EvaluationFailure {
			status = "failed"
		}
		envelope.EngineCalls = 1
		envelope.Observation = &runnerterminal.Observation{
			ExecutionMode: binding.ExecutionMode, EngineCalls: 1, Selection: binding.Selection,
			RunNumber: trial, Attempts: attempt, Status: status,
			Validations: []runnerterminal.Verdict{{Identifier: "check", Passed: outcome == runnerterminal.Success}},
		}
	default:
		envelope.FailureBoundary = "startup"
	}
	if outcome != runnerterminal.Success {
		envelope.Diagnostics = []runnerterminal.Diagnostic{{Code: "fixture", Message: "Authored control; not execution attestation."}}
	}
	terminal, err := runnerterminal.Encode(envelope, binding.TerminalBinding())
	require.NoError(t, err)
	return artifactmanifest.Manifest{
		Kind: "waza.artifact-manifest", SchemaVersion: artifactmanifest.Version, Binding: binding,
		TerminalArtifact: "terminal",
		Artifacts: []artifactmanifest.Artifact{{Name: "terminal", SHA256: artifactmanifest.Hash(terminal),
			SizeBytes: int64(len(terminal)), MediaType: "application/json"}},
	}, terminal
}

func publish(t *testing.T, backend artifactmanifest.Backend, manifest artifactmanifest.Manifest, terminal []byte, final bool) string {
	t.Helper()
	ctx := context.Background()
	store := artifactmanifest.NewStore(backend)
	data, err := artifactmanifest.Encode(manifest)
	require.NoError(t, err)
	digest, err := store.Create(ctx, data)
	require.NoError(t, err)
	require.NoError(t, store.Put(ctx, manifest.Artifacts[0], terminal))
	if final {
		receipt, err := store.Finalize(ctx, digest)
		require.NoError(t, err)
		require.Equal(t, "finalized", receipt.Publication)
		require.Equal(t, "verified", receipt.Integrity)
	}
	return digest
}

func TestAggregateSetReorderingAndDuplicates(t *testing.T) {
	backend := &memoryBackend{objects: map[string][]byte{}}
	m1, t1 := fixture(t, 1, 1, runnerterminal.Success)
	m2, t2 := fixture(t, 2, 1, runnerterminal.EvaluationFailure)
	d1, d2 := publish(t, backend, m1, t1, true), publish(t, backend, m2, t2, true)
	creates := backend.creates
	first, err := Aggregate(context.Background(), backend, Request{Manifests: []string{d1, d2}})
	require.NoError(t, err)
	second, err := Aggregate(context.Background(), backend, Request{Manifests: []string{d2, d1, d2, d1}})
	require.NoError(t, err)
	a, err := Encode(first)
	require.NoError(t, err)
	b, err := Encode(second)
	require.NoError(t, err)
	require.Equal(t, a, b)
	require.Equal(t, creates, backend.creates, "aggregation must not publish or finalize")
	require.Equal(t, map[string]int{"success": 1, "evaluation_failure": 1}, first.StateCounts)
	require.Equal(t, "complete", first.Completion, "membership completeness is not a passing verdict")
	require.Equal(t, "compatible", first.Compatibility, "different trial canonical digests are valid")
	require.Len(t, first.Members, 2)
	require.True(t, slices.IsSorted([]string{first.Members[0].ManifestSHA256, first.Members[1].ManifestSHA256}))
	for _, member := range first.Members {
		require.NotNil(t, member.Verdicts)
		require.Len(t, *member.Verdicts, 1)
		require.Equal(t, member.Outcome == runnerterminal.Success, (*member.Verdicts)[0].Passed)
	}
	require.NotContains(t, string(a), `"score"`)
	require.NotContains(t, string(a), `"ai_credits"`)
}

func TestAggregateEveryDegradedInput(t *testing.T) {
	for _, control := range []struct {
		name    string
		outcome runnerterminal.Outcome
		mutate  func(*memoryBackend, string, artifactmanifest.Manifest)
		final   bool
		state   string
	}{
		{"missing", runnerterminal.Success, func(b *memoryBackend, d string, _ artifactmanifest.Manifest) { delete(b.objects, "manifest-"+d) }, true, "missing"},
		{"missing artifact", runnerterminal.Success, func(b *memoryBackend, _ string, m artifactmanifest.Manifest) {
			delete(b.objects, "blob-"+m.Artifacts[0].SHA256)
		}, true, "missing"},
		{"unfinalized", runnerterminal.Success, nil, false, "unfinalized"},
		{"corrupt", runnerterminal.Success, func(b *memoryBackend, _ string, m artifactmanifest.Manifest) {
			b.objects["blob-"+m.Artifacts[0].SHA256] = []byte("corrupt")
		}, true, "corrupt"},
		{"marker conflict", runnerterminal.Success, func(b *memoryBackend, d string, _ artifactmanifest.Manifest) {
			b.objects["final-"+d] = []byte(strings.Repeat("0", 64))
		}, true, "conflicting"},
		{"cancellation", runnerterminal.Canceled, nil, true, string(runnerterminal.Canceled)},
		{"timeout", runnerterminal.Timeout, nil, true, "timeout"},
		{"failed", runnerterminal.InfrastructureFailure, nil, true, "infrastructure_failure"},
	} {
		t.Run(control.name, func(t *testing.T) {
			backend := &memoryBackend{objects: map[string][]byte{}}
			m, terminal := fixture(t, 1, 1, control.outcome)
			digest := publish(t, backend, m, terminal, control.final)
			if control.mutate != nil {
				control.mutate(backend, digest, m)
			}
			report, err := Aggregate(context.Background(), backend, Request{Manifests: []string{digest}})
			require.ErrorIs(t, err, ErrIncomplete)
			require.Len(t, report.Members, 1)
			require.Equal(t, control.state, report.Members[0].State)
			require.Equal(t, map[string]int{control.state: 1}, report.StateCounts)
			require.Equal(t, "incomplete", report.Completion)
			require.NotEmpty(t, report.Members[0].Diagnostics)
		})
	}
}

func TestAggregateNoWinnerForDistinctAttempts(t *testing.T) {
	backend := &memoryBackend{objects: map[string][]byte{}}
	m1, t1 := fixture(t, 1, 1, runnerterminal.Success)
	m2, t2 := fixture(t, 1, 2, runnerterminal.EvaluationFailure)
	d1, d2 := publish(t, backend, m1, t1, true), publish(t, backend, m2, t2, true)
	report, err := Aggregate(context.Background(), backend, Request{Manifests: []string{d2, d1}})
	require.ErrorIs(t, err, ErrIncomplete)
	require.Len(t, report.Conflicts, 1)
	expected := []string{d1, d2}
	slices.Sort(expected)
	require.Equal(t, expected, report.Conflicts[0].Manifests)
	require.Equal(t, map[string]int{"conflicting": 2}, report.StateCounts)
	require.Equal(t, runnerterminal.Success, report.Members[slices.Index(expected, d1)].Outcome)
	require.Equal(t, runnerterminal.EvaluationFailure, report.Members[slices.Index(expected, d2)].Outcome)
}

func TestAggregateIncompatibleContextsAndMixedVersions(t *testing.T) {
	backend := &memoryBackend{objects: map[string][]byte{}}
	m1, t1 := fixture(t, 1, 1, runnerterminal.Success)
	m2, t2 := fixture(t, 2, 1, runnerterminal.Success)
	m2.Binding.Identity.Run = "run-" + strings.Repeat("f", 64)
	t2 = bytes.ReplaceAll(t2, []byte(m1.Binding.Identity.Run), []byte(m2.Binding.Identity.Run))
	m2.Artifacts[0].SHA256 = artifactmanifest.Hash(t2)
	m2.Artifacts[0].SizeBytes = int64(len(t2))
	d1, d2 := publish(t, backend, m1, t1, true), publish(t, backend, m2, t2, true)
	report, err := Aggregate(context.Background(), backend, Request{Manifests: []string{d1, d2}})
	require.ErrorIs(t, err, ErrIncomplete)
	require.Equal(t, "incompatible", report.Compatibility)
	require.Len(t, report.Members, 2)
	require.Equal(t, "incompatible_context", report.Diagnostics[0].Code)
	original := backend.objects["manifest-"+d1]
	mixed := bytes.Replace(original, []byte(`"schema_version":"1.0"`), []byte(`"schema_version":"2.0"`), 1)
	require.NotEqual(t, original, mixed, "mutate the actual stored profile")
	badDigest := artifactmanifest.Hash(mixed)
	backend.objects["manifest-"+badDigest] = mixed
	backend.objects["final-"+badDigest] = []byte(badDigest)
	report, err = Aggregate(context.Background(), backend, Request{Manifests: []string{d2, badDigest}})
	require.Error(t, err)
	require.NotEqual(t, "complete", report.Completion)
	require.Len(t, report.Members, 2)
}

func TestAggregateRereadsAfterSuccessfulReceipt(t *testing.T) {
	backend := &memoryBackend{objects: map[string][]byte{}}
	m, terminal := fixture(t, 1, 1, runnerterminal.Success)
	digest := publish(t, backend, m, terminal, true)
	reads := 0
	backend.before = func(key string) {
		if key == "blob-"+m.Artifacts[0].SHA256 {
			reads++
			if reads == 2 {
				backend.objects[key] = []byte("changed after inspection")
			}
		}
	}
	report, err := Aggregate(context.Background(), backend, Request{Manifests: []string{digest}})
	require.ErrorIs(t, err, artifactmanifest.ErrCorrupt)
	require.Equal(t, 2, reads)
	require.Equal(t, "corrupt", report.Members[0].State)
	require.Empty(t, report.Members[0].Verdicts)
}

func TestAggregateAdmissionAndCancellation(t *testing.T) {
	backend := &memoryBackend{objects: map[string][]byte{}}
	for _, digests := range [][]string{nil, {}, {"../secret"}, {strings.Repeat("A", 64)}, slices.Repeat([]string{strings.Repeat("a", 64)}, MaxInputs+1)} {
		_, err := Aggregate(context.Background(), backend, Request{Manifests: digests})
		require.ErrorIs(t, err, ErrInvalid)
	}
	require.Zero(t, backend.gets, "invalid input must not touch storage")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report, err := Aggregate(ctx, backend, Request{Manifests: []string{strings.Repeat("a", 64)}})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, string(runnerterminal.Canceled), report.Members[0].State)
	bounded := &boundedBackend{Backend: backend, read: MaxReadBytes}
	_, err = bounded.Get(context.Background(), "blob-"+strings.Repeat("a", 64), 1)
	require.ErrorIs(t, err, ErrLimit)
	require.False(t, errors.Is(err, artifactmanifest.ErrMissing))
	bounded = &boundedBackend{Backend: backend}
	for range 4 {
		_, err = bounded.Get(context.Background(), "blob-"+strings.Repeat("a", 64), 64<<20)
		require.ErrorIs(t, err, artifactmanifest.ErrMissing)
	}
	gets := backend.gets
	_, err = bounded.Get(context.Background(), "blob-"+strings.Repeat("a", 64), 1)
	require.ErrorIs(t, err, ErrLimit)
	require.Equal(t, gets, backend.gets, "failed reads consume their requested limits")
}

func TestAggregateRequestOriginalWire(t *testing.T) {
	digest := strings.Repeat("a", 64)
	for _, data := range []string{`{}`, `null`, `{"manifests":null}`, `{"manifests":[]}`,
		`{"manifests":["` + digest + `"],"extra":true}`,
		`{"manifests":["` + digest + `"],"manifests":["` + digest + `"]}`,
		`{"manifests":["\ud800"]}`, strings.Repeat(" ", 16385)} {
		_, err := ParseRequest([]byte(data))
		require.ErrorIs(t, err, ErrInvalid, data)
	}
	request, err := ParseRequest([]byte("{\r\n\"manifests\":[\"" + digest + "\",\"" + digest + "\"]\r\n}"))
	require.NoError(t, err)
	require.Equal(t, []string{digest, digest}, request.Manifests)
}

func TestAggregateReportRejectsInconsistentCountsAndMembership(t *testing.T) {
	backend := &memoryBackend{objects: map[string][]byte{}}
	manifest, terminal := fixture(t, 1, 1, runnerterminal.Success)
	digest := publish(t, backend, manifest, terminal, true)
	report, err := Aggregate(context.Background(), backend, Request{Manifests: []string{digest}})
	require.NoError(t, err)
	report.StateCounts["success"] = 2
	_, err = Encode(report)
	require.ErrorIs(t, err, ErrInvalid)
	report.StateCounts["success"] = 1
	report.Members = append(report.Members, report.Members[0])
	report.StateCounts["success"] = 2
	_, err = Encode(report)
	require.ErrorIs(t, err, ErrInvalid)
}
