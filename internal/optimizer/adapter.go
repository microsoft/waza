package optimizer

import (
	"context"
)

// Adapter is the optimizer port. Implementations propose candidates only;
// they never execute or grade them.
type Adapter interface {
	Describe(ctx context.Context) (AdapterInfo, error)
	Propose(ctx context.Context, req ProposeRequest) (ProposeResponse, error)
}

// Operations carried in adapter requests.
const (
	OperationDescribe = "describe"
	OperationPropose  = "propose"
)

// SplitOptimization and SplitQualification name the two disjoint task splits.
const (
	SplitOptimization  = "optimization"
	SplitQualification = "qualification"
)

// ParentCandidate is the incumbent candidate an adapter may improve.
type ParentCandidate struct {
	ID    string        `json:"id"`
	Score *float64      `json:"score"`
	Files []FileContent `json:"files"`
}

// Remaining describes budget left for optimization (qualification calls are
// reserved by Waza and never exposed).
type Remaining struct {
	Iterations  int `json:"iterations"`
	Evaluations int `json:"evaluations"`
	// DeadlineUnixMs is 0 when no deadline is set.
	DeadlineUnixMs int64 `json:"deadline_unix_ms"`
}

// GraderFeedback is one failed grader's bounded message.
type GraderFeedback struct {
	Name    string `json:"name"`
	Message string `json:"message"`
}

// TaskFeedback summarizes one optimization-split task.
type TaskFeedback struct {
	TestID        string           `json:"test_id"`
	Status        string           `json:"status"`
	Score         float64          `json:"score"`
	FailedGraders []GraderFeedback `json:"failed_graders"`
}

// Feedback is the only evaluation information adapters receive. It is built
// exclusively from optimization-split evaluations.
type Feedback struct {
	CandidateID string         `json:"candidate_id"`
	ParentID    string         `json:"parent_id,omitempty"`
	Iteration   int            `json:"iteration"`
	Split       string         `json:"split"`
	State       string         `json:"state"`
	Score       *float64       `json:"score"`
	PassRate    *float64       `json:"pass_rate"`
	Tasks       []TaskFeedback `json:"tasks"`
}

// ProposeRequest is sent to Adapter.Propose.
type ProposeRequest struct {
	Kind            string          `json:"kind"`
	ContractVersion string          `json:"contract_version"`
	Operation       string          `json:"operation"`
	Iteration       int             `json:"iteration"`
	Target          Target          `json:"target"`
	Parent          ParentCandidate `json:"parent"`
	History         []Feedback      `json:"history"`
	Remaining       Remaining       `json:"remaining"`
}

// ProposedCandidate is a full replacement file set derived from ParentID.
type ProposedCandidate struct {
	ParentID  string        `json:"parent_id"`
	Files     []FileContent `json:"files"`
	Rationale string        `json:"rationale,omitempty"`
}

// Usage is adapter-reported provider usage. Absent usage is unavailable,
// never zero.
type Usage struct {
	InputTokens     int      `json:"input_tokens"`
	OutputTokens    int      `json:"output_tokens"`
	PremiumRequests float64  `json:"premium_requests"`
	AICredits       *float64 `json:"ai_credits,omitempty"`
}

// ProposeResponse returns zero or one candidate. Done=true without a
// candidate ends the loop; neither is malformed.
type ProposeResponse struct {
	Kind            string             `json:"kind"`
	ContractVersion string             `json:"contract_version"`
	Candidate       *ProposedCandidate `json:"candidate,omitempty"`
	Done            bool               `json:"done,omitempty"`
	Usage           *Usage             `json:"usage,omitempty"`
}
