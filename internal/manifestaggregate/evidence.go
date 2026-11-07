package manifestaggregate

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/microsoft/waza/internal/executionattempt"
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

type AdmittedCategory struct {
	Category      evidencestate.Category `json:"category"`
	State         evidencestate.State    `json:"state"`
	DeclaredState evidencestate.State    `json:"declared_state,omitempty"`
	Verification  string                 `json:"verification"`
	Reason        string                 `json:"reason,omitempty"`
	ObservedCount *string                `json:"observed_count,omitempty"`
}

type EvidenceCandidate struct {
	EvidenceSHA256 string                        `json:"evidence_sha256"`
	State          string                        `json:"state"`
	OriginManifest string                        `json:"origin_manifest,omitempty"`
	Sources        []string                      `json:"source_manifests"`
	Declarations   []CategoryDeclaration         `json:"declared_categories"`
	Categories     []AdmittedCategory            `json:"categories"`
	Usage          *RecordedUsage                `json:"usage,omitempty"`
	SourceContext  *SourceContext                `json:"source_context,omitempty"`
	Diagnostics    []artifactmanifest.Diagnostic `json:"diagnostics"`
}

type SourceContext struct {
	State         string                   `json:"state"`
	Verification  string                   `json:"verification"`
	Source        *executionattempt.Source `json:"source,omitempty"`
	ParentContext *ParentContext           `json:"parent_context,omitempty"`
}

type ParentContext struct {
	State        string                   `json:"state"`
	Verification string                   `json:"verification"`
	Relationship string                   `json:"relationship"`
	Source       *executionattempt.Source `json:"source,omitempty"`
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
	CategoryStateCounts    map[evidencestate.Category]map[evidencestate.State]int `json:"category_state_counts"`
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
		Qualification: "recorded-category-shape-only", UsageTotals: "withheld",
		Members:                []EvidenceMember{},
		DeclarationStateCounts: map[evidencestate.Category]map[evidencestate.State]int{},
		CategoryStateCounts:    map[evidencestate.Category]map[evidencestate.State]int{},
		Diagnostics: []artifactmanifest.Diagnostic{{
			Code: "qualification_unavailable", Message: "Recorded category shape is separate from declared metadata. Source ownership, execution and new billing attestation are not established; totals are withheld.",
		}},
	}
	var failures []error
	cursor := 0
	for _, member := range members {
		result := EvidenceMember{ManifestSHA256: member.ManifestSHA256, State: "not_selected", Candidates: []EvidenceCandidate{}}
		for cursor < len(references) && references[cursor].ManifestSHA256 == member.ManifestSHA256 {
			candidate, err := inspectEvidenceCandidate(ctx, backend, references[cursor])
			if candidate.State == "verified" && member.Logical == nil {
				candidate.State = "unavailable"
				candidate.Declarations = []CategoryDeclaration{}
				candidate.Categories = []AdmittedCategory{}
				candidate.Usage = nil
				candidate.SourceContext = nil
				candidate.Diagnostics = append(candidate.Diagnostics, artifactmanifest.Diagnostic{
					Code: "origin_admission_withheld", Message: "The origin was not admitted in the manifest pass. A later sidecar read cannot replace that observed failure; category counts and usage are withheld.",
				})
				err = errors.Join(err, ErrIncomplete)
			}
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
				for _, category := range result.Candidates[0].Categories {
					if !categoryAdmitted(category) {
						continue
					}
					if report.CategoryStateCounts[category.Category] == nil {
						report.CategoryStateCounts[category.Category] = map[evidencestate.State]int{}
					}
					report.CategoryStateCounts[category.Category][category.State]++
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
		Sources: []string{}, Declarations: []CategoryDeclaration{}, Categories: []AdmittedCategory{}, Diagnostics: []artifactmanifest.Diagnostic{},
	}
	hits, unavailable := backend.hits, backend.unavailable
	missingFinal, missingObjects, nonFinalErrors := backend.missingFinal, backend.missingObjects, backend.nonFinalErrors
	reader := &EvidenceReader{backend: backend}
	inspection, categories, err := reader.InspectCategories(ctx, reference.EvidenceSHA256)
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
	for _, category := range categories {
		value := AdmittedCategory{
			Category: category.Category, State: category.State, DeclaredState: category.DeclaredState,
			Verification: category.Verification, Reason: category.Reason,
		}
		if category.ObservedCount != nil {
			count := strconv.FormatInt(*category.ObservedCount, 10)
			value.ObservedCount = &count
		}
		result.Categories = append(result.Categories, value)
		if category.Category == evidencestate.Usage {
			result.Usage = &RecordedUsage{
				State: category.State, DeclaredState: category.DeclaredState,
				Verification: category.Verification, Reason: category.Reason,
			}
			if category.Verification == "source-content-and-recorded-usage-shape-only" &&
				(category.State == evidencestate.Present || category.State == evidencestate.Incomplete) {
				result.Usage.Recorded = category.Usage
			}
		}
	}
	slices.SortFunc(result.Categories, func(a, b AdmittedCategory) int { return strings.Compare(string(a.Category), string(b.Category)) })
	sourceContext, sourceErr := reader.InspectSourceContext(ctx, *inspection.Document)
	result.SourceContext = sourceContext
	if sourceErr != nil {
		diagnostic := artifactmanifest.Diagnostic{
			Code: "original_source_context_withheld", Message: "Current reservation, completion and original attempt bytes were not admitted together. Native shapes remain a separate observation; no retained-source or billing claim is made.",
		}
		if sourceContext != nil && sourceContext.State == "verified" && sourceContext.ParentContext != nil {
			diagnostic = artifactmanifest.Diagnostic{
				Code: "retry_parent_context_withheld", Message: "The immediate declared parent was not admitted. Verified child and category facts remain separate; no parent source link or runtime claim is made.",
			}
		}
		result.Diagnostics = append(result.Diagnostics, diagnostic)
	}
	if err != nil {
		result.Diagnostics = append(result.Diagnostics, artifactmanifest.Diagnostic{
			Code: "category_admission_failed", Message: "Source bytes were verified, but captured category admission failed. Unqualified counts and usage remain withheld; no score or billing total was substituted.",
		})
	}
	return result, errors.Join(err, sourceErr)
}

func categoryAdmitted(category AdmittedCategory) bool {
	switch category.Verification {
	case "source-content-and-recorded-category-shape-only", "source-content-and-recorded-usage-shape-only":
		return category.State != evidencestate.Present || category.ObservedCount != nil
	case "recorded-degradation-only":
		return category.State == evidencestate.Incomplete && category.ObservedCount == nil
	default:
		return false
	}
}
