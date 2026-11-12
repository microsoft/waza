package executionledger

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const LineageVersion = "1.0"

// Attempt statuses describe recorded evidence only. "dispatched" means a
// distinct decision exists without a completion; it is not liveness evidence.
const (
	AttemptReserved             = "reserved"
	AttemptDispatched           = "dispatched"
	AttemptCompleted            = "completed"
	AttemptCompletionUnverified = "completion_unverified"
)

type LineageTerminal struct {
	Outcome         runnerterminal.Outcome `json:"outcome"`
	ExitCode        int                    `json:"exit_code"`
	FailureBoundary string                 `json:"failure_boundary"`
	EngineCalls     int                    `json:"engine_calls"`
}

type LineageAttempt struct {
	Order             int                      `json:"order"`
	Depth             int                      `json:"depth"`
	ExecutionID       string                   `json:"execution_id"`
	ReservationDigest string                   `json:"reservation_digest"`
	NativeAttempt     int                      `json:"native_attempt"`
	Parent            *executionattempt.Parent `json:"parent,omitempty"`
	Status            string                   `json:"status"`
	DispatchDecision  string                   `json:"dispatch_decision,omitempty"`
	ManifestDigest    string                   `json:"manifest_digest,omitempty"`
	Terminal          *LineageTerminal         `json:"terminal,omitempty"`
	ReusedBy          []string                 `json:"reused_by"`
}

type LineageDecision struct {
	DecisionID    string                   `json:"decision_id"`
	Policy        string                   `json:"policy"`
	Outcome       string                   `json:"outcome"`
	NativeAttempt int                      `json:"native_attempt"`
	ExecutionID   string                   `json:"execution_id,omitempty"`
	Source        *executionattempt.Source `json:"source,omitempty"`
}

type LineageDiagnostic struct {
	Code         string   `json:"code"`
	Message      string   `json:"message"`
	ExecutionIDs []string `json:"execution_ids"`
}

// Lineage is a complete, read-only projection of one logical work's ledger
// records. Order follows parent relationships (roots first); siblings are
// sorted by execution ID. It is not a global, wall-clock or exactly-once order.
type Lineage struct {
	Kind          string                   `json:"kind"`
	SchemaVersion string                   `json:"schema_version"`
	Logical       executionattempt.Logical `json:"logical"`
	Attempts      []LineageAttempt         `json:"attempts"`
	Decisions     []LineageDecision        `json:"decisions"`
	Diagnostics   []LineageDiagnostic      `json:"diagnostics"`
}

var lineageSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(schemaloader.Offline{})
	for name, text := range map[string]string{
		"execution-lineage-1.0.schema.json": schemas.ExecutionLineageSchemaJSON,
		"execution-attempt-1.0.schema.json": schemas.ExecutionAttemptSchemaJSON,
		"artifact-manifest-1.0.schema.json": schemas.ArtifactManifestSchemaJSON,
		"runner-terminal-1.0.schema.json":   schemas.RunnerTerminalSchemaJSON,
	} {
		doc, err := jsonschema.UnmarshalJSON(strings.NewReader(text))
		if err != nil {
			return nil, fmt.Errorf("reading lineage schema %s: %w", name, err)
		}
		if err := compiler.AddResource("https://microsoft.github.io/waza/schemas/"+name, doc); err != nil {
			return nil, fmt.Errorf("registering lineage schema %s: %w", name, err)
		}
	}
	return compiler.Compile("https://microsoft.github.io/waza/schemas/execution-lineage-1.0.schema.json")
})

// EncodeLineage validates the wire against its published schema.
func EncodeLineage(lineage Lineage) ([]byte, error) {
	data, err := json.Marshal(lineage)
	if err != nil {
		return nil, fmt.Errorf("encoding execution lineage: %w", err)
	}
	if _, err := ParseLineage(data); err != nil {
		return nil, err
	}
	return data, nil
}

func ParseLineage(data []byte) (*Lineage, error) {
	doc, err := jsonutil.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parsing execution lineage: %w", err)
	}
	schema, err := lineageSchema()
	if err != nil {
		return nil, err
	}
	if err := schema.Validate(doc); err != nil {
		return nil, fmt.Errorf("execution lineage schema: %w", err)
	}
	var lineage Lineage
	if err := json.Unmarshal(data, &lineage); err != nil {
		return nil, fmt.Errorf("decoding execution lineage: %w", err)
	}
	return &lineage, nil
}

var (
	runPattern  = regexp.MustCompile(`^run-[0-9a-f]{64}$`)
	workPattern = regexp.MustCompile(`^trial-[0-9a-f]{64}$`)
)

// ValidateLogical checks the public run/work identifier forms.
func ValidateLogical(logical executionattempt.Logical) error {
	if !runPattern.MatchString(logical.Run) || !workPattern.MatchString(logical.Work) {
		return fmt.Errorf("%w: logical run must match run-<sha256> and work must match trial-<sha256>", ErrInvalid)
	}
	return nil
}

// Lineage reads every ledger record and projects those for one logical work.
// Truncated discovery, unreadable records or non-canonical records fail closed:
// absence and completeness cannot be proven from partial discovery. Completion
// verification failures are reported per attempt, never repaired or hidden.
func (l *Ledger) Lineage(ctx context.Context, logical executionattempt.Logical) (*Lineage, error) {
	if l == nil || l.records == nil {
		return nil, ErrCapability
	}
	if err := ValidateLogical(logical); err != nil {
		return nil, err
	}
	keys, truncated, err := l.records.ListAllUnverified(ctx, MaxLineageRecords)
	if err != nil {
		return nil, fmt.Errorf("discovering execution lineage: %w", err)
	}
	if truncated {
		return nil, fmt.Errorf("%w: execution lineage discovery is truncated; completeness is unavailable", ErrContext)
	}
	reservations := map[string]*Reservation{}
	reserved := map[string]bool{}
	completed := map[string]bool{}
	var decisions []executionattempt.Decision
	for _, key := range keys {
		kind, id, _ := strings.Cut(key, "/")
		switch kind {
		case "reservation":
			record, err := l.ReadReservation(ctx, id)
			if err != nil {
				return nil, err
			}
			reserved[id] = true
			if record.Attempt.Logical == logical {
				reservations[id] = record
			}
		case "completion":
			completed[id] = true
		case "decision":
			data, err := l.records.ReadUnverified(ctx, key)
			if err != nil {
				return nil, fmt.Errorf("reading execution decision: %w", err)
			}
			decision, err := executionattempt.ParseDecision(data)
			if err != nil {
				return nil, err
			}
			canonical, err := executionattempt.EncodeDecision(*decision)
			if err != nil {
				return nil, err
			}
			if !bytes.Equal(data, canonical) || decision.DecisionID != id {
				return nil, fmt.Errorf("%w: execution decision is not canonical or key-bound", ErrContext)
			}
			if decision.Logical == logical {
				decisions = append(decisions, *decision)
			}
		}
	}
	// Every completion is validated, not only those for the requested work, so
	// an unreadable record cannot hide behind an unrelated or missing reservation.
	completions := map[string]*CompletionRecord{}
	for id := range completed {
		if !reserved[id] {
			return nil, fmt.Errorf("%w: execution completion has no recorded reservation", ErrContext)
		}
		record, err := l.ReadCompletion(ctx, id)
		if err != nil {
			return nil, err
		}
		if _, ok := reservations[id]; ok {
			completions[id] = record
		}
	}
	if len(reservations) > MaxLineageAttempts {
		return nil, fmt.Errorf("%w: logical work has %d attempts; lineage supports at most %d", ErrContext, len(reservations), MaxLineageAttempts)
	}
	return l.project(ctx, logical, reservations, completions, decisions)
}

// MaxLineageAttempts bounds one lineage document; larger histories fail closed.
const MaxLineageAttempts = 1024

// MaxLineageRecords bounds paginated store-wide ledger discovery (all logical
// work). Larger ledgers fail closed rather than reporting partial lineage.
const MaxLineageRecords = 1 << 18

func decisionMatchesReservation(decision executionattempt.Decision, reserved *Reservation) bool {
	original, actual := reserved.Attempt.Binding, decision.Binding
	return decision.Logical == reserved.Attempt.Logical && original.Identity == actual.Identity &&
		original.Selection == actual.Selection && original.ExecutionMode == actual.ExecutionMode &&
		slices.Equal(original.GraderIDs, actual.GraderIDs)
}

func (l *Ledger) project(ctx context.Context, logical executionattempt.Logical, reservations map[string]*Reservation,
	completions map[string]*CompletionRecord, decisions []executionattempt.Decision,
) (*Lineage, error) {
	result := &Lineage{Kind: "waza.execution-lineage", SchemaVersion: LineageVersion, Logical: logical,
		Attempts: []LineageAttempt{}, Decisions: []LineageDecision{}, Diagnostics: []LineageDiagnostic{}}
	diagnose := func(code, message string, ids ...string) {
		slices.Sort(ids)
		result.Diagnostics = append(result.Diagnostics, LineageDiagnostic{Code: code, Message: message, ExecutionIDs: ids})
	}
	children := map[string][]string{}
	var roots []string
	for id, reserved := range reservations {
		parent := reserved.Attempt.Parent
		if parent == nil {
			roots = append(roots, id)
			continue
		}
		if _, ok := reservations[parent.ExecutionID]; !ok {
			roots = append(roots, id)
			diagnose("parent_unavailable", "Attempt names a parent that is not recorded for this logical work.", id)
			continue
		}
		children[parent.ExecutionID] = append(children[parent.ExecutionID], id)
	}
	slices.Sort(roots)
	dispatch := map[string]string{}
	reusedBy := map[string][]string{}
	slices.SortFunc(decisions, func(a, b executionattempt.Decision) int { return strings.Compare(a.DecisionID, b.DecisionID) })
	for _, decision := range decisions {
		result.Decisions = append(result.Decisions, LineageDecision{DecisionID: decision.DecisionID, Policy: decision.Policy,
			Outcome: decision.Outcome, NativeAttempt: decision.Binding.Selection.Attempt,
			ExecutionID: decision.ExecutionID, Source: decision.Source})
		switch decision.Policy {
		case "distinct":
			reserved, ok := reservations[decision.ExecutionID]
			if !ok {
				diagnose("decision_reservation_unavailable", "Distinct decision names an execution with no reservation for this logical work.", decision.ExecutionID)
				continue
			}
			if !decisionMatchesReservation(decision, reserved) {
				return nil, fmt.Errorf("%w: distinct decision %s contradicts its original reservation", ErrContext, decision.DecisionID)
			}
			if _, ok := dispatch[decision.ExecutionID]; ok {
				diagnose("duplicate_dispatch_decision", "More than one distinct decision names the same execution; all are preserved.", decision.ExecutionID)
				continue
			}
			dispatch[decision.ExecutionID] = decision.DecisionID
		case "reuse":
			// Unfinished or conflicting reuse attempts are preserved as
			// decisions but never link to, or require, a source completion.
			if decision.Source == nil || decision.Outcome != "reused" {
				continue
			}
			source, ok := reservations[decision.Source.ExecutionID]
			if !ok {
				diagnose("reuse_source_unavailable", "Reuse decision names a source with no reservation for this logical work.", decision.Source.ExecutionID)
				continue
			}
			record, ok := completions[decision.Source.ExecutionID]
			if !ok || record.Completion.ManifestDigest != decision.Source.ManifestDigest {
				return nil, fmt.Errorf("%w: reuse decision %s names a source completion that is not recorded", ErrContext, decision.DecisionID)
			}
			if err := ValidateReuseBinding(decision, source.Attempt); err != nil {
				return nil, fmt.Errorf("reuse decision %s contradicts its source reservation: %w", decision.DecisionID, err)
			}
			reusedBy[decision.Source.ExecutionID] = append(reusedBy[decision.Source.ExecutionID], decision.DecisionID)
		}
	}
	var visit func(string, int)
	visit = func(id string, depth int) {
		reserved := reservations[id]
		attempt := LineageAttempt{Order: len(result.Attempts) + 1, Depth: depth, ExecutionID: id,
			ReservationDigest: reserved.Digest, NativeAttempt: reserved.Attempt.Binding.Selection.Attempt,
			Parent: reserved.Attempt.Parent, Status: AttemptReserved, DispatchDecision: dispatch[id],
			ReusedBy: append([]string{}, reusedBy[id]...)}
		if attempt.DispatchDecision != "" {
			attempt.Status = AttemptDispatched
		}
		if record, ok := completions[id]; ok {
			attempt.ManifestDigest = record.Completion.ManifestDigest
			attempt.Status = AttemptCompletionUnverified
			completed, err := l.ReadCompleted(ctx, executionattempt.Source{ExecutionID: id, ManifestDigest: record.Completion.ManifestDigest})
			var envelope *runnerterminal.Envelope
			if err == nil {
				envelope, err = runnerterminal.Validate(completed.Publication.ArtifactBytes[completed.Publication.Manifest.TerminalArtifact],
					reserved.Attempt.Binding.TerminalBinding())
			}
			if err != nil {
				code := "completion_unverified"
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					code = "completion_verification_interrupted"
				}
				diagnose(code, "Completion is recorded but its finalized publication or terminal could not be verified; preserve and inspect it.", id)
			} else {
				attempt.Status = AttemptCompleted
				attempt.Terminal = &LineageTerminal{Outcome: envelope.Outcome, ExitCode: envelope.ExitCode,
					FailureBoundary: envelope.FailureBoundary, EngineCalls: envelope.EngineCalls}
			}
		} else if attempt.Status == AttemptDispatched {
			diagnose("dispatched_without_completion", "Execution was dispatched but no completion is recorded; it may be running, stopped or lost. Do not treat it as absent.", id)
		}
		result.Attempts = append(result.Attempts, attempt)
		next := children[id]
		slices.Sort(next)
		for _, child := range next {
			visit(child, depth+1)
		}
	}
	for _, root := range roots {
		visit(root, 0)
	}
	if len(result.Attempts) != len(reservations) {
		return nil, fmt.Errorf("%w: attempt parent relationships contain a cycle", ErrContext)
	}
	if independent := rootsWithoutParent(reservations, roots); len(independent) > 1 {
		diagnose("concurrent_distinct_attempts", "Multiple attempts for this logical work have no parent relationship; duplicate or concurrent writers each kept their own immutable evidence.", independent...)
	}
	for parent, ids := range children {
		if len(ids) > 1 {
			diagnose("sibling_attempts", "Multiple attempts name the same parent; concurrent retries each kept their own immutable evidence.", append([]string{parent}, ids...)...)
		}
	}
	slices.SortFunc(result.Diagnostics, func(a, b LineageDiagnostic) int {
		if c := strings.Compare(a.Code, b.Code); c != 0 {
			return c
		}
		return slices.Compare(a.ExecutionIDs, b.ExecutionIDs)
	})
	return result, nil
}

func rootsWithoutParent(reservations map[string]*Reservation, roots []string) []string {
	var ids []string
	for _, id := range roots {
		if reservations[id].Attempt.Parent == nil {
			ids = append(ids, id)
		}
	}
	return ids
}
