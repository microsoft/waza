package assurance

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func qualificationReplayTestRecords(t *testing.T, manifest qualificationManifest, events []qualificationEvent) (qualificationDocument, []qualificationRecord) {
	t.Helper()
	wire, err := qualificationManifestValue(manifest)
	require.NoError(t, err)
	backend, err := qualificationSeal(wire.Inputs.BackendProfile)
	require.NoError(t, err)
	ack := func(event qualificationDocument, sequence uint64, previous, payload *string) qualificationDocument {
		doc, err := qualificationSeal(qualificationAcknowledgment{
			Kind: "waza.qualification-acknowledgment", Version: qualificationVersion, InvocationID: wire.InvocationID,
			ManifestSHA256: manifest.document.sha256(), BackendProfileSHA256: backend.sha256(),
			Sequence: sequence, EventSHA256: event.sha256(), PreviousSHA256: previous,
			PayloadSHA256: payload, ReceiptToken: event.sha256(),
		})
		require.NoError(t, err)
		return doc
	}
	records := make([]qualificationRecord, len(events))
	previous := manifest.document.sha256()
	for i, event := range events {
		row, err := qualificationDecode[qualificationEventWire](event.document.bytes(), qualificationDocumentLimit)
		require.NoError(t, err)
		row.Sequence, row.PreviousSHA256 = uint64(i+1), previous
		doc, err := qualificationSeal(row)
		require.NoError(t, err)
		receipt, err := qualificationDecode[qualificationAcknowledgment](ack(doc, row.Sequence, new(previous), row.PayloadSHA256).bytes(), qualificationDocumentLimit)
		require.NoError(t, err)
		record, err := qualificationSeal(qualificationRecordWire{row, receipt})
		require.NoError(t, err)
		records[i] = qualificationRecord{record}
		previous = doc.sha256()
	}
	return ack(manifest.document, 0, nil, nil), records
}

func qualificationReplayAssertPrefix(t *testing.T, old, current qualificationPrefix) {
	t.Helper()
	require.Equal(t, old.document, current.document)
	require.Equal(t, old.manifest.document, current.manifest.document)
	require.Equal(t, old.ack, current.ack)
	require.Equal(t, old.records, current.records)
	require.Equal(t, old.pending, current.pending)
	require.Equal(t, old.complete, current.complete)
	require.Equal(t, old.nextStage, current.nextStage)
	require.Equal(t, old.nextOrdinal, current.nextOrdinal)
	require.Equal(t, old.final, current.final)
}

func TestQualificationReplayOldMachineDifferential(t *testing.T) {
	manifest, _ := qualificationProtocolFixture(t)
	events, _ := qualificationTestCompleteHistory(t, manifest, true)
	last := qualificationTestEvent(t, manifest, uint64(len(events)+1), events[len(events)-1].document.sha256(), "run_admission", nil)
	wire, err := qualificationDecode[qualificationEventWire](last.document.bytes(), qualificationDocumentLimit)
	require.NoError(t, err)
	wire.Type, wire.Admission = "run_terminal", nil
	wire.Completion = &qualificationCompletionWire{State: "not_assessed", ReasonCode: "report_invalid", CompletedJobs: 4}
	doc, err := qualificationSeal(wire)
	require.NoError(t, err)
	events = append(events, qualificationEvent{doc})
	ack, records := qualificationReplayTestRecords(t, manifest, events)
	for n := 0; n <= len(records); n++ {
		t.Run(fmt.Sprintf("accepted-prefix-%d", n), func(t *testing.T) {
			old, oldErr := qualificationOldDerivePrefix(manifest, ack, records[:n])
			current, err := qualificationDerivePrefix(manifest, ack, records[:n])
			require.NoError(t, oldErr)
			require.NoError(t, err)
			qualificationReplayAssertPrefix(t, old, current)
		})
	}
	for _, tc := range []struct {
		name   string
		events []qualificationEvent
	}{
		{"skipped-admission", events[1:2]},
		{"repeated-admission", []qualificationEvent{events[0], events[0]}},
		{"reordered-job", []qualificationEvent{events[0], events[1], events[3]}},
		{"wrong-next-job", []qualificationEvent{events[0], events[5]}},
		{"early-decision", []qualificationEvent{events[0], events[18]}},
		{"repeated-terminal", append(slices.Clone(events), events[len(events)-1])},
		{"terminal-before-suffix", append(slices.Clone(events), events[0])},
		{"premature-completed-count", events[len(events)-1:]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ack, bad := qualificationReplayTestRecords(t, manifest, tc.events)
			old, oldErr := qualificationOldDerivePrefix(manifest, ack, bad)
			current, err := qualificationDerivePrefix(manifest, ack, bad)
			require.Error(t, oldErr)
			require.Error(t, err)
			qualificationReplayAssertPrefix(t, old, current)
		})
	}
	for _, tc := range []struct {
		name   string
		index  int
		change func(*qualificationEventWire)
		pass   bool
	}{
		{"negative-admission", 0, func(w *qualificationEventWire) {
			w.Admission.Allowed = false
			w.Admission.ReasonCode = "admission_rejected"
		}, true},
		{"negative-currentness", 1, func(w *qualificationEventWire) { w.CurrentnessAcknowledgment.Current = false }, true},
		{"wrong-ordinal", 1, func(w *qualificationEventWire) { w.Ordinal = new(uint64(1)) }, false},
		{"wrong-stage", 1, func(w *qualificationEventWire) { w.CurrentnessRequest.Stage = "after_cleanup" }, false},
		{"wrong-chain", 0, func(w *qualificationEventWire) { w.PreviousSHA256 = strings.Repeat("f", 64) }, false},
		{"unsafe-sequence", 0, func(w *qualificationEventWire) { w.Sequence = 9007199254740992 }, false},
		{"repeated-challenge", 5, func(w *qualificationEventWire) {
			first, err := qualificationDecode[qualificationRecordWire](records[1].document.bytes(), qualificationDocumentLimit)
			require.NoError(t, err)
			w.CurrentnessRequest.Challenge = first.Event.CurrentnessRequest.Challenge
			request, err := qualificationSeal(*w.CurrentnessRequest)
			require.NoError(t, err)
			w.CurrentnessAcknowledgment.RequestSHA256 = request.sha256()
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := slices.Clone(records[:tc.index+1])
			row, err := qualificationDecode[qualificationRecordWire](bad[tc.index].document.bytes(), qualificationDocumentLimit)
			require.NoError(t, err)
			tc.change(&row.Event)
			eventDoc, err := qualificationSeal(row.Event)
			require.NoError(t, err)
			row.Acknowledgment.EventSHA256 = eventDoc.sha256()
			doc, err := qualificationSeal(row)
			require.NoError(t, err)
			bad[tc.index] = qualificationRecord{doc}
			old, oldErr := qualificationOldDerivePrefix(manifest, ack, bad)
			current, err := qualificationDerivePrefix(manifest, ack, bad)
			require.Equal(t, tc.pass, oldErr == nil)
			require.Equal(t, tc.pass, err == nil)
			qualificationReplayAssertPrefix(t, old, current)
		})
	}
	for _, raw := range []string{`{}`, `null`, `{"event":null}`, `{"event":1,"event":2}`, `{"é":1,"e\u0301":2}`} {
		old, oldErr := qualificationOldDerivePrefix(manifest, ack, []qualificationRecord{{qualificationDocument{canonical: raw}}})
		current, err := qualificationDerivePrefix(manifest, ack, []qualificationRecord{{qualificationDocument{canonical: raw}}})
		require.Error(t, oldErr)
		require.Error(t, err)
		qualificationReplayAssertPrefix(t, old, current)
	}
}

func TestQualificationReplayViewAndForkIsolation(t *testing.T) {
	manifest, _ := qualificationProtocolFixture(t)
	wire, err := qualificationManifestValue(manifest)
	require.NoError(t, err)
	view, err := qualificationManifestViewFromDecoded(manifest, wire)
	require.NoError(t, err)
	before, document, err := view.job(0)
	require.NoError(t, err)
	backend, currentness := view.backend, view.currentness
	wire.Jobs[0].Selector.CaseID = "mutated"
	wire.Jobs[0].NativeRequest.Message = "mutated"
	wire.Jobs[0].NativeRequest.Tools[0].Name = "mutated"
	wire.Inputs.SuppliedReview = json.RawMessage(`null`)
	wire.Inputs.BackendProfile.Namespace = "mutated"
	wire.Inputs.CurrentnessProfile.Authorities[0].AuthorityID = "mutated"
	after, afterDoc, err := view.job(0)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, document, afterDoc)
	require.Equal(t, backend, view.backend)
	require.Equal(t, currentness, view.currentness)
	after.Selector.CaseID = "returned-copy"
	again, _, err := view.job(0)
	require.NoError(t, err)
	require.Equal(t, before, again)
	_, _, err = view.job(^uint64(0))
	require.Error(t, err)
	events, _ := qualificationTestCompleteHistory(t, manifest, true)
	ack, records := qualificationReplayTestRecords(t, manifest, events)
	state, err := qualificationBeginReplay(view, ack)
	require.NoError(t, err)
	for _, record := range records[:2] {
		require.NoError(t, state.advance(record))
	}
	fork := state.fork()
	require.NoError(t, fork.advance(records[2]))
	fork.seenChallenges["mutated"] = true
	fork.records[0] = qualificationRecord{}
	require.Len(t, state.records, 2)
	require.NotEmpty(t, state.records[0].document.canonical)
	require.NotContains(t, state.seenChallenges, "mutated")
	state.final = new(records[0])
	fork = state.fork()
	*fork.final = qualificationRecord{}
	require.NotEmpty(t, state.final.document.canonical)
	badView := *view
	badView.manifest.document.canonical += "\n"
	_, err = qualificationParseEventBoundedView(events[0].document.bytes(), manifest, qualificationDocumentLimit, qualificationTotalLimit, nil, nil, &badView)
	require.Error(t, err)
	require.Error(t, qualificationMatchAckView(manifest, manifest.document, 0, nil, nil, ack, &badView))
	_, err = qualificationManifestViewFromDecoded(qualificationManifest{}, wire)
	require.Error(t, err)
}

type qualificationReplayReadIO struct {
	qualificationJournalIO
	reads []string
}

func (io *qualificationReplayReadIO) ReadDirect(ctx context.Context, root *os.Root, name string, limit uint64) ([]byte, error) {
	io.reads = append(io.reads, name)
	return io.qualificationJournalIO.ReadDirect(ctx, root, name, limit)
}

// The static call-shape check complements actual per-path I/O counts: replay
// must advance inside the one disk-row loop, never restart pure derivation.
func TestQualificationReplayLinearCallShape(t *testing.T) {
	_, path, _, ok := runtime.Caller(0)
	require.True(t, ok)
	file, err := parser.ParseFile(token.NewFileSet(), strings.TrimSuffix(path, "qualification_replay_test.go")+"qualification_journal.go", nil, 0)
	require.NoError(t, err)
	for _, name := range []string{"readChildWithReplay", "mutate"} {
		var method *ast.FuncDecl
		for _, declaration := range file.Decls {
			if fn, ok := declaration.(*ast.FuncDecl); ok && fn.Name.Name == name {
				method = fn
			}
		}
		require.NotNil(t, method)
		advances := 0
		ast.Inspect(method.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fn := call.Fun.(type) {
			case *ast.Ident:
				require.NotContains(t, []string{"qualificationDerivePrefix", "qualificationDerivePrefixView"}, fn.Name)
			case *ast.SelectorExpr:
				if fn.Sel.Name == "advance" {
					advances++
				}
			}
			return true
		})
		require.Equal(t, 1, advances, name+" must have one shared-state advance site")
	}
}
