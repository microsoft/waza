// Package manifestaggregate aggregates public completed publications without
// worker ownership, runtime construction, or execution attestation.
package manifestaggregate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnerterminal"
)

const (
	Version        = "1.0"
	MaxInputs      = 128
	MaxReportBytes = 32 << 20
)

var (
	ErrInvalid     = errors.New("invalid aggregate request")
	ErrIncomplete  = errors.New("aggregate admission is incomplete")
	ErrConflicting = errors.New("aggregate contains conflicting claims")
	ErrBudget      = errors.New("aggregate read budget exhausted")
)

type Input struct {
	Manifest string `json:"manifest_sha256"`
	Evidence string `json:"evidence_sha256,omitempty"`
}

// Sources are explicit read-only capabilities. A missing namespace never causes
// raw provider reads or hydration of a creator capability.
type Sources struct {
	Objects   artifactmanifest.ReadBackend
	Namespace *artifactmanifest.NamespaceReader
}

type Limits struct {
	Objects int
	Bytes   int64
}

func DefaultLimits() Limits { return Limits{Objects: 4096, Bytes: 512 << 20} }

type Category struct {
	Category      evidencestate.Category `json:"category"`
	State         evidencestate.State    `json:"state"`
	DeclaredState evidencestate.State    `json:"declared_state,omitempty"`
	Source        evidencestate.Source   `json:"source"`
	Context       evidencestate.Context  `json:"context"`
	Verification  string                 `json:"verification"`
	Reason        string                 `json:"reason,omitempty"`
	ObservedCount *int64                 `json:"observed_count,omitempty"`
}

type Member struct {
	Input
	State       string                    `json:"state"`
	Binding     *artifactmanifest.Binding `json:"binding,omitempty"`
	Outcome     runnerterminal.Outcome    `json:"outcome,omitempty"`
	Execution   string                    `json:"execution_id,omitempty"`
	Categories  []Category                `json:"categories"`
	Diagnostics []string                  `json:"diagnostics"`
}

type Group struct {
	Profile string  `json:"profile_sha256"`
	Members []Input `json:"members"`
}

type Counts struct {
	Members            int            `json:"input_members"`
	VerifiedManifests  int            `json:"unique_verified_manifests"`
	VerifiedExecutions int            `json:"unique_verified_execution_records"`
	MemberStates       map[string]int `json:"member_states"`
	MemberOutcomes     map[string]int `json:"member_outcomes"`
}

type Usage struct {
	State       string             `json:"state"`
	Population  string             `json:"population"`
	Executions  int                `json:"executions"`
	Recorded    *models.UsageStats `json:"recorded_historical_usage"`
	Diagnostics []string           `json:"diagnostics"`
}

type Report struct {
	Kind          string   `json:"kind"`
	SchemaVersion string   `json:"schema_version"`
	State         string   `json:"state"`
	Verification  string   `json:"verification"`
	Members       []Member `json:"members"`
	Groups        []Group  `json:"groups"`
	Counts        Counts   `json:"counts"`
	Usage         Usage    `json:"usage"`
}

type view struct {
	usage    *models.UsageStats
	eligible bool
}

// Normalize names the eligible requested population: unique exact reference
// pairs, not catalog discovery, logical attempt ordinals, or successful results.
func Normalize(inputs []Input) ([]Input, error) {
	if len(inputs) == 0 || len(inputs) > MaxInputs {
		return nil, fmt.Errorf("%w: require 1..%d input references", ErrInvalid, MaxInputs)
	}
	inputs = slices.Clone(inputs)
	for _, input := range inputs {
		if evidencestate.ValidateDigest(input.Manifest) != nil ||
			(input.Evidence != "" && evidencestate.ValidateDigest(input.Evidence) != nil) {
			return nil, fmt.Errorf("%w: expected full lowercase SHA-256 references", ErrInvalid)
		}
	}
	slices.SortFunc(inputs, compareInput)
	return slices.Compact(inputs), nil
}

func compareInput(a, b Input) int {
	if order := strings.Compare(a.Manifest, b.Manifest); order != 0 {
		return order
	}
	return strings.Compare(a.Evidence, b.Evidence)
}

// Build evaluates every independent requested member. Incomplete/conflicting
// admissions return both the full evaluated report and a classified error.
// Completeness is not a pass verdict: failed/canceled terminals, privacy
// redaction and supported legacy metadata-only sources can be complete.
func Build(ctx context.Context, sources Sources, inputs []Input, limits Limits) (*Report, error) {
	inputs, err := Normalize(inputs)
	if err != nil {
		return nil, err
	}
	if sources.Objects == nil || limits.Objects <= 0 || limits.Bytes <= 0 {
		return nil, fmt.Errorf("%w: readable store and positive read limits are required", ErrInvalid)
	}
	budget := &readBudget{remainingObjects: limits.Objects, remainingBytes: limits.Bytes}
	backend := readOnlyBackend{source: sources.Objects, budget: budget}
	store := artifactmanifest.NewStore(backend)
	report := &Report{
		Kind: "waza.manifest-aggregate", SchemaVersion: Version, State: "complete",
		Verification: "current-content-and-original-record-context-only",
		Members:      []Member{}, Groups: []Group{},
		Counts: Counts{Members: len(inputs), MemberStates: map[string]int{}, MemberOutcomes: map[string]int{}},
		Usage: Usage{State: "unavailable", Population: "unique-verified-execution-records",
			Diagnostics: []string{}, Recorded: nil},
	}
	manifests := map[string]bool{}
	executions := map[string][]int{}
	groups := map[string][]Input{}
	views := make([]view, 0, len(inputs))
	var failures []error
	for _, input := range inputs {
		member, usage, verified, memberErr := loadMember(ctx, sources, backend, store, budget, input)
		if verified {
			manifests[input.Manifest] = true
		}
		if member.Binding != nil {
			profile, encodeErr := json.Marshal(member.Binding)
			if encodeErr != nil {
				return nil, encodeErr
			}
			key := artifactmanifest.Hash(profile)
			groups[key] = append(groups[key], input)
		}
		if member.Execution != "" {
			executions[member.Execution] = append(executions[member.Execution], len(report.Members))
		}
		report.Members = append(report.Members, member)
		views = append(views, usage)
		if memberErr != nil {
			failures = append(failures, memberErr)
		}
	}
	// Distinct executions in the same profile are valid repeats. Only actual
	// incompatible views of one admitted record withhold that record's usage.
	usageStats := []*models.UsageStats{}
	usageComplete := len(executions) > 0
	for _, id := range sortedKeys(executions) {
		indices := executions[id]
		var original *models.UsageStats
		for _, index := range indices {
			current := views[index]
			if !current.eligible || current.usage == nil {
				usageComplete = false
				continue
			}
			if original == nil {
				original = current.usage
			} else if !equalUsage(original, current.usage) {
				usageComplete = false
				report.Usage.State = "conflicting"
				report.Usage.Diagnostics = append(report.Usage.Diagnostics, "incompatible_recorded_usage")
				for _, affected := range indices {
					report.Members[affected].State = "conflicting"
					report.Members[affected].Diagnostics = append(report.Members[affected].Diagnostics, "incompatible_recorded_usage")
				}
				failures = append(failures, ErrConflicting)
			}
		}
		if original != nil {
			usageStats = append(usageStats, original)
		}
	}
	for _, member := range report.Members {
		if member.Execution == "" || member.State != "admitted" {
			usageComplete = false
		}
	}
	report.Counts.VerifiedManifests, report.Counts.VerifiedExecutions = len(manifests), len(executions)
	report.Usage.Executions = len(executions)
	if usageComplete {
		recorded, reason := sumRecordedUsage(usageStats)
		if reason == "" {
			report.Usage.State, report.Usage.Recorded = "complete", recorded
		} else {
			report.Usage.Diagnostics = append(report.Usage.Diagnostics, reason)
		}
	} else {
		report.Usage.Diagnostics = append(report.Usage.Diagnostics, "complete_historical_usage_unavailable")
	}
	for _, key := range sortedKeys(groups) {
		report.Groups = append(report.Groups, Group{Profile: key, Members: groups[key]})
	}
	for i := range report.Members {
		member := &report.Members[i]
		slices.Sort(member.Diagnostics)
		member.Diagnostics = slices.Compact(member.Diagnostics)
		report.Counts.MemberStates[member.State]++
		if member.Outcome != "" {
			report.Counts.MemberOutcomes[string(member.Outcome)]++
		}
		if member.State == "conflicting" {
			report.State = "conflicting"
		} else if member.State != "admitted" && report.State != "conflicting" {
			report.State = "incomplete"
		}
	}
	slices.Sort(report.Usage.Diagnostics)
	report.Usage.Diagnostics = slices.Compact(report.Usage.Diagnostics)
	if report.State == "conflicting" {
		failures = append(failures, ErrConflicting)
	} else if report.State != "complete" {
		failures = append(failures, ErrIncomplete)
	}
	return report, errors.Join(failures...)
}

func loadMember(ctx context.Context, sources Sources, backend readOnlyBackend, store *artifactmanifest.Store,
	budget *readBudget, input Input) (Member, view, bool, error) {
	member := Member{Input: input, State: "admitted", Categories: []Category{}, Diagnostics: []string{}}
	fail := func(err error) (Member, view, bool, error) {
		member.State = "omitted"
		if errors.Is(err, artifactmanifest.ErrConflict) || errors.Is(err, executionledger.ErrContext) {
			member.State = "conflicting"
		}
		member.Diagnostics = append(member.Diagnostics, failureCode(err))
		if errors.Is(err, artifactmanifest.ErrInvalid) || errors.Is(err, evidencestate.ErrInvalid) {
			member.Diagnostics = append(member.Diagnostics, "unsupported_or_invalid_profile")
		}
		return member, view{}, member.Binding != nil, err
	}
	publication, err := store.ReadCompleted(ctx, input.Manifest)
	if err != nil {
		return fail(err)
	}
	member.Binding = &publication.Manifest.Binding
	terminal, err := runnerterminal.Validate(publication.ArtifactBytes[publication.Manifest.TerminalArtifact], publication.Manifest.Binding.TerminalBinding())
	if err != nil {
		return fail(err)
	}
	member.Outcome = terminal.Outcome
	if input.Evidence == "" {
		member.Diagnostics = append(member.Diagnostics, "execution_and_usage_unavailable_without_sidecar")
		return member, view{}, true, nil
	}
	inspection, categories, inspectionErr := evidencestate.InspectCategories(ctx, backend, input.Evidence)
	if inspection.Document == nil || inspection.Document.OriginManifest != input.Manifest {
		if inspectionErr != nil {
			return fail(inspectionErr)
		}
		return fail(artifactmanifest.ErrConflict)
	}
	for _, category := range categories {
		var declared evidencestate.Record
		for _, record := range inspection.Document.Categories {
			if record.Category == category.Category {
				declared = record
			}
		}
		member.Categories = append(member.Categories, Category{
			Category: category.Category, State: category.State, DeclaredState: category.DeclaredState,
			Source: declared.Source, Context: declared.Context,
			Verification: category.Verification, Reason: category.Reason, ObservedCount: category.ObservedCount,
		})
	}
	slices.SortFunc(member.Categories, func(a, b Category) int {
		return strings.Compare(string(a.Category), string(b.Category))
	})
	if inspectionErr != nil {
		return fail(inspectionErr)
	}
	document := inspection.Document
	if document.ExecutionSource == nil {
		member.Diagnostics = append(member.Diagnostics, "execution_identity_unavailable")
		return member, view{}, true, nil
	}
	if sources.Namespace == nil {
		return fail(fmt.Errorf("%w: namespace read capability unavailable", executionledger.ErrCapability))
	}
	if err := verifyExecution(ctx, sources.Namespace, budget, *document.ExecutionSource,
		publication.ManifestBytes, publication.Manifest.Artifacts, document.AttemptArtifact); err != nil {
		return fail(err)
	}
	member.Execution = document.ExecutionSource.ExecutionID
	result := view{}
	cached := false
	for _, link := range document.Lineage {
		cached = cached || link.Relationship == evidencestate.Cache
	}
	for _, record := range document.Categories {
		cached = cached || record.Source == evidencestate.Cached
	}
	for _, category := range categories {
		if category.Category == evidencestate.Usage && category.State == evidencestate.Present &&
			category.Usage != nil && !cached {
			result.usage, result.eligible = category.Usage, true
		}
	}
	if cached {
		member.Diagnostics = append(member.Diagnostics, "cached_historical_usage_excluded")
	}
	return member, result, true, nil
}

func verifyExecution(ctx context.Context, namespace *artifactmanifest.NamespaceReader, budget *readBudget,
	source executionattempt.Source, manifest []byte, artifacts []artifactmanifest.Artifact, selectedAttempt string) error {
	read := func(key string) ([]byte, error) {
		return budget.read(ctx, artifactmanifest.MaxNamespaceObjectBytes, func() ([]byte, error) {
			return namespace.ReadUnverified(ctx, key)
		})
	}
	reservation, err := read("reservation/" + source.ExecutionID)
	if err != nil {
		return err
	}
	selected := slices.IndexFunc(artifacts, func(artifact artifactmanifest.Artifact) bool {
		return artifact.Name == selectedAttempt
	})
	if selected < 0 || artifacts[selected].SHA256 != artifactmanifest.Hash(reservation) ||
		artifacts[selected].SizeBytes != int64(len(reservation)) || artifacts[selected].MediaType != "application/json" {
		return executionledger.ErrContext
	}
	completionBytes, err := read("completion/" + source.ExecutionID)
	if err != nil {
		return err
	}
	completion, err := executionledger.ParseCompletion(completionBytes)
	if err != nil {
		return err
	}
	canonical, err := executionledger.EncodeCompletion(*completion)
	if err != nil {
		return err
	}
	if !bytes.Equal(canonical, completionBytes) || completion.ExecutionID != source.ExecutionID ||
		completion.ManifestDigest != source.ManifestDigest {
		return executionledger.ErrContext
	}
	return executionledger.ValidateContext(*completion, reservation, manifest)
}

func equalUsage(a, b *models.UsageStats) bool {
	left, leftErr := json.Marshal(a)
	right, rightErr := json.Marshal(b)
	return leftErr == nil && rightErr == nil && bytes.Equal(left, right)
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func failureCode(err error) string {
	switch {
	case errors.Is(err, ErrBudget):
		return "read_budget_exhausted"
	case errors.Is(err, executionledger.ErrCapability):
		return "execution_read_capability_unavailable"
	case errors.Is(err, artifactmanifest.ErrConflict), errors.Is(err, executionledger.ErrContext):
		return "conflicting_source"
	case errors.Is(err, artifactmanifest.ErrCorrupt):
		return "corrupt_source"
	case errors.Is(err, artifactmanifest.ErrMissing):
		return "missing_source"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "read_canceled"
	case errors.Is(err, artifactmanifest.ErrInvalid), errors.Is(err, evidencestate.ErrInvalid), errors.Is(err, executionledger.ErrInvalid):
		return "unsupported_or_invalid_profile"
	default:
		return "source_read_error"
	}
}
