package assurance

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func admissionDocument() ReferenceDocument {
	digest := strings.Repeat("a", 64)
	return ReferenceDocument{
		SchemaVersion: ReferenceVersion, Kind: ReferenceKind, ID: "labels", Version: "1",
		EvalSourceSHA256: digest, EvalResolvedConfigSHA256: digest,
		Domains: []DomainCriteria{{ID: "cli", MinimumCases: 4, MinimumAgreement: 1}},
		Cases: []ReferenceCase{{
			ID: "good", ScenarioID: "target", Domain: "cli", Classification: CaseGood,
			TaskID: "task", TaskDeclarationSHA256: digest, Snapshot: "cases/good.json", ManifestSHA256: digest,
			Checks: []ReferenceCheck{{
				RequirementID: "state", Check: models.RequirementCheck{Scope: "eval", Grader: "check"},
				GraderDeclarationSHA256: digest, ExpectedPassed: new(true),
				Evidence: []models.EvidenceReference{{
					Origin:     models.EvidenceOrigin{EvalID: "eval", TaskID: "task", RunNumber: 1, AttemptCount: 1, PriorAttempts: "none"},
					ArtifactID: "workspace-file/state.txt",
				}},
			}},
		}},
	}
}

func marshalReferenceTest(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return data
}

func TestReferenceAdmissionAndDefensiveCopies(t *testing.T) {
	data := marshalReferenceTest(t, admissionDocument())
	original := string(data)
	set, err := ParseReferences(data)
	require.NoError(t, err)
	clear(data)
	subject, err := set.Subject()
	require.NoError(t, err)
	require.Equal(t, original, string(subject.Labels))
	subject.Labels[0] = '!'
	subject.ID = "mutated"
	require.Equal(t, "mutated", subject.ID)
	document, err := set.Document()
	require.NoError(t, err)
	*document.Cases[0].Checks[0].ExpectedPassed = false
	document.Cases[0].Checks[0].Evidence[0].Origin.TaskID = "mutated"
	document.Cases[0].Checks = nil
	document.Domains[0].MinimumCases = 100
	again, err := set.Document()
	require.NoError(t, err)
	require.True(t, *again.Cases[0].Checks[0].ExpectedPassed)
	require.Equal(t, "task", again.Cases[0].Checks[0].Evidence[0].Origin.TaskID)
	require.Equal(t, 4, again.Domains[0].MinimumCases)
	againSubject, err := set.Subject()
	require.NoError(t, err)
	require.Equal(t, original, string(againSubject.Labels))
	for _, invalid := range []*ReferenceSet{nil, {}} {
		_, err := invalid.Document()
		require.Error(t, err)
		_, err = invalid.Subject()
		require.Error(t, err)
	}
}

func TestReferenceAdmissionRejectsSemanticErrors(t *testing.T) {
	for _, candidate := range []struct {
		name   string
		mutate func(*ReferenceDocument)
	}{
		{"schema version", func(d *ReferenceDocument) { d.SchemaVersion = "1.1" }},
		{"kind", func(d *ReferenceDocument) { d.Kind = "task-snapshot" }},
		{"subject empty", func(d *ReferenceDocument) { d.ID = "" }},
		{"subject padded", func(d *ReferenceDocument) { d.ID = " labels" }},
		{"control identity", func(d *ReferenceDocument) { d.ID = "label\n" }},
		{"source digest", func(d *ReferenceDocument) { d.EvalSourceSHA256 = "bad" }},
		{"resolved digest", func(d *ReferenceDocument) { d.EvalResolvedConfigSHA256 = "" }},
		{"uppercase digest", func(d *ReferenceDocument) { d.EvalSourceSHA256 = strings.Repeat("A", 64) }},
		{"implementation digest", func(d *ReferenceDocument) { d.ImplementationExecutableSHA256 = new("version1") }},
		{"empty domains", func(d *ReferenceDocument) { d.Domains = nil }},
		{"duplicate domains", func(d *ReferenceDocument) { d.Domains = append(d.Domains, d.Domains[0]) }},
		{"zero minimum", func(d *ReferenceDocument) { d.Domains[0].MinimumCases = 0 }},
		{"excess minimum", func(d *ReferenceDocument) { d.Domains[0].MinimumCases = MaxReferenceCases + 1 }},
		{"zero agreement", func(d *ReferenceDocument) { d.Domains[0].MinimumAgreement = 0 }},
		{"excess agreement", func(d *ReferenceDocument) { d.Domains[0].MinimumAgreement = 1.01 }},
		{"empty cases", func(d *ReferenceDocument) { d.Cases = nil }},
		{"duplicate cases", func(d *ReferenceDocument) { d.Cases = append(d.Cases, d.Cases[0]) }},
		{"repeated manifest", func(d *ReferenceDocument) {
			copyCase := d.Cases[0]
			copyCase.ID = "other"
			d.Cases = append(d.Cases, copyCase)
		}},
		{"unknown domain", func(d *ReferenceDocument) { d.Cases[0].Domain = "repository" }},
		{"unknown classification", func(d *ReferenceDocument) { d.Cases[0].Classification = "golden" }},
		{"bad without rejection", func(d *ReferenceDocument) { d.Cases[0].Classification = CaseCriticalBad }},
		{"good rejected", func(d *ReferenceDocument) { d.Cases[0].Checks[0].ExpectedPassed = new(false) }},
		{"task digest missing", func(d *ReferenceDocument) { d.Cases[0].TaskDeclarationSHA256 = "" }},
		{"both sources", func(d *ReferenceDocument) {
			d.Cases[0].AuthoredInput = &AuthoredSource{Path: "input.json", DocumentSHA256: strings.Repeat("a", 64)}
		}},
		{"no source", func(d *ReferenceDocument) { d.Cases[0].Snapshot = "" }},
		{"authored path", func(d *ReferenceDocument) {
			d.Cases[0].Snapshot = ""
			d.Cases[0].AuthoredInput = &AuthoredSource{Path: "../input.json", DocumentSHA256: strings.Repeat("a", 64)}
		}},
		{"authored digest", func(d *ReferenceDocument) {
			d.Cases[0].Snapshot = ""
			d.Cases[0].AuthoredInput = &AuthoredSource{Path: "input.json"}
		}},
		{"empty checks", func(d *ReferenceDocument) { d.Cases[0].Checks = nil }},
		{"duplicate check", func(d *ReferenceDocument) { d.Cases[0].Checks = append(d.Cases[0].Checks, d.Cases[0].Checks[0]) }},
		{"missing expected", func(d *ReferenceDocument) { d.Cases[0].Checks[0].ExpectedPassed = nil }},
		{"missing declaration digest", func(d *ReferenceDocument) { d.Cases[0].Checks[0].GraderDeclarationSHA256 = "" }},
		{"invalid rubric digest", func(d *ReferenceDocument) { d.Cases[0].Checks[0].RubricContentSHA256 = new("") }},
		{"unknown scope", func(d *ReferenceDocument) { d.Cases[0].Checks[0].Check.Scope = "all" }},
		{"checkpoint missing turn", func(d *ReferenceDocument) { d.Cases[0].Checks[0].Check.Scope = "checkpoint" }},
		{"missing evidence", func(d *ReferenceDocument) { d.Cases[0].Checks[0].Evidence = nil }},
		{"duplicate evidence", func(d *ReferenceDocument) {
			c := &d.Cases[0].Checks[0]
			c.Evidence = append(c.Evidence, c.Evidence[0])
		}},
		{"wrong task origin", func(d *ReferenceDocument) { d.Cases[0].Checks[0].Evidence[0].Origin.TaskID = "other" }},
		{"empty eval origin", func(d *ReferenceDocument) { d.Cases[0].Checks[0].Evidence[0].Origin.EvalID = "" }},
		{"padded eval origin", func(d *ReferenceDocument) { d.Cases[0].Checks[0].Evidence[0].Origin.EvalID = " eval" }},
		{"zero run origin", func(d *ReferenceDocument) { d.Cases[0].Checks[0].Evidence[0].Origin.RunNumber = 0 }},
		{"zero attempt origin", func(d *ReferenceDocument) { d.Cases[0].Checks[0].Evidence[0].Origin.AttemptCount = 0 }},
		{"invalid pointer", func(d *ReferenceDocument) { d.Cases[0].Checks[0].Evidence[0].Pointer = "/a~2b" }},
		{"invalid pointer root", func(d *ReferenceDocument) { d.Cases[0].Checks[0].Evidence[0].Pointer = "output" }},
		{"unknown protocol", func(d *ReferenceDocument) {
			d.Calibration = &CalibrationPlan{Protocol: "confidence", Model: "test", MaxJudgeExecutions: 4}
		}},
		{"empty model", func(d *ReferenceDocument) {
			d.Calibration = &CalibrationPlan{Protocol: AgreementProtocol, MaxJudgeExecutions: 4}
		}},
		{"zero call bound", func(d *ReferenceDocument) {
			d.Calibration = &CalibrationPlan{Protocol: AgreementProtocol, Model: "test"}
		}},
	} {
		t.Run(candidate.name, func(t *testing.T) {
			document := admissionDocument()
			candidate.mutate(&document)
			_, err := ParseReferences(marshalReferenceTest(t, document))
			require.Error(t, err)
		})
	}
	for _, unsafe := range []string{"/tmp/input.json", "../input.json", "cases/../input.json", "./input.json", "C:/input.json", `cases\input.json`, ".", ""} {
		t.Run("path "+unsafe, func(t *testing.T) {
			document := admissionDocument()
			document.Cases[0].Snapshot = unsafe
			_, err := ParseReferences(marshalReferenceTest(t, document))
			require.Error(t, err)
		})
	}
}

func TestReferenceStrictJSONAdmission(t *testing.T) {
	base := string(marshalReferenceTest(t, admissionDocument()))
	for _, malformed := range []string{
		"", "null", "[]", base + " {}", base + "\x00",
		strings.Replace(base, `"id":"labels"`, `"id":"labels","id":"labels"`, 1),
		strings.Replace(base, `"id":"labels"`, `"id":"labels","secret-field":"not-for-diagnostics"`, 1),
		strings.Replace(base, `"expected_passed":true`, `"expected_passed":null`, 1),
		strings.Replace(base, `"scope":"eval"`, `"scope":"eval","after_turn":0`, 1),
		strings.Replace(base, `"scope":"eval"`, `"scope":"eval","after_turn":null`, 1),
		strings.Replace(base, `"scope":"eval"`, `"scope":"eval","AFTER_TURN":0`, 1),
		strings.Replace(base, `"id":"labels"`, `"id":"labels","ID":"replacement"`, 1),
		strings.Replace(base, `"expected_passed":true`, `"EXPECTED_PASSED":true`, 1),
		strings.Replace(base, `"run_number":1`, `"RUN_NUMBER":1`, 1),
		strings.Replace(base, `"grader":"check"`, `"grader":"check","unknown":"secret"`, 1),
		strings.Replace(base, `"run_number":1`, `"run_number":1,"extra":true`, 1),
		strings.Replace(base, `"minimum_agreement":1`, `"minimum_agreement":1e999`, 1),
		strings.Repeat(" ", MaxLabelBytes+1),
	} {
		_, err := ParseReferences([]byte(malformed))
		require.Error(t, err)
		require.NotContains(t, err.Error(), "not-for-diagnostics")
		require.NotContains(t, err.Error(), "secret")
	}
	document := admissionDocument()
	document.Cases[0].Checks[0].Check = models.RequirementCheck{Scope: "checkpoint", Grader: "check", AfterTurn: 2}
	_, err := ParseReferences(marshalReferenceTest(t, document))
	require.NoError(t, err)
}

func TestSerializedReviewRequiresSeparateAcceptanceAndExactBytes(t *testing.T) {
	data := marshalReferenceTest(t, admissionDocument())
	set, err := ParseReferences(data)
	require.NoError(t, err)
	subject, err := set.Subject()
	require.NoError(t, err)
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	document := ReviewDocument{
		SchemaVersion: ReferenceVersion, Kind: ReviewKind, SourceID: "synthetic-source",
		SubjectID: subject.ID, SubjectVersion: subject.Version, LabelsSHA256: byteSHA256(data),
		State: ReviewReviewed, Reviewer: "synthetic-test-reviewer", ReviewedAt: now.Add(-time.Hour),
	}
	review, err := ParseReview(marshalReferenceTest(t, document))
	require.NoError(t, err)
	eligibility, err := CheckAuthorReview(subject, review, ReviewSourceAcceptance{}, now)
	require.NoError(t, err)
	require.False(t, eligibility.Eligible)
	acceptance := ReviewSourceAcceptance{SourceID: document.SourceID, AcceptCurrentDecision: true}
	eligibility, err = CheckAuthorReview(subject, review, acceptance, now)
	require.NoError(t, err)
	require.True(t, eligibility.Eligible)
	subject.Labels = append(subject.Labels, '\n')
	eligibility, err = CheckAuthorReview(subject, review, acceptance, now)
	require.NoError(t, err)
	require.Equal(t, ReviewDigestMismatch, eligibility.Reason)
	for _, state := range []ReviewState{ReviewUnreviewed, ReviewSubmitted, ReviewRejected, ReviewRevoked} {
		document.State = state
		_, err := ParseReview(marshalReferenceTest(t, document))
		require.NoError(t, err)
	}
	for _, mutate := range []func(*ReviewDocument){
		func(d *ReviewDocument) { d.SchemaVersion = "2.0" },
		func(d *ReviewDocument) { d.Kind = ReferenceKind },
		func(d *ReviewDocument) { d.SourceID = "" },
		func(d *ReviewDocument) { d.SubjectID = "" },
		func(d *ReviewDocument) { d.LabelsSHA256 = "" },
		func(d *ReviewDocument) { d.State = "unknown" },
		func(d *ReviewDocument) { d.State = ReviewReviewed; d.Reviewer = "" },
		func(d *ReviewDocument) { d.State = ReviewReviewed; d.ReviewedAt = time.Time{} },
	} {
		copyDocument := document
		mutate(&copyDocument)
		_, err := ParseReview(marshalReferenceTest(t, copyDocument))
		require.Error(t, err)
	}
}
