// Package manifestfixture creates public completed sources through the actual
// mock runner, owner, capture and immutable store paths for integration tests.
package manifestfixture

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerunit"
)

type Source struct {
	Manifest  string
	Evidence  string
	Execution string
}

// Options selects capture privacy and an optional verified parent reservation.
// Nil Withheld keeps CaptureReport's conservative conversation/tool default.
type Options struct {
	ChangeRequest func(*runnercontract.Request)
	ChangeReport  func(*runnerunit.Report)
	Withheld      []evidencestate.Category
	Parent        *executionattempt.Parent
}

// Publish runs only the fixture's explicitly selected mock engine. Callers may
// alter the original request/report to produce alternative and negative controls.
func Publish(t *testing.T, backend *artifactmanifest.Local, requestPath string, changeRequest func(*runnercontract.Request), changeReport func(*runnerunit.Report)) Source {
	t.Helper()
	return PublishWith(t, backend, requestPath, Options{ChangeRequest: changeRequest, ChangeReport: changeReport})
}

// PublishWith is Publish with explicit capture privacy and execution parentage.
func PublishWith(t *testing.T, backend *artifactmanifest.Local, requestPath string, options Options) Source {
	t.Helper()
	changeRequest, changeReport := options.ChangeRequest, options.ChangeReport
	wire, err := os.ReadFile(requestPath)
	if err != nil {
		t.Fatal(err)
	}
	request, err := runnercontract.Parse(wire)
	if err != nil {
		t.Fatal(err)
	}
	if changeRequest != nil {
		changeRequest(request)
		wire, err = json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
	}
	report := runnerunit.NewService(nil).Run(t.Context(), wire)
	if report.Mode != "mock" || report.Calls != 1 || report.Observation == nil || report.Status != "completed" {
		t.Fatalf("fixture must execute exactly one actual mock call: %+v", report)
	}
	if changeReport != nil {
		changeReport(&report)
	}
	prepared, err := evidencestate.CaptureReport(report, options.Withheld)
	if err != nil {
		t.Fatal(err)
	}
	_, binding := report.Terminal()
	if binding == nil || binding.Identity == nil {
		t.Fatal("fixture report has no original binding")
	}
	ledger, err := executionledger.New(backend)
	if err != nil {
		t.Fatal(err)
	}
	creator, err := ledger.Reserve(t.Context(), artifactmanifest.Binding{
		Identity: *binding.Identity, Selection: binding.Selection,
		ExecutionMode: binding.ExecutionMode, GraderIDs: binding.GraderIDs,
	}, options.Parent)
	if err != nil {
		t.Fatal(err)
	}
	published, err := evidencestate.PersistOwnedCapture(t.Context(), backend, prepared, ledger, creator)
	if err != nil || published.Document == nil || published.Document.ExecutionSource == nil {
		t.Fatalf("fixture must persist a verified completed public source: %+v %v", published, err)
	}
	return Source{Manifest: published.Document.OriginManifest, Evidence: published.EvidenceSHA256,
		Execution: published.Document.ExecutionSource.ExecutionID}
}

// RecordedZero supplies explicit authored native usage, not observed billing.
func RecordedZero(report *runnerunit.Report) {
	zero := 0.0
	report.Observation.Usage = &models.UsageStats{
		AICredits: &zero, ModelMetrics: map[string]models.ModelUsage{"fixture-model": {AICredits: &zero}},
	}
}

// RecordedNonzero supplies distinct authored counters, not observed SDK billing.
func RecordedNonzero(factor int) func(*runnerunit.Report) {
	return func(report *runnerunit.Report) {
		credits := 1.25 * float64(factor)
		report.Observation.Usage = &models.UsageStats{
			Turns: factor, InputTokens: 11 * factor, OutputTokens: 7 * factor,
			CacheReadTokens: 3 * factor, CacheWriteTokens: 2 * factor,
			PremiumRequests: .5 * float64(factor), AICredits: &credits,
			ModelMetrics: map[string]models.ModelUsage{"fixture-model": {
				InputTokens: 11 * factor, OutputTokens: 7 * factor,
				CacheReadTokens: 3 * factor, CacheWriteTokens: 2 * factor,
				RequestCount: 2 * float64(factor), RequestCost: .25 * float64(factor), AICredits: &credits,
			}},
		}
	}
}
