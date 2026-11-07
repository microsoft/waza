// Package manifestaggregate derives reports from immutable public publications.
// Content verification and declared context are not execution attestation.
package manifestaggregate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/runnerterminal"
)

const (
	Version         = "1.0"
	MaxInputs       = 128
	MaxReadBytes    = 256 << 20
	MaxReportBytes  = 16 << 20
	MaxRequestBytes = 32 << 10
)

var (
	ErrInvalid     = errors.New("invalid aggregate request")
	ErrIncomplete  = errors.New("aggregate has incomplete or conflicting inputs")
	ErrLimit       = errors.New("aggregate read budget exceeded")
	ErrUnavailable = errors.New("aggregate storage is unavailable")
)

type Request struct {
	Manifests []string         `json:"manifests"`
	Evidence  *[]EvidenceInput `json:"evidence,omitempty"`
}

type Member struct {
	ManifestSHA256 string                        `json:"manifest_sha256"`
	State          string                        `json:"state"`
	Logical        *executionattempt.Logical     `json:"logical,omitempty"`
	Outcome        runnerterminal.Outcome        `json:"outcome,omitempty"`
	Verdicts       *[]runnerterminal.Verdict     `json:"verdicts,omitempty"`
	Diagnostics    []artifactmanifest.Diagnostic `json:"diagnostics"`
}

type Conflict struct {
	Logical   executionattempt.Logical `json:"logical"`
	Manifests []string                 `json:"manifests"`
}

type Report struct {
	Kind          string                        `json:"kind"`
	SchemaVersion string                        `json:"schema_version"`
	Publication   string                        `json:"publication"`
	Verification  string                        `json:"verification"`
	Completion    string                        `json:"completion"`
	Compatibility string                        `json:"compatibility"`
	Members       []Member                      `json:"members"`
	StateCounts   map[string]int                `json:"state_counts"`
	Conflicts     []Conflict                    `json:"conflicts"`
	Diagnostics   []artifactmanifest.Diagnostic `json:"diagnostics"`
	Evidence      *EvidenceReport               `json:"evidence,omitempty"`
}

func (r Request) Validate() error {
	if len(r.Manifests) < 1 || len(r.Manifests) > MaxInputs {
		return fmt.Errorf("%w: provide 1..%d manifest digests", ErrInvalid, MaxInputs)
	}
	for _, digest := range r.Manifests {
		if len(digest) != 64 || strings.Trim(digest, "0123456789abcdef") != "" {
			return fmt.Errorf("%w: provide full lowercase SHA-256 manifest digests", ErrInvalid)
		}
	}
	if r.Evidence != nil {
		if *r.Evidence == nil || len(*r.Evidence) > MaxInputs {
			return fmt.Errorf("%w: evidence must be an array containing at most %d references", ErrInvalid, MaxInputs)
		}
		for _, reference := range *r.Evidence {
			if !slices.Contains(r.Manifests, reference.ManifestSHA256) {
				return fmt.Errorf("%w: every evidence origin must be in the expected manifest set", ErrInvalid)
			}
			if len(reference.EvidenceSHA256) != 64 || strings.Trim(reference.EvidenceSHA256, "0123456789abcdef") != "" {
				return fmt.Errorf("%w: provide full lowercase evidence digests", ErrInvalid)
			}
		}
	}
	return nil
}

type boundedBackend struct {
	artifactmanifest.Backend
	*readBudget
}

type readBudget struct {
	read           int64
	hits           int
	unavailable    int
	missingFinal   int
	missingObjects int
	nonFinalErrors int
}

func (b *boundedBackend) Get(ctx context.Context, key string, limit int64) ([]byte, error) {
	return b.get(ctx, b.Backend, key, limit)
}

type boundedRead struct {
	artifactmanifest.ReadBackend
	*readBudget
}

func (b *boundedRead) Get(ctx context.Context, key string, limit int64) ([]byte, error) {
	return b.get(ctx, b.ReadBackend, key, limit)
}

func (b *readBudget) decorate(backend artifactmanifest.ReadBackend) artifactmanifest.ReadBackend {
	return &boundedRead{ReadBackend: backend, readBudget: b}
}

func (b *readBudget) get(ctx context.Context, backend artifactmanifest.ReadBackend, key string, limit int64) ([]byte, error) {
	if limit < 0 || limit >= MaxReadBytes-b.read {
		b.hits++
		return nil, ErrLimit
	}
	// Failed reads can have consumed bytes that a provider did not return.
	// Reserve their full admitted limit rather than assuming they cost zero.
	b.read += limit + 1
	data, err := backend.Get(ctx, key, limit)
	if err != nil && !strings.HasPrefix(key, "final-") {
		b.nonFinalErrors++
	}
	if errors.Is(err, artifactmanifest.ErrMissing) {
		if strings.HasPrefix(key, "final-") {
			b.missingFinal++
		} else {
			b.missingObjects++
		}
	}
	if err != nil && !errors.Is(err, artifactmanifest.ErrMissing) &&
		!errors.Is(err, artifactmanifest.ErrCorrupt) && !errors.Is(err, artifactmanifest.ErrConflict) &&
		!errors.Is(err, artifactmanifest.ErrInvalid) && !errors.Is(err, context.Canceled) &&
		!errors.Is(err, context.DeadlineExceeded) {
		b.unavailable++
	}
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		b.hits++
		return nil, ErrLimit
	}
	b.read -= limit + 1 - int64(len(data))
	return data, nil
}

// Aggregate never creates objects, starts workers, or loads project configuration.
// The request is the complete expected set; unknown omitted work is not inferred.
func Aggregate(ctx context.Context, backend artifactmanifest.Backend, request Request) (Report, error) {
	report := Report{
		Kind: "waza.manifest-aggregate", SchemaVersion: Version, Publication: "derived",
		Verification: "content-and-declared-context-only", Completion: "incomplete", Compatibility: "unknown",
		Members: []Member{}, StateCounts: map[string]int{}, Conflicts: []Conflict{}, Diagnostics: []artifactmanifest.Diagnostic{},
	}
	if err := request.Validate(); err != nil {
		return report, err
	}
	if backend == nil {
		return report, fmt.Errorf("%w: immutable backend is unavailable", ErrInvalid)
	}
	report.Completion, report.Compatibility = "complete", "compatible"
	digests := slices.Clone(request.Manifests)
	slices.Sort(digests)
	digests = slices.Compact(digests)
	var evidence []EvidenceInput
	if request.Evidence != nil {
		evidence = slices.Clone(*request.Evidence)
		slices.SortFunc(evidence, compareEvidence)
		evidence = slices.Compact(evidence)
	}
	bounded := &boundedBackend{Backend: backend, readBudget: &readBudget{}}
	store := artifactmanifest.NewStore(bounded)
	logicals := map[executionattempt.Logical][]int{}
	contexts := map[string]bool{}
	var failures []error
	for _, digest := range digests {
		budgetHits := bounded.hits
		storageFailures := bounded.unavailable
		missingFinal, missingObjects := bounded.missingFinal, bounded.missingObjects
		nonFinalErrors := bounded.nonFinalErrors
		member, manifest, err := load(ctx, store, digest)
		if bounded.unavailable != storageFailures {
			err = errors.Join(err, ErrUnavailable)
			member.State = classify(err)
		}
		if member.State == "missing" && bounded.missingFinal != missingFinal &&
			bounded.missingObjects == missingObjects && bounded.nonFinalErrors == nonFinalErrors {
			member.State = "unfinalized"
			member.Diagnostics = []artifactmanifest.Diagnostic{{
				Code: "not_completed", Message: "A verified COMPLETED publication marker is required; declarations and partial uploads are not completed inputs.",
			}}
		}
		if bounded.hits != budgetHits {
			member.State = "limit_exceeded"
			member.Logical, member.Verdicts, member.Outcome = nil, nil, ""
			member.Diagnostics = slices.DeleteFunc(member.Diagnostics, func(d artifactmanifest.Diagnostic) bool {
				return d.Code == "storage_error" && bounded.unavailable == storageFailures
			})
			member.Diagnostics = append(member.Diagnostics, artifactmanifest.Diagnostic{
				Code: "read_budget_exceeded", Message: "The cross-request 256 MiB admitted read budget withheld this member. Its source may be individually valid; no missing/corrupt result is inferred.",
			})
			err = errors.Join(err, ErrLimit)
		}
		report.Members = append(report.Members, member)
		if err != nil {
			failures = append(failures, err)
			report.Completion = "incomplete"
			report.Compatibility = "unknown"
			continue
		}
		logical := executionattempt.LogicalFrom(manifest.Binding.Identity)
		logicals[logical] = append(logicals[logical], len(report.Members)-1)
		contexts[manifest.SchemaVersion+"\x00"+manifest.Binding.Identity.Version+"\x00"+logical.Run+"\x00"+manifest.Binding.ExecutionMode] = true
		if member.State != "success" && member.State != "evaluation_failure" {
			report.Completion = "incomplete"
		}
	}
	for logical, indices := range logicals {
		if len(indices) < 2 {
			continue
		}
		conflict := Conflict{Logical: logical, Manifests: []string{}}
		for _, index := range indices {
			member := &report.Members[index]
			member.State = "conflicting"
			member.Diagnostics = append(member.Diagnostics, artifactmanifest.Diagnostic{
				Code: "logical_work_conflict", Message: "Distinct completed manifests name the same logical work. No source was selected or retagged.",
			})
			conflict.Manifests = append(conflict.Manifests, member.ManifestSHA256)
		}
		report.Conflicts = append(report.Conflicts, conflict)
		report.Completion = "incomplete"
	}
	slices.SortFunc(report.Conflicts, func(a, b Conflict) int {
		if order := strings.Compare(a.Logical.Run, b.Logical.Run); order != 0 {
			return order
		}
		return strings.Compare(a.Logical.Work, b.Logical.Work)
	})
	if len(contexts) > 1 {
		report.Compatibility = "incompatible"
		report.Completion = "incomplete"
		report.Diagnostics = append(report.Diagnostics, artifactmanifest.Diagnostic{
			Code: "incompatible_context", Message: "Manifest profiles, identity versions, logical runs or execution modes differ. Membership is retained; no cross-context result is inferred.",
		})
	}
	for _, member := range report.Members {
		report.StateCounts[member.State]++
	}
	if request.Evidence != nil {
		evidenceReport, err := aggregateEvidence(ctx, bounded, report.Members, evidence)
		report.Evidence = &evidenceReport
		if err != nil {
			failures = append(failures, err)
			report.Completion = "incomplete"
		}
	}
	if err := ctx.Err(); err != nil {
		report.Completion = "incomplete"
		report.Diagnostics = append(report.Diagnostics, artifactmanifest.Diagnostic{
			Code: "aggregation_canceled", Message: "The aggregate operation was canceled or reached its deadline; no complete-operation claim is made.",
		})
		failures = append(failures, err)
	}
	if report.Completion != "complete" {
		failures = append(failures, ErrIncomplete)
	}
	return report, errors.Join(failures...)
}

func load(ctx context.Context, store *artifactmanifest.Store, digest string) (Member, *artifactmanifest.Manifest, error) {
	member := Member{ManifestSHA256: digest, State: "unavailable", Diagnostics: []artifactmanifest.Diagnostic{}}
	completed, err := store.ReadCompleted(ctx, digest)
	if err != nil {
		return failedRead(member, err)
	}
	manifest := &completed.Manifest
	terminal := completed.ArtifactBytes[manifest.TerminalArtifact]
	envelope, err := runnerterminal.Validate(terminal, manifest.Binding.TerminalBinding())
	if err != nil {
		return failedRead(member, err)
	}
	logical := executionattempt.LogicalFrom(manifest.Binding.Identity)
	member.Logical = &logical
	member.Outcome = envelope.Outcome
	member.State = string(envelope.Outcome)
	if envelope.Observation != nil {
		verdicts := slices.Clone(envelope.Observation.Validations)
		slices.SortFunc(verdicts, func(a, b runnerterminal.Verdict) int { return strings.Compare(a.Identifier, b.Identifier) })
		member.Verdicts = &verdicts
	}
	// Never echo arbitrary terminal/provider diagnostics into public metadata.
	if envelope.Outcome != runnerterminal.Success {
		member.Diagnostics = append(member.Diagnostics, artifactmanifest.Diagnostic{
			Code: string(envelope.Outcome), Message: "The completed source records this terminal failure. Inspect the original source under its existing privacy policy.",
		})
	}
	return member, manifest, nil
}

func classify(err error) string {
	switch {
	case errors.Is(err, ErrLimit):
		return "limit_exceeded"
	case errors.Is(err, ErrUnavailable):
		return "unavailable"
	case errors.Is(err, artifactmanifest.ErrConflict):
		return "conflicting"
	case errors.Is(err, artifactmanifest.ErrInvalid), errors.Is(err, runnerterminal.ErrInvalid):
		return "incompatible"
	case errors.Is(err, artifactmanifest.ErrCorrupt):
		return "corrupt"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return string(runnerterminal.Canceled)
	case errors.Is(err, artifactmanifest.ErrMissing):
		return "missing"
	default:
		return "unavailable"
	}
}

func failedRead(member Member, err error) (Member, *artifactmanifest.Manifest, error) {
	member.State = classify(err)
	member.Diagnostics = append(member.Diagnostics, artifactmanifest.Diagnostic{
		Code: "source_read_failed", Message: "Current source bytes could not be admitted. No previous receipt or legacy result was substituted.",
	})
	return member, nil, err
}

func Encode(report Report) ([]byte, error) {
	data, err := json.Marshal(report)
	if err != nil {
		return nil, fmt.Errorf("encoding manifest aggregate: %w", err)
	}
	if len(data) > MaxReportBytes {
		return nil, ErrLimit
	}
	if err := validateReport(data); err != nil {
		return nil, fmt.Errorf("validating manifest aggregate: %w", err)
	}
	return data, nil
}
