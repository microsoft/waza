package releasepolicy

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"

	"github.com/microsoft/waza/internal/statistics"
)

type Dimension struct {
	State   string   `json:"state"`
	Reasons []string `json:"reasons"`
}

type Decision struct {
	Kind            string              `json:"kind"`
	Version         string              `json:"version"`
	Accepted        bool                `json:"accepted"`
	Compatibility   Dimension           `json:"compatibility"`
	Completeness    Dimension           `json:"completeness"`
	Assurance       Dimension           `json:"assurance"`
	Golden          Dimension           `json:"golden"`
	Billing         Dimension           `json:"billing"`
	Statistics      Dimension           `json:"statistics"`
	Operations      Dimension           `json:"operations"`
	Estimate        *float64            `json:"estimate,omitempty"`
	Lower           *float64            `json:"lower,omitempty"`
	Upper           *float64            `json:"upper,omitempty"`
	HalfWidth       *float64            `json:"half_width,omitempty"`
	Effective       *float64            `json:"effective_clusters,omitempty"`
	PlannedClusters int                 `json:"planned_clusters"`
	Limitations     []string            `json:"limitations"`
	Accounting      map[Arm]Reliability `json:"accounting"`
	Usage           map[Arm][]UsageAxis `json:"usage"`
}

type Reliability struct {
	PlannedTrials    int      `json:"planned_trials"`
	StartedTrials    int      `json:"started_trials"`
	CompleteTrials   int      `json:"complete_trials"`
	Attempts         int      `json:"attempts"`
	StartedAttempts  int      `json:"started_attempts"`
	CompleteAttempts int      `json:"complete_attempts"`
	FirstPasses      int      `json:"first_attempt_passes"`
	RetryPasses      int      `json:"retry_policy_passes"`
	RecoveredTrials  int      `json:"recovered_trials"`
	FirstRate        *float64 `json:"first_attempt_rate"`
	RetryRate        *float64 `json:"retry_policy_rate"`
}

func InitialDecision() Decision {
	return Decision{Kind: "waza.release-decision", Version: Version,
		Compatibility: Dimension{State: "not_assessed", Reasons: []string{}},
		Completeness:  Dimension{State: "missing", Reasons: []string{}},
		Assurance:     Dimension{State: "not_assessed", Reasons: []string{}},
		Golden:        Dimension{State: "not_assessed", Reasons: []string{}},
		Billing:       Dimension{State: "not_assessed", Reasons: []string{}},
		Statistics:    Dimension{State: "not_assessed", Reasons: []string{}},
		Operations:    Dimension{State: "not_assessed", Reasons: []string{}},
		Accounting:    map[Arm]Reliability{}, Usage: map[Arm][]UsageAxis{},
		Limitations: []string{
			"Local hashes establish consistency, not authenticity or independently witnessed precommitment.",
			"Independent bounded cluster differences are an assumption; retries and tasks do not add independent samples.",
			"One candidate-minus-baseline endpoint is assessed; family size is an external conservative declaration.",
			"Fixed allocation only; no adaptive stopping, post-hoc endpoints, selected successes, history adoption or resume.",
			"Billing is final observed spend, not an atomic limit; asynchronous overshoot can occur.",
		}}
}

// Assess consumes verified full receipts. The caller must first admit the raw
// policy/journal/receipts and VerifyReceipt against separately loaded results.
// Assurance has no implemented verdict contract here: absence is not assessed.
func Assess(p *Policy, receipts map[Arm]Receipt) (Decision, error) {
	d := InitialDecision()
	if err := ValidatePolicy(p); err != nil {
		d.Compatibility = Dimension{State: "invalid", Reasons: []string{err.Error()}}
		return d, err
	}
	d.PlannedClusters = len(p.Design.Clusters)
	d.Compatibility.State, d.Completeness.State, d.Operations.State = "compatible", "complete", "observed"
	for _, arm := range []Arm{Baseline, Candidate} {
		r, ok := receipts[arm]
		d.Usage[arm] = r.Usage
		counts := Reliability{PlannedTrials: len(PlannedSamples(p)), StartedTrials: len(r.Started),
			Attempts: len(r.Attempts), StartedAttempts: len(r.Attempts), CompleteAttempts: len(r.Attempts)}
		firstObserved, retryObserved := 0, 0
		for _, trial := range r.Trials {
			if trial.State == "complete" {
				counts.CompleteTrials++
			}
			if trial.FirstPass != nil {
				firstObserved++
				if *trial.FirstPass {
					counts.FirstPasses++
				}
			}
			if trial.RetryPass != nil {
				retryObserved++
				if *trial.RetryPass {
					counts.RetryPasses++
					if trial.FirstPass != nil && !*trial.FirstPass {
						counts.RecoveredTrials++
					}
				}
			}
		}
		if firstObserved == counts.PlannedTrials && counts.PlannedTrials > 0 {
			rate := float64(counts.FirstPasses) / float64(counts.PlannedTrials)
			counts.FirstRate = &rate
		}
		if retryObserved == counts.PlannedTrials && counts.PlannedTrials > 0 {
			rate := float64(counts.RetryPasses) / float64(counts.PlannedTrials)
			counts.RetryRate = &rate
		}
		d.Accounting[arm] = counts
		if !ok || r.State != "final" || r.PolicyDigest != p.Digest || r.PlanDigest != p.Arms[arm].Digest ||
			r.Arm != arm || r.Binding == nil || r.JournalDigest == nil ||
			len(r.Trials) != len(PlannedSamples(p)) {
			reason := string(arm) + " missing or mismatched final collection"
			escalate(&d.Compatibility, "invalid", reason)
			escalate(&d.Completeness, "missing", reason)
			escalate(&d.Operations, "incomplete", reason)
			continue
		}
		for _, identity := range p.Arms[arm].Plan.Identities {
			required := false
			for _, domain := range p.Requirements.IdentityDomains {
				required = required || domain == identity.Domain
			}
			if required && identity.Availability == "unavailable" {
				escalate(&d.Compatibility, "inconclusive",
					fmt.Sprintf("%s required %s/%s is unavailable", arm, identity.Domain, identity.TaskID))
			}
		}
		for _, trial := range r.Trials {
			if trial.State != "complete" || trial.FirstPass == nil || trial.RetryPass == nil {
				reason := fmt.Sprintf("%s/%s trial %d has incomplete/operational/unknown evidence", arm, trial.Key.TaskID, trial.Key.Trial)
				escalate(&d.Completeness, "partial", reason)
				escalate(&d.Operations, "incomplete", reason)
			}
		}
		for _, actual := range r.Runtime {
			if p.Requirements.Runtime && actual.Availability != "available" {
				escalate(&d.Compatibility, "inconclusive", string(arm)+" required observed runtime is unavailable")
			}
		}
	}
	// Unknown runtime is never assumed identical. Known observations must also
	// agree across trials and arms except the exact predeclared changed axis.
	for _, reason := range compareActualRuntime(p, receipts) {
		escalate(&d.Compatibility, "mismatched", reason)
	}
	if !p.Requirements.Assurance {
		d.Assurance.State = "not_required"
	} else {
		d.Assurance.Reasons = append(d.Assurance.Reasons, "required independently attributable assurance verdict is missing; declarations are not assurance")
	}
	d.Golden = assessGolden(p, receipts)
	d.Billing = assessBilling(p, receipts)
	if d.Compatibility.State != "compatible" || d.Completeness.State != "complete" {
		d.Statistics.State = "inconclusive"
		d.Statistics.Reasons = append(d.Statistics.Reasons, "required complete matched collection evidence is absent")
		return d, nil
	}
	if p.Design.Independence.Assessment != "justified" {
		d.Statistics.State = "inconclusive"
		d.Statistics.Reasons = append(d.Statistics.Reasons, "cluster independence is unjustified; descriptive evidence only")
		return d, nil
	}
	alpha, err := p.Design.Alpha.Float64()
	if err != nil {
		return d, err
	}
	width, err := p.Design.MaximumHalfWidth.Float64()
	if err != nil {
		return d, err
	}
	tokens := []json.Number{}
	for _, cluster := range p.Design.Clusters {
		tokens = append(tokens, cluster.Weight)
	}
	clusterWeights, err := canonicalWeights(tokens)
	if err != nil {
		return d, err
	}
	estimate := new(big.Rat)
	squaredMass := new(big.Rat)
	for ci, cluster := range p.Design.Clusters {
		taskTokens := []json.Number{}
		for _, task := range cluster.Tasks {
			taskTokens = append(taskTokens, task.Weight)
		}
		taskWeights, err := canonicalWeights(taskTokens)
		if err != nil {
			return d, err
		}
		difference := new(big.Rat)
		for ti, task := range cluster.Tasks {
			b, err := endpointMean(p, receipts[Baseline], task.ID)
			if err != nil {
				return d, err
			}
			c, err := endpointMean(p, receipts[Candidate], task.ID)
			if err != nil {
				return d, err
			}
			difference.Add(difference, new(big.Rat).Mul(taskWeights[ti], new(big.Rat).Sub(c, b)))
		}
		estimate.Add(estimate, new(big.Rat).Mul(clusterWeights[ci], difference))
		squaredMass.Add(squaredMass, new(big.Rat).Mul(clusterWeights[ci], clusterWeights[ci]))
	}
	// Both the point and radius use the same exact canonical normalized
	// coefficients; omitted-mass rejection already happened at admission.
	square, exact := squaredMass.Float64()
	if !exact && new(big.Rat).SetFloat64(square).Cmp(squaredMass) < 0 {
		square = math.Nextafter(square, math.Inf(1))
	}
	exactAlpha, ok := new(big.Rat).SetString(p.Design.Alpha.String())
	if !ok {
		return d, fmt.Errorf("invalid exact alpha")
	}
	if new(big.Rat).SetFloat64(alpha).Cmp(exactAlpha) > 0 {
		alpha = math.Nextafter(alpha, math.Inf(-1))
	}
	family := float64(p.Design.FamilySize)
	if new(big.Rat).SetFloat64(family).Cmp(new(big.Rat).SetInt64(int64(p.Design.FamilySize))) < 0 {
		family = math.Nextafter(family, math.Inf(1))
	}
	logFamily := math.Nextafter(math.Log(family), math.Inf(1))
	logTwo := math.Nextafter(math.Log(2), math.Inf(1))
	logAlpha := math.Nextafter(math.Log(alpha), math.Inf(-1))
	sum := math.Nextafter(logTwo+logFamily, math.Inf(1))
	factor := math.Nextafter(2*math.Nextafter(sum-logAlpha, math.Inf(1)), math.Inf(1))
	h := math.Nextafter(math.Sqrt(math.Nextafter(factor*square, math.Inf(1))), math.Inf(1))
	effectiveExact := new(big.Rat).Inv(squaredMass)
	effective, exact := effectiveExact.Float64()
	if !exact && new(big.Rat).SetFloat64(effective).Cmp(effectiveExact) > 0 {
		effective = math.Nextafter(effective, math.Inf(-1))
	}
	bound := statistics.PairedHoeffdingBound{HalfWidth: h, EffectiveClusters: effective}
	point, _ := estimate.Float64()
	// Directed rounding includes point conversion and interval arithmetic.
	lower := math.Nextafter(math.Nextafter(point, math.Inf(-1))-bound.HalfWidth, math.Inf(-1))
	upper := math.Nextafter(math.Nextafter(point, math.Inf(1))+bound.HalfWidth, math.Inf(1))
	d.Estimate, d.Lower, d.Upper = &point, &lower, &upper
	d.HalfWidth, d.Effective = &bound.HalfWidth, &bound.EffectiveClusters
	exactWidth, ok := new(big.Rat).SetString(p.Design.MaximumHalfWidth.String())
	if !ok || width <= 0 {
		return d, fmt.Errorf("invalid exact precision bound")
	}
	if new(big.Rat).SetFloat64(bound.HalfWidth).Cmp(exactWidth) > 0 {
		d.Statistics.State = "underpowered"
		d.Statistics.Reasons = append(d.Statistics.Reasons,
			"fixed design does not meet its predeclared precision bound (not a statistical power calculation)")
		return d, nil
	}
	margin, ok := new(big.Rat).SetString(p.Design.Margin.String())
	if !ok {
		return d, fmt.Errorf("invalid margin")
	}
	threshold := new(big.Rat)
	if p.Design.Accept == "noninferiority" {
		threshold.Neg(margin)
	}
	lowerExact := new(big.Rat).SetFloat64(lower)
	upperExact := new(big.Rat).SetFloat64(upper)
	if (p.Design.Accept == "improvement" && lowerExact.Cmp(threshold) > 0) ||
		(p.Design.Accept == "noninferiority" && lowerExact.Cmp(threshold) >= 0) {
		d.Statistics.State = p.Design.Accept
	} else if upperExact.Cmp(threshold) < 0 {
		d.Statistics.State = "regression"
	} else {
		d.Statistics.State = "inconclusive"
	}
	d.Accepted = d.Compatibility.State == "compatible" && d.Completeness.State == "complete" &&
		d.Operations.State == "observed" && d.Assurance.State == "not_required" &&
		(d.Golden.State == "passed" || d.Golden.State == "not_required") &&
		(d.Billing.State == "within_budget" || d.Billing.State == "not_required") &&
		(d.Statistics.State == "noninferiority" || d.Statistics.State == "improvement")
	return d, nil
}

func endpointMean(p *Policy, r Receipt, taskID string) (*big.Rat, error) {
	n, passes := int64(0), int64(0)
	for _, trial := range r.Trials {
		if trial.Key.TaskID != taskID {
			continue
		}
		value := trial.FirstPass
		if p.Design.Endpoint == "retry_policy_pass" {
			value = trial.RetryPass
		}
		if trial.State != "complete" || value == nil {
			return nil, fmt.Errorf("missing attributable endpoint for %s", taskID)
		}
		n++
		if *value {
			passes++
		}
	}
	if n == 0 {
		return nil, fmt.Errorf("no planned endpoint observations for %s", taskID)
	}
	return new(big.Rat).SetFrac64(passes, n), nil
}

func assessGolden(p *Policy, receipts map[Arm]Receipt) Dimension {
	d := Dimension{State: "not_required", Reasons: []string{}}
	if len(p.GoldenIDs) == 0 {
		return d
	}
	d.State = "passed"
	for _, id := range p.GoldenIDs {
		for _, arm := range []Arm{Baseline, Candidate} {
			found := 0
			for _, trial := range receipts[arm].Trials {
				if trial.Key.TaskID != id {
					continue
				}
				found++
				if trial.FirstPass == nil {
					escalate(&d, "missing_required_evidence", fmt.Sprintf("%s/%s trial %d lacks golden first-attempt evidence", arm, id, trial.Key.Trial))
				} else if !*trial.FirstPass {
					escalate(&d, "failed", fmt.Sprintf("%s/%s trial %d golden first attempt failed (recovery cannot erase it)", arm, id, trial.Key.Trial))
				}
			}
			expected := 0
			for _, sample := range PlannedSamples(p) {
				if sample.TaskID == id {
					expected++
				}
			}
			if found != expected {
				escalate(&d, "missing_required_evidence", fmt.Sprintf("%s/%s required golden trials are missing", arm, id))
			}
		}
	}
	return d
}

func assessBilling(p *Policy, receipts map[Arm]Receipt) Dimension {
	d := Dimension{State: "not_required", Reasons: []string{}}
	if len(p.Requirements.Billing) == 0 {
		return d
	}
	d.State = "within_budget"
	for _, requirement := range p.Requirements.Billing {
		maximum, ok := new(big.Rat).SetString(requirement.Maximum.String())
		if !ok {
			escalate(&d, "invalid", "invalid billing maximum for "+requirement.Axis)
			continue
		}
		for _, arm := range []Arm{Baseline, Candidate} {
			found := false
			for _, usage := range receipts[arm].Usage {
				if usage.Axis != requirement.Axis || usage.Currency != requirement.Currency {
					continue
				}
				found = true
				if usage.Availability != "available" || usage.Value == nil ||
					usage.Observation != "final_complete_attributable" {
					escalate(&d, "unavailable", fmt.Sprintf("%s/%s final complete attributable billing is unavailable", arm, usage.Axis))
					continue
				}
				actual, ok := new(big.Rat).SetString(usage.Value.String())
				if !ok {
					escalate(&d, "invalid", fmt.Sprintf("%s/%s has invalid observed billing value", arm, usage.Axis))
					continue
				}
				if actual.Cmp(maximum) > 0 {
					escalate(&d, "exceeded", fmt.Sprintf("%s/%s exceeds its final observed budget; asynchronous overshoot is not prevented", arm, usage.Axis))
				}
			}
			if !found {
				escalate(&d, "unavailable", fmt.Sprintf("%s/%s required billing axis is unavailable", arm, requirement.Axis))
			}
		}
	}
	return d
}

func compareActualRuntime(p *Policy, receipts map[Arm]Receipt) []string {
	var reasons []string
	observed := map[Arm]map[string]RuntimeObservation{}
	for _, arm := range []Arm{Baseline, Candidate} {
		observed[arm] = map[string]RuntimeObservation{}
		for _, r := range receipts[arm].Runtime {
			if err := validateRuntime(p, arm, r, r.TaskID); err != nil {
				reasons = append(reasons, fmt.Sprintf("%s/%s: %v", arm, r.TaskID, err))
				continue
			}
			if previous, ok := observed[arm][r.TaskID]; ok && previous != r {
				reasons = append(reasons, fmt.Sprintf("actual runtime changed within %s task %s", arm, r.TaskID))
				continue
			}
			observed[arm][r.TaskID] = r
		}
	}
	for _, task := range p.Arms[Baseline].Plan.Tasks {
		b, bok := observed[Baseline][task.ID]
		a, aok := observed[Candidate][task.ID]
		if !bok || !aok {
			if p.Requirements.Runtime {
				reasons = append(reasons, fmt.Sprintf("required actual runtime is missing: %s", task.ID))
			}
			continue
		}
		if b.Availability != "available" || a.Availability != "available" {
			continue
		}
		before, after := settingsFor(p, Baseline, task.ID), settingsFor(p, Candidate, task.ID)
		if before.Engine == after.Engine && b.EngineImplementation != a.EngineImplementation {
			reasons = append(reasons, fmt.Sprintf("unrelated engine implementation drift: %s", task.ID))
		}
		if before.Model == after.Model && b.ModelVersion != a.ModelVersion {
			reasons = append(reasons, fmt.Sprintf("unrelated actual model version drift: %s", task.ID))
		}
	}
	return reasons
}
