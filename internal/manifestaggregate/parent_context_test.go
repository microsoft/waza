package manifestaggregate

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/stretchr/testify/require"
)

func TestParentContextBudgetFailureRetainsPrimaryChild(t *testing.T) {
	local, err := artifactmanifest.OpenLocal(t.TempDir())
	require.NoError(t, err)
	defer func() { require.NoError(t, local.Close()) }()
	parent, _, _ := sourceContextPublication(t, local, nil)
	_, document, _ := sourceContextPublication(t, local, &executionattempt.Parent{ExecutionID: parent.ExecutionID, Relationship: "retry"})
	reader, err := NewEvidenceReader(local)
	require.NoError(t, err)
	primary, err := inspectSourceContext(t.Context(), reader.backend, document, false)
	require.ErrorIs(t, err, ErrUnavailable)
	require.Equal(t, "verified", primary.State)
	primaryCost := reader.backend.read
	require.Positive(t, primaryCost)
	reader.backend.read = MaxReadBytes - primaryCost - artifactmanifest.MaxNamespaceObjectBytes
	documentWire, err := json.Marshal(document)
	require.NoError(t, err)
	reader.categoryDocument = artifactmanifest.Hash(documentWire)
	view, err := reader.InspectSourceContext(t.Context(), document)
	require.ErrorIs(t, err, ErrLimit)
	require.Equal(t, "verified", view.State)
	require.Equal(t, primary.Source, view.Source)
	require.Equal(t, "limit_exceeded", view.ParentContext.State)
	require.Nil(t, view.ParentContext.Source)
	require.EqualValues(t, MaxReadBytes-artifactmanifest.MaxNamespaceObjectBytes, reader.backend.read)
	before := reader.backend.read
	view, err = reader.InspectSourceContext(t.Context(), document)
	require.ErrorIs(t, err, ErrLimit)
	require.Equal(t, "limit_exceeded", view.State)
	require.Nil(t, view.ParentContext)
	require.Equal(t, before, reader.backend.read, "denied repeat does no provider I/O or fresh-budget retry")
}

func TestParentContextRequiresPrimaryCategoryAdmissionBeforeOptionalRead(t *testing.T) {
	directory := t.TempDir()
	local, err := artifactmanifest.OpenLocal(directory)
	require.NoError(t, err)
	defer func() { require.NoError(t, local.Close()) }()
	parent, _, _ := sourceContextPublication(t, local, nil)
	_, document, _ := sourceContextPublication(t, local, &executionattempt.Parent{ExecutionID: parent.ExecutionID, Relationship: "retry"})
	path := filepath.Join(directory, artifactmanifest.ExecutionLedgerNamespaceV1+"-completion-"+parent.ExecutionID)
	require.FileExists(t, path)
	require.NoError(t, os.Remove(path))
	reader, err := NewEvidenceReader(local)
	require.NoError(t, err)
	_, _, categoryErr := reader.InspectCategories(t.Context(), strings.Repeat("0", 64))
	require.ErrorIs(t, categoryErr, artifactmanifest.ErrMissing)
	view, err := reader.InspectSourceContext(t.Context(), document)
	require.ErrorIs(t, err, ErrUnavailable)
	require.False(t, errors.Is(err, artifactmanifest.ErrMissing), "the missing optional parent was never probed after primary category failure")
	require.Equal(t, "verified", view.State)
	require.Equal(t, "unavailable", view.ParentContext.State)
	require.Nil(t, view.ParentContext.Source)
}

func TestParentContextCannotSpliceChangedNamespaceParentWithVerifiedManifest(t *testing.T) {
	directory := t.TempDir()
	local, err := artifactmanifest.OpenLocal(directory)
	require.NoError(t, err)
	defer func() { require.NoError(t, local.Close()) }()
	parent, _, _ := sourceContextPublication(t, local, nil)
	child, document, _ := sourceContextPublication(t, local, &executionattempt.Parent{ExecutionID: parent.ExecutionID, Relationship: "retry"})
	bounded := &boundedBackend{Backend: local, readBudget: &readBudget{}}
	namespace, err := artifactmanifest.NewNamespace(local, artifactmanifest.ExecutionLedgerNamespaceV1)
	require.NoError(t, err)
	nsReader, err := namespace.Reader(bounded.decorate)
	require.NoError(t, err)
	reader, err := executionledger.NewReader(nsReader, bounded)
	require.NoError(t, err)
	primary, err := reader.ReadCompleted(t.Context(), child, document.AttemptArtifact)
	require.NoError(t, err)
	changed := primary.Reservation.Attempt
	changed.Parent = &executionattempt.Parent{ExecutionID: "exec-" + strings.Repeat("a", 64), Relationship: "retry"}
	wire, err := executionattempt.EncodeAttempt(changed)
	require.NoError(t, err)
	completion := primary.Completion
	completion.ReservationDigest = artifactmanifest.Hash(wire)
	completionWire, err := executionledger.EncodeCompletion(completion)
	require.NoError(t, err)
	for key, data := range map[string][]byte{"reservation": wire, "completion": completionWire} {
		path := filepath.Join(directory, artifactmanifest.ExecutionLedgerNamespaceV1+"-"+key+"-"+child.ExecutionID)
		require.NoError(t, os.Chmod(path, 0o600))
		require.NoError(t, os.WriteFile(path, data, 0o600))
	}
	_, err = reader.ReadCompleted(t.Context(), child, document.AttemptArtifact)
	require.NoError(t, err, "canonical IDs/binding alone still match; exact descriptor equality is the gate")
	view, err := inspectParentContext(t.Context(), bounded, reader, primary, child, document.AttemptArtifact, true)
	require.ErrorIs(t, err, artifactmanifest.ErrConflict)
	require.Equal(t, "conflicting", view.State)
	require.Nil(t, view.Source, "a second-view parent ID can never be combined with the first-view manifest")
}

func TestSourceContextNoParentFrozenWire(t *testing.T) {
	view := SourceContext{
		State: "verified", Verification: "content-and-declared-context-only",
		Source: &executionattempt.Source{ExecutionID: "exec-" + strings.Repeat("a", 64), ManifestDigest: strings.Repeat("b", 64)},
	}

	wire, err := json.Marshal(view)
	require.NoError(t, err)
	require.Equal(t, `{"state":"verified","verification":"content-and-declared-context-only","source":{"execution_id":"exec-`+
		strings.Repeat("a", 64)+`","manifest_digest":"`+strings.Repeat("b", 64)+`"}}`, string(wire))
}

func TestParentContextProfileRejectsFalseOrTransitiveLinks(t *testing.T) {
	backend := &memoryBackend{objects: map[string][]byte{}}
	origin, digest, _ := evidenceFixture(t, backend, 1, evidencestate.Unavailable, nil)
	report, err := Aggregate(t.Context(), backend, evidenceRequest(origin, digest))
	require.NoError(t, err)
	source := &SourceContext{
		State: "verified", Verification: "content-and-declared-context-only",
		Source: &executionattempt.Source{ExecutionID: "exec-" + strings.Repeat("a", 64), ManifestDigest: origin},
		ParentContext: &ParentContext{
			State: "verified", Verification: "content-and-declared-context-only", Relationship: "retry",
			Source: &executionattempt.Source{ExecutionID: "exec-" + strings.Repeat("b", 64), ManifestDigest: strings.Repeat("c", 64)},
		},
	}
	report.Evidence.Members[0].Candidates[0].SourceContext = source
	wire, err := Encode(report)
	require.NoError(t, err, "authored wire-shape control, not execution admission evidence")
	for _, control := range []string{"null", "nested", "unverified-source", "verified-branch", "self", "complete-missing", "unknown"} {
		t.Run(control, func(t *testing.T) {
			var object map[string]any
			require.NoError(t, json.Unmarshal(wire, &object))
			evidence, ok := object["evidence"].(map[string]any)
			require.True(t, ok)
			members, ok := evidence["members"].([]any)
			require.True(t, ok)
			require.Len(t, members, 1)
			member, ok := members[0].(map[string]any)
			require.True(t, ok)
			candidates, ok := member["candidates"].([]any)
			require.True(t, ok)
			require.Len(t, candidates, 1)
			candidate, ok := candidates[0].(map[string]any)
			require.True(t, ok)
			context, ok := candidate["source_context"].(map[string]any)
			require.True(t, ok)
			parent, ok := context["parent_context"].(map[string]any)
			require.True(t, ok)
			switch control {
			case "null":
				context["parent_context"] = nil
			case "nested":
				parent["parent_context"] = map[string]any{"state": "verified"}
			case "unverified-source":
				parent["state"], parent["verification"] = "unavailable", "unverified"
			case "verified-branch":
				parent["relationship"] = "branch"
			case "self":
				parent["source"] = context["source"]
			case "complete-missing":
				require.Equal(t, "complete", object["completion"])
				parent["state"], parent["verification"] = "unavailable", "unverified"
				delete(parent, "source")
			case "unknown":
				parent["latest_execution"] = true
			}
			changed, err := json.Marshal(object)
			require.NoError(t, err)
			require.Error(t, validateReport(changed))
		})
	}
}
