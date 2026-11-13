package executionisolation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ProviderStatus says whether a capability provider is present on this host.
type ProviderStatus string

const (
	ProviderAvailable   ProviderStatus = "available"
	ProviderAbsent      ProviderStatus = "absent"
	ProviderUnavailable ProviderStatus = "unavailable"
)

// Outcome is the result of one conformance probe.
type Outcome string

const (
	// Enforced means the attempted violation was blocked by the boundary.
	Enforced Outcome = "enforced"
	// Violated means the attempted violation succeeded.
	Violated Outcome = "violated"
	// Inconclusive means the probe could not tell a block from another failure.
	Inconclusive Outcome = "inconclusive"
)

// ProbeResult is the evidence from one attempted violation. Details never
// contain secret values.
type ProbeResult struct {
	Outcome Outcome
	Detail  string
}

// Claim is a provider's assertion that it enforces one dimension. A claim
// without a probe cannot be advertised.
type Claim struct {
	Dimension Dimension
	Probe     func(context.Context) ProbeResult
}

// Discovery is what a provider reports about itself on this host.
type Discovery struct {
	Status     ProviderStatus
	Reason     string
	EnforcedBy string
	Claims     []Claim
}

// Provider is a host or runtime component that may enforce boundaries. Waza
// discovers providers and probes their claims; it never enforces for them.
type Provider interface {
	Name() string
	Discover(context.Context) Discovery
}

// ProviderReport is the reported state of one provider.
type ProviderReport struct {
	Name       string         `json:"name"`
	Status     ProviderStatus `json:"status"`
	Reason     string         `json:"reason,omitempty"`
	EnforcedBy string         `json:"enforced_by,omitempty"`
}

// ProbeReport records one probe outcome.
type ProbeReport struct {
	Provider string  `json:"provider"`
	Outcome  Outcome `json:"outcome"`
	Detail   string  `json:"detail"`
}

// Diagnostic explains why a dimension is not available.
type Diagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Diagnostic codes.
const (
	CodeNoProvider        = "no_provider"
	CodeProbeViolated     = "probe_violated"
	CodeProbeInconclusive = "probe_inconclusive"
)

// Effective is the posture in force for one dimension after preflight.
type Effective string

const (
	EffectiveUnrestricted Effective = "unrestricted"
	EffectiveRestricted   Effective = "restricted"
	EffectiveUnavailable  Effective = "unavailable"
)

// Capability is the requested, available and effective state of a dimension.
type Capability struct {
	Dimension         Dimension     `json:"dimension"`
	Requested         Posture       `json:"requested"`
	Available         bool          `json:"available"`
	Effective         Effective     `json:"effective"`
	EnforcementSource string        `json:"enforcement_source,omitempty"`
	Probes            []ProbeReport `json:"probes"`
	Diagnostic        *Diagnostic   `json:"diagnostic,omitempty"`
}

// Decision is the preflight admission outcome.
type Decision string

const (
	Admitted Decision = "admitted"
	Rejected Decision = "rejected"
)

// Report is the waza.execution-isolation-preflight document.
type Report struct {
	Kind          string           `json:"kind"`
	SchemaVersion string           `json:"schema_version"`
	CheckedAt     time.Time        `json:"checked_at"`
	Decision      Decision         `json:"decision"`
	Providers     []ProviderReport `json:"providers"`
	Dimensions    []Capability     `json:"dimensions"`
}

// RejectedError is returned when a requested boundary is unavailable.
type RejectedError struct {
	Report *Report
}

func (e *RejectedError) Error() string {
	var unavailable []string
	for _, capability := range e.Report.Dimensions {
		if capability.Effective == EffectiveUnavailable {
			unavailable = append(unavailable, fmt.Sprintf("%s (%s)", capability.Dimension, capability.Diagnostic.Code))
		}
	}
	return "isolation preflight rejected before execution; unavailable: " + strings.Join(unavailable, ", ")
}

// Admit returns a *RejectedError when the report rejects execution.
func (r *Report) Admit() error {
	if r.Decision == Rejected {
		return &RejectedError{Report: r}
	}
	return nil
}

const maxText = 512

func clip(text string) string {
	if len(text) <= maxText {
		return text
	}
	return text[:maxText-3] + "..."
}

type probedClaim struct {
	provider string
	result   ProbeResult
}

// Preflight discovers providers, probes every advertised claim and matches
// the results against the request. It never starts execution and never
// enforces a boundary itself.
func Preflight(ctx context.Context, request Request, providers []Provider, now time.Time) *Report {
	report := &Report{Kind: PreflightKind, SchemaVersion: Version, CheckedAt: now.UTC().Truncate(time.Second), Decision: Admitted}
	probes := map[Dimension][]probedClaim{}
	for _, provider := range providers {
		discovery := provider.Discover(ctx)
		entry := ProviderReport{Name: provider.Name(), Status: discovery.Status}
		if discovery.Status == ProviderAvailable {
			entry.EnforcedBy = clip(discovery.EnforcedBy)
		} else {
			entry.Reason = clip(discovery.Reason)
			if entry.Reason == "" {
				entry.Reason = "provider reported no reason"
			}
		}
		report.Providers = append(report.Providers, entry)
		if discovery.Status != ProviderAvailable {
			continue
		}
		for _, claim := range discovery.Claims {
			result := ProbeResult{Outcome: Inconclusive, Detail: "claim has no conformance probe and cannot be advertised"}
			if claim.Probe != nil {
				result = claim.Probe(ctx)
			}
			if result.Detail == "" {
				result.Detail = "probe reported no detail"
			}
			probes[claim.Dimension] = append(probes[claim.Dimension], probedClaim{provider: provider.Name(), result: result})
		}
	}
	for _, dimension := range Dimensions {
		capability := match(dimension, request.Posture(dimension), probes[dimension])
		if capability.Effective == EffectiveUnavailable {
			report.Decision = Rejected
		}
		report.Dimensions = append(report.Dimensions, capability)
	}
	return report
}

func match(dimension Dimension, requested Posture, claims []probedClaim) Capability {
	capability := Capability{Dimension: dimension, Requested: requested, Probes: []ProbeReport{}}
	var enforced, violated, inconclusive []string
	for _, claim := range claims {
		capability.Probes = append(capability.Probes, ProbeReport{Provider: claim.provider, Outcome: claim.result.Outcome, Detail: clip(claim.result.Detail)})
		switch claim.result.Outcome {
		case Enforced:
			enforced = append(enforced, claim.provider)
		case Violated:
			violated = append(violated, claim.provider)
		default:
			inconclusive = append(inconclusive, claim.provider)
		}
	}
	switch {
	case len(violated) > 0:
		capability.Diagnostic = &Diagnostic{Code: CodeProbeViolated, Message: clip(fmt.Sprintf("conformance probe from %s violated the %s boundary", strings.Join(violated, ", "), dimension))}
	case len(enforced) > 0:
		capability.Available, capability.Effective, capability.EnforcementSource = true, EffectiveRestricted, enforced[0]
		return capability
	case len(inconclusive) > 0:
		capability.Diagnostic = &Diagnostic{Code: CodeProbeInconclusive, Message: clip(fmt.Sprintf("conformance probe from %s could not prove the %s boundary", strings.Join(inconclusive, ", "), dimension))}
	default:
		capability.Diagnostic = &Diagnostic{Code: CodeNoProvider, Message: fmt.Sprintf("no available provider claims to enforce the %s boundary", dimension)}
	}
	if requested == Restricted {
		capability.Effective = EffectiveUnavailable
	} else {
		capability.Effective = EffectiveUnrestricted
	}
	return capability
}

// IsRejected reports whether err is a preflight rejection.
func IsRejected(err error) bool {
	_, ok := errors.AsType[*RejectedError](err)
	return ok
}
