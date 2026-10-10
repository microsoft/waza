package assurance

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func qualificationTestObservedTerminal(t *testing.T, manifest qualificationManifest, ordinal uint64) qualificationTerminal {
	t.Helper()
	terminal := qualificationTestOperationalTerminal(t, manifest, ordinal)
	wire, err := qualificationDecode[qualificationTerminalWire](terminal.document.bytes(), qualificationDocumentLimit)
	require.NoError(t, err)
	job, _, err := qualificationJobAt(manifest, ordinal)
	require.NoError(t, err)
	entry := syntheticCompleteJudgeExecution()
	entry.CaseID, entry.TaskID, entry.RequirementID = job.Selector.CaseID, job.Selector.TaskID, job.Selector.RequirementID
	entry.Check = models.RequirementCheck{Scope: job.Selector.Check.Scope, AfterTurn: job.Selector.Check.AfterTurn, Grader: job.Selector.Check.Grader}
	entry.RequestedModel = job.RequestedModel
	entry.EventModels, entry.AccountingModels = []string{job.RequestedModel}, []string{job.RequestedModel}
	entry.Usage.ModelMetrics = map[string]models.ModelUsage{job.RequestedModel: {AICredits: new(0.0)}}
	wire.Execution, err = qualificationMarshalBounded(entry, qualificationDocumentLimit, nil)
	require.NoError(t, err)
	result, err := qualificationMarshalBounded(models.GraderResults{Name: job.Selector.Check.Grader, Type: models.GraderKindPrompt,
		Passed: job.ExpectedPassed, Score: 1, Weight: 1}, qualificationDocumentLimit, nil)
	require.NoError(t, err)
	wire.NativeResult = new(json.RawMessage(result))
	wire.ObservationState, wire.Agreement, wire.FailureCodes = "observed", new(true), []string{}
	wire.FactoryReturnedEngine, wire.InitializeAttempted, wire.ShutdownAttempted = true, true, true
	wire.AccountingFinalized, wire.CleanupComplete = true, true
	doc, err := qualificationSeal(wire)
	require.NoError(t, err)
	parsed, err := qualificationParseTerminal(doc.bytes(), manifest)
	require.NoError(t, err)
	return parsed
}

func qualificationTestCompletePrefix(t *testing.T, manifest qualificationManifest) (*qualificationLocalJournal, qualificationPrefix, []qualificationTerminal) {
	t.Helper()
	journal, _, _ := qualificationLocalTestJournal(t, manifest)
	_, err := journal.Claim(t.Context(), manifest)
	require.NoError(t, err)
	sequence, previous := uint64(1), manifest.document.sha256()
	appendEvent := func(event qualificationEvent) {
		qualificationTestAppend(t, journal, manifest, event)
		sequence++
		previous = event.document.sha256()
	}
	appendEvent(qualificationTestEvent(t, manifest, sequence, previous, "run_admission", nil))
	m, err := qualificationManifestValue(manifest)
	require.NoError(t, err)
	terminals := []qualificationTerminal{}
	for ordinal := range m.Jobs {
		o := uint64(ordinal)
		appendEvent(qualificationTestCurrentness(t, manifest, sequence, previous, "before_job", new(o), fmt.Sprintf("%064x", ordinal+1)))
		appendEvent(qualificationTestEvent(t, manifest, sequence, previous, "job_admission", new(o)))
		appendEvent(qualificationTestEvent(t, manifest, sequence, previous, "job_start", new(o)))
		terminal := qualificationTestObservedTerminal(t, manifest, o)
		event := qualificationTestTerminalEvent(t, manifest, sequence, previous, o, terminal)
		_, err := journal.CommitTerminal(t.Context(), terminal, event)
		require.NoError(t, err)
		sequence++
		previous = event.document.sha256()
		terminals = append(terminals, terminal)
	}
	for i, stage := range []string{"after_cleanup", "before_decision"} {
		appendEvent(qualificationTestCurrentness(t, manifest, sequence, previous, stage, nil, fmt.Sprintf("%064x", i+100)))
	}
	return journal, journal.Evidence(), terminals
}

func TestQualificationArtifactActualCoreTapeCutoff(t *testing.T) {
	manifest, fixture := qualificationProtocolFixture(t)
	journal, prefix, terminals := qualificationTestCompletePrefix(t, manifest)
	require.True(t, prefix.complete)
	artifacts, err := qualificationDeriveCoreAndTape(prefix, terminals)
	require.NoError(t, err)
	require.Len(t, artifacts, 6)
	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()
	reads := []qualificationArtifactRead{}
	for _, artifact := range artifacts {
		name, err := qualificationArtifactFilename(artifact.Role, artifact.Ordinal)
		require.NoError(t, err)
		data, err := base64.StdEncoding.Strict().DecodeString(artifact.BytesBase64)
		require.NoError(t, err)
		require.NoError(t, root.WriteFile(name, data, 0o600))
		reads = append(reads, qualificationArtifactRead{artifact.Role, artifact.Ordinal, root})
	}
	acquired, err := qualificationAcquireArtifacts(t.Context(), reads)
	require.NoError(t, err)
	bytes1, err := acquired.original("core_journal", nil)
	require.NoError(t, err)
	bytes1[0] = '!'
	bytes2, err := acquired.original("core_journal", nil)
	require.NoError(t, err)
	require.Equal(t, byte('{'), bytes2[0])
	_, err = qualificationDeriveInventory(prefix, acquired)
	require.Error(t, err, "original actual report is mandatory")
	// This is a synthetic nonpassing evidence document, not a paid producer.
	report, err := Verify(t.Context(), fixture.request)
	require.NoError(t, err)
	report.SchemaVersion, report.AssessmentMode = CalibratedReportVersion, CalibrationOperation
	report.State, report.Reason = AssessmentNotAssessed, "synthetic_offline_inventory_control"
	m, err := qualificationManifestValue(manifest)
	require.NoError(t, err)
	for i := range report.Bindings {
		binding := &report.Bindings[i]
		if binding.Domain == "eval_source_bytes" {
			binding.Actual = new(m.Inputs.ArmInput.SourceInventory.EvalSourceSHA256)
			binding.State = "verified"
		}
		if binding.Domain == "implementation_executable_bytes" {
			for _, source := range m.Inputs.Sources {
				if source.Role == "implementation_executable" {
					binding.Actual = new(source.SHA256)
					binding.State = "verified"
				}
			}
		}
	}
	report.Calibration.Model, report.Calibration.Protocol = m.Plan.Model, m.Plan.Protocol
	report.Calibration.MaxJudgeExecutions, report.Calibration.Executions = m.Plan.MaxJudgeExecutions, len(terminals)
	ledger := []JudgeExecution{}
	usages := []*models.UsageStats{}
	for _, terminal := range terminals {
		wire, err := qualificationDecode[qualificationTerminalWire](terminal.document.bytes(), qualificationDocumentLimit)
		require.NoError(t, err)
		var entry JudgeExecution
		require.NoError(t, qualificationDecodeNative(wire.Execution, &entry))
		ledger = append(ledger, entry)
		usages = append(usages, entry.Usage)
	}
	report.Calibration.ExecutionLedger = &ledger
	report.Calibration.Usage = models.AggregateUsageStats(usages)
	report.Calibration.Credits = report.Calibration.Usage.AICredits
	reportBytes := append(marshalReferenceTest(t, report), '\n')
	_, err = ParseCalibratedReport(reportBytes)
	require.NoError(t, err)
	require.NoError(t, root.WriteFile("actual_calibration_report.json", reportBytes, 0o600))
	completeReads := append(append([]qualificationArtifactRead{}, reads...), qualificationArtifactRead{Role: "actual_calibration_report", Root: root})
	complete, err := qualificationAcquireArtifacts(t.Context(), completeReads)
	require.NoError(t, err)
	inventory, err := qualificationDeriveInventory(prefix, complete)
	require.NoError(t, err)
	require.NotEmpty(t, inventory.document.bytes())
	complete.originals["actual_calibration_report.json"] = string(bytes.TrimSpace(reportBytes))
	_, err = qualificationDeriveInventory(prefix, complete)
	require.Error(t, err, "original-byte report identity cannot be normalized")
	_, err = qualificationAcquireArtifacts(t.Context(), append(reads, reads[0]))
	require.Error(t, err)
	require.NoError(t, root.WriteFile("core_journal.json", append(bytes2, '\n'), 0o600))
	_, err = qualificationAcquireArtifacts(t.Context(), []qualificationArtifactRead{{Role: "core_journal", Root: root}})
	require.Error(t, err, "reformatted json-v1 artifact bytes are not canonical")
	head, err := qualificationDecode[qualificationPrefixWire](prefix.document.bytes(), qualificationDocumentLimit)
	require.NoError(t, err)
	event := qualificationTestEvent(t, manifest, head.LastSequence+1, head.LastEventSHA256, "run_admission", nil)
	wire, err := qualificationDecode[qualificationEventWire](event.document.bytes(), qualificationDocumentLimit)
	require.NoError(t, err)
	wire.Type, wire.Admission = "run_terminal", nil
	wire.Completion = &qualificationCompletionWire{State: "failed", ReasonCode: "grade_disagreement", CompletedJobs: 4}
	doc, err := qualificationSeal(wire)
	require.NoError(t, err)
	final, err := qualificationParseEvent(doc.bytes(), manifest)
	require.NoError(t, err)
	_, err = journal.Append(t.Context(), final)
	require.NoError(t, err)
	published := journal.Evidence()
	require.NotNil(t, published.final)
	require.True(t, bytes.Equal(prefix.document.bytes(), published.document.bytes()), "inventory cutoff excludes separate terminal")
	_, err = journal.Append(t.Context(), final)
	require.Error(t, err, "final publication is one-shot")
	_, err = qualificationDeriveCoreAndTape(prefix, terminals[:3])
	require.Error(t, err)
	terminals[0] = qualificationTestOperationalTerminal(t, manifest, 0)
	_, err = qualificationDeriveCoreAndTape(prefix, terminals)
	require.Error(t, err)
}

func TestQualificationArtifactFixedRolesAndOriginalNewline(t *testing.T) {
	for _, role := range []string{"receipt", "../core_journal", "run_terminal", ""} {
		_, err := qualificationArtifactFilename(role, nil)
		require.Error(t, err)
	}

	t.Run("acquisition preflight entrypoint", func(t *testing.T) {
		manifest, _ := qualificationProtocolFixture(t)
		terminal := qualificationTestOperationalTerminal(t, manifest, 0)
		root, err := os.OpenRoot(t.TempDir())
		require.NoError(t, err)
		defer func() { require.NoError(t, root.Close()) }()
		data := terminal.document.bytes()
		require.NoError(t, root.WriteFile("job_terminal_payload-0000.json", data, 0o600))
		calls := 0
		reads := []qualificationArtifactRead{{Role: "job_terminal_payload", Ordinal: new(uint64(0)), Root: root}}
		_, err = qualificationAcquireArtifactsBounded(t.Context(), reads, len(data), uint64(len(data)), func() { calls++ })
		require.NoError(t, err)
		require.Equal(t, 1, calls)
		calls = 0
		_, err = qualificationAcquireArtifactsBounded(t.Context(), reads, len(data)-1, uint64(len(data)), func() { calls++ })
		require.Error(t, err)
		require.Zero(t, calls, "file boundary+1 before envelope/base64 materialization")
		require.NoError(t, root.WriteFile("job_terminal_payload-0001.json", data, 0o600))
		reads = append(reads, qualificationArtifactRead{Role: "job_terminal_payload", Ordinal: new(uint64(1)), Root: root})
		_, err = qualificationAcquireArtifactsBounded(t.Context(), reads, len(data), uint64(2*len(data)-1), func() { calls++ })
		require.Error(t, err)
		require.Equal(t, 1, calls, "aggregate rejects second artifact before its materialization")
	})
	_, err := qualificationArtifactFilename("core_journal", new(uint64(0)))
	require.Error(t, err)
	_, err = qualificationArtifactFilename("job_terminal_payload", new(uint64(4096)))
	require.Error(t, err)
	report := calibratedClaimFixture(t)
	reportBytes := append(marshalReferenceTest(t, report), '\n')
	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()
	require.NoError(t, root.WriteFile("actual_calibration_report.json", reportBytes, 0o600))
	acquired, err := qualificationAcquireArtifacts(t.Context(), []qualificationArtifactRead{{Role: "actual_calibration_report", Root: root}})
	require.NoError(t, err)
	actual, err := acquired.original("actual_calibration_report", nil)
	require.NoError(t, err)
	require.Equal(t, reportBytes, actual)
	require.Equal(t, byteSHA256(reportBytes), acquired.artifacts[0].SHA256)
	require.NotEqual(t, byteSHA256(bytes.TrimSpace(actual)), acquired.artifacts[0].SHA256)
	_, err = acquired.original("job_terminal_payload", new(uint64(0)))
	require.Error(t, err)
	require.NoError(t, root.WriteFile("core_journal.json", []byte(`{"x":"é","x":null}`), 0o600))
	_, err = qualificationAcquireArtifacts(t.Context(), []qualificationArtifactRead{{Role: "core_journal", Root: root}})
	require.Error(t, err, "duplicate Unicode object rejected")
	require.NotEmpty(t, strings.TrimSpace(string(actual)))
}
