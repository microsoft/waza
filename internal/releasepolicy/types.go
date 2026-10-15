// Package releasepolicy defines explicit local collection contracts. Digests
// establish consistency, not authenticity or independently witnessed commitment.
package releasepolicy

import (
	"encoding/json"

	"github.com/microsoft/waza/internal/models"
)

const Version = "1.0"

type Arm string

const (
	Baseline  Arm = "baseline"
	Candidate Arm = "candidate"
)

// Identity names the projection and its scope, not a captured content digest.
// not_applicable requires a verified empty-set projection, never an absent hash.
type Identity struct {
	Domain       string                 `json:"domain"`
	TaskID       string                 `json:"task_id,omitempty"`
	Availability string                 `json:"availability"`
	Digest       *models.EvidenceDigest `json:"digest,omitempty"`
	Reason       string                 `json:"reason,omitempty"`
}

type Settings struct {
	Engine               string                `json:"engine"`
	Model                string                `json:"model"`
	ReasoningEffort      string                `json:"reasoning_effort"`
	JudgeModel           string                `json:"judge_model"`
	JudgeReasoningEffort string                `json:"judge_reasoning_effort"`
	TimeoutSeconds       int                   `json:"timeout_seconds"`
	MaxAttempts          int                   `json:"max_attempts"`
	TrialsPerTask        int                   `json:"trials_per_task"`
	NoSkills             bool                  `json:"no_skills"`
	OtherSettingsDigest  models.EvidenceDigest `json:"other_settings_digest"`
}

// ExpectedRuntime is a precollection constraint, not an observed runtime claim.
// Unavailable actual identity must not be filled from the requested model name.
type ExpectedRuntime struct {
	Availability         string `json:"availability"`
	EngineImplementation string `json:"engine_implementation"`
	ModelVersion         string `json:"model_version"`
	Reason               string `json:"reason"`
}

type TaskPlan struct {
	ID              string          `json:"task_id"`
	Settings        Settings        `json:"settings"`
	ExpectedRuntime ExpectedRuntime `json:"expected_runtime"`
}

type ResolvedPlan struct {
	Kind       string     `json:"kind"`
	Version    string     `json:"version"`
	Tasks      []TaskPlan `json:"tasks"`
	Identities []Identity `json:"identities"`
}

type PolicyArm struct {
	Plan   ResolvedPlan          `json:"resolved_plan"`
	Digest models.EvidenceDigest `json:"resolved_plan_digest"`
}

type PlannedTask struct {
	ID            string      `json:"task_id"`
	Weight        json.Number `json:"weight"`
	TrialOrdinals []int       `json:"trial_ordinals"`
}

type Cluster struct {
	ID     string        `json:"cluster_id"`
	Weight json.Number   `json:"weight"`
	Tasks  []PlannedTask `json:"tasks"`
}

type Assignment struct {
	ClusterID string `json:"cluster_id"`
	Order     []Arm  `json:"arm_order"`
}

type Independence struct {
	Assessment    string   `json:"assessment"`
	Justification string   `json:"justification"`
	Limitations   []string `json:"limitations"`
}

type Allocation struct {
	Mechanism   string       `json:"mechanism"`
	Seed        string       `json:"seed"`
	Assignments []Assignment `json:"assignments"`
}

type Design struct {
	Estimand         string       `json:"estimand"`
	Endpoint         string       `json:"endpoint"`
	Alpha            json.Number  `json:"alpha"`
	FamilySize       int          `json:"comparison_family_size"`
	MaximumHalfWidth json.Number  `json:"maximum_half_width"`
	Margin           json.Number  `json:"noninferiority_margin"`
	Accept           string       `json:"accept"`
	Independence     Independence `json:"independence"`
	Clusters         []Cluster    `json:"clusters"`
	Allocation       Allocation   `json:"allocation"`
}

type AllowedChange struct {
	TaskID string `json:"task_id"`
	Field  string `json:"field"`
	Before string `json:"before"`
	After  string `json:"after"`
}

type BillingRequirement struct {
	Axis     string      `json:"axis"`
	Currency string      `json:"currency,omitempty"`
	Maximum  json.Number `json:"maximum"`
}

type Requirements struct {
	IdentityDomains []string             `json:"required_identity_domains"`
	Assurance       bool                 `json:"assurance"`
	Runtime         bool                 `json:"runtime"`
	Billing         []BillingRequirement `json:"billing"`
}

type Policy struct {
	Kind         string                `json:"kind"`
	Version      string                `json:"version"`
	Digest       models.EvidenceDigest `json:"digest"`
	Design       Design                `json:"design"`
	Arms         map[Arm]PolicyArm     `json:"arms"`
	Changes      []AllowedChange       `json:"allowed_changes"`
	GoldenIDs    []string              `json:"required_golden_task_ids"`
	GoldenRule   string                `json:"golden_rule"`
	FamilyRule   string                `json:"family_rule"`
	Requirements Requirements          `json:"requirements"`
}

type SampleKey struct {
	ClusterID string `json:"cluster_id"`
	TaskID    string `json:"task_id"`
	Trial     int    `json:"trial_ordinal"`
}

// RunNumber equals Trial. Attempt is allocated before request construction.
type AttemptKey struct {
	SampleKey
	Arm     Arm    `json:"arm"`
	EvalID  string `json:"eval_id"`
	Attempt int    `json:"attempt_ordinal"`
}

type CheckSummary struct {
	Scope            string      `json:"scope"`
	Grader           string      `json:"grader"`
	AfterTurn        int         `json:"after_turn,omitempty"`
	Passed           bool        `json:"passed"`
	Score            json.Number `json:"score"`
	OperationalState string      `json:"operational_state"`
}

// OperationalState is observed/unknown, not inferred from a failed score.
type AttemptSummary struct {
	Key        AttemptKey                 `json:"key"`
	Origin     models.EvidenceOrigin      `json:"origin"`
	Status     string                     `json:"status"`
	Category   string                     `json:"category"`
	Checks     []CheckSummary             `json:"checks"`
	Runtime    RuntimeObservation         `json:"runtime"`
	References []models.EvidenceReference `json:"evidence_references"`
}

type TrialSummary struct {
	Key       SampleKey `json:"key"`
	Terminal  int       `json:"terminal_attempt_ordinal"`
	State     string    `json:"state"`
	FirstPass *bool     `json:"first_attempt_pass"`
	RetryPass *bool     `json:"retry_policy_pass"`
}

type UsageAxis struct {
	Axis         string       `json:"axis"`
	Currency     string       `json:"currency,omitempty"`
	Availability string       `json:"availability"`
	Value        *json.Number `json:"value,omitempty"`
	Reason       string       `json:"reason,omitempty"`
	Observation  string       `json:"observation"`
}

// RuntimeObservation must satisfy this arm's constraints. Cross-arm comparison
// permits only the declared requested changes, not unrelated implementation drift.
type RuntimeObservation struct {
	TaskID               string `json:"task_id"`
	RequestedEngine      string `json:"requested_engine"`
	RequestedModel       string `json:"requested_model"`
	RequestedReasoning   string `json:"requested_reasoning"`
	Availability         string `json:"availability"`
	EngineImplementation string `json:"engine_implementation"`
	ModelVersion         string `json:"model_version"`
	Reason               string `json:"reason"`
}

// Event's shape is discriminated by Type. Admission rejects fields belonging to
// another transition, duplicate starts/terminals and noncontiguous sequences.
type Event struct {
	Sequence     int                   `json:"sequence"`
	CollectionID string                `json:"collection_id"`
	PolicyDigest models.EvidenceDigest `json:"policy_digest"`
	Type         string                `json:"type"`
	Arm          Arm                   `json:"arm,omitempty"`
	EvalID       string                `json:"eval_id,omitempty"`
	ClusterID    string                `json:"cluster_id,omitempty"`
	Key          *AttemptKey           `json:"key,omitempty"`
	Attempt      *AttemptSummary       `json:"attempt,omitempty"`
	Trial        *TrialSummary         `json:"trial,omitempty"`
	Usage        []UsageAxis           `json:"usage,omitempty"`
	Runtime      []RuntimeObservation  `json:"runtime,omitempty"`
}

type Journal struct {
	Kind         string                `json:"kind"`
	Version      string                `json:"version"`
	CollectionID string                `json:"collection_id"`
	Digest       models.EvidenceDigest `json:"digest"`
	StreamDigest models.EvidenceDigest `json:"stream_digest"`
	Events       []Event               `json:"events"`
}

// ResultBinding has no receipt/hash/path member, avoiding circular identities.
type ResultBinding struct {
	Kind         string                `json:"kind"`
	Version      string                `json:"version"`
	CollectionID string                `json:"collection_id"`
	PolicyDigest models.EvidenceDigest `json:"policy_digest"`
	Arm          Arm                   `json:"arm"`
	EvalID       string                `json:"eval_id"`
	PlanDigest   models.EvidenceDigest `json:"resolved_plan_digest"`
	Samples      []SampleKey           `json:"planned_samples"`
	Trials       []TrialSummary        `json:"trials"`
	Attempts     []AttemptSummary      `json:"attempts"`
	Usage        []UsageAxis           `json:"usage"`
}

type ActualRunRow struct {
	Origin models.EvidenceOrigin `json:"origin"`
	Run    models.RunResult      `json:"run"`
}

type Results struct {
	Kind         string         `json:"kind"`
	Version      string         `json:"version"`
	CollectionID string         `json:"collection_id"`
	Arm          Arm            `json:"arm"`
	EvalID       string         `json:"eval_id"`
	Rows         []ActualRunRow `json:"rows"`
}

type Receipt struct {
	Kind          string                 `json:"kind"`
	Version       string                 `json:"version"`
	Digest        models.EvidenceDigest  `json:"digest"`
	CollectionID  string                 `json:"collection_id"`
	PolicyDigest  models.EvidenceDigest  `json:"policy_digest"`
	Arm           Arm                    `json:"arm"`
	EvalID        string                 `json:"eval_id"`
	PlanDigest    models.EvidenceDigest  `json:"resolved_plan_digest"`
	State         string                 `json:"state"`
	Samples       []SampleKey            `json:"planned_samples"`
	Started       []SampleKey            `json:"started_samples"`
	Attempts      []AttemptSummary       `json:"attempts"`
	Trials        []TrialSummary         `json:"trials"`
	Runtime       []RuntimeObservation   `json:"runtime"`
	Usage         []UsageAxis            `json:"usage"`
	JournalDigest *models.EvidenceDigest `json:"journal_digest,omitempty"`
	JournalCount  int                    `json:"journal_count"`
	Binding       *ResultBinding         `json:"result_binding,omitempty"`
	BindingDigest *models.EvidenceDigest `json:"result_binding_digest,omitempty"`
}
