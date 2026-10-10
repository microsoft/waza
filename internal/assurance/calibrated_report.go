package assurance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sync"

	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

var calibratedReportSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	return compileLocalSchema("grader-assurance-1.1.schema.json", map[string]string{
		"evidence-manifest-1.0.schema.json": schemas.EvidenceManifestSchemaJSON,
		"grader-reference-1.0.schema.json":  schemas.GraderReferenceSchemaJSON,
		"grader-assurance-1.0.schema.json":  schemas.GraderAssuranceSchemaJSON,
		"grader-assurance-1.1.schema.json":  schemas.CalibratedGraderAssuranceSchemaJSON,
	})
})

func compileLocalSchema(name string, resources map[string]string) (*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(schemaloader.Offline{})
	for name, source := range resources {
		var resource any
		if err := json.Unmarshal([]byte(source), &resource); err != nil {
			return nil, err
		}
		if err := compiler.AddResource(name, resource); err != nil {
			return nil, err
		}
		if err := compiler.AddResource("https://raw.githubusercontent.com/microsoft/waza/main/schemas/"+name, resource); err != nil {
			return nil, err
		}
	}
	return compiler.Compile(name)
}

// ParseCalibratedReport admits explicitly selected 1.1 report claims. It neither
// revalidates their source evidence nor invokes an engine. Offline-only readers
// must continue to reject 1.1 rather than call this parser implicitly.
func ParseCalibratedReport(data []byte) (*Report, error) {
	if len(data) == 0 || len(data) > maxSnapshotBytes {
		return nil, errors.New("assurance: calibrated report must be nonempty and at most 16 MiB")
	}
	if _, err := jsonutil.Parse(data); err != nil {
		return nil, fmt.Errorf("assurance: calibrated report JSON: %w", err)
	}
	if err := validateUnicodeEscapes(data); err != nil {
		return nil, err
	}
	schema, err := calibratedReportSchema()
	if err != nil {
		return nil, fmt.Errorf("assurance: calibrated report schema: %w", err)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("assurance: calibrated report numbers: %w", err)
	}
	if err := schema.Validate(instance); err != nil {
		return nil, fmt.Errorf("assurance: invalid calibrated report shape: %w", err)
	}
	var report Report
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, fmt.Errorf("assurance: calibrated report types: %w", err)
	}
	if err := validateJudgeLedger(&report); err != nil {
		return nil, err
	}
	return &report, nil
}

func validateJudgeLedger(report *Report) error {
	type selector struct {
		caseID, taskID, requirementID string
		check                         models.RequirementCheck
	}
	seen := map[selector]bool{}
	executions := 0
	accountingComplete := true
	usage := make([]*models.UsageStats, 0, len(*report.Calibration.ExecutionLedger))
	usagePresent := false
	for _, entry := range *report.Calibration.ExecutionLedger {
		if entry.Check.Scope != "eval" && entry.Check.Scope != "task" {
			return errors.New("assurance: calibrated ledger uses an unsupported scoped check")
		}
		key := selector{entry.CaseID, entry.TaskID, entry.RequirementID, entry.Check}
		if seen[key] {
			return errors.New("assurance: calibrated ledger repeats a scoped execution")
		}
		seen[key] = true
		usage = append(usage, entry.Usage)
		usagePresent = usagePresent || entry.Usage != nil
		if entry.Executed {
			executions++
		}
		if report.Calibration.Model != entry.RequestedModel {
			return errors.New("assurance: calibrated ledger requested model differs from the plan")
		}
		if entry.UsageComplete {
			keys := slices.Sorted(maps.Keys(entry.Usage.ModelMetrics))
			models := slices.Clone(entry.AccountingModels)
			slices.Sort(models)
			if !slices.Equal(keys, models) || *entry.Usage.AICredits != *entry.Credits {
				return errors.New("assurance: calibrated ledger accounting projections disagree")
			}
		} else {
			accountingComplete = false
		}
		if entry.State == AssessmentPassed &&
			(!slices.Equal(entry.EventModels, []string{entry.RequestedModel}) ||
				!slices.Equal(entry.AccountingModels, []string{entry.RequestedModel})) {
			return errors.New("assurance: calibrated ledger pass has conflicting observed models")
		}
		if report.Calibration.State == AssessmentPassed && entry.State != AssessmentPassed {
			return errors.New("assurance: calibrated pass masks unsuccessful execution evidence")
		}
	}
	if executions != report.Calibration.Executions || executions > report.Calibration.MaxJudgeExecutions {
		return errors.New("assurance: calibrated ledger execution count or budget disagrees")
	}
	if !accountingComplete && report.Calibration.Credits != nil {
		return errors.New("assurance: calibrated total hides incomplete execution accounting")
	}
	var aggregate *models.UsageStats
	var credits *float64
	if usagePresent {
		aggregate = models.AggregateUsageStats(usage)
		credits = aggregate.AICredits
	}
	if !reflect.DeepEqual(report.Calibration.Usage, aggregate) ||
		!reflect.DeepEqual(report.Calibration.Credits, credits) {
		return errors.New("assurance: calibrated aggregate accounting disagrees with the ledger")
	}
	if report.Calibration.State == AssessmentPassed &&
		(executions == 0 || report.Calibration.Usage == nil || report.Calibration.Credits == nil) {
		return errors.New("assurance: calibration pass has incomplete execution accounting")
	}
	if report.State == AssessmentPassed &&
		(report.Calibration.State != AssessmentPassed || executions == 0 ||
			report.Calibration.Usage == nil || report.Calibration.Credits == nil) {
		return errors.New("assurance: calibrated pass has incomplete execution accounting")
	}
	if report.State == AssessmentPassed {
		return validateCalibratedPass(report)
	}
	return nil
}

func validateCalibratedPass(report *Report) error {
	if !report.Review.Eligible || !report.Review.CurrentSourceAccepted ||
		report.Review.DeclaredState != ReviewReviewed || len(report.Bindings) == 0 {
		return errors.New("assurance: calibrated pass lacks eligible review or provenance")
	}
	if err := validatePassedBindings(report.Bindings); err != nil {
		return err
	}
	for _, required := range []string{"eval_source_bytes", "eval_resolved_config_json_v1", "implementation_executable_bytes"} {
		if !slices.ContainsFunc(report.Bindings, func(binding Binding) bool {
			return binding.Domain == required && binding.Applicable
		}) {
			return errors.New("assurance: calibrated pass lacks required provenance bindings")
		}
	}
	if len(report.Requirements) == 0 || len(report.Domains) == 0 {
		return errors.New("assurance: calibrated pass has empty coverage")
	}
	type identity struct {
		task, requirement string
		check             models.RequirementCheck
	}
	type counts struct {
		cases              map[string]bool
		checks, agreements int
		paid               *bool
	}
	domains := map[string]*counts{}
	for _, domain := range report.Domains {
		if domains[domain.ID] != nil {
			return errors.New("assurance: calibrated pass repeats a domain")
		}
		domains[domain.ID] = &counts{cases: map[string]bool{}}
	}
	requirements := map[identity]map[string]bool{}
	paidObservations := map[identity]map[string]bool{}
	for _, requirement := range report.Requirements {
		key := identity{requirement.TaskID, requirement.RequirementID, requirement.Check}
		if requirements[key] != nil || requirement.State != AssessmentPassed {
			return errors.New("assurance: calibrated pass repeats or masks an unsuccessful requirement")
		}
		cases := map[string]bool{}
		requirements[key] = cases
		paidObservations[key] = map[string]bool{}
		var coverage Coverage
		for _, observation := range requirement.Observations {
			domain := domains[observation.Domain]
			if _, duplicate := cases[observation.CaseID]; duplicate || domain == nil || observation.State != Observed ||
				observation.Result == nil || observation.Agreement == nil ||
				*observation.Agreement != (observation.Result.Passed == observation.ExpectedPassed) {
				return errors.New("assurance: calibrated pass contains ambiguous or incomplete observations")
			}
			paid := observation.Result.Type == models.GraderKindPrompt
			if domain.paid == nil {
				domain.paid = new(paid)
			}
			if *domain.paid != paid {
				return errors.New("assurance: calibrated domain mixes mechanical and paid observations")
			}
			if (paid && observation.SourceScope != "authored_finite_output") || (!paid && !*observation.Agreement) {
				return errors.New("assurance: calibrated pass masks unsupported paid input or mechanical disagreement")
			}
			if (observation.Classification == CaseCriticalBad && !observation.ExpectedPassed && observation.Result.Passed) ||
				(observation.Classification != CaseCriticalBad && !observation.ExpectedPassed) {
				return errors.New("assurance: calibrated pass masks a critical false acceptance or contradictory label")
			}
			if err := validatePassedBindings(observation.Bindings); err != nil {
				return err
			}
			requiredBindings := []string{"native_grader_declaration_json_v1", "native_task_declaration_json_v1"}
			if paid {
				requiredBindings = append(requiredBindings, "rubric_content_bytes")
				paidObservations[key][observation.CaseID] = true
			}
			for _, required := range requiredBindings {
				if !slices.ContainsFunc(observation.Bindings, func(binding Binding) bool {
					return binding.Domain == required && binding.Applicable
				}) {
					return errors.New("assurance: calibrated pass lacks required observation provenance")
				}
			}
			cases[observation.CaseID] = false
			domain.cases[observation.CaseID] = true
			domain.checks++
			if *observation.Agreement {
				domain.agreements++
			}
			addCoverage(&coverage, observation.Classification, observation.ExpectedPassed)
		}
		if requirement.Declared != coverage || requirement.Observed != coverage ||
			coverage.Good < 1 || coverage.AlternativeValid < 1 || coverage.CriticalBad < 2 ||
			coverage.IntendedNegative < 1 {
			return errors.New("assurance: calibrated pass has inconsistent or insufficient requirement coverage")
		}
	}
	for _, entry := range *report.Calibration.ExecutionLedger {
		key := identity{entry.TaskID, entry.RequirementID, entry.Check}
		if _, exists := requirements[key][entry.CaseID]; !exists || !paidObservations[key][entry.CaseID] {
			return errors.New("assurance: calibrated ledger does not resolve to a scoped observation")
		}
		requirements[key][entry.CaseID] = true
	}
	for key, cases := range requirements {
		for caseID, matched := range cases {
			if paidObservations[key][caseID] && !matched {
				return errors.New("assurance: calibrated observation lacks scoped execution evidence")
			}
		}
	}
	for _, domain := range report.Domains {
		actual := domains[domain.ID]
		if actual.checks == 0 || domain.State != AssessmentPassed ||
			domain.DeclaredCases != len(actual.cases) || domain.ObservedCases != len(actual.cases) ||
			domain.ExpectedChecks != actual.checks || domain.ObservedChecks != actual.checks ||
			domain.Agreements != actual.agreements || domain.ObservedCases < domain.MinimumCases ||
			domain.Agreement == nil || *domain.Agreement != float64(actual.agreements)/float64(actual.checks) ||
			*domain.Agreement < domain.MinimumAgreement {
			return errors.New("assurance: calibrated pass masks inconsistent or incomplete domain criteria")
		}
	}
	return nil
}

func validatePassedBindings(bindings []Binding) error {
	for _, binding := range bindings {
		if binding.Applicable && (binding.State != "verified" || binding.Expected == nil ||
			binding.Actual == nil || *binding.Expected != *binding.Actual) {
			return errors.New("assurance: calibrated pass masks unverified provenance")
		}
	}
	return nil
}
