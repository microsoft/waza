package assurance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"sync"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	PreservedReportVersion     = "1.2"
	PreservedAssessmentMode    = "preserved_native_mechanical_assurance"
	PreservedNativeSourceScope = "preserved_native_selected_artifacts"
	preservedManifestBinding   = "native_evidence_manifest_sha256"
)

var preservedReportSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	return compileLocalSchema("grader-assurance-1.2.schema.json", map[string]string{
		"evidence-manifest-1.0.schema.json": schemas.EvidenceManifestSchemaJSON,
		"grader-reference-1.0.schema.json":  schemas.GraderReferenceSchemaJSON,
		"grader-assurance-1.0.schema.json":  schemas.GraderAssuranceSchemaJSON,
		"grader-assurance-1.2.schema.json":  schemas.PreservedGraderAssuranceSchemaJSON,
	})
})

// ParsePreservedReport validates explicitly selected 1.2 supplied claims. It
// cannot authenticate review or reproduce native results without their sources.
// Schema resolution is embedded and offline, with no "latest" fallback.
func ParsePreservedReport(data []byte) (*Report, error) {
	if len(data) == 0 || len(data) > maxSnapshotBytes {
		return nil, errors.New("assurance: preserved report must be nonempty and at most 16 MiB")
	}
	if _, err := jsonutil.Parse(data); err != nil {
		return nil, fmt.Errorf("assurance: preserved report JSON: %w", err)
	}
	if err := validateUnicodeEscapes(data); err != nil {
		return nil, err
	}
	schema, err := preservedReportSchema()
	if err != nil {
		return nil, fmt.Errorf("assurance: preserved report schema: %w", err)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("assurance: preserved report numbers: %w", err)
	}
	if err := schema.Validate(instance); err != nil {
		return nil, fmt.Errorf("assurance: invalid preserved report shape: %w", err)
	}
	var report, reduced Report
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, fmt.Errorf("assurance: preserved report types: %w", err)
	}
	if err := json.Unmarshal(data, &reduced); err != nil {
		return nil, err
	}
	if report.CreatedAt.IsZero() {
		return nil, errors.New("assurance: preserved report requires a current time")
	}
	if err := reducePreserved(&reduced); err != nil {
		return nil, err
	}
	if report.State != reduced.State || report.Reason != reduced.Reason {
		return nil, errors.New("assurance: preserved report state contradicts its supplied claims")
	}
	for i, requirement := range report.Requirements {
		actual := reduced.Requirements[i]
		if requirement.State != actual.State || requirement.Reason != actual.Reason ||
			requirement.Declared != actual.Declared || requirement.Observed != actual.Observed {
			return nil, errors.New("assurance: preserved requirement state or coverage contradicts observations")
		}
	}
	if !reflect.DeepEqual(report.Domains, reduced.Domains) {
		return nil, errors.New("assurance: preserved domain state, counters or agreement contradict observations")
	}
	return &report, nil
}

type preservedRequirementKey struct {
	task, requirement string
	check             models.RequirementCheck
}

// The producer and supplied-claim validator share this fresh reduction. No
// prior Verify state, unsupported baseline or metadata-only pass is inherited.
func reducePreserved(report *Report) error {
	if err := validatePreservedReview(report.Review); err != nil {
		return err
	}
	if err := validatePreservedBindings(report.Bindings); err != nil {
		return err
	}
	report.State, report.Reason = AssessmentPassed, "corpus_agreement"
	globalState, globalReason := preservedProvenanceState(report.Bindings,
		[]string{"eval_source_bytes", "eval_resolved_config_json_v1", "implementation_executable_bytes"})
	if report.Review.Reason == ReviewInvalid {
		globalState, globalReason = AssessmentInvalid, "invalid_review"
	} else if !report.Review.Eligible && statePriority(AssessmentNotAssessed) > statePriority(globalState) {
		globalState, globalReason = AssessmentNotAssessed, "review_not_eligible"
	}
	setReportState(report, globalState, globalReason)
	type counts struct {
		cases map[string]bool
	}
	domains := map[string]int{}
	domainCounts := map[string]*counts{}
	for i := range report.Domains {
		domain := &report.Domains[i]
		if !referenceIdentifier(domain.ID) || domainCounts[domain.ID] != nil ||
			domain.MinimumCases < 1 || domain.MinimumAgreement <= 0 || domain.MinimumAgreement > 1 {
			return errors.New("assurance: preserved domain identity or criteria are invalid or repeated")
		}
		domains[domain.ID] = i
		domainCounts[domain.ID] = &counts{cases: map[string]bool{}}
		domain.DeclaredCases, domain.ObservedCases, domain.ExpectedChecks = 0, 0, 0
		domain.ObservedChecks, domain.Agreements, domain.Agreement = 0, 0, nil
		domain.State = globalState
	}
	type caseIdentity struct {
		task, scenario, domain string
		classification         CaseClassification
	}
	cases := map[string]caseIdentity{}
	intendedNegatives := map[string]bool{}
	caseManifests := map[string]string{}
	manifestCases := map[string]string{}
	caseOrigins := map[string]models.EvidenceOrigin{}
	requirements := map[preservedRequirementKey]bool{}
	if len(report.Requirements) == 0 || len(report.Domains) == 0 {
		setReportState(report, AssessmentNotAssessed, "requirement_uncovered")
	}
	for i := range report.Requirements {
		requirement := &report.Requirements[i]
		key := preservedRequirementKey{requirement.TaskID, requirement.RequirementID, requirement.Check}
		if requirements[key] || !referenceIdentifier(requirement.TaskID) || !referenceIdentifier(requirement.RequirementID) {
			return errors.New("assurance: preserved requirement identity is invalid or repeated")
		}
		if err := validateIdentity(ReferenceInput{TaskID: requirement.TaskID, Check: requirement.Check}); err != nil {
			return err
		}
		requirements[key] = true
		requirement.Declared, requirement.Observed = Coverage{}, Coverage{}
		requirement.State, requirement.Reason = globalState, globalReason
		if requirement.State == AssessmentPassed {
			requirement.Reason = "corpus_agreement"
		}
		seenCases := map[string]bool{}
		for _, observation := range requirement.Observations {
			index, exists := domains[observation.Domain]
			if !exists || seenCases[observation.CaseID] || !referenceIdentifier(observation.CaseID) || !referenceIdentifier(observation.ScenarioID) {
				return errors.New("assurance: preserved observation has an ambiguous case or undeclared domain")
			}
			seenCases[observation.CaseID] = true
			identity := caseIdentity{requirement.TaskID, observation.ScenarioID, observation.Domain, observation.Classification}
			if prior, exists := cases[observation.CaseID]; exists && prior != identity {
				return errors.New("assurance: preserved case identity differs across scoped checks")
			}
			cases[observation.CaseID] = identity
			intendedNegatives[observation.CaseID] = intendedNegatives[observation.CaseID] || !observation.ExpectedPassed
			for _, binding := range observation.Bindings {
				if binding.Domain != preservedManifestBinding || binding.Expected == nil {
					continue
				}
				if previous, exists := caseManifests[observation.CaseID]; exists && previous != *binding.Expected {
					return errors.New("assurance: preserved case manifest differs across scoped checks")
				}
				if previous, exists := manifestCases[*binding.Expected]; exists && previous != observation.CaseID {
					return errors.New("assurance: preserved cases repeat a selected manifest")
				}
				caseManifests[observation.CaseID] = *binding.Expected
				manifestCases[*binding.Expected] = observation.CaseID
			}
			for _, reference := range observation.Evidence {
				if previous, exists := caseOrigins[observation.CaseID]; exists && previous != reference.Origin {
					return errors.New("assurance: preserved case reference origin differs across scoped checks")
				}
				caseOrigins[observation.CaseID] = reference.Origin
			}
			if err := validatePreservedObservation(requirement.TaskID, requirement.Check, observation); err != nil {
				return err
			}
			domain := &report.Domains[index]
			counter := domainCounts[observation.Domain]
			if _, exists := counter.cases[observation.CaseID]; !exists {
				counter.cases[observation.CaseID] = true
			}
			addCoverage(&requirement.Declared, observation.Classification, observation.ExpectedPassed)
			domain.ExpectedChecks++
			state, reason := assessmentState(observation.State), observation.Reason
			if observation.State == Observed {
				state, reason = AssessmentPassed, "corpus_agreement"
				addCoverage(&requirement.Observed, observation.Classification, observation.ExpectedPassed)
				domain.ObservedChecks++
				if *observation.Agreement {
					domain.Agreements++
				} else {
					state, reason = AssessmentFailed, "label_disagreement"
					if observation.Classification == CaseCriticalBad && !observation.ExpectedPassed && observation.Result.Passed {
						reason = "critical_false_acceptance"
					}
				}
			} else {
				counter.cases[observation.CaseID] = false
			}
			boundState, boundReason := preservedProvenanceState(observation.Bindings,
				[]string{"native_grader_declaration_json_v1", "native_task_declaration_json_v1", preservedManifestBinding})
			if statePriority(boundState) > statePriority(state) {
				state, reason = boundState, boundReason
			}
			if statePriority(state) > statePriority(requirement.State) {
				requirement.State, requirement.Reason = state, reason
			}
			if statePriority(state) > statePriority(domain.State) {
				domain.State = state
			}
			setReportState(report, state, reason)
		}
		if requirement.State == AssessmentPassed && !preservedCoverageComplete(requirement.Observed) {
			requirement.State, requirement.Reason = AssessmentNotAssessed, "challenge_coverage_missing"
			setReportState(report, requirement.State, requirement.Reason)
		}
	}
	for caseID, identity := range cases {
		if identity.classification == CaseCriticalBad && !intendedNegatives[caseID] {
			return errors.New("assurance: preserved critical case has no independently intended negative check")
		}
	}
	for i := range report.Domains {
		domain := &report.Domains[i]
		for _, observed := range domainCounts[domain.ID].cases {
			domain.DeclaredCases++
			if observed {
				domain.ObservedCases++
			}
		}
		if domain.ObservedChecks != 0 {
			domain.Agreement = new(float64(domain.Agreements) / float64(domain.ObservedChecks))
		}
		state, reason := AssessmentPassed, "corpus_agreement"
		if domain.ObservedCases < domain.MinimumCases || domain.ObservedChecks != domain.ExpectedChecks {
			state, reason = AssessmentInsufficient, "domain_samples_unavailable"
		} else if domain.Agreement == nil || *domain.Agreement < domain.MinimumAgreement {
			state, reason = AssessmentFailed, "domain_criterion_failed"
		}
		if statePriority(state) > statePriority(domain.State) {
			domain.State = state
		}
		setReportState(report, state, reason)
	}
	return nil
}

func preservedCoverageComplete(coverage Coverage) bool {
	return coverage.Good >= 1 && coverage.AlternativeValid >= 1 && coverage.CriticalBad >= 2 && coverage.IntendedNegative >= 1
}

func validatePreservedReview(review ReviewReport) error {
	if review.Eligible {
		if !referenceIdentifier(review.SourceID) || !review.CurrentSourceAccepted || review.DeclaredState != ReviewReviewed || review.Reason != "" {
			return errors.New("assurance: preserved eligible review contradicts current acceptance or decision")
		}
		return nil
	}
	switch review.Reason {
	case ReviewSourceNotAccepted:
		if review.CurrentSourceAccepted {
			return errors.New("assurance: preserved review contradicts source acceptance")
		}
	case ReviewDecisionMissing:
		if !review.CurrentSourceAccepted || (review.DeclaredState != "" && review.DeclaredState != ReviewUnreviewed) {
			return errors.New("assurance: preserved missing review contradicts acceptance or decision")
		}
	case ReviewNotApproved:
		if !review.CurrentSourceAccepted || review.DeclaredState == "" || review.DeclaredState == ReviewReviewed {
			return errors.New("assurance: preserved unapproved review contradicts acceptance or decision")
		}
	case ReviewIdentityMismatch, ReviewVersionMismatch, ReviewDigestMismatch:
		if !review.CurrentSourceAccepted || review.DeclaredState == "" {
			return errors.New("assurance: preserved mismatched review lacks accepted decision")
		}
	case ReviewInvalid:
	default:
		return errors.New("assurance: preserved ineligible review requires an explicit reason")
	}
	return nil
}

func validatePreservedBindings(bindings []Binding) error {
	seen := map[string]bool{}
	for _, binding := range bindings {
		if seen[binding.Domain] || !referenceIdentifier(binding.Domain) {
			return errors.New("assurance: preserved binding domain is invalid or repeated")
		}
		seen[binding.Domain] = true
		for _, digest := range []*string{binding.Expected, binding.Actual} {
			if digest != nil && !validSHA256(*digest) {
				return errors.New("assurance: preserved binding requires an exact SHA256 digest")
			}
		}
		if !binding.Applicable {
			if binding.State != "not_applicable" || binding.Expected != nil || binding.Actual != nil {
				return errors.New("assurance: preserved nonapplicable binding contains provenance claims")
			}
			continue
		}
		expected := checkBinding(binding.Domain, binding.Expected, binding.Actual)
		if binding.State != expected.State {
			return errors.New("assurance: preserved binding state contradicts its supplied digests")
		}
	}
	return nil
}

func preservedProvenanceState(bindings []Binding, required []string) (AssessmentState, string) {
	state, reason := AssessmentPassed, "corpus_agreement"
	for _, binding := range bindings {
		if binding.Applicable && binding.State == "mismatch" {
			return AssessmentInvalid, "binding_mismatch"
		}
		if binding.Applicable && binding.State == "missing" {
			state, reason = AssessmentNotAssessed, "provenance_unavailable"
		}
	}
	for _, domain := range required {
		if !slices.ContainsFunc(bindings, func(binding Binding) bool {
			return binding.Domain == domain && binding.Applicable && binding.State == "verified"
		}) {
			state, reason = AssessmentNotAssessed, "provenance_unavailable"
		}
	}
	return state, reason
}

func validatePreservedObservation(taskID string, check models.RequirementCheck, observation ChallengeObservation) error {
	if err := validatePreservedBindings(observation.Bindings); err != nil {
		return err
	}
	if observation.Classification != CaseGood && observation.Classification != CaseAlternative && observation.Classification != CaseCriticalBad {
		return errors.New("assurance: preserved case classification is unsupported")
	}
	if observation.Classification != CaseCriticalBad && !observation.ExpectedPassed {
		return errors.New("assurance: preserved valid case has a negative label")
	}
	if observation.SourceScope != PreservedNativeSourceScope && observation.SourceScope != "preserved_file_subset" {
		return errors.New("assurance: preserved observation uses an unsupported source scope")
	}
	if observation.Result != nil {
		switch observation.Result.Type {
		case "":
			// Native tool_calls returns a blank Type. Preserve that result,
			// admitting only its exact mechanical counter/score projection.
			if observation.SourceScope != PreservedNativeSourceScope || !preservedToolCallsResult(observation.Result) {
				return errors.New("assurance: preserved blank result type lacks native tool_calls counters")
			}
		case models.GraderKindFile:
			if observation.SourceScope != "preserved_file_subset" {
				return errors.New("assurance: preserved file result is mislabeled as event evidence")
			}
		case models.GraderKindToolCalls, models.GraderKindToolConstraint, models.GraderKindActionSequence:
			if observation.SourceScope != PreservedNativeSourceScope {
				return errors.New("assurance: preserved event result is mislabeled as file evidence")
			}
		default:
			return errors.New("assurance: preserved result is not a supported native mechanical grader")
		}
		if observation.Result.Name != check.Grader {
			return errors.New("assurance: preserved result does not match its scoped grader")
		}
	}
	if observation.State != Observed {
		if observation.Agreement != nil {
			return errors.New("assurance: preserved unavailable observation claims label agreement")
		}
		switch observation.State {
		case NotAssessed, InsufficientEvidence, OperationalError, Invalid:
			if observation.State != OperationalError && observation.Result != nil {
				return errors.New("assurance: preserved pre-observation state contains a native result")
			}
			return nil
		default:
			return errors.New("assurance: preserved observation state is unsupported")
		}
	}
	if check.Scope == "checkpoint" || observation.Result == nil || observation.Agreement == nil ||
		*observation.Agreement != (observation.Result.Passed == observation.ExpectedPassed) {
		return errors.New("assurance: preserved observed result or label agreement is incomplete or contradictory")
	}
	expectedReason := "label_agreement"
	if !*observation.Agreement {
		expectedReason = "label_disagreement"
	}
	if observation.Reason != expectedReason {
		return errors.New("assurance: preserved observed reason contradicts label agreement")
	}
	state, _ := preservedProvenanceState(observation.Bindings,
		[]string{"native_grader_declaration_json_v1", "native_task_declaration_json_v1", preservedManifestBinding})
	if state != AssessmentPassed {
		return errors.New("assurance: preserved observed result lacks verified native provenance")
	}
	if err := validatePassedBindings(observation.Bindings); err != nil {
		return err
	}
	if len(observation.Evidence) == 0 {
		return errors.New("assurance: preserved observed result lacks selected native evidence")
	}
	origin := observation.Evidence[0].Origin
	if !evidence.CompleteOrigin(origin) || origin.TaskID != taskID || strings.HasPrefix(origin.EvalID, "reference:") {
		return errors.New("assurance: preserved observed result lacks complete native origin")
	}
	selected := map[string]bool{}
	whole := false
	for _, reference := range observation.Evidence {
		if reference.Origin != origin || reference.ArtifactID == "" {
			return errors.New("assurance: preserved observed references disagree on native origin")
		}
		key := reference.ArtifactID + "\x00" + reference.Pointer
		if selected[key] {
			return errors.New("assurance: preserved observed reference is repeated")
		}
		selected[key] = true
		if reference.Pointer == "" {
			if observation.SourceScope == PreservedNativeSourceScope && reference.ArtifactID == "tool-events" {
				whole = true
			}
			if observation.SourceScope == "preserved_file_subset" && strings.HasPrefix(reference.ArtifactID, "workspace-file/") {
				whole = true
			}
		}
	}
	if !whole {
		return errors.New("assurance: preserved observed result lacks its whole selected artifact")
	}
	return nil
}

func preservedToolCallsResult(result *models.GraderResults) bool {
	count := func(name string) (float64, bool) {
		var value float64
		switch number := result.Details[name].(type) {
		case int:
			value = float64(number)
		case float64:
			value = number
		default:
			return 0, false
		}
		return value, !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && math.Trunc(value) == value
	}
	_, calls := count("total_calls")
	total, totalOK := count("total_checks")
	passed, passedOK := count("passed_checks")
	if !calls || !totalOK || !passedOK || total == 0 || passed > total ||
		result.Score != passed/total || result.Passed != (passed == total) {
		return false
	}
	switch names := result.Details["unique_tools"].(type) {
	case []string:
		return true
	case []any:
		for _, name := range names {
			if _, ok := name.(string); !ok {
				return false
			}
		}
		return true
	default:
		return false
	}
}
