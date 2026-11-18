// Package optimizer defines the versioned boundary between external optimizers
// and the Waza evaluation harness. Adapters only propose candidates; Waza
// executes, grades, records evidence and decides acceptance through the
// existing orchestration runner.
package optimizer

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
)

const (
	// ContractVersion is the only optimizer contract version this build speaks.
	ContractVersion = "1.0"

	CapabilitiesKind    = "waza.optimizer-capabilities"
	ReportKind          = "waza.optimizer-report"
	AdapterRequestKind  = "waza.optimizer-adapter-request"
	AdapterResponseKind = "waza.optimizer-adapter-response"
)

// Harness capabilities. Names are exact and case-sensitive.
const (
	CapProposeFiles          = "propose.files"
	CapTargetSkillDirectory  = "target.skill-directory"
	CapTargetContextOverlay  = "target.context-overlay"
	CapFeedbackOptimization  = "feedback.optimization-split"
	CapFeedbackTaskResults   = "feedback.task-results"
	CapBudgetIterations      = "budget.iterations"
	CapBudgetEvaluations     = "budget.evaluations"
	CapBudgetDeadline        = "budget.deadline"
	CapAdapterUsageReporting = "usage.adapter-reported"
)

// Target selects how a candidate's files reach the harness.
type Target string

const (
	// TargetSkillDirectory materializes candidate files as the only skill
	// directory for the evaluation (SKILL.md is required).
	TargetSkillDirectory Target = "skill-directory"
	// TargetContextOverlay copies the pinned fixture directory and overlays
	// candidate files on the copy.
	TargetContextOverlay Target = "context-overlay"
)

func (t Target) capability() string { return "target." + string(t) }

// ParseTarget validates a target name.
func ParseTarget(s string) (Target, error) {
	switch Target(s) {
	case TargetSkillDirectory, TargetContextOverlay:
		return Target(s), nil
	}
	return "", fmt.Errorf("unknown optimizer target %q (use %q or %q)", s, TargetSkillDirectory, TargetContextOverlay)
}

var harnessCapabilities = []string{
	CapBudgetDeadline,
	CapBudgetEvaluations,
	CapBudgetIterations,
	CapFeedbackOptimization,
	CapFeedbackTaskResults,
	CapProposeFiles,
	CapTargetContextOverlay,
	CapTargetSkillDirectory,
	CapAdapterUsageReporting,
}

// CapabilityDocument is printed by `waza optimize capabilities`.
type CapabilityDocument struct {
	Kind          string   `json:"kind"`
	SchemaVersion string   `json:"schema_version"`
	Versions      []string `json:"versions"`
	Capabilities  []string `json:"capabilities"`
}

// Capabilities returns the harness side of the negotiation.
func Capabilities() CapabilityDocument {
	caps := slices.Clone(harnessCapabilities)
	sort.Strings(caps)
	return CapabilityDocument{
		Kind:          CapabilitiesKind,
		SchemaVersion: ContractVersion,
		Versions:      []string{ContractVersion},
		Capabilities:  caps,
	}
}

// AdapterInfo is returned by Adapter.Describe.
type AdapterInfo struct {
	Name                 string   `json:"name"`
	Version              string   `json:"version"`
	ContractVersions     []string `json:"contract_versions"`
	RequiredCapabilities []string `json:"required_capabilities"`
	OptionalCapabilities []string `json:"optional_capabilities"`
	// PaidCalls is the adapter's own declaration. Waza cannot observe calls an
	// external process makes, so consent rules do not rely on it alone.
	PaidCalls bool `json:"paid_calls"`
}

// Negotiation is the recorded outcome of Negotiate.
type Negotiation struct {
	Version                         string   `json:"version"`
	Target                          Target   `json:"target"`
	SelectedCapabilities            []string `json:"selected_capabilities"`
	UnavailableOptionalCapabilities []string `json:"unavailable_optional_capabilities"`
}

// Failure categories recorded in reports and returned as *Error.
const (
	CategoryUnsupportedVersion    = "unsupported_version"
	CategoryIncompatible          = "incompatible_capability"
	CategoryInvalidSuite          = "invalid_suite"
	CategoryMalformedProposal     = "malformed_proposal"
	CategoryUnsafeCandidatePath   = "unsafe_candidate_path"
	CategoryOptimizerFailure      = "optimizer_failure"
	CategoryInfrastructureFailure = "infrastructure_failure"
	CategoryMissingEvidence       = "missing_evidence"
	CategorySuiteChanged          = "suite_changed"
	CategoryCancelled             = "cancelled" //nolint:misspell // Stable external spelling.
	CategoryTimeout               = "timeout"
	CategoryConsentRequired       = "consent_required"
	CategoryInvalidConfig         = "invalid_config"
)

// Error is a categorized optimizer failure.
type Error struct {
	Category string
	Message  string
	Err      error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Category, e.Message, e.Err)
	}
	return e.Category + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.Err }

func newError(category, format string, args ...any) *Error {
	return &Error{Category: category, Message: fmt.Sprintf(format, args...)}
}

func wrapError(category string, err error, format string, args ...any) *Error {
	return &Error{Category: category, Message: fmt.Sprintf(format, args...), Err: err}
}

// CategoryOf returns the category of err, or "" when err is not an *Error.
func CategoryOf(err error) string {
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Category
	}
	return ""
}

// Negotiate selects the contract version and capabilities for an adapter and
// target, failing before any evaluation when they are incompatible.
func Negotiate(info AdapterInfo, target Target) (Negotiation, error) {
	if !slices.Contains(info.ContractVersions, ContractVersion) {
		return Negotiation{}, newError(CategoryUnsupportedVersion,
			"adapter %q offers contract versions %v; this harness supports only %q",
			info.Name, info.ContractVersions, ContractVersion)
	}
	for _, name := range info.OptionalCapabilities {
		if slices.Contains(info.RequiredCapabilities, name) {
			return Negotiation{}, newError(CategoryIncompatible, "capability %q is both required and optional", name)
		}
	}
	targetCap := target.capability()
	if !slices.Contains(harnessCapabilities, targetCap) {
		return Negotiation{}, newError(CategoryIncompatible, "harness does not offer target %q", target)
	}
	if !slices.Contains(info.RequiredCapabilities, targetCap) && !slices.Contains(info.OptionalCapabilities, targetCap) {
		return Negotiation{}, newError(CategoryIncompatible,
			"adapter %q does not support target %q; it must declare %q", info.Name, target, targetCap)
	}
	var missing []string
	for _, name := range info.RequiredCapabilities {
		if strings.HasPrefix(name, "target.") && name != targetCap {
			missing = append(missing, name)
			continue
		}
		if !slices.Contains(harnessCapabilities, name) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return Negotiation{}, newError(CategoryIncompatible,
			"adapter %q requires capabilities unavailable for target %q: %v; run `waza optimize capabilities`",
			info.Name, target, missing)
	}
	selected := []string{}
	unavailable := []string{}
	for _, name := range slices.Concat(info.RequiredCapabilities, info.OptionalCapabilities) {
		switch {
		case strings.HasPrefix(name, "target.") && name != targetCap:
			continue
		case slices.Contains(harnessCapabilities, name):
			if !slices.Contains(selected, name) {
				selected = append(selected, name)
			}
		case !slices.Contains(unavailable, name):
			unavailable = append(unavailable, name)
		}
	}
	sort.Strings(selected)
	sort.Strings(unavailable)
	return Negotiation{
		Version:                         ContractVersion,
		Target:                          target,
		SelectedCapabilities:            selected,
		UnavailableOptionalCapabilities: unavailable,
	}, nil
}
