package assurance

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func calibratedClaimFixture(t *testing.T) *Report {
	t.Helper()
	request, _, _ := authoredVerificationFixture(t)
	report, err := Verify(t.Context(), request)
	require.NoError(t, err)
	report.SchemaVersion, report.AssessmentMode = CalibratedReportVersion, CalibrationOperation
	report.State, report.Reason = AssessmentNotAssessed, "synthetic_calibration_not_executed"
	report.Calibration.Model = "synthetic-model"
	report.Calibration.MaxJudgeExecutions = 1
	report.Calibration.ExecutionLedger = new([]JudgeExecution{})
	return report
}

func TestParseCalibratedReportExplicitClaims(t *testing.T) {
	report := calibratedClaimFixture(t)
	parsed, err := ParseCalibratedReport(marshalReferenceTest(t, report))
	require.NoError(t, err)
	require.Equal(t, CalibratedReportVersion, parsed.SchemaVersion)
	require.NotNil(t, parsed.Calibration.ExecutionLedger)
	require.Empty(t, *parsed.Calibration.ExecutionLedger)
	require.Nil(t, parsed.Calibration.Usage)
	require.Nil(t, parsed.Calibration.Credits)

	entry := JudgeExecution{
		CaseID: "good", TaskID: "task", RequirementID: "state",
		Check:          models.RequirementCheck{Scope: "task", Grader: "check"},
		RequestedModel: "synthetic-model", State: AssessmentError,
		Reason: "synthetic_factory_failure", Diagnostics: []JudgeDiagnostic{{Stage: "initialize", Code: "provider_failed"}},
	}
	report.Calibration.ExecutionLedger = new([]JudgeExecution{entry})
	parsed, err = ParseCalibratedReport(marshalReferenceTest(t, report))
	require.NoError(t, err)
	require.Equal(t, entry, (*parsed.Calibration.ExecutionLedger)[0])
	require.Zero(t, parsed.Calibration.Executions)
}

func TestParseCalibratedReportModelAccountingAndDomainReconciliation(t *testing.T) {
	report := calibratedClaimFixture(t)
	entry := syntheticCompleteJudgeExecution()
	report.Calibration.ExecutionLedger = new([]JudgeExecution{entry})
	report.Calibration.Executions = 1
	report.Calibration.Usage, report.Calibration.Credits = entry.Usage, entry.Credits
	parsed, err := ParseCalibratedReport(marshalReferenceTest(t, report))
	require.NoError(t, err)
	require.Equal(t, 0.0, *parsed.Calibration.Credits)
	for _, test := range []struct {
		name   string
		change func(*Report)
	}{
		{"accounting projection", func(report *Report) {
			(*report.Calibration.ExecutionLedger)[0].AccountingModels = []string{"another-model"}
		}},
		{"credit projection", func(report *Report) { (*report.Calibration.ExecutionLedger)[0].Credits = new(1.0) }},
		{"conflicting event model", func(report *Report) { (*report.Calibration.ExecutionLedger)[0].EventModels = []string{"another-model"} }},
		{"budget", func(report *Report) { report.Calibration.MaxJudgeExecutions = 0 }},
		{"domain failure masked", func(report *Report) {
			report.State, report.Calibration.State = AssessmentPassed, AssessmentPassed
			report.Domains[0].State = AssessmentFailed
		}},
		{"domain sample count masked", func(report *Report) {
			report.State, report.Calibration.State = AssessmentPassed, AssessmentPassed
			report.Domains[0].ObservedCases = 0
		}},
		{"domain agreement masked", func(report *Report) {
			report.State, report.Calibration.State = AssessmentPassed, AssessmentPassed
			report.Domains[0].Agreement = new(0.0)
		}},
		{"requirement failure masked", func(report *Report) {
			report.State, report.Calibration.State = AssessmentPassed, AssessmentPassed
			report.Requirements[0].State = AssessmentError
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var changed Report
			require.NoError(t, json.Unmarshal(marshalReferenceTest(t, report), &changed))
			test.change(&changed)
			_, err := ParseCalibratedReport(marshalReferenceTest(t, &changed))
			require.Error(t, err)
		})
	}
}

func TestCalibratedReportSchemaCompilerFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name      string
		resources map[string]string
	}{
		{"malformed", map[string]string{"root.json": "{"}},
		{"missing", map[string]string{}},
		{"external", map[string]string{"root.json": `{"$ref":"https://example.invalid/private-schema.json"}`}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := compileLocalSchema("root.json", test.resources)
			require.Error(t, err)
		})
	}

}

func syntheticPassedCalibratedClaim(t *testing.T) *Report {
	t.Helper()
	report := calibratedClaimFixture(t)
	report.State, report.Calibration.State = AssessmentPassed, AssessmentPassed
	report.Calibration.Protocol = AgreementProtocol
	var entries []JudgeExecution
	var usage []*models.UsageStats
	for _, requirement := range report.Requirements {
		for index := range requirement.Observations {
			observation := &requirement.Observations[index]
			observation.Result.Type = models.GraderKindPrompt
			for index := range observation.Bindings {
				binding := &observation.Bindings[index]
				if binding.Domain == "rubric_content_bytes" {
					binding.Applicable, binding.State = true, "verified"
					binding.Expected, binding.Actual = new(strings.Repeat("a", 64)), new(strings.Repeat("a", 64))
				}
			}
			entry := syntheticCompleteJudgeExecution()
			entry.CaseID, entry.TaskID = observation.CaseID, requirement.TaskID
			entry.RequirementID, entry.Check = requirement.RequirementID, requirement.Check
			entries = append(entries, entry)
			usage = append(usage, entry.Usage)
		}
	}
	report.Calibration.ExecutionLedger = &entries
	report.Calibration.Executions, report.Calibration.MaxJudgeExecutions = len(entries), len(entries)
	report.Calibration.Samples = len(entries)
	report.Calibration.Usage = models.AggregateUsageStats(usage)
	report.Calibration.Credits = report.Calibration.Usage.AICredits
	return report
}

func TestParseCalibratedReportReconcilesAggregateAccounting(t *testing.T) {
	report := syntheticPassedCalibratedClaim(t)
	_, err := ParseCalibratedReport(marshalReferenceTest(t, report))
	require.NoError(t, err)
	for _, test := range []struct {
		name   string
		change func(*Report)
	}{
		{"root credits", func(report *Report) { report.Calibration.Credits = new(1.0) }},
		{"root tokens", func(report *Report) { report.Calibration.Usage.InputTokens++ }},
		{"root model tokens", func(report *Report) {
			model := report.Calibration.Usage.ModelMetrics["synthetic-model"]
			model.InputTokens++
			report.Calibration.Usage.ModelMetrics["synthetic-model"] = model
		}},
		{"empty ledger accounting", func(report *Report) {
			report.State, report.Calibration.State = AssessmentNotAssessed, AssessmentNotAssessed
			report.Calibration.ExecutionLedger = new([]JudgeExecution{})
			report.Calibration.Executions = 0
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var changed Report
			require.NoError(t, json.Unmarshal(marshalReferenceTest(t, report), &changed))
			test.change(&changed)
			_, err := ParseCalibratedReport(marshalReferenceTest(t, &changed))
			require.Error(t, err)
		})
	}
}

func TestParseCalibratedReportReconcilesStrictPassCoverage(t *testing.T) {
	report := syntheticPassedCalibratedClaim(t)
	for _, test := range []struct {
		name   string
		change func(*Report)
	}{
		{"empty requirements", func(report *Report) { report.Requirements = []RequirementAssessment{} }},
		{"empty domains", func(report *Report) { report.Domains = []DomainAssessment{} }},
		{"disconnected execution", func(report *Report) { (*report.Calibration.ExecutionLedger)[0].CaseID = "absent" }},
		{"disconnected scoped execution", func(report *Report) { (*report.Calibration.ExecutionLedger)[0].Check.Scope = "task" }},
		{"missing execution", func(report *Report) {
			entries := (*report.Calibration.ExecutionLedger)[1:]
			report.Calibration.ExecutionLedger = &entries
			report.Calibration.Executions--
		}},
		{"contradictory agreement count", func(report *Report) { report.Domains[0].Agreements = 0 }},
		{"contradictory ratio", func(report *Report) {
			report.Domains[0].Agreement = new(0.75)
			report.Domains[0].MinimumAgreement = 0.5
		}},
		{"contradictory coverage", func(report *Report) { report.Requirements[0].Observed.Good++ }},
		{"unavailable observation", func(report *Report) { report.Requirements[0].Observations[0].State = NotAssessed }},
		{"missing native result", func(report *Report) { report.Requirements[0].Observations[0].Result = nil }},
		{"contradictory native agreement", func(report *Report) { report.Requirements[0].Observations[0].Agreement = new(false) }},
		{"critical false acceptance", func(report *Report) {
			observation := &report.Requirements[0].Observations[2]
			observation.Result.Passed, observation.Agreement = true, new(false)
			report.Domains[0].Agreements--
			report.Domains[0].Agreement, report.Domains[0].MinimumAgreement = new(0.75), 0.75
		}},
		{"mechanical domain dilution", func(report *Report) { report.Requirements[0].Observations[0].Result.Type = models.GraderKindText }},
		{"ineligible review", func(report *Report) { report.Review.Eligible = false }},
		{"unaccepted review", func(report *Report) { report.Review.CurrentSourceAccepted = false }},
		{"unverified provenance", func(report *Report) { report.Bindings[0].State = "missing" }},
		{"missing provenance", func(report *Report) { report.Bindings = report.Bindings[1:] }},
		{"contradictory observation provenance", func(report *Report) {
			report.Requirements[0].Observations[0].Bindings[0].Actual = new(strings.Repeat("f", 64))
		}},
		{"empty observation provenance", func(report *Report) {
			report.Requirements[0].Observations[0].Bindings = []Binding{}
		}},
		{"nonapplicable observation provenance", func(report *Report) {
			report.Requirements[0].Observations[0].Bindings[0].Applicable = false
		}},
		{"nonapplicable rubric provenance", func(report *Report) {
			report.Requirements[0].Observations[0].Bindings[2].Applicable = false
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var changed Report
			require.NoError(t, json.Unmarshal(marshalReferenceTest(t, report), &changed))
			test.change(&changed)
			_, err := ParseCalibratedReport(marshalReferenceTest(t, &changed))
			require.Error(t, err)
		})
	}
	// Ordinary paid disagreement follows the declared criterion, unlike critical acceptance.
	observation := &report.Requirements[0].Observations[0]
	observation.Result.Passed, observation.Agreement = false, new(false)
	report.Domains[0].Agreements--
	report.Domains[0].Agreement, report.Domains[0].MinimumAgreement = new(0.75), 0.75
	_, err := ParseCalibratedReport(marshalReferenceTest(t, report))
	require.NoError(t, err)
}

func TestParseCalibratedReportAllowsSeparateMechanicalDomain(t *testing.T) {
	report := syntheticPassedCalibratedClaim(t)
	var mechanical RequirementAssessment
	require.NoError(t, json.Unmarshal(marshalReferenceTest(t, report.Requirements[0]), &mechanical))
	mechanical.RequirementID, mechanical.Check.Grader = "mechanical", "mechanical-check"
	for index := range mechanical.Observations {
		observation := &mechanical.Observations[index]
		observation.Domain = "mechanical-domain"
		observation.CaseID += "-mechanical"
		observation.Result.Type = models.GraderKindText
		observation.Bindings[2] = Binding{Domain: "rubric_content_bytes", Applicable: false, State: "not_applicable"}
	}
	domain := report.Domains[0]
	domain.ID = "mechanical-domain"
	report.Requirements = append(report.Requirements, mechanical)
	report.Domains = append(report.Domains, domain)
	_, err := ParseCalibratedReport(marshalReferenceTest(t, report))
	require.NoError(t, err)
	observation := &report.Requirements[1].Observations[0]
	observation.Result.Passed, observation.Agreement = false, new(false)
	report.Domains[1].Agreements--
	report.Domains[1].Agreement, report.Domains[1].MinimumAgreement = new(0.75), 0.75
	_, err = ParseCalibratedReport(marshalReferenceTest(t, report))
	require.ErrorContains(t, err, "mechanical disagreement")
}

func TestParseCalibratedReportMatchesPaidObservationNotWholeRequirement(t *testing.T) {
	report := syntheticPassedCalibratedClaim(t)
	var mechanical RequirementAssessment
	require.NoError(t, json.Unmarshal(marshalReferenceTest(t, report.Requirements[0]), &mechanical))
	for index := range mechanical.Observations {
		observation := &mechanical.Observations[index]
		observation.Domain, observation.CaseID = "mechanical-domain", observation.CaseID+"-mechanical"
		observation.Result.Type = models.GraderKindText
	}
	requirement := &report.Requirements[0]
	requirement.Observations = append(requirement.Observations, mechanical.Observations...)
	requirement.Declared = Coverage{Good: 2, AlternativeValid: 2, CriticalBad: 4, IntendedNegative: 4}
	requirement.Observed = requirement.Declared
	domain := report.Domains[0]
	domain.ID = "mechanical-domain"
	report.Domains = append(report.Domains, domain)
	_, err := ParseCalibratedReport(marshalReferenceTest(t, report))
	require.NoError(t, err)

	entry := syntheticCompleteJudgeExecution()
	entry.CaseID = mechanical.Observations[0].CaseID
	entries := append(*report.Calibration.ExecutionLedger, entry)
	report.Calibration.ExecutionLedger = &entries
	report.Calibration.Executions, report.Calibration.MaxJudgeExecutions = len(entries), len(entries)
	var usage []*models.UsageStats
	for _, entry := range entries {
		usage = append(usage, entry.Usage)
	}
	report.Calibration.Usage = models.AggregateUsageStats(usage)
	report.Calibration.Credits = report.Calibration.Usage.AICredits
	_, err = ParseCalibratedReport(marshalReferenceTest(t, report))
	require.ErrorContains(t, err, "scoped observation")
}

func TestParseCalibratedReportPreservesUnrelatedPassOnCriticalCase(t *testing.T) {
	report := syntheticPassedCalibratedClaim(t)
	observation := &report.Requirements[0].Observations[2]
	observation.ExpectedPassed, observation.Result.Passed = true, true
	report.Requirements[0].Declared.IntendedNegative--
	report.Requirements[0].Observed.IntendedNegative--
	_, err := ParseCalibratedReport(marshalReferenceTest(t, report))
	require.NoError(t, err)
}

func TestParseCalibratedReportRejectsAmbiguousAndContradictoryClaims(t *testing.T) {
	report := calibratedClaimFixture(t)
	entry := JudgeExecution{
		CaseID: "good", TaskID: "task", RequirementID: "state",
		Check:          models.RequirementCheck{Scope: "eval", Grader: "check"},
		RequestedModel: "synthetic-model", State: AssessmentError,
		Reason: "synthetic_initialize_failure", Diagnostics: []JudgeDiagnostic{{Stage: "initialize", Code: "start_failed"}},
	}
	report.Calibration.ExecutionLedger = new([]JudgeExecution{entry})
	for _, test := range []struct {
		name   string
		change func(*Report)
	}{
		{"repeat scoped execution", func(report *Report) { report.Calibration.ExecutionLedger = new([]JudgeExecution{entry, entry}) }},
		{"requested model disagreement", func(report *Report) { report.Calibration.Model = "another-model" }},
		{"unsupported checkpoint", func(report *Report) {
			(*report.Calibration.ExecutionLedger)[0].Check = models.RequirementCheck{Scope: "checkpoint", Grader: "check", AfterTurn: 1}
		}},
		{"count without entry", func(report *Report) { report.Calibration.Executions = 1 }},
		{"hidden incomplete total", func(report *Report) { report.Calibration.Credits = new(0.0) }},
		{"pass without execution", func(report *Report) { report.State = AssessmentPassed }},
		{"calibration pass hides failure", func(report *Report) { report.Calibration.State = AssessmentPassed }},
		{"unselected version", func(report *Report) { report.SchemaVersion = ReferenceVersion }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var changed Report
			require.NoError(t, json.Unmarshal(marshalReferenceTest(t, report), &changed))
			test.change(&changed)
			_, err := ParseCalibratedReport(marshalReferenceTest(t, &changed))
			require.Error(t, err)
		})
	}
	valid := marshalReferenceTest(t, report)
	for _, test := range []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"oversized", bytes.Repeat([]byte(" "), maxSnapshotBytes+1)},
		{"duplicate", bytes.Replace(valid, []byte(`"schema_version":"1.1"`), []byte(`"schema_version":"1.1","schema_version":"1.1"`), 1)},
		{"unknown minor", bytes.Replace(valid, []byte(`"schema_version":"1.1"`), []byte(`"schema_version":"1.2"`), 1)},
		{"invalid UTF8", append(valid, 0xff)},
		{"unpaired surrogate", bytes.Replace(valid, []byte(`synthetic_calibration_not_executed`), []byte(`\ud800`), 1)},
		{"overflow", bytes.Replace(valid, []byte(`"executions":0`), []byte(`"executions":1e400`), 1)},
		{"trailing", append(valid, []byte(`{}`)...)},
		{"depth", []byte(strings.Repeat("[", 65) + strings.Repeat("]", 65))},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.NotEqual(t, string(valid), string(test.data))
			_, err := ParseCalibratedReport(test.data)
			require.Error(t, err)
		})
	}
}
