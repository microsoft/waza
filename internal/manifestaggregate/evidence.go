package manifestaggregate

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/microsoft/waza/internal/models"
)

type EvidenceInput struct {
	ManifestSHA256 string `json:"manifest_sha256"`
	EvidenceSHA256 string `json:"evidence_sha256"`
}

type CategoryDeclaration struct {
	Category      evidencestate.Category `json:"category"`
	DeclaredState evidencestate.State    `json:"declared_state"`
	Source        evidencestate.Source   `json:"declared_source"`
	Context       evidencestate.Context  `json:"declared_context"`
	ObservedCount *string                `json:"declared_observed_count,omitempty"`
	Reason        evidencestate.Reason   `json:"declared_reason,omitempty"`
	Limit         *DeclaredLimit         `json:"declared_limit,omitempty"`
}

type DeclaredLimit struct {
	Observed string  `json:"observed"`
	Total    *string `json:"total,omitempty"`
}

type RecordedUsage struct {
	State         evidencestate.State `json:"state"`
	DeclaredState evidencestate.State `json:"declared_state,omitempty"`
	Verification  string              `json:"verification"`
	Reason        string              `json:"reason,omitempty"`
	Recorded      *models.UsageStats  `json:"recorded,omitempty"`
}

type EvidenceCandidate struct {
	EvidenceSHA256 string                        `json:"evidence_sha256"`
	State          string                        `json:"state"`
	OriginManifest string                        `json:"origin_manifest,omitempty"`
	Sources        []string                      `json:"source_manifests"`
	Declarations   []CategoryDeclaration         `json:"declared_categories"`
	Usage          *RecordedUsage                `json:"usage,omitempty"`
	Diagnostics    []artifactmanifest.Diagnostic `json:"diagnostics"`
}

type EvidenceMember struct {
	ManifestSHA256 string              `json:"manifest_sha256"`
	State          string              `json:"state"`
	Candidates     []EvidenceCandidate `json:"candidates"`
}

type EvidenceReport struct {
	Completion             string                                                 `json:"completion"`
	Verification           string                                                 `json:"verification"`
	Qualification          string                                                 `json:"qualification"`
	UsageTotals            string                                                 `json:"usage_totals"`
	Members                []EvidenceMember                                       `json:"members"`
	DeclarationStateCounts map[evidencestate.Category]map[evidencestate.State]int `json:"declaration_state_counts"`
	Diagnostics            []artifactmanifest.Diagnostic                          `json:"diagnostics"`
}

func compareEvidence(a, b EvidenceInput) int {
	if order := strings.Compare(a.ManifestSHA256, b.ManifestSHA256); order != 0 {
		return order
	}
	return strings.Compare(a.EvidenceSHA256, b.EvidenceSHA256)
}

func aggregateEvidence(ctx context.Context, backend *boundedBackend, members []Member, references []EvidenceInput) (EvidenceReport, error) {
	report := EvidenceReport{
		Completion: "incomplete", Verification: "content-and-declared-context-only",
		Qualification: "producer-and-category-qualification-unavailable", UsageTotals: "withheld",
		Members:                []EvidenceMember{},
		DeclarationStateCounts: map[evidencestate.Category]map[evidencestate.State]int{},
		Diagnostics: []artifactmanifest.Diagnostic{{
			Code: "qualification_unavailable", Message: "Category states and source kinds remain declarations. Recorded usage shape does not establish producer, ownership or new billing qualification; totals are withheld.",
		}},
	}
	var failures []error
	cursor := 0
	for _, member := range members {
		result := EvidenceMember{ManifestSHA256: member.ManifestSHA256, State: "not_selected", Candidates: []EvidenceCandidate{}}
		for cursor < len(references) && references[cursor].ManifestSHA256 == member.ManifestSHA256 {
			candidate, err := inspectEvidenceCandidate(ctx, backend, references[cursor])
			result.Candidates = append(result.Candidates, candidate)
			if err != nil {
				failures = append(failures, err)
			}
			cursor++
		}
		if len(result.Candidates) == 1 {
			result.State = result.Candidates[0].State
			if result.State == "verified" {
				for _, declaration := range result.Candidates[0].Declarations {
					if report.DeclarationStateCounts[declaration.Category] == nil {
						report.DeclarationStateCounts[declaration.Category] = map[evidencestate.State]int{}
					}
					report.DeclarationStateCounts[declaration.Category][declaration.DeclaredState]++
				}
			}
		} else if len(result.Candidates) > 1 {
			result.State = "conflicting"
			failures = append(failures, artifactmanifest.ErrConflict)
			report.Diagnostics = append(report.Diagnostics, artifactmanifest.Diagnostic{
				Code: "competing_evidence_sidecars", Message: "Multiple distinct sidecars were selected for one origin. All candidates are retained; no declaration or usage winner is chosen.",
			})
		}
		report.Members = append(report.Members, result)
	}
	return report, errors.Join(failures...)
}

func inspectEvidenceCandidate(ctx context.Context, backend *boundedBackend, reference EvidenceInput) (EvidenceCandidate, error) {
	result := EvidenceCandidate{
		EvidenceSHA256: reference.EvidenceSHA256, State: "unavailable",
		Sources: []string{}, Declarations: []CategoryDeclaration{}, Diagnostics: []artifactmanifest.Diagnostic{},
	}
	hits, unavailable := backend.hits, backend.unavailable
	missingFinal, missingObjects, nonFinalErrors := backend.missingFinal, backend.missingObjects, backend.nonFinalErrors
	inspection, usage, err := evidencestate.InspectWithUsage(ctx, backend, reference.EvidenceSHA256)
	if backend.unavailable != unavailable {
		err = errors.Join(err, ErrUnavailable)
	}
	if backend.hits != hits {
		result.State = "limit_exceeded"
		result.Diagnostics = append(result.Diagnostics, artifactmanifest.Diagnostic{
			Code: "read_budget_exceeded", Message: "The cross-request 256 MiB read budget withheld this evidence source; no missing or corrupt result is inferred.",
		})
		return result, errors.Join(err, ErrLimit)
	}
	if inspection.Integrity != "verified" || inspection.Document == nil {
		if err == nil {
			err = ErrUnavailable
		}
		result.State = classify(err)
		if result.State == "missing" && backend.missingFinal != missingFinal &&
			backend.missingObjects == missingObjects && backend.nonFinalErrors == nonFinalErrors {
			result.State = "unfinalized"
		}
		result.Diagnostics = append(result.Diagnostics, artifactmanifest.Diagnostic{
			Code: "evidence_read_failed", Message: "Current sidecar and completed source bytes were not admitted. No previous inspection or declaration was substituted.",
		})
		return result, err
	}

	result.OriginManifest = inspection.Document.OriginManifest
	if result.OriginManifest != reference.ManifestSHA256 {
		result.State = "conflicting"
		result.Diagnostics = append(result.Diagnostics, artifactmanifest.Diagnostic{
			Code: "evidence_origin_mismatch", Message: "The sidecar references a different origin than the selected manifest. Its category declarations and recorded usage were withheld.",
		})
		return result, artifactmanifest.ErrConflict
	}
	result.State = "verified"
	for _, source := range inspection.Sources {
		result.Sources = append(result.Sources, source.ManifestSHA256)
	}
	slices.Sort(result.Sources)
	result.Sources = slices.Compact(result.Sources)
	for _, record := range inspection.Document.Categories {
		declaration := CategoryDeclaration{
			Category: record.Category, DeclaredState: record.State, Source: record.Source, Context: record.Context, Reason: record.Reason,
		}
		if record.ObservedCount != nil {
			count := strconv.FormatInt(*record.ObservedCount, 10)
			declaration.ObservedCount = &count
		}
		if record.Limit != nil {
			declaration.Limit = &DeclaredLimit{Observed: strconv.FormatInt(record.Limit.Observed, 10)}
			if record.Limit.Total != nil {
				total := strconv.FormatInt(*record.Limit.Total, 10)
				declaration.Limit.Total = &total
			}
		}
		result.Declarations = append(result.Declarations, declaration)
	}
	slices.SortFunc(result.Declarations, func(a, b CategoryDeclaration) int { return strings.Compare(string(a.Category), string(b.Category)) })
	result.Usage = &RecordedUsage{
		State: usage.State, DeclaredState: usage.DeclaredState, Verification: usage.Verification, Reason: usage.Reason,
	}
	if usage.Verification == "source-content-and-recorded-usage-shape-only" &&
		(usage.State == evidencestate.Present || usage.State == evidencestate.Incomplete) {
		result.Usage.Recorded = usage.Usage
	}
	if err != nil {
		result.Diagnostics = append(result.Diagnostics, artifactmanifest.Diagnostic{
			Code: "usage_admission_failed", Message: "Source bytes were verified, but recorded usage shape was not admitted. No credits, score or billing total was substituted.",
		})
	}
	return result, err
}
