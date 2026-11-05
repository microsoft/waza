package releasepolicy

import (
	"github.com/microsoft/waza/internal/assurance"
	"github.com/microsoft/waza/internal/models"
)

const (
	AssuranceContractKind = "waza.release-assurance-contract"
	AssuranceLedgerKind   = "waza.release-assurance-ledger"
	AssuranceRowKind      = "waza.release-assurance-attempt"
	AssuredDecisionKind   = "waza.release-assured-decision"
	AssuranceVersion      = "1.0"
)

// These independent profiles do not change the base release protocol.
type AssuranceContract struct {
	Kind         string                `json:"kind"`
	Version      string                `json:"version"`
	Nonce        string                `json:"nonce"`
	PolicyDigest models.EvidenceDigest `json:"policy_digest"`
	Arms         map[Arm]AssuranceArm  `json:"arms"`
	Digest       models.EvidenceDigest `json:"digest"`
}

type AssuranceArm struct {
	Mode                 string                 `json:"mode"`
	PlanDigest           models.EvidenceDigest  `json:"resolved_plan_digest"`
	EvalSourceDigest     models.EvidenceDigest  `json:"eval_source_digest"`
	ResolvedConfigDigest models.EvidenceDigest  `json:"resolved_config_digest"`
	ExecutableDigest     models.EvidenceDigest  `json:"executable_digest"`
	Tasks                []AssuranceTask        `json:"tasks"`
	Checks               []AssuranceCheck       `json:"checks"`
	LabelsDigest         models.EvidenceDigest  `json:"labels_digest"`
	ReviewDigest         *models.EvidenceDigest `json:"review_digest,omitempty"`
	ReviewSourceID       string                 `json:"review_source_id"`
	Inputs               []AssuranceInput       `json:"inputs"`
}

type AssuranceTask struct {
	TaskID string                `json:"task_id"`
	Digest models.EvidenceDigest `json:"declaration_digest"`
}

type AssuranceCheck struct {
	TaskID        string                  `json:"task_id"`
	RequirementID string                  `json:"requirement_id"`
	Check         models.RequirementCheck `json:"check"`
	Declaration   models.EvidenceDigest   `json:"declaration_digest"`
	ValidationKey string                  `json:"validation_key"`
}

type AssuranceInput struct {
	Path   string                `json:"path"`
	Digest models.EvidenceDigest `json:"source_digest"`
}

// Available empty output has a present value:""; unavailable has no value.
// Only an actual execution response may supply an available value.
type AssuranceOutput struct {
	Availability string  `json:"availability"`
	Value        *string `json:"value,omitempty"`
	Reason       string  `json:"reason,omitempty"`
}

type AssuranceRow struct {
	Kind         string                `json:"kind"`
	Version      string                `json:"version"`
	Sequence     int                   `json:"sequence"`
	CollectionID string                `json:"collection_id"`
	Key          AttemptKey            `json:"key"`
	Origin       models.EvidenceOrigin `json:"origin"`
	RowDigest    models.EvidenceDigest `json:"row_digest"`
	Output       AssuranceOutput       `json:"output"`
}

type AssuranceLedger struct {
	Kind              string                        `json:"kind"`
	Version           string                        `json:"version"`
	CollectionID      string                        `json:"collection_id"`
	ContractDigest    models.EvidenceDigest         `json:"contract_digest"`
	Rows              []AssuranceRow                `json:"rows"`
	CoreJournalDigest models.EvidenceDigest         `json:"core_journal_digest"`
	RawResults        map[Arm]models.EvidenceDigest `json:"raw_result_digests"`
	StreamDigest      models.EvidenceDigest         `json:"stream_digest"`
	Digest            models.EvidenceDigest         `json:"digest"`
}

type AssuredDecision struct {
	Kind           string                    `json:"kind"`
	Version        string                    `json:"version"`
	Accepted       bool                      `json:"accepted"`
	ContractDigest models.EvidenceDigest     `json:"contract_digest"`
	CollectionID   string                    `json:"collection_id"`
	BaseDecision   Decision                  `json:"base_decision"`
	Assurance      Dimension                 `json:"assurance"`
	Regrade        Dimension                 `json:"regrade"`
	Reports        map[Arm]*assurance.Report `json:"reports"`
	Limitations    []string                  `json:"limitations"`
}
