package executionlifecycle

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/stretchr/testify/require"
)

func TestInspectionStrictWireBeforeStorage(t *testing.T) {
	reservation, _, binding := source(t)
	value := Inspection{
		Kind: "waza.execution-lifecycle-inspection", SchemaVersion: Version,
		OriginalAdmission: artifactmanifest.Binding{
			Identity: *binding.Identity, Selection: binding.Selection,
			ExecutionMode: binding.ExecutionMode, GraderIDs: binding.GraderIDs,
		},
		ReservationDigest: artifactmanifest.Hash(reservation), ObservationDigests: []string{}, StaleAfterMS: 60000,
	}
	data, err := json.Marshal(value)
	require.NoError(t, err)
	parsed, err := ParseInspection(data)
	require.NoError(t, err)
	require.Equal(t, value, *parsed)
	backend := &faultyBackend{}
	for _, mutation := range []string{
		`{}`,
		strings.Replace(string(data), `"observation_digests":[]`, `"observation_digests":null`, 1),
		strings.Replace(string(data), `"stale_after_ms":60000`, `"stale_after_ms":0`, 1),
		strings.Replace(string(data), `"stale_after_ms":60000`, `"stale_after_ms":60000.5`, 1),
		strings.Replace(string(data), `"kind":`, `"k\u0069nd":"wrong","kind":`, 1),
		strings.Replace(string(data), `"schema_version":"1.0"`, `"schema_version":"1.0","store":"/private/path"`, 1),
	} {
		require.NotEqual(t, string(data), mutation)
		_, err := Inspect(t.Context(), backend, []byte(mutation))
		require.ErrorIs(t, err, ErrInvalid)
		require.Zero(t, backend.gets)
	}
	value.AsOf = "2026-11-07T17:00:00.000Z"
	data, err = json.Marshal(value)
	require.NoError(t, err)
	_, err = ParseInspection(data)
	require.ErrorIs(t, err, ErrInvalid)
}

func TestInspectionActualLocalRechecksEveryBlob(t *testing.T) {
	backend, err := artifactmanifest.OpenLocal(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, backend.Close()) })
	reservation, _, binding := source(t)
	digest := artifactmanifest.Hash(reservation)
	require.NoError(t, artifactmanifest.NewStore(backend).Put(t.Context(), artifactmanifest.Artifact{
		Name: "reservation", SHA256: digest, SizeBytes: int64(len(reservation)), MediaType: "application/json",
	}, reservation))
	p, err := New(reservation, binding, "")
	require.NoError(t, err)
	first := event(p, "started", origin)
	firstDigest, err := NewBlobs(backend).Put(t.Context(), first)
	require.NoError(t, err)
	require.NoError(t, p.Apply(first))
	heartbeat := event(p, "heartbeat", origin)
	heartbeatDigest, err := NewBlobs(backend).Put(t.Context(), heartbeat)
	require.NoError(t, err)
	contextValue := Inspection{
		Kind: "waza.execution-lifecycle-inspection", SchemaVersion: Version,
		OriginalAdmission: artifactmanifest.Binding{
			Identity: *binding.Identity, Selection: binding.Selection,
			ExecutionMode: binding.ExecutionMode, GraderIDs: binding.GraderIDs,
		},
		ReservationDigest: digest, ObservationDigests: []string{firstDigest, heartbeatDigest},
		AsOf: "2026-11-07T17:01:00Z", StaleAfterMS: 60000,
	}
	encode := func() []byte {
		t.Helper()
		data, err := json.Marshal(contextValue)
		require.NoError(t, err)
		return data
	}
	result, err := Inspect(t.Context(), backend, encode())
	require.NoError(t, err)
	require.True(t, *result.SuspectedOrphan)
	require.Equal(t, "caller-declared-unverified", result.Evidence)
	require.Len(t, result.Observations, 2)
	contextValue.ObservationDigests = []string{heartbeatDigest, firstDigest}
	_, err = Inspect(t.Context(), backend, encode())
	require.ErrorIs(t, err, ErrTransition)
	contextValue.ObservationDigests = []string{firstDigest, heartbeatDigest}
	contextValue.OriginalAdmission.Selection.Attempt++
	_, err = Inspect(t.Context(), backend, encode())
	require.ErrorIs(t, err, ErrContext)
	contextValue.OriginalAdmission.Selection = binding.Selection
	contextValue.ObservationDigests = []string{strings.Repeat("b", 64)}
	_, err = Inspect(t.Context(), backend, encode())
	require.ErrorIs(t, err, artifactmanifest.ErrMissing)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = Inspect(ctx, backend, encode())
	require.ErrorIs(t, err, context.Canceled)
}

func TestProjectionSchemaRejectsIncoherentDeclarations(t *testing.T) {
	reservation, terminal, binding := source(t)
	p, err := New(reservation, binding, "")
	require.NoError(t, err)
	require.NoError(t, p.Apply(event(p, "started", origin)))
	running, err := p.Snapshot("", time.Second)
	require.NoError(t, err)
	final := event(p, "terminal_recorded", origin)
	final.Terminal = &terminal
	require.NoError(t, p.Apply(final))
	finished, err := p.Snapshot("", time.Second)
	require.NoError(t, err)
	for _, control := range []struct {
		name     string
		original Snapshot
		change   func(*Snapshot)
	}{
		{"running-completed", running, func(s *Snapshot) { s.CompletedUnits = new(1) }},
		{"expiry-without-deadline", running, func(s *Snapshot) { s.DeadlineExpired = true }},
		{"retagged-handling", running, func(s *Snapshot) { s.Observations[0].Handling = "sdk-abort-returned" }},
		{"terminal-wrong-exit", finished, func(s *Snapshot) { s.Terminal.ExitCode = 1 }},
		{"terminal-wrong-boundary", finished, func(s *Snapshot) { s.Terminal.FailureBoundary = "shutdown" }},
		{"terminal-wrong-progress", finished, func(s *Snapshot) { s.CompletedUnits = new(0) }},
		{"terminal-transient-advice", finished, func(s *Snapshot) {
			s.Retry = RetryAdvice{"conditional-fresh-attempt", "requires-independent-admission-and-side-effect-review"}
		}},
	} {
		t.Run(control.name, func(t *testing.T) {
			snapshot := control.original
			snapshot.Observations = append([]Observation{}, snapshot.Observations...)
			if snapshot.Terminal != nil {
				original := *snapshot.Terminal
				snapshot.Terminal = &original
			}
			before, err := json.Marshal(snapshot)
			require.NoError(t, err)
			require.NoError(t, validateSnapshot(before))
			control.change(&snapshot)
			after, err := json.Marshal(snapshot)
			require.NoError(t, err)
			require.NotEqual(t, string(before), string(after), "control must alter the admitted projection")
			require.ErrorIs(t, validateSnapshot(after), ErrInvalid)
		})
	}
}
