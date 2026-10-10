package releasepolicy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"strings"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/statistics"
)

const (
	PolicyKind  = "waza.release-policy"
	JournalKind = "waza.release-journal"
	ReceiptKind = "waza.release-receipt"
	PlanKind    = "waza.release-resolved-plan"
	BindingKind = "waza.release-result-binding"
)

// SealJSON removes the identity member entirely, using the normative JSON-v1
// projection. Number spelling is retained in admission, including nested plans.
func SealJSON(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encoding release document: %w", err)
	}
	object, err := objectJSON(data)
	if err != nil {
		return nil, err
	}
	delete(object, "digest")
	digest, err := evidence.JSONDigest(object)
	if err != nil {
		return nil, err
	}
	object["digest"] = digest
	return json.Marshal(object)
}

func objectJSON(data []byte) (map[string]any, error) {
	value, err := jsonutil.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("reading release JSON: %w", err)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("release document must be a JSON object")
	}
	return object, nil
}

func admit(data []byte, kind string, target any) (map[string]any, error) {
	object, err := objectJSON(data)
	if err != nil {
		return nil, err
	}
	if object["kind"] != kind || object["version"] != Version {
		return nil, fmt.Errorf("unsupported release document kind/version")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return nil, fmt.Errorf("decoding release document: %w", err)
	}
	if err := requireFields(object, reflect.TypeOf(target), ""); err != nil {
		return nil, err
	}
	declared, exists := object["digest"]
	if !exists {
		return nil, fmt.Errorf("release document digest is missing")
	}
	delete(object, "digest")
	actual, err := evidence.JSONDigest(object)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(declared)
	if err != nil {
		return nil, err
	}
	var expected models.EvidenceDigest
	if err := json.Unmarshal(encoded, &expected); err != nil || expected != *actual {
		return nil, fmt.Errorf("release document JSON-v1 identity mismatch")
	}
	return object, nil
}

func DecodePolicy(data []byte) (*Policy, error) {
	var policy Policy
	object, err := admit(data, PolicyKind, &policy)
	if err != nil {
		return nil, err
	}
	// Verify the original numeric-token projection, not a float64 re-encoding.
	arms, ok := object["arms"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("policy arms must be an object")
	}
	for _, arm := range []Arm{Baseline, Candidate} {
		raw, ok := arms[string(arm)].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("missing %s arm", arm)
		}
		digest, err := evidence.JSONDigest(raw["resolved_plan"])
		if err != nil || *digest != policy.Arms[arm].Digest {
			return nil, fmt.Errorf("%s resolved plan identity mismatch", arm)
		}
	}
	if err := ValidatePolicy(&policy); err != nil {
		return nil, err
	}
	return &policy, nil
}

func number(value json.Number, name string, lower, upper float64) (float64, error) {
	f, err := value.Float64()
	exact, ok := new(big.Rat).SetString(value.String())
	if err != nil || !ok || math.IsNaN(f) || math.IsInf(f, 0) ||
		exact.Cmp(new(big.Rat).SetFloat64(lower)) < 0 || exact.Cmp(new(big.Rat).SetFloat64(upper)) > 0 {
		return 0, fmt.Errorf("%s must be finite in [%g,%g]", name, lower, upper)
	}
	return f, nil
}

func isDigest(d models.EvidenceDigest) bool {
	decoded, err := hex.DecodeString(d.SHA256)
	return err == nil && len(decoded) == sha256.Size && d.Encoding == "json-v1" &&
		d.SHA256 == strings.ToLower(d.SHA256)
}

var taskDomains = []string{
	"task_definition", "resolved_prompt", "fixture_inventory",
	"instruction_inventory", "grader_configuration", "dependency_mode",
}

var suiteDomains = []string{"grader_implementation", "rubric_inventory", "lock_inventory"}

func validDomain(domain string) bool {
	for _, d := range append(append([]string(nil), taskDomains...), suiteDomains...) {
		if domain == d {
			return true
		}
	}
	return false
}

// EmptyIdentity is the canonical verified empty-set projection. Producers may
// use it only after inspection proves rubric/lock applicability is empty.
func EmptyIdentity(domain string) (models.EvidenceDigest, error) {
	if domain != "rubric_inventory" && domain != "lock_inventory" {
		return models.EvidenceDigest{}, fmt.Errorf("%s has no not-applicable projection", domain)
	}
	digest, err := evidence.JSONDigest(struct {
		Domain  string   `json:"domain"`
		Entries []string `json:"entries"`
	}{domain, []string{}})
	if err != nil {
		return models.EvidenceDigest{}, err
	}
	return *digest, nil
}

func ValidatePolicy(p *Policy) error {
	if p == nil || p.Kind != PolicyKind || p.Version != Version ||
		p.GoldenRule != "both_arms_first_attempt_pass" ||
		p.FamilyRule != "single_contrast_external_family_bound" {
		return fmt.Errorf("unsupported policy contract")
	}
	d := p.Design
	if d.Estimand != "fixed_suite_repeated_execution" ||
		(d.Endpoint != "first_attempt_pass" && d.Endpoint != "retry_policy_pass") ||
		(d.Accept != "noninferiority" && d.Accept != "improvement") {
		return fmt.Errorf("unsupported fixed-design estimand/endpoint/contrast")
	}
	alpha, err := number(d.Alpha, "alpha", 0, 1)
	if err != nil || alpha == 0 || alpha == 1 || d.FamilySize < 1 {
		return fmt.Errorf("alpha must be strictly between 0 and 1 and family size positive")
	}
	width, err := number(d.MaximumHalfWidth, "maximum half width", 0, 1)
	if err != nil || width == 0 {
		return fmt.Errorf("maximum half width must be positive and at most 1")
	}
	if _, err := number(d.Margin, "noninferiority margin", 0, 1); err != nil {
		return err
	}
	if d.Independence.Assessment != "justified" && d.Independence.Assessment != "unjustified" {
		return fmt.Errorf("independence assessment must be justified or unjustified")
	}
	if strings.TrimSpace(d.Independence.Justification) == "" || len(d.Independence.Limitations) == 0 {
		return fmt.Errorf("independence needs a mechanism and explicit limitations")
	}
	weights := []float64{}
	taskIDs := map[string]bool{}
	clusterIDs := map[string]bool{}
	for _, c := range d.Clusters {
		if strings.TrimSpace(c.ID) == "" || clusterIDs[c.ID] || len(c.Tasks) == 0 {
			return fmt.Errorf("cluster IDs must be unique, nonempty, and contain tasks")
		}
		clusterIDs[c.ID] = true
		w, err := number(c.Weight, "cluster weight", 0, 1)
		if err != nil || w == 0 {
			return fmt.Errorf("cluster weights must be positive")
		}
		weights = append(weights, w)
		if _, err := canonicalCoefficient(c.Weight); err != nil {
			return err
		}
		taskWeights := []float64{}
		for _, t := range c.Tasks {
			if strings.TrimSpace(t.ID) == "" || taskIDs[t.ID] || len(t.TrialOrdinals) == 0 {
				return fmt.Errorf("each task must occur in exactly one cluster with planned trials")
			}
			taskIDs[t.ID] = true
			tw, err := number(t.Weight, "task weight", 0, 1)
			if err != nil || tw == 0 {
				return fmt.Errorf("task weights must be positive")
			}
			taskWeights = append(taskWeights, tw)
			if _, err := canonicalCoefficient(t.Weight); err != nil {
				return err
			}
			for i, ordinal := range t.TrialOrdinals {
				if ordinal != i+1 {
					return fmt.Errorf("trial ordinals must be contiguous starting at 1")
				}
			}
		}
		if _, err := statistics.FixedPairedHoeffdingBound(taskWeights, alpha, d.FamilySize); err != nil {
			return fmt.Errorf("cluster %s allocation: %w", c.ID, err)
		}
	}
	if _, err := statistics.FixedPairedHoeffdingBound(weights, alpha, d.FamilySize); err != nil {
		return fmt.Errorf("cluster allocation: %w", err)
	}
	if err := validateAllocation(d); err != nil {
		return err
	}
	if len(p.Arms) != 2 {
		return fmt.Errorf("exactly baseline and candidate arms are required")
	}
	for _, arm := range []Arm{Baseline, Candidate} {
		a, ok := p.Arms[arm]
		if !ok || !isDigest(a.Digest) {
			return fmt.Errorf("invalid %s plan identity", arm)
		}
		if err := validatePlan(a.Plan, taskIDs); err != nil {
			return fmt.Errorf("%s plan: %w", arm, err)
		}
		for _, cluster := range d.Clusters {
			for _, task := range cluster.Tasks {
				if settingsFor(p, arm, task.ID).TrialsPerTask != len(task.TrialOrdinals) {
					return fmt.Errorf("%s/%s planned trials differ from effective trials_per_task", arm, task.ID)
				}
			}
		}
	}
	if err := validateChanges(p); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, id := range p.GoldenIDs {
		if !taskIDs[id] || seen[id] {
			return fmt.Errorf("required golden task is missing or duplicated: %s", id)
		}
		seen[id] = true
	}
	seen = map[string]bool{}
	for _, domain := range p.Requirements.IdentityDomains {
		if !validDomain(domain) || seen[domain] {
			return fmt.Errorf("unknown or duplicated required identity domain: %s", domain)
		}
		seen[domain] = true
	}
	seen = map[string]bool{}
	for _, b := range p.Requirements.Billing {
		key := b.Axis + ":" + b.Currency
		if seen[key] {
			return fmt.Errorf("duplicated billing requirement: %s", key)
		}
		seen[key] = true
		value := b.Maximum
		if err := ValidateUsage(UsageAxis{Axis: b.Axis, Currency: b.Currency,
			Availability: "available", Value: &value, Observation: "final_complete_attributable"}); err != nil {
			return fmt.Errorf("billing requirement: %w", err)
		}
	}
	return nil
}

// canonicalCoefficient restricts weight tokens to the Go JSON float64
// representation. Hidden extra precision cannot select different coefficients
// for admission and inference.
func canonicalCoefficient(n json.Number) (*big.Rat, error) {
	value, err := number(n, "weight", 0, 1)
	if err != nil || value == 0 {
		return nil, fmt.Errorf("weight must be a positive finite canonical coefficient")
	}
	data, err := json.Marshal(value)
	if err != nil || string(data) != n.String() {
		return nil, fmt.Errorf("weight must use its canonical Go JSON float64 token; generate allocation before collection")
	}
	r, ok := new(big.Rat).SetString(n.String())
	if !ok {
		return nil, fmt.Errorf("invalid rational weight")
	}
	return r, nil
}

// canonicalWeights normalizes the exact declared set only after narrow
// representation-mass admission. It never repairs omitted cluster/task mass.
func canonicalWeights(tokens []json.Number) ([]*big.Rat, error) {
	total := new(big.Rat)
	weights := make([]*big.Rat, len(tokens))
	numeric := make([]float64, len(tokens))
	for i, token := range tokens {
		var err error
		weights[i], err = canonicalCoefficient(token)
		if err != nil {
			return nil, err
		}
		numeric[i], err = token.Float64()
		if err != nil {
			return nil, err
		}
		total.Add(total, weights[i])
	}
	if _, err := statistics.FixedPairedHoeffdingBound(numeric, .05, 1); err != nil {
		return nil, fmt.Errorf("canonical coefficient mass: %w", err)
	}
	for _, weight := range weights {
		weight.Quo(weight, total)
	}
	return weights, nil
}

func validateAllocation(d Design) error {
	seed, err := hex.DecodeString(d.Allocation.Seed)
	if err != nil || len(seed) != 32 || d.Allocation.Seed != strings.ToLower(d.Allocation.Seed) ||
		d.Allocation.Mechanism != "sha256_seed_cluster_first_bit" ||
		len(d.Allocation.Assignments) != len(d.Clusters) {
		return fmt.Errorf("invalid precommitted paired allocation")
	}
	for i, c := range d.Clusters {
		expected, err := ArmOrder(d.Allocation.Seed, c.ID)
		if err != nil {
			return err
		}
		a := d.Allocation.Assignments[i]
		if a.ClusterID != c.ID || !reflect.DeepEqual(a.Order, expected) {
			return fmt.Errorf("allocation mismatch for cluster %s", c.ID)
		}
	}
	return nil
}

func ArmOrder(seed, clusterID string) ([]Arm, error) {
	digest, err := evidence.JSONDigest(struct {
		Seed      string `json:"seed"`
		ClusterID string `json:"cluster_id"`
	}{seed, clusterID})
	if err != nil {
		return nil, err
	}
	raw, err := hex.DecodeString(digest.SHA256)
	if err != nil {
		return nil, err
	}
	if raw[0]&1 == 0 {
		return []Arm{Baseline, Candidate}, nil
	}
	return []Arm{Candidate, Baseline}, nil
}

func validatePlan(p ResolvedPlan, tasks map[string]bool) error {
	if p.Kind != PlanKind || p.Version != Version || len(p.Tasks) != len(tasks) {
		return fmt.Errorf("unsupported plan or task inventory mismatch")
	}
	seen := map[string]bool{}
	for _, t := range p.Tasks {
		if !tasks[t.ID] || seen[t.ID] || t.Settings.Engine == "" ||
			t.Settings.MaxAttempts < 1 || t.Settings.TrialsPerTask < 1 || t.Settings.TimeoutSeconds < 1 || !isDigest(t.Settings.OtherSettingsDigest) {
			return fmt.Errorf("invalid effective settings/task inventory")
		}
		seen[t.ID] = true
		r := t.ExpectedRuntime
		switch r.Availability {
		case "expected":
			if r.EngineImplementation == "" || r.ModelVersion == "" || r.Reason != "" {
				return fmt.Errorf("expected runtime needs concrete implementation and model versions")
			}
		case "unavailable":
			if r.EngineImplementation != "" || r.ModelVersion != "" || strings.TrimSpace(r.Reason) == "" {
				return fmt.Errorf("unavailable runtime cannot supply expected versions")
			}
		default:
			return fmt.Errorf("unknown expected runtime availability")
		}
	}
	seen = map[string]bool{}
	for _, identity := range p.Identities {
		key := identity.Domain + ":" + identity.TaskID
		if seen[key] || !validDomain(identity.Domain) {
			return fmt.Errorf("unknown or duplicate scoped identity")
		}
		seen[key] = true
		suite := false
		for _, domain := range suiteDomains {
			suite = suite || identity.Domain == domain
		}
		if (suite && identity.TaskID != "") || (!suite && !tasks[identity.TaskID]) {
			return fmt.Errorf("identity domain has incorrect suite/task scope")
		}
		switch identity.Availability {
		case "available":
			if identity.Digest == nil || !isDigest(*identity.Digest) || identity.Reason != "" {
				return fmt.Errorf("available identity needs only its JSON-v1 digest")
			}
		case "unavailable":
			if identity.Digest != nil || strings.TrimSpace(identity.Reason) == "" {
				return fmt.Errorf("unavailable identity needs a reason and no digest")
			}
		case "not_applicable":
			expected, err := EmptyIdentity(identity.Domain)
			if err != nil || identity.Digest == nil || *identity.Digest != expected || identity.Reason != "" {
				return fmt.Errorf("not-applicable identity must bind a supported verified empty set")
			}
		default:
			return fmt.Errorf("unknown identity availability")
		}
	}
	for _, domain := range suiteDomains {
		if !seen[domain+":"] {
			return fmt.Errorf("missing suite identity: %s", domain)
		}
	}
	for task := range tasks {
		for _, domain := range taskDomains {
			if !seen[domain+":"+task] {
				return fmt.Errorf("missing task identity: %s/%s", task, domain)
			}
		}
	}
	return nil
}

func validateChanges(p *Policy) error {
	before := p.Arms[Baseline].Plan
	after := p.Arms[Candidate].Plan
	changes := map[string]AllowedChange{}
	for _, c := range p.Changes {
		key := c.TaskID + ":" + c.Field
		if c.Before == c.After || changes[key].TaskID != "" ||
			(c.Field != "model" && c.Field != "engine" && c.Field != "reasoning_effort") {
			return fmt.Errorf("allowed changes must be unique exact task settings changes")
		}
		changes[key] = c
	}
	for i, b := range before.Tasks {
		a := after.Tasks[i]
		if b.ID != a.ID {
			return fmt.Errorf("arm task ordering differs")
		}
		bs, as := b.Settings, a.Settings
		values := [][3]string{{"engine", bs.Engine, as.Engine}, {"model", bs.Model, as.Model},
			{"reasoning_effort", bs.ReasoningEffort, as.ReasoningEffort}}
		for _, value := range values {
			key := b.ID + ":" + value[0]
			change, declared := changes[key]
			if value[1] != value[2] {
				if !declared || change.Before != value[1] || change.After != value[2] {
					return fmt.Errorf("undeclared effective drift: %s", key)
				}
				delete(changes, key)
			} else if declared {
				return fmt.Errorf("allowed change was not observed: %s", key)
			}
		}
		bs.Engine, bs.Model, bs.ReasoningEffort = as.Engine, as.Model, as.ReasoningEffort
		if bs != as {
			return fmt.Errorf("undeclared per-task execution setting drift: %s", b.ID)
		}
		br, ar := b.ExpectedRuntime, a.ExpectedRuntime
		if b.Settings.Engine != a.Settings.Engine {
			br.EngineImplementation = ar.EngineImplementation
		}
		if b.Settings.Model != a.Settings.Model {
			br.ModelVersion = ar.ModelVersion
		}
		if br != ar {
			return fmt.Errorf("unrelated expected-runtime drift: %s", b.ID)
		}
	}
	if len(changes) != 0 {
		return fmt.Errorf("allowed change addresses an unplanned task")
	}
	for i, b := range before.Identities {
		if i >= len(after.Identities) || !reflect.DeepEqual(b, after.Identities[i]) {
			return fmt.Errorf("task/fixture/instruction/grader/dependency identity drift")
		}
	}
	if len(before.Identities) != len(after.Identities) {
		return fmt.Errorf("arm identity inventory differs")
	}
	return nil
}

func ValidateUsage(u UsageAxis) error {
	tokens := u.Axis == "input_tokens" || u.Axis == "output_tokens"
	if !tokens && u.Axis != "ai_credits" && u.Axis != "provider_currency" {
		return fmt.Errorf("unknown usage axis")
	}
	if (u.Axis == "provider_currency") != (u.Currency != "") {
		return fmt.Errorf("currency is required only for provider currency")
	}
	if u.Availability == "unavailable" {
		if u.Value != nil || strings.TrimSpace(u.Reason) == "" || u.Observation != "unknown" {
			return fmt.Errorf("unavailable usage requires no value, unknown observation and a reason")
		}
		return nil
	}
	if u.Availability != "available" || u.Value == nil || u.Reason != "" ||
		(u.Observation != "partial" && u.Observation != "final_complete_attributable") {
		return fmt.Errorf("invalid conditional usage fields")
	}
	if tokens {
		s := u.Value.String()
		if s == "" || (len(s) > 1 && s[0] == '0') {
			return fmt.Errorf("tokens must be nonnegative exact integer JSON tokens")
		}
		for _, ch := range s {
			if ch < '0' || ch > '9' {
				return fmt.Errorf("tokens must be nonnegative exact integer JSON tokens")
			}
		}
		return nil
	}
	_, err := number(*u.Value, "usage", 0, math.MaxFloat64)
	return err
}
