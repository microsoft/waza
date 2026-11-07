package manifestaggregate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/stretchr/testify/require"
)

func evidenceFixture(t *testing.T, backend *memoryBackend, trial int, state evidencestate.State, payload []byte) (string, string, evidencestate.Document) {
	t.Helper()
	manifest, terminal := fixture(t, trial, 1, runnerterminal.Success)
	var usageArtifact artifactmanifest.Artifact
	if payload != nil {
		usageArtifact = artifactmanifest.Artifact{
			Name: "caller-usage-alias", SHA256: artifactmanifest.Hash(payload),
			SizeBytes: int64(len(payload)), MediaType: "application/json",
		}
		manifest.Artifacts = append(manifest.Artifacts, usageArtifact)
	}
	origin := publish(t, backend, manifest, terminal, false)
	store := artifactmanifest.NewStore(backend)
	if payload != nil {
		require.NoError(t, store.Put(t.Context(), usageArtifact, payload))
	}
	receipt, err := store.Finalize(t.Context(), origin)
	require.NoError(t, err)
	require.Equal(t, "finalized", receipt.Publication)
	require.Equal(t, "verified", receipt.Integrity)
	doc := evidencestate.Document{
		Kind: "waza.execution-evidence", SchemaVersion: evidencestate.Version,
		Publication: "derived", OriginManifest: origin, Lineage: []evidencestate.Lineage{},
	}
	for _, category := range []evidencestate.Category{
		evidencestate.Conversation, evidencestate.Tool, evidencestate.Grader,
		evidencestate.Reliability, evidencestate.Usage, evidencestate.Provenance,
	} {
		record := evidencestate.Record{
			Category: category, State: evidencestate.Unavailable, Source: evidencestate.Unknown,
			Context: evidencestate.UnknownContext, Artifacts: []string{}, Reason: evidencestate.NotRecorded,
		}
		if category == evidencestate.Usage {
			record.State, record.Source, record.Context = state, evidencestate.Authored, evidencestate.Declared
			switch state {
			case evidencestate.Present:
				count := int64(0)
				if payload != nil {
					count = 1
					record.Artifacts = []string{usageArtifact.Name}
				}
				record.ObservedCount, record.Reason = &count, ""
			case evidencestate.Redacted:
				record.Reason = evidencestate.Privacy
			case evidencestate.Incomplete:
				record.Reason = evidencestate.PartialCapture
			case evidencestate.Conflicting:
				record.Reason = evidencestate.SourceConflict
			}
		}
		doc.Categories = append(doc.Categories, record)
	}
	published, err := evidencestate.Publish(t.Context(), backend, doc)
	require.NoError(t, err)
	require.Equal(t, "verified", published.Integrity)
	return origin, published.EvidenceSHA256, doc
}

func recordedUsage(t *testing.T, credits *float64) []byte {
	t.Helper()
	zero := 0.0
	usage := models.UsageStats{AICredits: credits, ModelMetrics: map[string]models.ModelUsage{"recorded-model": {AICredits: &zero}}}
	data, err := json.Marshal(usage)
	require.NoError(t, err)
	return data
}

func evidenceRequest(origin, digest string) Request {
	references := []EvidenceInput{{ManifestSHA256: origin, EvidenceSHA256: digest}}
	return Request{Manifests: []string{origin}, Evidence: &references}
}

func TestAggregateEvidenceRecordedZeroPrecisionAndUnqualifiedTotals(t *testing.T) {
	for _, credits := range []float64{0, 0.12345678901234567} {
		backend := &memoryBackend{objects: map[string][]byte{}}
		origin, digest, _ := evidenceFixture(t, backend, 1, evidencestate.Present, recordedUsage(t, &credits))
		creates := backend.creates
		report, err := Aggregate(t.Context(), backend, evidenceRequest(origin, digest))
		require.NoError(t, err)
		require.Equal(t, creates, backend.creates)
		require.Equal(t, "complete", report.Completion, "membership is not category qualification")
		require.Equal(t, "incomplete", report.Evidence.Completion)
		require.Equal(t, "withheld", report.Evidence.UsageTotals)
		require.Equal(t, "producer-and-category-qualification-unavailable", report.Evidence.Qualification)
		candidate := report.Evidence.Members[0].Candidates[0]
		require.Equal(t, "verified", candidate.State)
		require.Equal(t, origin, candidate.OriginManifest)
		require.Len(t, candidate.Declarations, 6)
		require.Equal(t, evidencestate.Present, candidate.Usage.State)
		require.Equal(t, "source-content-and-recorded-usage-shape-only", candidate.Usage.Verification)
		require.Equal(t, credits, *candidate.Usage.Recorded.AICredits)
		require.Equal(t, 1, report.Evidence.DeclarationStateCounts[evidencestate.Usage][evidencestate.Present])
		data, err := Encode(report)
		require.NoError(t, err)
		require.NotContains(t, string(data), "caller-usage-alias")
		require.NotContains(t, string(data), `"score"`)
		require.NotContains(t, string(data), `"new_operation_credits"`)
	}
}

func TestAggregateEvidenceMissingIncompleteRedactedAndPresentEmpty(t *testing.T) {
	for _, control := range []struct {
		state   evidencestate.State
		payload []byte
		want    evidencestate.State
	}{
		{evidencestate.Present, recordedUsage(t, nil), evidencestate.Incomplete},
		{evidencestate.Present, nil, evidencestate.Present},
		{evidencestate.Unavailable, nil, evidencestate.Unavailable},
		{evidencestate.Redacted, []byte(`{"restricted":"do-not-expose"}`), evidencestate.Redacted},
		{evidencestate.Incomplete, nil, evidencestate.Incomplete},
		{evidencestate.Conflicting, nil, evidencestate.Conflicting},
	} {
		t.Run(string(control.state)+"/"+string(control.want), func(t *testing.T) {
			backend := &memoryBackend{objects: map[string][]byte{}}
			origin, digest, _ := evidenceFixture(t, backend, 1, control.state, control.payload)
			report, err := Aggregate(t.Context(), backend, evidenceRequest(origin, digest))
			require.NoError(t, err)
			usage := report.Evidence.Members[0].Candidates[0].Usage
			require.Equal(t, control.want, usage.State)
			if control.payload == nil || control.state != evidencestate.Present {
				require.Nil(t, usage.Recorded)
			} else {
				require.Nil(t, usage.Recorded.AICredits)
			}
			data, err := Encode(report)
			require.NoError(t, err)
			require.NotContains(t, string(data), "do-not-expose")
			require.Equal(t, "withheld", report.Evidence.UsageTotals)
		})
	}
}

func TestAggregateEvidenceSemanticErrorKeepsByteReceiptNotUsage(t *testing.T) {
	backend := &memoryBackend{objects: map[string][]byte{}}
	origin, digest, _ := evidenceFixture(t, backend, 1, evidencestate.Present, []byte(`{}`))
	report, err := Aggregate(t.Context(), backend, evidenceRequest(origin, digest))
	require.ErrorIs(t, err, ErrIncomplete)
	candidate := report.Evidence.Members[0].Candidates[0]
	require.Equal(t, "verified", candidate.State)
	require.Len(t, candidate.Declarations, 6)
	require.Equal(t, evidencestate.Unavailable, candidate.Usage.State)
	require.Nil(t, candidate.Usage.Recorded)
	require.Contains(t, candidate.Diagnostics, artifactmanifest.Diagnostic{
		Code: "usage_admission_failed", Message: "Source bytes were verified, but recorded usage shape was not admitted. No credits, score or billing total was substituted.",
	})
	_, err = Encode(report)
	require.NoError(t, err)
}

func TestAggregateEvidenceReorderingDuplicatesAndCompetingSidecars(t *testing.T) {
	backend := &memoryBackend{objects: map[string][]byte{}}
	zero := 0.0
	origin, first, doc := evidenceFixture(t, backend, 1, evidencestate.Present, recordedUsage(t, &zero))
	doc.Categories[0].Source = evidencestate.Authored
	doc.Categories[0].Context = evidencestate.Declared
	other, err := evidencestate.Publish(t.Context(), backend, doc)
	require.NoError(t, err)
	require.NotEqual(t, first, other.EvidenceSHA256)
	references := []EvidenceInput{{origin, other.EvidenceSHA256}, {origin, first}}
	report, err := Aggregate(t.Context(), backend, Request{Manifests: []string{origin}, Evidence: &references})
	require.ErrorIs(t, err, ErrIncomplete)
	require.Equal(t, "conflicting", report.Evidence.Members[0].State)
	require.Len(t, report.Evidence.Members[0].Candidates, 2)
	require.Empty(t, report.Evidence.DeclarationStateCounts, "competing sidecars must not be weighted as two origin declarations")
	firstJSON, err := Encode(report)
	require.NoError(t, err)
	slices.Reverse(references)
	references = append(references, references...)
	reordered, err := Aggregate(t.Context(), backend, Request{Manifests: []string{origin, origin}, Evidence: &references})
	require.ErrorIs(t, err, ErrIncomplete)
	otherJSON, err := Encode(reordered)
	require.NoError(t, err)
	require.Equal(t, firstJSON, otherJSON)
}

func TestAggregateEvidenceActualOriginMismatchAndChangingSource(t *testing.T) {
	backend := &memoryBackend{objects: map[string][]byte{}}
	zero := 0.0
	origin, digest, _ := evidenceFixture(t, backend, 1, evidencestate.Present, recordedUsage(t, &zero))
	otherOrigin, _, _ := evidenceFixture(t, backend, 2, evidencestate.Unavailable, nil)
	report, err := Aggregate(t.Context(), backend, evidenceRequest(otherOrigin, digest))
	require.ErrorIs(t, err, ErrIncomplete)
	candidate := report.Evidence.Members[0].Candidates[0]
	require.Equal(t, "conflicting", candidate.State)
	require.Equal(t, origin, candidate.OriginManifest)
	require.Empty(t, candidate.Declarations)
	require.Nil(t, candidate.Usage)
	require.Equal(t, "evidence_origin_mismatch", candidate.Diagnostics[0].Code)
	_, err = Encode(report)
	require.NoError(t, err)
	reads := 0
	backend.before = func(key string) {
		if key == "manifest-"+origin {
			reads++
			if reads == 2 {
				delete(backend.objects, "final-"+origin)
			}
		}
	}
	report, err = Aggregate(context.Background(), backend, evidenceRequest(origin, digest))
	require.ErrorIs(t, err, ErrIncomplete)
	require.Equal(t, 2, reads)
	require.Equal(t, "success", report.Members[0].State)
	require.Equal(t, "unfinalized", report.Evidence.Members[0].Candidates[0].State)
	require.Nil(t, report.Evidence.Members[0].Candidates[0].Usage)
	_, err = Encode(report)
	require.NoError(t, err)
}

func TestAggregateEvidenceRequestPresenceBoundsAndWireSize(t *testing.T) {
	request := Request{}
	references := []EvidenceInput{}
	for index := range MaxInputs {
		digest := fmt.Sprintf("%064x", index+1)
		request.Manifests = append(request.Manifests, digest)
		references = append(references, EvidenceInput{digest, strings.Repeat("b", 64)})
	}
	request.Evidence = &references
	data, err := json.Marshal(request)
	require.NoError(t, err)
	require.LessOrEqual(t, len(data), MaxRequestBytes)
	require.Greater(t, len(data), 16<<10)
	boundary := append(bytes.Clone(data), bytes.Repeat([]byte(" "), MaxRequestBytes-len(data))...)
	_, err = ParseRequest(boundary)
	require.NoError(t, err)
	_, err = ParseRequest(append(boundary, ' '))
	require.ErrorIs(t, err, ErrInvalid)
	references = append(references, references[0])
	require.ErrorIs(t, request.Validate(), ErrInvalid, "submitted count is bounded before deduplication")
	origin := strings.Repeat("a", 64)
	for _, suffix := range []string{`, "evidence":null`, `, "evidence":false`} {
		_, err := ParseRequest([]byte(`{"manifests":["` + origin + `"]` + suffix + `}`))
		require.ErrorIs(t, err, ErrInvalid)
	}
	for _, suffix := range []string{"", `, "evidence":[]`} {
		parsed, err := ParseRequest([]byte(`{"manifests":["` + origin + `"]` + suffix + `}`))
		require.NoError(t, err)
		require.Equal(t, suffix != "", parsed.Evidence != nil)
	}
	unselected := []EvidenceInput{{strings.Repeat("c", 64), strings.Repeat("b", 64)}}
	require.ErrorIs(t, (Request{Manifests: []string{origin}, Evidence: &unselected}).Validate(), ErrInvalid)
}

func TestAggregateEvidence128SmallSourcesAndReorderedBudgetWithholding(t *testing.T) {
	for _, control := range []struct {
		name     string
		size     int
		count    int
		withheld bool
	}{
		{"128-small", 0, MaxInputs, false},
		{"large-valid-sources", 20 << 20, 8, true},
	} {
		t.Run(control.name, func(t *testing.T) {
			backend := &memoryBackend{objects: map[string][]byte{}}
			request := Request{}
			references := []EvidenceInput{}
			var payload []byte
			if control.size > 0 {
				payload = append([]byte{'"'}, bytes.Repeat([]byte("a"), control.size-2)...)
				payload = append(payload, '"')
				require.True(t, json.Valid(payload))
			}
			for index := range control.count {
				origin, digest, _ := evidenceFixture(t, backend, index+1, evidencestate.Unavailable, payload)
				request.Manifests = append(request.Manifests, origin)
				references = append(references, EvidenceInput{origin, digest})
			}
			request.Evidence = &references
			creates := backend.creates
			first, firstErr := Aggregate(t.Context(), backend, request)
			require.Len(t, first.Members, control.count)
			require.Equal(t, control.count, first.StateCounts["success"])
			limits := 0
			for _, member := range first.Evidence.Members {
				if member.State == "limit_exceeded" {
					limits++
					require.Nil(t, member.Candidates[0].Usage)
					require.Equal(t, "read_budget_exceeded", member.Candidates[0].Diagnostics[0].Code)
				}
			}
			if control.withheld {
				require.ErrorIs(t, firstErr, ErrLimit)
				require.ErrorIs(t, firstErr, ErrIncomplete)
				require.Positive(t, limits)
				require.Equal(t, "incomplete", first.Completion)
			} else {
				require.NoError(t, firstErr)
				require.Zero(t, limits)
				require.Equal(t, MaxInputs, first.Evidence.DeclarationStateCounts[evidencestate.Usage][evidencestate.Unavailable])
			}
			encoded, err := Encode(first)
			require.NoError(t, err)
			slices.Reverse(request.Manifests)
			slices.Reverse(references)
			second, secondErr := Aggregate(t.Context(), backend, request)
			require.Equal(t, firstErr == nil, secondErr == nil)
			reordered, err := Encode(second)
			require.NoError(t, err)
			require.Equal(t, encoded, reordered)
			require.Equal(t, creates, backend.creates, "aggregation remains read-only")
		})
	}
}
