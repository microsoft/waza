// Package nativetask contains engine-free internal draft protocol primitives.
// It does not resolve or execute task sources, register a public profile, or
// certify the authenticity, accounting, runtime or assurance of supplied data.
package nativetask

import (
	"encoding/json"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/releasepolicy"
)

const (
	profileKind = "waza.release-native-task-profile"
	recordKind  = "waza.release-native-task-attempt"
	eventKind   = "waza.release-native-task-event"
	version     = "1.0"
)

type Selection struct {
	Operation string `json:"operation"`
	Version   string `json:"version"`
}

type RequestPlan struct {
	TaskID        string                `json:"task_id"`
	RequestDigest models.EvidenceDigest `json:"request_digest"`
}

type ArmPlan struct {
	PlanDigest models.EvidenceDigest `json:"resolved_plan_digest"`
	Requests   []RequestPlan         `json:"requests"`
}

type Profile struct {
	Kind           string                        `json:"kind"`
	Version        string                        `json:"version"`
	Digest         models.EvidenceDigest         `json:"digest"`
	Nonce          string                        `json:"nonce"`
	Selection      Selection                     `json:"selection"`
	PolicyDigest   models.EvidenceDigest         `json:"policy_digest"`
	PermissionMode string                        `json:"permission_mode"`
	Arms           map[releasepolicy.Arm]ArmPlan `json:"arms"`
}

type Admission struct {
	CollectionID  string                   `json:"collection_id"`
	PolicyDigest  models.EvidenceDigest    `json:"policy_digest"`
	ProfileDigest models.EvidenceDigest    `json:"profile_digest"`
	PlanDigest    models.EvidenceDigest    `json:"resolved_plan_digest"`
	RequestDigest models.EvidenceDigest    `json:"request_digest"`
	Key           releasepolicy.AttemptKey `json:"key"`
}

type Phase struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

type Lifecycle struct {
	Construction Phase `json:"construction"`
	Initialize   Phase `json:"initialize"`
	Execute      Phase `json:"execute"`
	Grade        Phase `json:"grade"`
	Shutdown     Phase `json:"shutdown"`
}

type Message struct {
	EventOrdinal    int    `json:"event_ordinal"`
	Content         string `json:"content"`
	ContentPresence string `json:"content_presence"`
}

type Output struct {
	Availability string    `json:"availability"`
	Value        *string   `json:"value,omitempty"`
	Messages     []Message `json:"messages"`
	Reason       string    `json:"reason,omitempty"`
}

type Response struct {
	SessionKey        *models.EvidenceDigest    `json:"session_key"`
	SessionReason     string                    `json:"session_reason,omitempty"`
	Success           bool                      `json:"success"`
	ErrorMsg          string                    `json:"error_msg"`
	FinalOutput       string                    `json:"final_output"`
	DurationMS        int64                     `json:"duration_ms"`
	CanonicalEvents   []json.RawMessage         `json:"canonical_events"`
	ToolPolicyMode    string                    `json:"tool_policy_mode"`
	ToolPolicyDenials []models.ToolPolicyDenial `json:"tool_policy_denials"`
	UsageIsCumulative bool                      `json:"usage_is_cumulative"`
	UsageRevision     uint64                    `json:"usage_revision"`
	Usage             *models.UsageStats        `json:"usage"`
}

// Stage and Code are sanitized string projections, not imported native hooks.
type Diagnostic struct {
	Stage      string                 `json:"stage"`
	Code       string                 `json:"code"`
	SessionKey *models.EvidenceDigest `json:"session_key"`
}

type EventModels struct {
	Models         []string `json:"models"`
	EventsObserved uint64   `json:"events_observed"`
	Complete       bool     `json:"complete"`
}

type Accounting struct {
	SessionKey               *models.EvidenceDigest `json:"session_key"`
	Source                   string                 `json:"source"`
	Complete                 bool                   `json:"complete"`
	ModelAttributionComplete bool                   `json:"model_attribution_complete"`
	Models                   []string               `json:"models"`
	Usage                    *models.UsageStats     `json:"usage"`
	ProviderCurrencyState    string                 `json:"provider_currency_state"`
	Reason                   string                 `json:"reason,omitempty"`
}

type Record struct {
	Kind          string                       `json:"kind"`
	Version       string                       `json:"version"`
	CollectionID  string                       `json:"collection_id"`
	PolicyDigest  models.EvidenceDigest        `json:"policy_digest"`
	ProfileDigest models.EvidenceDigest        `json:"profile_digest"`
	PlanDigest    models.EvidenceDigest        `json:"resolved_plan_digest"`
	RequestDigest models.EvidenceDigest        `json:"request_digest"`
	Key           releasepolicy.AttemptKey     `json:"key"`
	Origin        models.EvidenceOrigin        `json:"origin"`
	Lifecycle     Lifecycle                    `json:"lifecycle"`
	Response      *Response                    `json:"response"`
	Output        Output                       `json:"output"`
	Diagnostics   []Diagnostic                 `json:"diagnostics"`
	EventModels   EventModels                  `json:"event_models"`
	Accounting    Accounting                   `json:"accounting"`
	ActualRow     releasepolicy.ActualRunRow   `json:"actual_row"`
	Summary       releasepolicy.AttemptSummary `json:"summary"`
}

type Event struct {
	Kind         string                 `json:"kind"`
	Version      string                 `json:"version"`
	Sequence     int                    `json:"sequence"`
	Type         string                 `json:"type"`
	Admission    Admission              `json:"admission"`
	State        string                 `json:"state,omitempty"`
	RecordDigest *models.EvidenceDigest `json:"record_digest,omitempty"`
	RowDigest    *models.EvidenceDigest `json:"row_digest,omitempty"`
}

// Capabilities describe this draft reader, independently of legacy row defaults
// and received model-attribution completeness. None is a producer attestation.
type Capabilities struct {
	FullToolTape, Session, RuntimeVersion, ProviderCurrency, Assurance string
}

func (*Record) Capabilities() Capabilities {
	return Capabilities{"not_assessed", "not_assessed", "not_assessed", "not_assessed", "not_assessed"}
}
