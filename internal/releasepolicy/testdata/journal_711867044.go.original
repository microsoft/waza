package releasepolicy

import (
	"errors"
	"fmt"
	"io"
	"reflect"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/models"
)

func DecodeJournal(data []byte, policy *Policy) (*Journal, error) {
	var j Journal
	if _, err := admit(data, JournalKind, &j); err != nil {
		return nil, err
	}
	if _, err := Replay(policy, &j); err != nil {
		return nil, err
	}
	return &j, nil
}

func DecodeReceipt(data []byte) (*Receipt, error) {
	var r Receipt
	if _, err := admit(data, ReceiptKind, &r); err != nil {
		return nil, err
	}
	if r.State != "begin" && r.State != "final" {
		return nil, fmt.Errorf("unsupported collection receipt state")
	}
	return &r, nil
}

// Replay reconstructs both receipts from the single ordered stream. This is a
// local consistency check, not evidence that a separate witness saw collection.
func Replay(p *Policy, j *Journal) (map[Arm]Receipt, error) {
	return replay(p, j, false)
}

// replay's prefix mode validates exactly the same transition machine. EOF is
// incomplete publication, never permission to skip or accept a bad transition.
func replay(p *Policy, j *Journal, prefix bool) (result map[Arm]Receipt, err error) {
	if err := ValidatePolicy(p); err != nil {
		return nil, err
	}
	if j == nil || j.Kind != JournalKind || j.Version != Version ||
		j.CollectionID == "" || !isDigest(j.Digest) {
		return nil, fmt.Errorf("invalid journal identity/collection")
	}
	for i, event := range j.Events {
		if event.Sequence != i+1 || event.CollectionID != j.CollectionID ||
			event.PolicyDigest != p.Digest {
			return nil, fmt.Errorf("journal event %d sequence/collection/policy mismatch", i+1)
		}
	}
	receipts := map[Arm]Receipt{}
	defer func() {
		if prefix && errors.Is(err, io.EOF) {
			result, err = receipts, nil
		}
	}()
	offset := 0
	take := func(kind string) (*Event, error) {
		if offset >= len(j.Events) && prefix {
			return nil, io.EOF
		}
		if offset >= len(j.Events) || j.Events[offset].Type != kind {
			return nil, fmt.Errorf("journal event %d: expected %s", offset+1, kind)
		}
		event := &j.Events[offset]
		offset++
		// Each event type has an exact conditional field set.
		fields := Event{Sequence: event.Sequence, CollectionID: event.CollectionID,
			PolicyDigest: event.PolicyDigest, Type: event.Type}
		switch kind {
		case "begin_arm":
			fields.Arm, fields.EvalID = event.Arm, event.EvalID
		case "cluster_start":
			fields.ClusterID = event.ClusterID
		case "attempt_start":
			fields.Key = event.Key
		case "attempt_terminal":
			fields.Attempt = event.Attempt
		case "trial_terminal":
			fields.Arm, fields.Trial = event.Arm, event.Trial
		case "end_arm":
			fields.Arm, fields.Usage = event.Arm, event.Usage
		case "collection_end":
		default:
			return nil, fmt.Errorf("unsupported event transition")
		}
		if !reflect.DeepEqual(fields, *event) {
			return nil, fmt.Errorf("journal event %d has fields outside its transition", offset)
		}
		return event, nil
	}
	for _, arm := range []Arm{Baseline, Candidate} {
		e, err := take("begin_arm")
		if err != nil {
			return nil, err
		}
		if e.Arm != arm || e.EvalID == "" ||
			(arm == Candidate && e.EvalID == receipts[Baseline].EvalID) {
			return nil, fmt.Errorf("both distinct arm BEGIN identities are required before collection")
		}
		receipts[arm] = Receipt{Kind: ReceiptKind, Version: Version, CollectionID: j.CollectionID,
			PolicyDigest: p.Digest, Arm: arm, EvalID: e.EvalID,
			PlanDigest: p.Arms[arm].Digest, State: "final",
			Samples: PlannedSamples(p), Started: []SampleKey{}, Attempts: []AttemptSummary{},
			Trials: []TrialSummary{}, Runtime: []RuntimeObservation{}, Usage: []UsageAxis{},
			JournalDigest: &j.Digest, JournalCount: len(j.Events)}
	}
	for ci, cluster := range p.Design.Clusters {
		e, err := take("cluster_start")
		if err != nil {
			return nil, err
		}
		if e.ClusterID != cluster.ID {
			return nil, fmt.Errorf("cluster order differs from frozen allocation")
		}
		for _, arm := range p.Design.Allocation.Assignments[ci].Order {
			r := receipts[arm]
			for _, task := range cluster.Tasks {
				settings := settingsFor(p, arm, task.ID)
				for _, trial := range task.TrialOrdinals {
					sample := SampleKey{ClusterID: cluster.ID, TaskID: task.ID, Trial: trial}
					attempts := []AttemptSummary{}
					for attempt := 1; attempt <= settings.MaxAttempts; attempt++ {
						key := AttemptKey{SampleKey: sample, Arm: arm, EvalID: r.EvalID, Attempt: attempt}
						start, err := take("attempt_start")
						if err != nil {
							return nil, err
						}
						if start.Key == nil || *start.Key != key {
							return nil, fmt.Errorf("attempt does not match frozen cluster/arm/task/trial/retry order")
						}
						end, err := take("attempt_terminal")
						if err != nil {
							return nil, err
						}
						if end.Attempt == nil || end.Attempt.Key != key {
							return nil, fmt.Errorf("attempt terminal is missing or mismatched")
						}
						if err := validateAttempt(p, *end.Attempt); err != nil {
							return nil, err
						}
						attempts = append(attempts, *end.Attempt)
						if end.Attempt.Status == "passed" || end.Attempt.Category != "behavioral" {
							break
						}
					}
					terminal, err := take("trial_terminal")
					if err != nil {
						return nil, err
					}
					expected := summarizeTrial(sample, attempts)
					if terminal.Arm != arm || terminal.Trial == nil || !reflect.DeepEqual(*terminal.Trial, expected) {
						return nil, fmt.Errorf("trial terminal contradicts its complete attempt tape")
					}
					r.Started = append(r.Started, sample)
					r.Attempts = append(r.Attempts, attempts...)
					r.Trials = append(r.Trials, expected)
					for _, a := range attempts {
						r.Runtime = append(r.Runtime, a.Runtime)
					}
				}
			}
			receipts[arm] = r
		}
	}
	for _, arm := range []Arm{Baseline, Candidate} {
		e, err := take("end_arm")
		if err != nil {
			return nil, err
		}
		if e.Arm != arm || e.Usage == nil {
			return nil, fmt.Errorf("arm final usage inventory is required")
		}
		seen := map[string]bool{}
		for _, u := range e.Usage {
			if err := ValidateUsage(u); err != nil {
				return nil, err
			}
			key := u.Axis + ":" + u.Currency
			if seen[key] {
				return nil, fmt.Errorf("duplicated usage axis")
			}
			seen[key] = true
		}
		for _, axis := range []string{"input_tokens:", "output_tokens:", "ai_credits:"} {
			if !seen[axis] {
				return nil, fmt.Errorf("missing usage availability: %s", axis)
			}
		}
		r := receipts[arm]
		r.Usage = e.Usage
		binding := ResultBinding{Kind: BindingKind, Version: Version, CollectionID: r.CollectionID,
			PolicyDigest: r.PolicyDigest, Arm: arm, EvalID: r.EvalID, PlanDigest: r.PlanDigest,
			Samples: r.Samples, Trials: r.Trials, Attempts: r.Attempts, Usage: r.Usage}
		digest, err := evidence.JSONDigest(binding)
		if err != nil {
			return nil, err
		}
		r.Binding, r.BindingDigest = &binding, digest
		receipts[arm] = r
	}
	if _, err := take("collection_end"); err != nil {
		return nil, err
	}
	if offset != len(j.Events) {
		return nil, fmt.Errorf("extra journal transitions after collection end")
	}
	return receipts, nil
}

func PlannedSamples(p *Policy) []SampleKey {
	samples := []SampleKey{}
	for _, c := range p.Design.Clusters {
		for _, t := range c.Tasks {
			for _, ordinal := range t.TrialOrdinals {
				samples = append(samples, SampleKey{ClusterID: c.ID, TaskID: t.ID, Trial: ordinal})
			}
		}
	}
	return samples
}

func settingsFor(p *Policy, arm Arm, taskID string) Settings {
	for _, task := range p.Arms[arm].Plan.Tasks {
		if task.ID == taskID {
			return task.Settings
		}
	}
	return Settings{}
}

func validateAttempt(p *Policy, a AttemptSummary) error {
	expected := models.EvidenceOrigin{EvalID: a.Key.EvalID, TaskID: a.Key.TaskID,
		RunNumber: a.Key.Trial, AttemptCount: a.Key.Attempt}
	if a.Origin != expected {
		return fmt.Errorf("attempt origin must exactly match its preallocated identity")
	}
	for _, ref := range a.References {
		if ref.Origin.EvalID != expected.EvalID || ref.Origin.TaskID != expected.TaskID ||
			ref.Origin.RunNumber != expected.RunNumber || ref.Origin.AttemptCount != expected.AttemptCount ||
			ref.ArtifactID == "" {
			return fmt.Errorf("attempt evidence reference origin is missing or mismatched")
		}
	}
	if err := validateRuntime(p, a.Key.Arm, a.Runtime, a.Key.TaskID); err != nil {
		return err
	}
	seen := map[string]bool{}
	allPass := true
	for _, check := range a.Checks {
		key := fmt.Sprintf("%s:%s:%d", check.Scope, check.Grader, check.AfterTurn)
		if check.Grader == "" || seen[key] || check.AfterTurn < 0 ||
			(check.Scope != "eval" && check.Scope != "task" && check.Scope != "checkpoint" && check.Scope != "expectation") {
			return fmt.Errorf("invalid or ambiguous scoped check attribution")
		}
		seen[key] = true
		if _, err := number(check.Score, "check score", 0, 1); err != nil {
			return err
		}
		if check.OperationalState != "observed" && check.OperationalState != "unknown" {
			return fmt.Errorf("invalid check operational observability")
		}
		allPass = allPass && check.Passed
	}
	switch a.Category {
	case "behavioral":
		if len(a.Checks) == 0 || (a.Status != "passed" && a.Status != "failed") ||
			(a.Status == "passed") != allPass {
			return fmt.Errorf("behavioral status contradicts independently attributable checks")
		}
		for _, c := range a.Checks {
			if c.OperationalState != "observed" {
				return fmt.Errorf("unknown operational observability cannot be behavioral evidence")
			}
		}
	case "operational", "unknown":
		if a.Status != "incomplete" {
			return fmt.Errorf("operational or unknown evidence must be incomplete")
		}
	default:
		return fmt.Errorf("unknown attempt category")
	}
	return nil
}

func validateRuntime(p *Policy, arm Arm, actual RuntimeObservation, id string) error {
	for _, task := range p.Arms[arm].Plan.Tasks {
		if task.ID != id {
			continue
		}
		s, expected := task.Settings, task.ExpectedRuntime
		if actual.TaskID != id || actual.RequestedEngine != s.Engine ||
			actual.RequestedModel != s.Model || actual.RequestedReasoning != s.ReasoningEffort {
			return fmt.Errorf("observed runtime is not bound to this task's effective settings")
		}
		switch actual.Availability {
		case "available":
			if actual.EngineImplementation == "" || actual.ModelVersion == "" || actual.Reason != "" {
				return fmt.Errorf("available runtime must have independently observed versions")
			}
			if expected.Availability == "expected" &&
				(actual.EngineImplementation != expected.EngineImplementation || actual.ModelVersion != expected.ModelVersion) {
				return fmt.Errorf("observed runtime violates precollection constraints")
			}
		case "unavailable":
			if actual.EngineImplementation != "" || actual.ModelVersion != "" || actual.Reason == "" {
				return fmt.Errorf("unavailable runtime cannot manufacture observed versions")
			}
		default:
			return fmt.Errorf("unsupported runtime observation")
		}
		return nil
	}
	return fmt.Errorf("runtime observation names an unplanned task")
}

func summarizeTrial(key SampleKey, attempts []AttemptSummary) TrialSummary {
	s := TrialSummary{Key: key, Terminal: len(attempts), State: "incomplete"}
	if attempts[0].Category == "behavioral" {
		first := attempts[0].Status == "passed"
		s.FirstPass = &first
	}
	for _, a := range attempts {
		if a.Category != "behavioral" {
			return s
		}
	}
	retry := attempts[len(attempts)-1].Status == "passed"
	s.State, s.RetryPass = "complete", &retry
	return s
}

// VerifyReceipt requires a final receipt to match the reconstructed tape and a
// separately loaded result projection. A sealed summary alone cannot pass.
func VerifyReceipt(actual Receipt, expected Receipt, result ResultBinding) error {
	if actual.State != "final" || actual.JournalDigest == nil || actual.Binding == nil ||
		actual.BindingDigest == nil || !reflect.DeepEqual(result, *actual.Binding) {
		return fmt.Errorf("final receipt is missing journal/result linkage")
	}
	digest, err := evidence.JSONDigest(result)
	if err != nil || *actual.BindingDigest != *digest {
		return fmt.Errorf("result binding projection mismatch")
	}
	// Document identity was validated against original JSON during decoding.
	actual.Digest, expected.Digest = models.EvidenceDigest{}, models.EvidenceDigest{}
	if !reflect.DeepEqual(actual, expected) {
		return fmt.Errorf("receipt summaries do not match the shared ordered journal")
	}
	return nil
}
