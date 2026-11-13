package executiontelemetry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/manifestaggregate"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnerterminal"
)

var (
	// ErrInvalid reports an unreadable or contract-invalid source document.
	ErrInvalid = errors.New("invalid telemetry source")
	// ErrUncorrelated reports sources that do not describe the same execution.
	ErrUncorrelated = errors.New("telemetry sources do not correlate")
)

// MaxReportBytes bounds the optional rich report source.
const MaxReportBytes = 64 << 20

// AttemptArtifact is the manifest artifact that carries the execution-attempt
// sidecar bytes published with a ledger-backed execution.
const AttemptArtifact = "execution-attempt.json"

var schedulerWorkPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,127}$`)

// Inputs are raw public contract documents. Only Terminal is required.
type Inputs struct {
	Terminal        []byte
	Manifest        []byte
	Attempt         []byte
	Lineage         []byte
	Report          []byte
	Aggregate       []byte
	SchedulerWorkID string
}

// Source states reported per input.
const (
	SourcePresent     = "present"
	SourceUnavailable = "unavailable"
	SourceUnmatched   = "unmatched"
)

// Source is the admitted, mutually consistent set of contract documents.
type Source struct {
	Terminal       runnerterminal.Envelope
	Manifest       *artifactmanifest.Manifest
	ManifestSHA256 string
	Attempt        *executionattempt.Attempt
	Lineage        *executionledger.Lineage
	// LineageIndex is the lineage entry for the terminal's execution, or -1.
	LineageIndex    int
	Run             *models.RunResult
	ReportState     string
	Aggregate       *manifestaggregate.Report
	AggregateSHA256 string
	SchedulerWorkID string
}

// Load validates every supplied document against its published contract and
// rejects documents that describe a different run, work or attempt.
func Load(in Inputs) (*Source, error) {
	src := &Source{ReportState: SourceUnavailable, LineageIndex: -1}
	if in.SchedulerWorkID != "" {
		if !schedulerWorkPattern.MatchString(in.SchedulerWorkID) {
			return nil, fmt.Errorf("%w: scheduler work ID must match %s", ErrInvalid, schedulerWorkPattern)
		}
		src.SchedulerWorkID = in.SchedulerWorkID
	}
	var expected *runnerterminal.Binding
	if len(in.Manifest) > 0 {
		manifest, err := artifactmanifest.Parse(in.Manifest)
		if err != nil {
			return nil, fmt.Errorf("%w: manifest: %w", ErrInvalid, err)
		}
		digest, err := artifactmanifest.Digest(*manifest)
		if err != nil {
			return nil, fmt.Errorf("%w: manifest digest: %w", ErrInvalid, err)
		}
		src.Manifest, src.ManifestSHA256 = manifest, digest
		expected = manifest.Binding.TerminalBinding()
	}
	if len(in.Terminal) == 0 {
		return nil, fmt.Errorf("%w: a runner terminal document is required", ErrInvalid)
	}
	var attempt *executionattempt.Attempt
	if len(in.Attempt) > 0 {
		var err error
		if attempt, err = executionattempt.ParseAttempt(in.Attempt); err != nil {
			return nil, fmt.Errorf("%w: execution attempt: %w", ErrInvalid, err)
		}
		if expected == nil {
			expected = attempt.Binding.TerminalBinding()
		}
	}
	admitted := expected != nil
	if expected == nil {
		expected = selfBinding(in.Terminal)
	}
	terminal, err := runnerterminal.Validate(in.Terminal, expected)
	if err != nil {
		if admitted {
			return nil, fmt.Errorf("%w: terminal does not match admitted binding: %w", ErrUncorrelated, err)
		}
		return nil, fmt.Errorf("%w: terminal: %w", ErrInvalid, err)
	}
	if terminal.Identity == nil || terminal.Selection == nil {
		return nil, fmt.Errorf("%w: terminal has no admitted identity; correlation identifiers are unavailable before identity derivation", ErrUncorrelated)
	}
	src.Terminal = *terminal
	identity := *terminal.Identity
	logical := executionattempt.LogicalFrom(identity)

	if src.Manifest != nil {
		if err := declared(*src.Manifest, src.Manifest.TerminalArtifact, in.Terminal); err != nil {
			return nil, fmt.Errorf("%w: terminal is not the manifest terminal artifact: %w", ErrUncorrelated, err)
		}
	}
	if attempt != nil {
		if src.Manifest != nil {
			if !reflect.DeepEqual(attempt.Binding, src.Manifest.Binding) {
				return nil, fmt.Errorf("%w: execution attempt binding differs from manifest binding", ErrUncorrelated)
			}
			if err := declared(*src.Manifest, AttemptArtifact, in.Attempt); err != nil {
				return nil, fmt.Errorf("%w: execution attempt is not the manifest attempt artifact: %w", ErrUncorrelated, err)
			}
		}
		if attempt.Binding.Identity != identity {
			return nil, fmt.Errorf("%w: execution attempt identity differs from terminal identity", ErrUncorrelated)
		}
		if _, err := runnerterminal.Validate(in.Terminal, attempt.Binding.TerminalBinding()); err != nil {
			return nil, fmt.Errorf("%w: terminal does not match execution attempt binding: %w", ErrUncorrelated, err)
		}
		src.Attempt = attempt
	}
	if len(in.Lineage) > 0 {
		lineage, err := executionledger.ParseLineage(in.Lineage)
		if err != nil {
			return nil, fmt.Errorf("%w: lineage: %w", ErrInvalid, err)
		}
		if lineage.Logical != logical {
			return nil, fmt.Errorf("%w: lineage logical run/work differs from terminal identity", ErrUncorrelated)
		}
		if attempt == nil && src.Manifest == nil {
			return nil, fmt.Errorf("%w: a lineage needs an execution attempt or manifest to select the terminal's execution", ErrUncorrelated)
		}
		index, err := selectLineage(*lineage, src)
		if err != nil {
			return nil, err
		}
		src.Lineage, src.LineageIndex = lineage, index
	}
	if len(in.Report) > 0 {
		if len(in.Report) > MaxReportBytes {
			return nil, fmt.Errorf("%w: report exceeds %d bytes", ErrInvalid, MaxReportBytes)
		}
		report, err := models.ParseEvaluationOutcome(in.Report, "report")
		if err != nil {
			return nil, fmt.Errorf("%w: report: %w", ErrInvalid, err)
		}
		run, err := selectRun(*report, *terminal)
		if err != nil {
			return nil, err
		}
		src.Run, src.ReportState = run, SourceUnmatched
		if run != nil {
			src.ReportState = SourcePresent
		}
	}
	if len(in.Aggregate) > 0 {
		aggregate, err := manifestaggregate.Parse(in.Aggregate)
		if err != nil {
			return nil, fmt.Errorf("%w: aggregate: %w", ErrInvalid, err)
		}
		canonical, err := manifestaggregate.Encode(*aggregate)
		if err != nil {
			return nil, fmt.Errorf("%w: aggregate: %w", ErrInvalid, err)
		}
		if !aggregateIncludes(*aggregate, src.ManifestSHA256, logical) {
			return nil, fmt.Errorf("%w: aggregate has no member for this work", ErrUncorrelated)
		}
		src.Aggregate, src.AggregateSHA256 = aggregate, artifactmanifest.Hash(bytes.TrimSpace(canonical))
	}
	return src, nil
}

// aggregateIncludes reports whether the aggregate has a member for this work:
// the admitted manifest when one was supplied, otherwise any member whose
// verified binding names the same logical run and work.
func aggregateIncludes(aggregate manifestaggregate.Report, manifest string, logical executionattempt.Logical) bool {
	for _, member := range aggregate.Members {
		if manifest != "" {
			if member.Manifest == manifest {
				return true
			}
			continue
		}
		if member.Binding != nil && executionattempt.LogicalFrom(member.Binding.Identity) == logical {
			return true
		}
	}
	return false
}

// selfBinding reconstructs the binding a terminal claims when no manifest or
// attempt sidecar is supplied. Validation then still enforces the schema,
// bounds and internal consistency, but not external admission.
func selfBinding(data []byte) *runnerterminal.Binding {
	var e runnerterminal.Envelope
	if json.Unmarshal(data, &e) != nil || e.Selection == nil || e.Identity == nil {
		return nil
	}
	binding := &runnerterminal.Binding{Selection: *e.Selection, Identity: e.Identity, GraderIDs: []string{}}
	if e.Observation != nil {
		binding.ExecutionMode = e.Observation.ExecutionMode
		for _, v := range e.Observation.Validations {
			binding.GraderIDs = append(binding.GraderIDs, v.Identifier)
		}
	}
	return binding
}

// declared verifies data is the exact artifact the manifest names.
func declared(manifest artifactmanifest.Manifest, name string, data []byte) error {
	for _, artifact := range manifest.Artifacts {
		if artifact.Name != name {
			continue
		}
		if artifact.SizeBytes != int64(len(data)) || artifact.SHA256 != artifactmanifest.Hash(data) {
			return fmt.Errorf("%s bytes differ from the declared SHA-256 or size", name)
		}
		return nil
	}
	return fmt.Errorf("manifest declares no %s artifact", name)
}

// selectLineage finds the entry for the terminal's execution (by attempt
// sidecar execution ID, else by manifest digest) and rejects recorded fields
// that contradict the supplied documents. Fields an earlier snapshot has not
// recorded yet stay unavailable. A missing entry returns -1.
func selectLineage(lineage executionledger.Lineage, src *Source) (int, error) {
	index := -1
	for i, entry := range lineage.Attempts {
		if (src.Attempt != nil && entry.ExecutionID == src.Attempt.ExecutionID) ||
			(src.Attempt == nil && src.ManifestSHA256 != "" && entry.ManifestDigest == src.ManifestSHA256) {
			index = i
			break
		}
	}
	for i, entry := range lineage.Attempts {
		if i != index && src.ManifestSHA256 != "" && entry.ManifestDigest == src.ManifestSHA256 {
			return -1, fmt.Errorf("%w: lineage records this manifest for another execution", ErrUncorrelated)
		}
	}
	if index < 0 {
		return -1, nil
	}
	entry, terminal := lineage.Attempts[index], src.Terminal
	switch {
	case entry.NativeAttempt != terminal.Selection.Attempt:
		return -1, fmt.Errorf("%w: lineage native attempt differs from terminal selection", ErrUncorrelated)
	case entry.ManifestDigest != "" && src.ManifestSHA256 != "" && entry.ManifestDigest != src.ManifestSHA256:
		return -1, fmt.Errorf("%w: lineage manifest differs from supplied manifest", ErrUncorrelated)
	case src.Attempt != nil && entry.Parent != nil && !reflect.DeepEqual(entry.Parent, src.Attempt.Parent):
		return -1, fmt.Errorf("%w: lineage parent differs from execution attempt parent", ErrUncorrelated)
	}
	if t := entry.Terminal; t != nil && (t.Outcome != terminal.Outcome || t.ExitCode != terminal.ExitCode ||
		t.FailureBoundary != terminal.FailureBoundary || t.EngineCalls != terminal.EngineCalls) {
		return -1, fmt.Errorf("%w: lineage terminal outcome differs from supplied terminal", ErrUncorrelated)
	}
	return index, nil
}

// selectRun returns the report run for the terminal's exact selection. The
// terminal observation is projected from that run, so its run number,
// attempts, status, error and verdicts bind the run and any difference is a
// conflict. The report stays unmatched (nil, nil) when the terminal has no
// observation to bind it or the run is absent. It never falls back.
func selectRun(report models.EvaluationOutcome, terminal runnerterminal.Envelope) (*models.RunResult, error) {
	obs := terminal.Observation
	if obs == nil {
		return nil, nil
	}
	for i := range report.TestOutcomes {
		outcome := &report.TestOutcomes[i]
		if outcome.TestID != terminal.Selection.Task {
			continue
		}
		for j := range outcome.Runs {
			run := &outcome.Runs[j]
			if run.RunNumber != obs.RunNumber {
				continue
			}
			if run.Attempts != obs.Attempts || string(run.Status) != obs.Status || run.ErrorMsg != obs.ErrorMsg ||
				!sameVerdicts(run.Validations, obs.Validations) {
				return nil, fmt.Errorf("%w: report run differs from the terminal observation", ErrUncorrelated)
			}
			return run, nil
		}
	}
	return nil, nil
}

func sameVerdicts(results map[string]models.GraderResults, verdicts []runnerterminal.Verdict) bool {
	if len(results) != len(verdicts) {
		return false
	}
	got := make([]runnerterminal.Verdict, 0, len(results))
	for name, result := range results {
		got = append(got, runnerterminal.Verdict{Identifier: name, Passed: result.Passed})
	}
	want := append(make([]runnerterminal.Verdict, 0, len(verdicts)), verdicts...)
	for _, s := range [][]runnerterminal.Verdict{got, want} {
		sort.Slice(s, func(i, j int) bool { return s[i].Identifier < s[j].Identifier })
	}
	return reflect.DeepEqual(got, want)
}
