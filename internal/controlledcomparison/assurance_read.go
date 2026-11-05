package controlledcomparison

import (
	"context"
	"fmt"
	"reflect"

	"github.com/microsoft/waza/internal/assurance"
	"github.com/microsoft/waza/internal/graders"
	"github.com/microsoft/waza/internal/releasepolicy"
)

// AssessWithAssurance is an explicit additive decision, not an upgrade to the
// base reader. It consumes current evaluator inputs and recomputes all verdicts.
func AssessWithAssurance(ctx context.Context, policyData, contractData []byte, directory string,
	baseline, candidate AssuranceSource) (releasepolicy.AssuredDecision, error) {
	d := releasepolicy.AssuredDecision{Kind: releasepolicy.AssuredDecisionKind, Version: releasepolicy.AssuranceVersion,
		BaseDecision: releasepolicy.InitialDecision(),
		Assurance:    releasepolicy.Dimension{State: "not_assessed", Reasons: []string{}},
		Regrade:      releasepolicy.Dimension{State: "not_assessed", Reasons: []string{}},
		Reports:      map[releasepolicy.Arm]*assurance.Report{},
		Limitations: []string{
			"Finite supplied-corpus grader qualification and deterministic output regrade consistency only.",
			"Local hashes and nonce do not authenticate humans, prove global uniqueness or witness precommitment.",
			"Current review-source acceptance is caller-supplied; withheld revocations cannot be discovered.",
			"No paid calibration, live-agent reliability, provider billing or historical-state certification.",
			"The unchanged base 1.0 decision remains nonpassing when assurance is required.",
		}}
	fail := func(state string, err error) (releasepolicy.AssuredDecision, error) {
		d.Accepted = false
		d.Assurance.State = state
		d.Assurance.Reasons = append(d.Assurance.Reasons, err.Error())
		return d, err
	}
	p, err := releasepolicy.DecodePolicy(policyData)
	if err != nil {
		return fail("invalid", err)
	}
	c, err := releasepolicy.DecodeAssuranceContract(contractData, p)
	if err != nil {
		return fail("invalid", err)
	}
	d.ContractDigest, d.CollectionID = c.Digest, c.Digest.SHA256
	d.BaseDecision, err = releasepolicy.ReadSelectedDecisionContext(ctx, directory, p.Digest)
	if err != nil {
		return fail("not_assessed", fmt.Errorf("base collection raw verification: %w", err))
	}
	ledger, results, err := releasepolicy.ReadAssuranceLedgerContext(ctx, directory, p, c)
	if err != nil {
		d.Regrade.State = "invalid"
		d.Regrade.Reasons = append(d.Regrade.Reasons, err.Error())
		return fail("not_assessed", err)
	}
	current := map[releasepolicy.Arm]*preparedAssurance{}
	d.Assurance.State, d.Regrade.State = "passed", "passed"
	recordReport := func(arm releasepolicy.Arm, report *assurance.Report) {
		d.Reports[arm] = report
		if report.State == assurance.AssessmentPassed {
			return
		}
		state := "not_assessed"
		switch report.State {
		case assurance.AssessmentFailed:
			state = "failed"
		case assurance.AssessmentInvalid:
			state = "invalid"
		case assurance.AssessmentError:
			state = "incomplete"
		}
		d.Assurance.Escalate(state,
			fmt.Sprintf("%s offline assurance %s: %s", arm, report.State, report.Reason))
	}
	for _, item := range []struct {
		arm    releasepolicy.Arm
		source AssuranceSource
	}{{releasepolicy.Baseline, baseline}, {releasepolicy.Candidate, candidate}} {
		prepared, err := verifyAssuranceSource(ctx, p, contractData, item.arm, item.source)
		if err != nil {
			d.Assurance.Escalate("invalid", fmt.Sprintf("%s: %v", item.arm, err))
			continue
		}
		current[item.arm] = prepared
		recordReport(item.arm, prepared.report)
	}
	indices := map[releasepolicy.Arm]int{}
	for _, row := range ledger.Rows {
		arm := row.Key.Arm
		actual := results[arm].Rows[indices[arm]]
		indices[arm]++
		prepared := current[arm]
		if prepared == nil || row.Output.Availability != "available" || row.Output.Value == nil {
			d.Regrade.Escalate("incomplete", fmt.Sprintf("%s/%s actual output or current declaration unavailable", arm, row.Key.TaskID))
			continue
		}
		count := 0
		for _, mapping := range c.Arms[arm].Checks {
			if mapping.TaskID != row.Key.TaskID {
				continue
			}
			count++
			task := prepared.prepared.tasks[mapping.TaskID]
			observed := assurance.ObserveDeclaredMechanical(ctx, task, prepared.prepared.cfg.Spec(), mapping.Check,
				&graders.Context{Output: *row.Output.Value, TestCase: task})
			retained, exists := actual.Run.Validations[mapping.ValidationKey]
			if observed.State != assurance.Observed || observed.Err != nil || observed.Result == nil || !exists ||
				observed.Result.Passed != retained.Passed || observed.Result.Score != retained.Score ||
				observed.Result.Name != retained.Name || observed.Result.Type != retained.Type {
				d.Regrade.Escalate("mismatched",
					fmt.Sprintf("%s/%s %s/%s actual native verdict does not match deterministic regrade", arm, mapping.TaskID, mapping.Check.Scope, mapping.Check.Grader))
			}
		}
		if count != len(actual.Run.Validations) {
			d.Regrade.Escalate("mismatched", "actual endpoint grader inventory differs from the complete mapping")
		}
	}
	for _, item := range []struct {
		arm    releasepolicy.Arm
		source AssuranceSource
	}{{releasepolicy.Baseline, baseline}, {releasepolicy.Candidate, candidate}} {
		prepared, err := verifyAssuranceSource(ctx, p, contractData, item.arm, item.source)
		if err != nil {
			d.Assurance.Escalate("invalid", fmt.Sprintf("%s final current-source recheck: %v", item.arm, err))
		} else {
			recordReport(item.arm, prepared.report)
		}
	}
	finalBase, err := releasepolicy.ReadSelectedDecisionContext(ctx, directory, p.Digest)
	if err != nil || !reflect.DeepEqual(finalBase, d.BaseDecision) {
		d.Regrade.Escalate("invalid", fmt.Sprintf("final base collection recheck changed or failed: %v", err))
	}
	finalLedger, _, err := releasepolicy.ReadAssuranceLedgerContext(ctx, directory, p, c)
	if err != nil || finalLedger.Digest != ledger.Digest {
		d.Regrade.Escalate("invalid", fmt.Sprintf("final assurance collection recheck changed or failed: %v", err))
	}
	d.Accepted = d.BaseDecision.NonAssurancePass() && d.Assurance.State == "passed" && d.Regrade.State == "passed" &&
		len(d.Reports) == 2
	return d, nil
}
