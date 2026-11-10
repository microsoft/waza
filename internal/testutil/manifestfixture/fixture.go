// Package manifestfixture creates public completed sources through the actual
// mock runner, owner, capture and immutable store paths for integration tests.
package manifestfixture

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/evidencestate"
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

// Publish runs only the fixture's explicitly selected mock engine. Callers may
// alter the original request/report to produce alternative and negative controls.
func Publish(t *testing.T, backend *artifactmanifest.Local, requestPath string, changeRequest func(*runnercontract.Request), changeReport func(*runnerunit.Report)) Source {
	t.Helper()
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
	prepared, err := evidencestate.CaptureReport(report, nil)
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
	}, nil)
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
