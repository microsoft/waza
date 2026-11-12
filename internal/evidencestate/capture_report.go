package evidencestate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/microsoft/waza/internal/snapshot"
)

// PreparedCapture owns detached, privacy-filtered bytes from one fresh report.
// It cannot hydrate a Creator or prove that a caller-supplied report executed.
type PreparedCapture struct {
	index   CaptureIndex
	objects map[string][]byte
}

// Artifacts returns detached first-publication bytes. The native terminal has
// already undergone capture-profile diagnostic sanitization; callers must not
// replace a finalized/reused terminal with it. No storage or runtime is invoked.
func (p *PreparedCapture) Artifacts() ([]artifactmanifest.Artifact, map[string][]byte, error) {
	if p == nil || p.objects == nil {
		return nil, nil, fmt.Errorf("%w: capture is unavailable", ErrInvalid)
	}
	index, err := EncodeCapture(p.index)
	if err != nil {
		return nil, nil, err
	}
	if !bytes.Equal(index, p.objects[CaptureArtifactName]) {
		return nil, nil, fmt.Errorf("%w: capture index and prepared bytes disagree", ErrInvalid)
	}
	artifacts := make([]artifactmanifest.Artifact, 0, len(p.objects))
	objects := make(map[string][]byte, len(p.objects))
	for name, data := range p.objects {
		artifacts = append(artifacts, artifactmanifest.Artifact{
			Name: name, SHA256: artifactmanifest.Hash(data), SizeBytes: int64(len(data)), MediaType: "application/json",
		})
		objects[name] = bytes.Clone(data)
	}
	slices.SortFunc(artifacts, func(a, b artifactmanifest.Artifact) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	return artifacts, objects, nil
}

// CaptureReport never runs an engine. Nil withheld uses the conservative default
// (conversation/tool withheld); explicit [] enables recorded content subject to
// default secret filtering. Operators must additionally withhold restricted
// categories: regex filtering is not comprehensive disclosure authorization.
func CaptureReport(report runnerunit.Report, withheld []Category) (*PreparedCapture, error) {
	envelope, binding := report.Terminal()
	if binding == nil || binding.Identity == nil {
		return nil, fmt.Errorf("%w: capture requires an admitted original report", ErrInvalid)
	}
	policy := snapshot.DefaultPolicy()
	if err := ValidateCaptureBinding(artifactmanifest.Binding{
		Identity: *binding.Identity, Selection: binding.Selection,
		ExecutionMode: binding.ExecutionMode, GraderIDs: binding.GraderIDs,
	}); err != nil {
		return nil, err
	}
	blocked := map[Category]bool{}
	if withheld == nil {
		withheld = []Category{Conversation, Tool}
	}
	for _, category := range withheld {
		if category != Conversation && category != Tool && category != Grader &&
			category != Reliability && category != Usage {
			return nil, fmt.Errorf("%w: unsupported withheld category", ErrInvalid)
		}
		blocked[category] = true
	}
	for _, diagnostic := range envelope.Diagnostics {
		blocked[Reliability] = blocked[Reliability] || policy.RedactString(diagnostic.Message) != diagnostic.Message
	}
	if envelope.Observation != nil {
		message := envelope.Observation.ErrorMsg
		blocked[Reliability] = blocked[Reliability] || policy.RedactString(message) != message
	}
	// Withholding a category must remove all its free text, not just recognized
	// secrets. Native codes, context, outcome and counters remain unchanged.
	if blocked[Reliability] {
		for i := range envelope.Diagnostics {
			if envelope.Diagnostics[i].Message != "" {
				envelope.Diagnostics[i].Message = "[REDACTED]"
			}
		}
		if envelope.Observation != nil && envelope.Observation.ErrorMsg != "" {
			envelope.Observation.ErrorMsg = "[REDACTED]"
		}
	}
	terminal, err := runnerterminal.Encode(envelope, binding)
	if err != nil {
		return nil, err
	}
	const terminalName = "terminal.json"
	source := Unknown
	if report.Mode == "mock" {
		source = Mock
	}
	p := &PreparedCapture{
		index: CaptureIndex{
			Kind: "waza.execution-evidence-capture", SchemaVersion: Version,
			Binding: artifactmanifest.Binding{
				Identity: *binding.Identity, Selection: binding.Selection,
				ExecutionMode: binding.ExecutionMode, GraderIDs: slices.Clone(binding.GraderIDs),
			},
			TerminalArtifact: terminalName, Categories: []Record{},
			EmptyPayloads: map[Category]json.RawMessage{},
		},
		objects: map[string][]byte{terminalName: terminal},
	}
	payloads := map[Category]any{}
	if run := report.Observation; run != nil {
		if run.Transcript != nil {
			messages := []json.RawMessage{}
			for _, event := range run.Transcript {
				if event.Type() != "user.message" && event.Type() != "assistant.message" {
					continue
				}
				data, marshalErr := json.Marshal(event)
				if marshalErr != nil {
					return nil, fmt.Errorf("%w: recorded message serialization failed", ErrInvalid)
				}
				messages = append(messages, data)
			}
			payloads[Conversation] = messages
		}
		if run.ToolEvents != nil {
			payloads[Tool] = run.ToolEvents
		}
		if run.Validations != nil {
			payloads[Grader] = run.Validations
		}
		usage := run.Usage
		if usage == nil {
			usage = run.SessionDigest.Usage
		}
		if usage != nil {
			payloads[Usage] = usage
		}
	}
	for _, category := range []Category{Conversation, Tool, Grader, Reliability, Usage, Provenance} {
		record := Record{Category: category, State: Unavailable, Source: source, Context: Observed,
			Artifacts: []string{}, Reason: NotRecorded}
		switch {
		case category == Provenance:
			record.State, record.Context, record.Reason = Present, Declared, ""
			record.ObservedCount = captureCount(1)
			record.Artifacts = []string{CaptureArtifactName, terminalName}
		case blocked[category]:
			record.State, record.Reason = Redacted, Privacy
		case category == Reliability:
			record.State, record.Reason = Present, ""
			record.ObservedCount, record.Artifacts = captureCount(1), []string{terminalName}
		case payloads[category] != nil:
			data, marshalErr := json.Marshal(payloads[category])
			if marshalErr != nil {
				return nil, fmt.Errorf("%w: recorded category serialization failed", ErrInvalid)
			}
			if len(data) > artifactmanifest.MaxArtifactBytes {
				record.State, record.Reason = Incomplete, ObjectSizeLimit
				record.Limit = &Limit{Observed: int64(len(data)), Total: captureCount(int64(len(data)))}
				break
			}
			value, parseErr := jsonutil.Parse(data)
			if parseErr != nil {
				return nil, parseErr
			}
			if restrictedCategory(policy, category, value) {
				record.State, record.Reason = Redacted, Privacy
				break
			}
			state, count := Present, int64(1)
			if category != Usage {
				state, count, err = categoryShape(category, data, &envelope, binding)
				if err != nil {
					return nil, err
				}
			} else {
				if _, usageErr := parseRecordedUsage(data); usageErr != nil {
					return nil, usageErr
				}
			}
			record.State, record.Reason, record.ObservedCount = state, "", captureCount(count)
			switch state {
			case Incomplete:
				record.Reason = PartialCapture
			case Conflicting:
				record.Reason = SourceConflict
			}
			if state == Present && count == 0 {
				p.index.EmptyPayloads[category] = bytes.Clone(data)
			} else {
				name := "execution-evidence-" + string(category) + ".json"
				record.Artifacts = []string{name}
				p.objects[name] = bytes.Clone(data)
			}
		}
		p.index.Categories = append(p.index.Categories, record)
	}
	if err := p.seal(); err != nil {
		return nil, err
	}
	return p, nil
}

func captureCount(value int64) *int64 { return &value }

// ValidateCaptureBinding runs before reservation or any other persistence.
// Known native identity digests are opaque hashes, not credential candidates.
func ValidateCaptureBinding(binding artifactmanifest.Binding) error {
	policy := snapshot.DefaultPolicy()
	for _, identifier := range append(slices.Clone(binding.GraderIDs), binding.Selection.Shard, binding.Selection.Task) {
		if policy.RedactString(identifier) != identifier {
			return fmt.Errorf("%w: restricted binding identifier cannot be persisted", ErrInvalid)
		}
	}
	return nil
}

func restrictedCategory(policy *snapshot.Policy, category Category, value any) bool {
	// Native counter field names are not credentials. Freeform values retain
	// the existing sensitive-key policy; fixed native shapes scan string values.
	var walk func(any, bool) bool
	walk = func(v any, freeform bool) bool {
		if freeform && !reflect.DeepEqual(v, policy.RedactAny(v)) {
			return true
		}
		switch item := v.(type) {
		case string:
			return policy.RedactString(item) != item
		case []any:
			for _, child := range item {
				if walk(child, false) {
					return true
				}
			}
		case map[string]any:
			for key, child := range item {
				if policy.RedactString(key) != key ||
					walk(child, (category == Tool && (key == "args" || key == "result")) ||
						(category == Grader && key == "details")) {
					return true
				}
			}
		}
		return false
	}
	return walk(value, false)
}

func (p *PreparedCapture) seal() error {
	for {
		data, err := EncodeCapture(p.index)
		if err != nil {
			return err
		}
		total := int64(len(data))
		var largest string
		for name, object := range p.objects {
			if name == CaptureArtifactName {
				continue
			}
			total += int64(len(object))
			if name != p.index.TerminalArtifact && name != "execution-attempt.json" &&
				(largest == "" || len(object) > len(p.objects[largest]) ||
					(len(object) == len(p.objects[largest]) && name < largest)) {
				largest = name
			}
		}
		if total <= artifactmanifest.MaxTotalBytes {
			p.objects[CaptureArtifactName] = data
			return nil
		}
		if largest == "" {
			return fmt.Errorf("%w: control metadata exceeds publication bounds", ErrInvalid)
		}
		for i := range p.index.Categories {
			record := &p.index.Categories[i]
			if slices.Contains(record.Artifacts, largest) {
				record.State, record.Reason, record.Artifacts = Incomplete, TotalSizeLimit, []string{}
				record.Limit = &Limit{Observed: total, Total: captureCount(total)}
			}
		}
		delete(p.objects, largest)
	}
}

// PersistOwnedCapture requires the actual retained creator. The caller reserves
// before execution and rechecks runtime eligibility before its runtime factory.
// Partial storage/completion/sidecar failures are returned, never rolled back.
func PersistOwnedCapture(ctx context.Context, backend artifactmanifest.Backend, p *PreparedCapture,
	ledger *executionledger.Ledger, creator executionledger.Creator) (Inspection, error) {
	if p == nil || ledger == nil {
		return Inspection{}, fmt.Errorf("%w: capture or retained owner is unavailable", ErrInvalid)
	}
	reservation, err := ledger.Reservation(creator)
	if err != nil {
		return Inspection{}, err
	}
	if !reflect.DeepEqual(reservation.Attempt.Binding, p.index.Binding) {
		return Inspection{}, fmt.Errorf("%w: capture does not retain reserved binding", artifactmanifest.ErrConflict)
	}
	if err := ledger.ValidateCreator(ctx, creator); err != nil {
		return Inspection{}, err
	}
	if err := ValidateCaptureBinding(p.index.Binding); err != nil {
		return Inspection{}, err
	}
	indexBytes, err := EncodeCapture(p.index)
	if err != nil {
		return Inspection{}, err
	}
	index, err := ParseCapture(indexBytes)
	if err != nil {
		return Inspection{}, err
	}
	owned := &PreparedCapture{index: *index, objects: make(map[string][]byte, len(p.objects)+1)}
	for name, data := range p.objects {
		owned.objects[name] = bytes.Clone(data)
	}
	const attemptName = "execution-attempt.json"
	owned.objects[attemptName] = bytes.Clone(reservation.Bytes)
	if err := owned.seal(); err != nil {
		return Inspection{}, err
	}
	objects := owned.objects
	manifest := artifactmanifest.Manifest{
		Kind: "waza.artifact-manifest", SchemaVersion: artifactmanifest.Version,
		Binding: owned.index.Binding, TerminalArtifact: owned.index.TerminalArtifact,
		Artifacts: []artifactmanifest.Artifact{},
	}
	for name, data := range objects {
		manifest.Artifacts = append(manifest.Artifacts, artifactmanifest.Artifact{
			Name: name, SHA256: artifactmanifest.Hash(data), SizeBytes: int64(len(data)), MediaType: "application/json",
		})
	}
	wire, err := artifactmanifest.Encode(manifest)
	if err != nil {
		return Inspection{}, err
	}
	store := artifactmanifest.NewStore(backend)
	for _, artifact := range manifest.Artifacts {
		if err := store.Put(ctx, artifact, objects[artifact.Name]); err != nil {
			return Inspection{}, err
		}
	}
	digest, err := store.Create(ctx, wire)
	if err != nil {
		return Inspection{}, err
	}
	if _, err := store.Finalize(ctx, digest); err != nil {
		return Inspection{}, err
	}
	if _, err := ledger.Complete(ctx, creator, digest); err != nil {
		return Inspection{}, err
	}
	document := Document{
		Kind: "waza.execution-evidence", SchemaVersion: Version, Publication: "derived",
		OriginManifest: digest, Categories: owned.index.Categories, Lineage: []Lineage{},
		ExecutionSource: &executionattempt.Source{ExecutionID: reservation.Attempt.ExecutionID, ManifestDigest: digest},
		AttemptArtifact: attemptName,
	}
	inspection, err := Publish(ctx, backend, document)
	if err != nil {
		return inspection, err
	}
	// Semantic admission is mandatory for this producer path, not inferred
	// from a successful byte publication.
	verified, _, semanticErr := InspectCategories(ctx, backend, inspection.EvidenceSHA256)
	return verified, errors.Join(err, semanticErr)
}
