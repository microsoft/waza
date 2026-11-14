// Package executionbudget declares runner resource budgets, reports which of
// them this host can enforce or measure, and meters one selected attempt.
// It never claims enforcement the host cannot apply: unsupported limits are
// rejected before execution or reported explicitly as unenforced.
package executionbudget

import (
	"fmt"
	"runtime"
	"strings"
)

const (
	Version          = "1.0"
	CapabilitiesKind = "waza.execution-budget-capabilities"

	PolicyReject = "reject"
	PolicyReport = "report"

	Enforced    = "enforced"
	Measured    = "measured"
	Unsupported = "unsupported"

	ScopeAttempt       = "attempt"
	ScopeRunnerProcess = "runner_process"

	StateRejectedUnsupported = "rejected_unsupported"
	StateNotStarted          = "not_started"
	StateWithinBudget        = "within_budget"
	StateExceeded            = "exceeded"
	StateTerminated          = "terminated"
)

type Resource string

const (
	WallTime     Resource = "wall_time_ms"
	ModelTurns   Resource = "model_turns"
	ToolCalls    Resource = "tool_calls"
	CPUTime      Resource = "cpu_time_ms"
	Memory       Resource = "memory_bytes"
	Disk         Resource = "disk_bytes"
	ProcessCount Resource = "process_count"
)

// Resources is the stable report order.
var Resources = []Resource{WallTime, ModelTurns, ToolCalls, CPUTime, Memory, Disk, ProcessCount}

// Limits are upper bounds; nil means the resource is not limited.
type Limits struct {
	WallTimeMS   *int64 `json:"wall_time_ms,omitempty"`
	ModelTurns   *int64 `json:"model_turns,omitempty"`
	ToolCalls    *int64 `json:"tool_calls,omitempty"`
	CPUTimeMS    *int64 `json:"cpu_time_ms,omitempty"`
	MemoryBytes  *int64 `json:"memory_bytes,omitempty"`
	DiskBytes    *int64 `json:"disk_bytes,omitempty"`
	ProcessCount *int64 `json:"process_count,omitempty"`
}

func (l Limits) field(resource Resource) *int64 {
	switch resource {
	case WallTime:
		return l.WallTimeMS
	case ModelTurns:
		return l.ModelTurns
	case ToolCalls:
		return l.ToolCalls
	case CPUTime:
		return l.CPUTimeMS
	case Memory:
		return l.MemoryBytes
	case Disk:
		return l.DiskBytes
	case ProcessCount:
		return l.ProcessCount
	}
	return nil
}

// Get returns the configured limit for resource.
func (l Limits) Get(resource Resource) (int64, bool) {
	if value := l.field(resource); value != nil {
		return *value, true
	}
	return 0, false
}

// Request is the optional runner-request budgets field.
type Request struct {
	Limits      Limits `json:"limits"`
	Unsupported string `json:"unsupported,omitempty"`
}

// Policy returns the explicit or default unsupported-limit policy.
func (r Request) Policy() string {
	if r.Unsupported == "" {
		return PolicyReject
	}
	return r.Unsupported
}

type Platform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

// Capability states whether this host enforces and measures one resource.
type Capability struct {
	Resource    Resource `json:"resource"`
	Enforcement string   `json:"enforcement"`
	Measurement string   `json:"measurement"`
	Scope       string   `json:"scope,omitempty"`
	Detail      string   `json:"detail"`
}

type Capabilities struct {
	Kind          string       `json:"kind"`
	SchemaVersion string       `json:"schema_version"`
	Platform      Platform     `json:"platform"`
	Resources     []Capability `json:"resources"`
}

// Usage is one resource row of a terminal budget report.
type Usage struct {
	Capability
	Limit     *int64 `json:"limit,omitempty"`
	Used      *int64 `json:"used,omitempty"`
	Remaining *int64 `json:"remaining,omitempty"`
	Exceeded  *bool  `json:"exceeded,omitempty"`
}

// Termination records the enforced limit that stopped the attempt.
type Termination struct {
	Resource Resource `json:"resource"`
	Limit    int64    `json:"limit"`
	Observed int64    `json:"observed"`
}

// Report is the terminal budget evidence for one selected attempt. It carries
// no identity; correlate it with the enclosing terminal identity/selection.
type Report struct {
	SchemaVersion string       `json:"schema_version"`
	Platform      Platform     `json:"platform"`
	Unsupported   string       `json:"unsupported"`
	State         string       `json:"state"`
	Resources     []Usage      `json:"resources"`
	Termination   *Termination `json:"termination,omitempty"`
}

// HostCapabilities reports the budget capability matrix for this OS.
func HostCapabilities() Capabilities {
	return capabilitiesFor(runtime.GOOS, runtime.GOARCH, hostMeasurement())
}

// measurementSupport describes which runner-process counters the OS exposes.
type measurementSupport struct {
	cpu, memory           bool
	cpuDetail, memoryNote string
}

func capabilitiesFor(goos, goarch string, support measurementSupport) Capabilities {
	notOwned := "not enforced: the runner does not own an OS quota (cgroup, job object or rlimit) for the runtime process tree"
	rows := []Capability{
		{Resource: WallTime, Enforcement: Enforced, Measurement: Measured, Scope: ScopeAttempt,
			Detail: "enforced by an attempt deadline covering startup, execution, grading and shutdown; exceeding it ends the attempt with outcome timeout"},
		{Resource: ModelTurns, Enforcement: Enforced, Measurement: Measured, Scope: ScopeAttempt,
			Detail: "counted from each observed model turn start; the first turn beyond the limit cancels the attempt (outcome cancelled)"}, //nolint:misspell // wire value
		{Resource: ToolCalls, Enforcement: Enforced, Measurement: Measured, Scope: ScopeAttempt,
			Detail: "counted from each observed tool execution start; the first call beyond the limit cancels the attempt, and that call may already have begun"},
		{Resource: CPUTime, Enforcement: Unsupported, Measurement: Unsupported, Detail: notOwned + "; CPU time is not measurable on " + goos},
		{Resource: Memory, Enforcement: Unsupported, Measurement: Unsupported, Detail: notOwned + "; peak memory is not measurable on " + goos},
		{Resource: Disk, Enforcement: Unsupported, Measurement: Unsupported,
			Detail: "not enforced or measured: inline runner profiles declare no workspace quota and the host exposes no per-attempt disk accounting"},
		{Resource: ProcessCount, Enforcement: Unsupported, Measurement: Unsupported,
			Detail: "not enforced or measured: runtimes may spawn subprocesses outside a runner-owned process group or job object"},
	}
	if support.cpu {
		rows[3].Measurement, rows[3].Scope, rows[3].Detail = Measured, ScopeRunnerProcess, notOwned+"; "+support.cpuDetail
	}
	if support.memory {
		rows[4].Measurement, rows[4].Scope, rows[4].Detail = Measured, ScopeRunnerProcess, notOwned+"; "+support.memoryNote
	}
	return Capabilities{Kind: CapabilitiesKind, SchemaVersion: Version, Platform: Platform{OS: goos, Arch: goarch}, Resources: rows}
}

// Plan checks request against capabilities before execution. It returns the
// initial report and one message per declared limit the host cannot enforce.
func Plan(request Request, capabilities Capabilities) (*Report, []string) {
	report := &Report{
		SchemaVersion: Version, Platform: capabilities.Platform, Unsupported: request.Policy(),
		State: StateNotStarted, Resources: make([]Usage, 0, len(capabilities.Resources)),
	}
	var unsupported []string
	for _, capability := range capabilities.Resources {
		row := Usage{Capability: capability}
		if limit, ok := request.Limits.Get(capability.Resource); ok {
			row.Limit = &limit
			if capability.Enforcement != Enforced {
				unsupported = append(unsupported, fmt.Sprintf("%s limit %d cannot be enforced on %s/%s (%s)",
					capability.Resource, limit, capabilities.Platform.OS, capabilities.Platform.Arch, capability.Detail))
			}
		}
		report.Resources = append(report.Resources, row)
	}
	if len(unsupported) != 0 && report.Unsupported == PolicyReject {
		report.State = StateRejectedUnsupported
	}
	return report, unsupported
}

// Summary is a bounded one-line description of unsupported limits.
func Summary(unsupported []string) string {
	return "unsupported execution budget: " + strings.Join(unsupported, "; ") +
		`; remove the limit, run on a host that enforces it, or set budgets.unsupported to "report"`
}
