package executiontelemetry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

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
	Terminal        runnerterminal.Envelope
	Manifest        *artifactmanifest.Manifest
	ManifestSHA256  string
	Attempt         *executionattempt.Attempt
	Lineage         *executionledger.Lineage
	Run             *models.RunResult
	ReportState     string
	Aggregate       *manifestaggregate.Report
	AggregateSHA256 string
	SchedulerWorkID string
}

// Load validates every supplied document against its published contract and
// rejects documents that describe a different run, work or attempt.
func Load(in Inputs) (*Source, error) {
	src := &Source{ReportState: SourceUnavailable}
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

	if attempt != nil {
		if attempt.Binding.Identity != identity {
			return nil, fmt.Errorf("%w: execution attempt identity differs from terminal identity", ErrUncorrelated)
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
		src.Lineage = lineage
	}
	if len(in.Report) > 0 {
		if len(in.Report) > MaxReportBytes {
			return nil, fmt.Errorf("%w: report exceeds %d bytes", ErrInvalid, MaxReportBytes)
		}
		var report models.EvaluationOutcome
		if err := json.Unmarshal(in.Report, &report); err != nil {
			return nil, fmt.Errorf("%w: report: %w", ErrInvalid, err)
		}
		src.Run, src.ReportState = selectRun(report, *terminal), SourceUnmatched
		if src.Run != nil {
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
		src.Aggregate, src.AggregateSHA256 = aggregate, artifactmanifest.Hash(bytes.TrimSpace(canonical))
	}
	return src, nil
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

// selectRun returns the report run for the terminal's exact selection, or nil
// when the report does not contain it. It never falls back to another run.
func selectRun(report models.EvaluationOutcome, terminal runnerterminal.Envelope) *models.RunResult {
	runNumber := terminal.Selection.Trial
	if terminal.Observation != nil {
		runNumber = terminal.Observation.RunNumber
	}
	for i := range report.TestOutcomes {
		outcome := &report.TestOutcomes[i]
		if outcome.TestID != terminal.Selection.Task {
			continue
		}
		for j := range outcome.Runs {
			if outcome.Runs[j].RunNumber == runNumber {
				return &outcome.Runs[j]
			}
		}
	}
	return nil
}
