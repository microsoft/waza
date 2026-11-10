package evidencestate

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
)

func capturedMockReport(t *testing.T) runnerunit.Report {
	t.Helper()
	data, err := os.ReadFile("../../examples/runner/unit.json")
	if err != nil {
		t.Fatal(err)
	}
	report := runnerunit.NewService(nil).Run(t.Context(), data)
	if report.Calls != 1 || report.Observation == nil || report.Status != "completed" {
		t.Fatalf("production mock runner did not execute exactly once: %+v", report)
	}
	return report
}

func TestCaptureWithheldReliabilityRemovesUnmatchedFreeTextBeforePersistence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		withheld []Category
		trigger  string
		redacted bool
	}{
		{"explicit restricted reliability", []Category{Reliability}, "ordinary undisclosed SDK detail", true},
		{"secret match withholds whole category", []Category{}, "ghp_" + strings.Repeat("a", 40), true},
		{"explicit unrestricted alternative", []Category{}, "ordinary undisclosed SDK detail", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := capturedMockReport(t)
			// Authored failure controls on the real report preserve a valid
			// native error observation; success plus error text is invalid.
			report.Status = "execution_error"
			report.Observation.Status = models.StatusError
			report.Observation.ErrorMsg = tc.trigger
			before, binding := report.Terminal()
			if before.Observation == nil || len(before.Diagnostics) == 0 {
				t.Fatal("failure control did not produce native diagnostic/error evidence")
			}
			p, err := CaptureReport(report, tc.withheld)
			if err != nil {
				t.Fatal(err)
			}
			backend, publication := persistCaptureFixture(t, p)
			_, categories, err := InspectCategories(t.Context(), backend, publication.EvidenceSHA256)
			if err != nil {
				t.Fatal(err)
			}
			terminal, err := runnerterminal.Validate(p.objects["terminal.json"], binding)
			if err != nil {
				t.Fatal(err)
			}
			if tc.redacted {
				for _, diagnostic := range terminal.Diagnostics {
					if diagnostic.Message != "[REDACTED]" {
						t.Fatal("withheld reliability retained unmatched free text")
					}
				}
				if terminal.Observation.ErrorMsg != "[REDACTED]" {
					t.Fatal("withheld reliability retained observation free text")
				}
			} else if terminal.Diagnostics[0].Message != before.Diagnostics[0].Message ||
				terminal.Observation.ErrorMsg != before.Observation.ErrorMsg {
				t.Fatal("unrestricted alternative lost its recorded text")
			}
			terminal.Diagnostics = before.Diagnostics
			terminal.Observation.ErrorMsg = before.Observation.ErrorMsg
			restored, err := runnerterminal.Encode(*terminal, binding)
			if err != nil {
				t.Fatal(err)
			}
			original, err := runnerterminal.Encode(before, binding)
			if err != nil || !bytes.Equal(restored, original) {
				t.Fatal("sanitization changed fields other than diagnostic/error text")
			}
			for _, category := range categories {
				if category.Category == Reliability && ((category.State == Redacted) != tc.redacted) {
					t.Fatal("reliability state contradicts actual source withholding")
				}
			}
			if report.Observation.ErrorMsg != tc.trigger {
				t.Fatal("capture mutated the invocation's private original report")
			}
		})
	}
}

func persistCaptureFixture(t *testing.T, p *PreparedCapture) (artifactmanifest.Backend, Inspection) {
	t.Helper()
	backend, _ := localStore(t)
	ledger, err := executionledger.New(backend)
	if err != nil {
		t.Fatal(err)
	}
	creator, err := ledger.Reserve(t.Context(), p.index.Binding, nil)
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := PersistOwnedCapture(t.Context(), backend, p, ledger, creator)
	if err != nil || inspection.Document == nil || inspection.Integrity != "verified" {
		t.Fatalf("actual creator/store/sidecar path failed: %+v, %v", inspection, err)
	}
	return backend, inspection
}

func TestCapturedMockCategoriesUseActualOwnerAndCurrentSource(t *testing.T) {
	report := capturedMockReport(t)
	p, err := CaptureReport(report, nil)
	if err != nil {
		t.Fatal(err)
	}
	backend, published := persistCaptureFixture(t, p)
	inspection, categories, err := InspectCategories(t.Context(), backend, published.EvidenceSHA256)
	if err != nil || len(categories) != 6 {
		t.Fatalf("category source admission failed: %+v, %v", categories, err)
	}
	for _, result := range categories {
		switch result.Category {
		case Conversation, Tool:
			if result.State != Redacted || result.ObservedCount != nil {
				t.Fatal("default withheld category acquired count or content")
			}
		case Usage:
			if result.State != Unavailable || result.Usage != nil {
				t.Fatal("mock absence became fabricated usage")
			}
		default:
			if result.State != Present || result.ObservedCount == nil {
				t.Fatalf("actual recorded native category was not admitted: %+v", result)
			}
		}
	}
	if inspection.Document.ExecutionSource == nil {
		t.Fatal("actual reserved execution source was omitted")
	}
	ledger, err := executionledger.New(backend)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := ledger.ReadCompleted(t.Context(), *inspection.Document.ExecutionSource)
	if err != nil || completed.Reservation.Attempt.ExecutionID != inspection.Document.ExecutionSource.ExecutionID {
		t.Fatalf("source did not retain actual reservation/completion: %v", err)
	}
	if completed.Publication.Manifest.Binding.Selection.Attempt != report.Resolved.Attempt {
		// Native selection, not legacy identity text, is the original ordinal.
		t.Fatal("original ordinal was retagged")
	}
}

func TestCaptureEmptyProofIsExplicitAndDoesNotRepairAbsence(t *testing.T) {
	report := capturedMockReport(t)
	// Authored presence controls applied to a real report; not live evidence.
	report.Observation.Transcript = []models.TranscriptEvent{}
	report.Observation.ToolEvents = []models.ToolEvent{}
	p, err := CaptureReport(report, []Category{})
	if err != nil {
		t.Fatal(err)
	}
	backend, published := persistCaptureFixture(t, p)
	_, categories, err := InspectCategories(t.Context(), backend, published.EvidenceSHA256)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range categories {
		if result.Category == Conversation || result.Category == Tool {
			if result.State != Present || result.ObservedCount == nil || *result.ObservedCount != 0 {
				t.Fatalf("explicit [] was not retained: %+v", result)
			}
		}
	}
	delete(p.index.EmptyPayloads, Conversation)
	if _, err := EncodeCapture(p.index); !errors.Is(err, ErrInvalid) {
		t.Fatal("missing explicit proof became observed empty")
	}
}

func TestCaptureWithholdsRestrictedBytesBeforeAnyObjectPersistence(t *testing.T) {
	report := capturedMockReport(t)
	const secret = "ghp_abcdefghijklmnopqrstuvwxyz12345678901234567890"
	report.Observation.ToolEvents = []models.ToolEvent{{
		Turn: 1, Sequence: 1, ToolName: "test", Success: true,
		Args: map[string]any{"api_key": secret}, Result: "done",
	}}
	for name, grader := range report.Observation.Validations {
		grader.Feedback = secret
		report.Observation.Validations[name] = grader
	}
	p, err := CaptureReport(report, []Category{})
	if err != nil {
		t.Fatal(err)
	}
	for name, object := range p.objects {
		if bytes.Contains(object, []byte(secret)) || name == "execution-evidence-tool.json" ||
			name == "execution-evidence-grader.json" {
			t.Fatalf("restricted payload entered prepared immutable bytes: %s", name)
		}
	}
	backend, published := persistCaptureFixture(t, p)
	origin, err := artifactmanifest.NewStore(backend).ReadCompleted(t.Context(), published.Document.OriginManifest)
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range origin.Manifest.Artifacts {
		data, err := backend.Get(t.Context(), "blob-"+artifact.SHA256, artifactmanifest.MaxArtifactBytes)
		if err != nil || bytes.Contains(data, []byte(secret)) {
			t.Fatalf("restricted data reached origin storage: %s, %v", artifact.Name, err)
		}
	}
}

func TestCaptureIndexStrictnessAndCategoryKeyedComparison(t *testing.T) {
	p, err := CaptureReport(capturedMockReport(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	original, err := EncodeCapture(p.index)
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(p.index.Categories)
	encoded, err := EncodeCapture(p.index)
	if err != nil || !bytes.Equal(encoded, original) {
		t.Fatal("category insertion order changed canonical capture")
	}
	for _, mutate := range []func(map[string]any){
		func(v map[string]any) { delete(v, "empty_payloads") },
		func(v map[string]any) { v["empty_payloads"] = nil },
		func(v map[string]any) { v["origin_manifest"] = artifactmanifest.Hash(nil) },
		func(v map[string]any) {
			records, ok := v["categories"].([]any)
			if !ok || len(records) != 6 {
				t.Fatal("fixture must actually have all six categories")
			}
			records[0] = records[1]
		},
	} {
		var value map[string]any
		if err := json.Unmarshal(original, &value); err != nil {
			t.Fatal(err)
		}
		mutate(value)
		if _, err := ParseCapture(wire(t, value)); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid capture index was accepted")
		}
	}

}

func TestCategoryCountConflictNeverAcquiresSemanticSuccess(t *testing.T) {
	p, err := CaptureReport(capturedMockReport(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range p.index.Categories {
		if p.index.Categories[i].Category == Grader {
			p.index.Categories[i].ObservedCount = captureCount(2)
		}
	}
	if err := p.seal(); err != nil {
		t.Fatal(err)
	}
	backend, _ := localStore(t)
	ledger, err := executionledger.New(backend)
	if err != nil {
		t.Fatal(err)
	}
	creator, err := ledger.Reserve(t.Context(), p.index.Binding, nil)
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := PersistOwnedCapture(t.Context(), backend, p, ledger, creator)
	if !errors.Is(err, artifactmanifest.ErrConflict) || inspection.Document == nil {
		t.Fatalf("count contradiction became semantic success or fabricated byte corruption: %v", err)
	}
	_, categories, err := InspectCategories(t.Context(), backend, inspection.EvidenceSHA256)
	if !errors.Is(err, artifactmanifest.ErrConflict) {
		t.Fatal("persisted conflict was not reproduced through the actual reader")
	}
	for _, category := range categories {
		if category.Category == Grader && (category.State != Conflicting || category.ObservedCount != nil) {
			t.Fatal("conflicting count became authoritative")
		}
	}
}

func TestCapturedSourceRemovalWithholdsEveryCategory(t *testing.T) {
	p, err := CaptureReport(capturedMockReport(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	backend, directory := localStore(t)
	ledger, err := executionledger.New(backend)
	if err != nil {
		t.Fatal(err)
	}
	creator, err := ledger.Reserve(t.Context(), p.index.Binding, nil)
	if err != nil {
		t.Fatal(err)
	}
	published, err := PersistOwnedCapture(t.Context(), backend, p, ledger, creator)
	if err != nil {
		t.Fatal(err)
	}
	name := "execution-evidence-grader.json"
	data, exists := p.objects[name]
	if !exists || len(data) == 0 {
		t.Fatal("setup must contain a real nonempty grader payload")
	}
	if err := os.Remove(filepath.Join(directory, "blob-"+artifactmanifest.Hash(data))); err != nil {
		t.Fatal(err)
	}
	inspection, categories, err := InspectCategories(t.Context(), backend, published.EvidenceSHA256)
	if !errors.Is(err, artifactmanifest.ErrMissing) || inspection.Document != nil || len(categories) != 6 {
		t.Fatalf("missing artifact acquired admitted source/category evidence: %v", err)
	}
	for _, category := range categories {
		if category.State != Unavailable || category.ObservedCount != nil || category.Usage != nil {
			t.Fatalf("failed whole-source read retained authoritative evidence: %+v", category)
		}
	}
}

func TestCaptureOversizePayloadUsesMeasuredBoundAndNoTruncation(t *testing.T) {
	report := capturedMockReport(t)
	report.Observation.ToolEvents = []models.ToolEvent{{
		Turn: 1, Sequence: 1, ToolName: "bounded", Success: true,
		Result: strings.Repeat("x", artifactmanifest.MaxArtifactBytes+1),
	}}
	p, err := CaptureReport(report, []Category{})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := p.objects["execution-evidence-tool.json"]; exists {
		t.Fatal("oversize payload was persisted or silently truncated")
	}
	found := false
	for _, record := range p.index.Categories {
		if record.Category == Tool {
			found = true
			if record.State != Incomplete || record.Reason != ObjectSizeLimit ||
				record.Limit == nil || record.Limit.Observed <= artifactmanifest.MaxArtifactBytes ||
				record.Limit.Total == nil || *record.Limit.Total != record.Limit.Observed ||
				record.ObservedCount != nil || len(record.Artifacts) != 0 {
				t.Fatalf("unparsed oversize payload invented counts/completeness: %+v", record)
			}
		}
	}
	if !found {
		t.Fatal("oversize tool category was silently dropped")
	}
	backend, published := persistCaptureFixture(t, p)
	_, categories, err := InspectCategories(t.Context(), backend, published.EvidenceSHA256)
	if err != nil {
		t.Fatal(err)
	}
	for _, category := range categories {
		if category.Category == Tool && (category.State != Incomplete ||
			category.Verification != "recorded-degradation-only" || category.ObservedCount != nil) {
			t.Fatal("measured degradation became complete/authoritative category evidence")
		}
	}
}
