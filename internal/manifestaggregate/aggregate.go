// Package manifestaggregate derives reports from immutable public publications.
// Content verification and declared context are not execution attestation.
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
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/runnerterminal"
)

const (
	Version        = "1.0"
	MaxInputs      = 128
	MaxReadBytes   = 256 << 20
	MaxReportBytes = 16 << 20
)

var (
	ErrInvalid     = errors.New("invalid aggregate request")
	ErrIncomplete  = errors.New("aggregate has incomplete or conflicting inputs")
	ErrLimit       = errors.New("aggregate read budget exceeded")
	ErrUnavailable = errors.New("aggregate storage is unavailable")
)

type Request struct {
	Manifests []string `json:"manifests"`
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
	return nil
}

type boundedBackend struct {
	artifactmanifest.Backend
	read        int64
	hits        int
	unavailable int
}

func (b *boundedBackend) Get(ctx context.Context, key string, limit int64) ([]byte, error) {
	if limit < 0 || limit > MaxReadBytes-b.read {
		b.hits++
		return nil, ErrLimit
	}
	// Failed reads can have consumed bytes that a provider did not return.
	// Reserve their full admitted limit rather than assuming they cost zero.
	b.read += limit
	data, err := b.Backend.Get(ctx, key, limit)
	if err != nil && !errors.Is(err, artifactmanifest.ErrMissing) &&
		!errors.Is(err, artifactmanifest.ErrCorrupt) && !errors.Is(err, artifactmanifest.ErrConflict) &&
		!errors.Is(err, artifactmanifest.ErrInvalid) && !errors.Is(err, context.Canceled) &&
		!errors.Is(err, context.DeadlineExceeded) {
		b.unavailable++
	}
	if int64(len(data)) > limit {
		b.hits++
		return nil, ErrLimit
	}
	if err == nil {
		b.read -= limit - int64(len(data))
	}
	return data, err
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
	bounded := &boundedBackend{Backend: backend}
	store := artifactmanifest.NewStore(bounded)
	logicals := map[executionattempt.Logical][]int{}
	contexts := map[string]bool{}
	var failures []error
	for _, digest := range digests {
		budgetHits := bounded.hits
		storageFailures := bounded.unavailable
		member, manifest, err := load(ctx, bounded, store, digest)
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
		if bounded.unavailable != storageFailures {
			err = errors.Join(err, ErrUnavailable)
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

func load(ctx context.Context, backend artifactmanifest.Backend, store *artifactmanifest.Store, digest string) (Member, *artifactmanifest.Manifest, error) {
	member := Member{ManifestSHA256: digest, State: "unavailable", Diagnostics: []artifactmanifest.Diagnostic{}}
	receipt, err := store.Inspect(ctx, digest)
	if err != nil {
		member.State = classify(err)
		member.Diagnostics = receipt.Diagnostics
		return member, nil, err
	}
	if receipt.Publication != "finalized" || receipt.Integrity != "verified" {
		member.State = "unfinalized"
		member.Diagnostics = append(member.Diagnostics, artifactmanifest.Diagnostic{
			Code: "not_completed", Message: "A verified COMPLETED publication marker is required; declarations and partial uploads are not completed inputs.",
		})
		return member, nil, ErrIncomplete
	}
	data, err := backend.Get(ctx, "manifest-"+digest, artifactmanifest.MaxManifestBytes)
	if err != nil {
		return failedRead(member, err)
	}
	manifest, err := artifactmanifest.Parse(data)
	if err != nil {
		return failedRead(member, err)
	}
	canonical, err := artifactmanifest.Encode(*manifest)
	if err != nil {
		return failedRead(member, err)
	}
	if artifactmanifest.Hash(data) != digest || !bytes.Equal(canonical, data) {
		return failedRead(member, artifactmanifest.ErrCorrupt)
	}
	var terminal []byte
	for _, artifact := range manifest.Artifacts {
		if artifact.Name != manifest.TerminalArtifact {
			continue
		}
		terminal, err = backend.Get(ctx, "blob-"+artifact.SHA256, artifact.SizeBytes)
		if err != nil {
			return failedRead(member, err)
		}
		if int64(len(terminal)) != artifact.SizeBytes || artifactmanifest.Hash(terminal) != artifact.SHA256 {
			return failedRead(member, artifactmanifest.ErrCorrupt)
		}
	}
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
	case errors.Is(err, artifactmanifest.ErrMissing):
		return "missing"
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
