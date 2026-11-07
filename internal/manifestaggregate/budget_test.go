package manifestaggregate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/stretchr/testify/require"
)

func TestAggregate128SmallValidMembers(t *testing.T) {
	backend := &memoryBackend{objects: map[string][]byte{}}
	digests := []string{}
	for trial := 1; trial <= MaxInputs; trial++ {
		manifest, terminal := fixture(t, trial, 1, runnerterminal.Success)
		digests = append(digests, publish(t, backend, manifest, terminal, true))
	}
	report, err := Aggregate(context.Background(), backend, Request{Manifests: digests})
	require.NoError(t, err)
	require.Len(t, report.Members, MaxInputs)
	require.Equal(t, map[string]int{"success": MaxInputs}, report.StateCounts)
	require.Equal(t, "complete", report.Completion)
	_, err = Encode(report)
	require.NoError(t, err)
}

func TestAggregateLargeValidSetWithheldAtCrossRequestBudget(t *testing.T) {
	backend := &memoryBackend{objects: map[string][]byte{}}
	payload := bytes.Repeat([]byte("x"), 2<<20)
	descriptor := artifactmanifest.Artifact{Name: "payload", SHA256: artifactmanifest.Hash(payload), SizeBytes: int64(len(payload)), MediaType: "text/plain"}
	require.NoError(t, artifactmanifest.NewStore(backend).Put(context.Background(), descriptor, payload))
	digests := []string{}
	for trial := 1; trial <= MaxInputs; trial++ {
		manifest, terminal := fixture(t, trial, 1, runnerterminal.Success)
		manifest.Artifacts = append(manifest.Artifacts, descriptor)
		digests = append(digests, publish(t, backend, manifest, terminal, true))
	}
	first, err := Aggregate(context.Background(), backend, Request{Manifests: digests})
	require.ErrorIs(t, err, ErrLimit)
	require.ErrorIs(t, err, ErrIncomplete)
	require.Len(t, first.Members, MaxInputs, "retain the entire expected set, not just the fitting prefix")
	require.Positive(t, first.StateCounts["success"])
	require.Positive(t, first.StateCounts["limit_exceeded"])
	require.Equal(t, MaxInputs, first.StateCounts["success"]+first.StateCounts["limit_exceeded"])
	for _, member := range first.Members {
		if member.State != "limit_exceeded" {
			continue
		}
		require.Nil(t, member.Verdicts)
		require.Nil(t, member.Logical)
		require.Empty(t, member.Outcome)
		require.Contains(t, member.Diagnostics, artifactmanifest.Diagnostic{
			Code: "read_budget_exceeded", Message: "The cross-request 256 MiB admitted read budget withheld this member. Its source may be individually valid; no missing/corrupt result is inferred.",
		})
		for _, diagnostic := range member.Diagnostics {
			require.NotEqual(t, "storage_error", diagnostic.Code)
		}
	}
	slices.Reverse(digests)
	second, err := Aggregate(context.Background(), backend, Request{Manifests: digests})
	require.ErrorIs(t, err, ErrLimit)
	a, err := Encode(first)
	require.NoError(t, err)
	b, err := Encode(second)
	require.NoError(t, err)
	require.Equal(t, a, b, "sort/dedup must precede every read and budget reservation")
}

func TestAggregateRecordedEmptyVersusAbsentVerdicts(t *testing.T) {
	for _, outcome := range []runnerterminal.Outcome{runnerterminal.Success, runnerterminal.InfrastructureFailure} {
		t.Run(string(outcome), func(t *testing.T) {
			backend := &memoryBackend{objects: map[string][]byte{}}
			manifest, terminal := fixture(t, 1, 1, outcome)
			if outcome == runnerterminal.Success {
				var envelope runnerterminal.Envelope
				require.NoError(t, json.Unmarshal(terminal, &envelope))
				manifest.Binding.GraderIDs = []string{}
				envelope.Observation.Validations = []runnerterminal.Verdict{}
				var err error
				terminal, err = runnerterminal.Encode(envelope, manifest.Binding.TerminalBinding())
				require.NoError(t, err)
				manifest.Artifacts[0].SHA256 = artifactmanifest.Hash(terminal)
				manifest.Artifacts[0].SizeBytes = int64(len(terminal))
			}
			digest := publish(t, backend, manifest, terminal, true)
			report, err := Aggregate(context.Background(), backend, Request{Manifests: []string{digest}})
			if outcome == runnerterminal.Success {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrIncomplete)
			}
			data, err := Encode(report)
			require.NoError(t, err)
			if outcome == runnerterminal.Success {
				require.NotNil(t, report.Members[0].Verdicts)
				require.Empty(t, *report.Members[0].Verdicts)
				require.Contains(t, string(data), `"verdicts":[]`)
			} else {
				require.Nil(t, report.Members[0].Verdicts)
				require.NotContains(t, string(data), `"verdicts"`)
			}
		})
	}
}

type partialFailureBackend struct{ calls int }

func (b *partialFailureBackend) Create(context.Context, string, []byte) (bool, error) {
	panic("aggregation is read only")
}
func (b *partialFailureBackend) Get(context.Context, string, int64) ([]byte, error) {
	b.calls++
	return []byte("discarded partial bytes"), fmt.Errorf("unknown discarded byte count")
}

func TestAggregateFailedReadsReserveUnknownConsumption(t *testing.T) {
	backend := &partialFailureBackend{}
	bounded := &boundedBackend{Backend: backend, readBudget: &readBudget{}}
	for range 4 {
		data, err := bounded.Get(context.Background(), "blob", (MaxReadBytes/4)-1)
		require.Error(t, err)
		require.Nil(t, data, "failed providers must not disclose partial bytes")
	}
	require.EqualValues(t, MaxReadBytes, bounded.read)
	_, err := bounded.Get(context.Background(), "blob", 1)
	require.ErrorIs(t, err, ErrLimit)
	require.Equal(t, 4, backend.calls)
	require.Equal(t, 1, bounded.hits)
}
