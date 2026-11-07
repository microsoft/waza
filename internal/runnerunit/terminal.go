package runnerunit

import (
	"context"
	"fmt"
	"slices"

	"github.com/microsoft/waza/internal/runnerterminal"
)

func graderIDs(input *admitted) []string {
	ids := make([]string, 0, len(input.spec.Graders)+len(input.task.Validators)+3)
	for _, g := range input.spec.Graders {
		ids = append(ids, g.Identifier)
	}
	for _, g := range input.task.Validators {
		ids = append(ids, g.Identifier)
	}
	for _, check := range []struct {
		name string
		size int
	}{
		{"_output_contains", len(input.task.Expectation.MustInclude)},
		{"_output_not_contains", len(input.task.Expectation.MustExclude)},
		{"_output_contains_any", len(input.task.Expectation.MayInclude)},
	} {
		if check.size != 0 {
			ids = append(ids, check.name)
		}
	}
	slices.Sort(ids)
	return ids
}

// RunTerminal is the shared CLI/HTTP terminal decision. Admission, identity and
// observed evidence come from the same invocation, never a second parse/run.
func (s *Service) RunTerminal(ctx context.Context, data []byte) (runnerterminal.Envelope, *runnerterminal.Binding) {
	return terminal(s.Run(ctx, data))
}

func terminal(report Report) (runnerterminal.Envelope, *runnerterminal.Binding) {
	var envelope runnerterminal.Envelope
	switch {
	case report.failure != nil:
		envelope = runnerterminal.Failure(report.boundary, report.failure)
		envelope.Validation = report.Validation
	case report.Status == "rejected":
		outcome, boundary := runnerterminal.InvalidInput, "validation"
		if report.Compatibility.ExitCode() == 5 {
			outcome, boundary = runnerterminal.UnsupportedCapability, "negotiation"
		}
		envelope = runnerterminal.NewFailure(outcome, boundary, report.Validation, string(outcome), "Runner request was rejected.")
	case report.Status == "completed":
		envelope = runnerterminal.Envelope{
			Kind: "waza.runner-terminal", SchemaVersion: runnerterminal.Version,
			Outcome: runnerterminal.Success, FailureBoundary: "none", Validation: "valid",
			Diagnostics: []runnerterminal.Diagnostic{},
		}
		if report.Observation != nil && report.Observation.Status == "failed" {
			envelope.Outcome, envelope.ExitCode = runnerterminal.EvaluationFailure, 1
			envelope.Diagnostics = append(envelope.Diagnostics, runnerterminal.Diagnostic{
				Code: "failed_checks", Message: "One or more observed checks failed; inspect observation.validations.",
			})
		}
	default:
		envelope = runnerterminal.NewFailure(runnerterminal.InfrastructureFailure, "terminalization", report.Validation,
			"incomplete_result", "Runner did not return a terminal execution result.")
	}
	if report.Status == "rejected" {
		envelope.Diagnostics = []runnerterminal.Diagnostic{}
		diagnostics := append(slices.Clone(report.Compatibility.Diagnostics), report.Diagnostics...)
		for _, diagnostic := range diagnostics {
			if len(envelope.Diagnostics) == runnerterminal.MaxDiagnostics-1 && len(diagnostics) > runnerterminal.MaxDiagnostics {
				envelope.Diagnostics = append(envelope.Diagnostics, runnerterminal.Diagnostic{
					Code: "additional_diagnostics", Message: fmt.Sprintf("%d additional rejection diagnostics; compare the complete request with the advertised capabilities and schemas.", len(diagnostics)-len(envelope.Diagnostics)),
				})
				break
			}
			if len(diagnostic.Message) > runnerterminal.MaxMessageBytes {
				diagnostic.Message = fmt.Sprintf("%.900s [diagnostic truncated; inspect the original request against the published schema]", diagnostic.Message)
			}
			envelope.Diagnostics = append(envelope.Diagnostics, runnerterminal.Diagnostic(diagnostic))
		}
	}
	envelope.EngineCalls = report.Calls
	envelope.Selection, envelope.Identity = report.Resolved, report.Identity
	if run := report.Observation; run != nil {
		observed := &runnerterminal.Observation{
			ExecutionMode: report.Mode, EngineCalls: report.Calls, RunNumber: run.RunNumber,
			Attempts: run.Attempts, Status: string(run.Status), ErrorMsg: run.ErrorMsg,
			Validations: []runnerterminal.Verdict{},
		}
		if report.Resolved != nil {
			observed.Selection = *report.Resolved
		}
		for name, result := range run.Validations {
			if name != result.Name {
				observed.Status, observed.ErrorMsg = "error", "Observed grader identifier does not match its result key."
			}
			observed.Validations = append(observed.Validations, runnerterminal.Verdict{Identifier: name, Passed: result.Passed})
		}
		slices.SortFunc(observed.Validations, func(a, b runnerterminal.Verdict) int {
			if a.Identifier < b.Identifier {
				return -1
			}
			if a.Identifier > b.Identifier {
				return 1
			}
			return 0
		})
		envelope.Observation = observed
	}
	if _, err := runnerterminal.Encode(envelope, report.binding); err != nil {
		// Bounds never silently turn incomplete/oversize evidence into success.
		// The admitted binding and call count survive; the unavailable summary
		// is explicitly diagnosed before the sole output attempt.
		envelope = runnerterminal.NewFailure(runnerterminal.InfrastructureFailure, "terminalization", report.Validation,
			"unrepresentable_evidence", fmt.Sprintf("Original outcome %s at %s cannot be represented; observation/diagnostic detail unavailable: %.600s", envelope.Outcome, envelope.FailureBoundary, err))
		envelope.Selection, envelope.Identity, envelope.EngineCalls = report.Resolved, report.Identity, report.Calls
	}
	return envelope, report.binding
}
