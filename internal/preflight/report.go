// Package preflight inspects local eval prerequisites without executing them.
package preflight

import "github.com/microsoft/waza/internal/models"

const ReportKind = "waza.preflight"

type State string

const (
	Verified    State = "verified"
	Unresolved  State = "unresolved"
	Unsupported State = "unsupported"
	Invalid     State = "invalid"
)

type Diagnostic struct {
	Code          string `json:"code"`
	State         State  `json:"state"`
	Severity      string `json:"severity"`
	Source        string `json:"source"`
	TaskID        string `json:"task_id,omitempty"`
	RequirementID string `json:"requirement_id,omitempty"`
	Message       string `json:"message"`
	Remediation   string `json:"remediation"`
}

type Capability struct {
	Name    string `json:"name"`
	State   State  `json:"state"`
	Meaning string `json:"meaning"`
}

type Dependency struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Mode   string `json:"mode"`
	State  State  `json:"state"`
	TaskID string `json:"task_id,omitempty"`
}

type RequirementPlan struct {
	models.Requirement
	State   State  `json:"state"`
	Meaning string `json:"meaning"`
}

type TaskPlan struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Source       string            `json:"source"`
	Enabled      bool              `json:"enabled"`
	ContextDir   string            `json:"context_dir"`
	Requirements []RequirementPlan `json:"requirements"`
}

type Report struct {
	Kind          string       `json:"kind"`
	SchemaVersion string       `json:"schemaVersion"`
	Source        string       `json:"source"`
	Executor      string       `json:"executor"`
	Complete      bool         `json:"complete"`
	Tasks         []TaskPlan   `json:"tasks"`
	Capabilities  []Capability `json:"capabilities"`
	Dependencies  []Dependency `json:"dependencies"`
	Diagnostics   []Diagnostic `json:"diagnostics"`
}

// Failed applies only to preflight. It does not change evaluation exit policy.
func (r *Report) Failed(strict bool) bool {
	for _, d := range r.Diagnostics {
		if d.State == Invalid || strict && (d.State == Unresolved || d.State == Unsupported) {
			return true
		}
	}
	return false
}

func (r *Report) add(code string, state State, source, task, requirement, message, remediation string) {
	severity := "info"
	if state == Invalid {
		severity = "error"
	} else if state != Verified {
		severity = "warning"
	}
	r.Diagnostics = append(r.Diagnostics, Diagnostic{
		Code: code, State: state, Severity: severity, Source: source, TaskID: task,
		RequirementID: requirement, Message: message, Remediation: remediation,
	})
}
