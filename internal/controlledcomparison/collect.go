package controlledcomparison

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/orchestration"
	"github.com/microsoft/waza/internal/releasepolicy"
)

// Collect owns the admitted lifecycle. No arbitrary engine/callback can be
// supplied through this producer; every attempt has a fresh mock and workspace.
func Collect(ctx context.Context, policyData []byte, baseline, candidate Source, directory string) error {
	return collectControlled(ctx, policyData, baseline, candidate, directory, nil, nil)
}

func collectControlled(ctx context.Context, policyData []byte, baseline, candidate Source, directory string,
	contractData []byte, assureSources map[releasepolicy.Arm]AssuranceSource) error {
	policy, err := releasepolicy.DecodePolicy(policyData)
	if err != nil {
		return err
	}
	sources := map[releasepolicy.Arm]Source{releasepolicy.Baseline: baseline, releasepolicy.Candidate: candidate}
	verify := func(arm releasepolicy.Arm) (*preparedPlan, error) {
		if contractData != nil {
			if _, err := verifyAssuranceSource(ctx, policy, contractData, arm, assureSources[arm]); err != nil {
				return nil, err
			}
		}
		prepared, err := prepare(sources[arm])
		if err != nil {
			return nil, err
		}
		if prepared.plan.Digest != policy.Arms[arm].Digest {
			return nil, fmt.Errorf("%s inspected source/effective configuration drifted from precollection plan", arm)
		}
		return prepared, nil
	}
	// Both sources must still match before any BEGIN is written or engine starts.
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		if _, err := verify(arm); err != nil {
			return err
		}
	}
	collector := releasepolicy.Collector{
		Initialize: func(context.Context) error { return nil },
		Shutdown:   func(context.Context) error { return nil },
		Usage: func(context.Context, releasepolicy.Arm) ([]releasepolicy.UsageAxis, error) {
			usage := []releasepolicy.UsageAxis{}
			for _, axis := range []string{"input_tokens", "output_tokens", "ai_credits"} {
				usage = append(usage, releasepolicy.UsageAxis{Axis: axis, Availability: "unavailable",
					Observation: "unknown", Reason: "Mock execution has no provider billing observation."})
			}
			for _, budget := range policy.Requirements.Billing {
				if budget.Axis == "provider_currency" {
					usage = append(usage, releasepolicy.UsageAxis{Axis: budget.Axis, Currency: budget.Currency,
						Availability: "unavailable", Observation: "unknown",
						Reason: "The selected currency budget has no provider billing observation."})
				}
			}
			return usage, nil
		},
		Attempt: func(ctx context.Context, key releasepolicy.AttemptKey) (result releasepolicy.AttemptObservation, err error) {
			// The durable start already exists. A preparation error leaves an
			// explicit started-but-incomplete attempt, never a behavioral zero.
			prepared, err := verify(key.Arm)
			if err != nil {
				return result, err
			}
			task := prepared.tasks[key.TaskID]
			if task == nil {
				return result, fmt.Errorf("allocated task absent from inspected source")
			}
			engine := execution.NewMockEngine(prepared.cfg.Spec().Config.ModelID)
			if err := engine.Initialize(ctx); err != nil {
				return result, fmt.Errorf("initializing fresh controlled mock: %w", err)
			}
			defer func() {
				err = errors.Join(err, engine.Shutdown(context.WithoutCancel(ctx)))
			}()
			runner := orchestration.NewEvalRunner(prepared.cfg, engine)
			origin := models.EvidenceOrigin{EvalID: key.EvalID, TaskID: key.TaskID,
				RunNumber: key.Trial, AttemptCount: key.Attempt}
			observation, err := runner.ExecutePreparedControlledAttempt(ctx, task, prepared.inputs[key.TaskID], origin)
			if err != nil {
				return result, err
			}
			var plan releasepolicy.TaskPlan
			for _, item := range prepared.plan.Plan.Tasks {
				if item.ID == key.TaskID {
					plan = item
				}
			}
			summary := releasepolicy.AttemptSummary{Key: key, Origin: origin, Status: string(observation.Run.Status),
				Category: "behavioral", Checks: []releasepolicy.CheckSummary{}, References: []models.EvidenceReference{},
				Runtime: releasepolicy.RuntimeObservation{TaskID: key.TaskID,
					RequestedEngine: plan.Settings.Engine, RequestedModel: plan.Settings.Model,
					RequestedReasoning: plan.Settings.ReasoningEffort, Availability: "available",
					EngineImplementation: plan.ExpectedRuntime.EngineImplementation, ModelVersion: "mock_no_provider"}}
			if observation.Run.ErrorMsg != "" || (observation.Run.Status != models.StatusPassed && observation.Run.Status != models.StatusFailed) {
				summary.Status, summary.Category = "incomplete", "operational"
			}
			for _, check := range observation.Checks {
				summary.Checks = append(summary.Checks, releasepolicy.CheckSummary{Scope: check.Scope,
					Grader: check.Result.Name, Passed: check.Result.Passed,
					Score: releaseNumber(check.Result.Score), OperationalState: "observed"})
			}
			output := releasepolicy.AssuranceOutput{Availability: "unavailable",
				Reason: "No successful execution response was preserved."}
			if observation.Output != nil {
				output = releasepolicy.AssuranceOutput{Availability: "available", Value: observation.Output}
			}
			return releasepolicy.AttemptObservation{Summary: summary, Output: &output,
				Result: releasepolicy.ActualRunRow{Origin: observation.Origin, Run: observation.Run}}, nil
		},
	}
	if contractData != nil {
		collector.BeforePublish = func(ctx context.Context) error {
			for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
				if _, err := verify(arm); err != nil {
					return err
				}
			}
			return nil
		}
		return releasepolicy.CollectAssured(ctx, policyData, contractData, directory, collector)
	}
	return releasepolicy.Collect(ctx, policyData, directory, collector)
}

func releaseNumber(value float64) json.Number {
	return json.Number(strconv.FormatFloat(value, 'g', -1, 64))
}
