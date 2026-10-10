package releasepolicy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"unicode/utf8"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/models"
)

const (
	retryRawLimit            = 16 << 20
	retryLineLimit           = 1 << 20
	retryEventLimit          = 65536
	retryTokenLimit          = 1048576
	retryRepresentationLimit = 64 << 20
	retryPrefixDomain        = "waza.internal.retry-prefix.v1"
)

// RetryPrefix owns supplied bytes, not an execution, persistence or start permit.
type RetryPrefix struct {
	rawPolicy  []byte
	rawStream  []byte
	rawJournal []byte
	mode       string
	view       RetryPrefixView
	seal       models.EvidenceDigest
}

type RetryPrefixView struct {
	Mode                   string                 `json:"mode"`
	State                  string                 `json:"state"`
	Policy                 models.EvidenceDigest  `json:"policy"`
	Stream                 *models.EvidenceDigest `json:"stream"`
	Journal                *models.EvidenceDigest `json:"journal"`
	CollectionID           string                 `json:"collection_id"`
	EvalIDs                map[Arm]string         `json:"eval_ids"`
	Events                 []Event                `json:"validated_events"`
	TrailingFragmentBytes  int                    `json:"trailing_fragment_bytes"`
	CollectionEndValidated bool                   `json:"collection_end_validated"`
}

type retryPrefixSeal struct {
	Domain    string                `json:"domain"`
	RawPolicy models.EvidenceDigest `json:"raw_policy"`
	View      RetryPrefixView       `json:"view"`
}

type replayTrace struct {
	ctx                    context.Context
	events                 []Event
	collectionEndValidated bool
}

func (t *replayTrace) check() error {
	if t == nil {
		return nil
	}
	return retryContext(t.ctx)
}

func (t *replayTrace) accept(e Event) error {
	if t == nil {
		return nil
	}
	if err := t.check(); err != nil {
		return err
	}
	if len(t.events) >= retryEventLimit {
		return fmt.Errorf("retry trace exceeds event limit")
	}
	b := newRetryBudget(t.ctx)
	if err := b.value(e); err != nil {
		return err
	}
	// The aggregate trace was budgeted before replay; each detached append is
	// checked again, including nested slices, before copying.
	e, err := cloneRetryEvent(t.ctx, e)
	if err != nil {
		return err
	}
	t.events = append(t.events, e)
	t.collectionEndValidated = e.Type == "collection_end"
	return t.check()
}

func retryContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("retry inspection requires a context")
	}
	return ctx.Err()
}

func InspectRetryPrefix(ctx context.Context, rawPolicy, rawStream []byte) (*RetryPrefix, error) {
	return inspectRetry(ctx, rawPolicy, nil, rawStream, "stream_prefix")
}

func InspectRetryJournal(ctx context.Context, rawPolicy, rawJournal, rawStream []byte) (*RetryPrefix, error) {
	return inspectRetry(ctx, rawPolicy, rawJournal, rawStream, "sealed_journal")
}

func inspectRetry(ctx context.Context, policy, journal, stream []byte, mode string) (*RetryPrefix, error) {
	if err := preflightRetryInputs(ctx, policy, journal, stream, mode); err != nil {
		return nil, err
	}
	r := &RetryPrefix{mode: mode}
	var err error
	if r.rawPolicy, err = copyRetryBytes(ctx, policy); err != nil {
		return nil, err
	}
	if r.rawJournal, err = copyRetryBytes(ctx, journal); err != nil {
		return nil, err
	}
	if r.rawStream, err = copyRetryBytes(ctx, stream); err != nil {
		return nil, err
	}
	r.view, r.seal, err = deriveRetry(ctx, r.rawPolicy, r.rawJournal, r.rawStream, mode)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// View reparses and replays owned sources, comparing both independent seal and
// retained view. Its returned graph shares no mutable storage with the prefix.
func (r *RetryPrefix) View(ctx context.Context) (RetryPrefixView, error) {
	if r == nil {
		return RetryPrefixView{}, fmt.Errorf("missing retry prefix")
	}
	v, seal, err := deriveRetry(ctx, r.rawPolicy, r.rawJournal, r.rawStream, r.mode)
	if err != nil {
		return RetryPrefixView{}, err
	}
	if seal != r.seal || !reflect.DeepEqual(v, r.view) {
		return RetryPrefixView{}, fmt.Errorf("retry prefix source/view/seal mismatch")
	}
	if err := retryContext(ctx); err != nil {
		return RetryPrefixView{}, err
	}
	return v, nil
}

func deriveRetry(ctx context.Context, rawPolicy, rawJournal, rawStream []byte, mode string) (RetryPrefixView, models.EvidenceDigest, error) {
	fail := func(err error) (RetryPrefixView, models.EvidenceDigest, error) {
		return RetryPrefixView{}, models.EvidenceDigest{}, err
	}
	if err := preflightRetryInputs(ctx, rawPolicy, rawJournal, rawStream, mode); err != nil {
		return fail(err)
	}
	if err := retryContext(ctx); err != nil {
		return fail(err)
	}
	p, err := DecodePolicy(rawPolicy)
	if err != nil {
		return fail(err)
	}
	if err := retryContext(ctx); err != nil {
		return fail(err)
	}
	v := RetryPrefixView{Mode: mode, State: "partial", Policy: p.Digest,
		EvalIDs: map[Arm]string{}, Events: []Event{}}
	events, tail, err := retryStreamEvents(ctx, rawStream)
	if err != nil {
		return fail(err)
	}
	v.TrailingFragmentBytes = tail
	if rawStream == nil {
		v.State = "missing"
	} else {
		d := SourceDigest(rawStream)
		if err := retryContext(ctx); err != nil {
			return fail(err)
		}
		v.Stream = &d
	}
	var j *Journal
	if mode == "sealed_journal" {
		// Admission here is intentionally separate from DecodeJournal: the latter
		// performs the first legacy replay, which must already be budgeted.
		if err := retryContext(ctx); err != nil {
			return fail(err)
		}
		var inventory Journal
		if _, err := admit(rawJournal, JournalKind, &inventory); err != nil {
			return fail(err)
		}
		if err := retryContext(ctx); err != nil {
			return fail(err)
		}
		if err := preflightReplayExpansion(ctx, p, inventory.Events); err != nil {
			return fail(err)
		}
		if err := retryContext(ctx); err != nil {
			return fail(err)
		}
		j, err = DecodeJournal(rawJournal, p) // First replay, bounded but noncontextual.
		if err != nil {
			return fail(err)
		}
		if err := retryContext(ctx); err != nil {
			return fail(err)
		}
		if tail != 0 || len(rawStream) == 0 || rawStream[len(rawStream)-1] != '\n' ||
			j.StreamDigest != *v.Stream || !reflect.DeepEqual(j.Events, events) {
			return fail(fmt.Errorf("sealed journal stream byte/count/order binding mismatch"))
		}
		d := SourceDigest(rawJournal)
		if err := retryContext(ctx); err != nil {
			return fail(err)
		}
		v.Journal = &d
	}
	if err := preflightReplayExpansion(ctx, p, events); err != nil {
		return fail(err)
	}
	if len(events) > 0 {
		v.CollectionID = events[0].CollectionID
		if v.CollectionID == "" {
			return fail(fmt.Errorf("invalid retry collection identity"))
		}
		trace := &replayTrace{ctx: ctx, events: []Event{}}
		var digest *models.EvidenceDigest
		if j != nil {
			digest = &j.Digest
		}
		if _, err := replayEvents(p, v.CollectionID, events, digest, mode == "stream_prefix", trace); err != nil {
			return fail(err)
		}
		if trace.collectionEndValidated && tail != 0 {
			return fail(fmt.Errorf("trailing fragment after collection end"))
		}
		v.Events, v.CollectionEndValidated = trace.events, trace.collectionEndValidated
		for _, e := range v.Events {
			if e.Type == "begin_arm" {
				v.EvalIDs[e.Arm] = e.EvalID
			}
		}
		if v.CollectionEndValidated {
			v.State = "structurally_complete"
		}
	}
	if err := retryContext(ctx); err != nil {
		return fail(err)
	}
	sealInput := retryPrefixSeal{Domain: retryPrefixDomain, RawPolicy: SourceDigest(rawPolicy), View: v}
	if err := retryContext(ctx); err != nil {
		return fail(err)
	}
	b := newRetryBudget(ctx)
	if err := b.value(sealInput); err != nil {
		return fail(err)
	}
	if err := retryContext(ctx); err != nil {
		return fail(err)
	}
	seal, err := evidence.JSONDigest(sealInput)
	if err != nil {
		return fail(err)
	}
	if err := retryContext(ctx); err != nil {
		return fail(err)
	}
	return v, *seal, nil
}

func copyRetryBytes(ctx context.Context, data []byte) ([]byte, error) {
	if err := retryContext(ctx); err != nil {
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	if len(data) > retryRawLimit {
		return nil, fmt.Errorf("retry input exceeds raw byte limit")
	}
	out := make([]byte, len(data))
	for i := 0; i < len(data); {
		if err := retryContext(ctx); err != nil {
			return nil, err
		}
		n := min(65536, len(data)-i)
		copy(out[i:i+n], data[i:i+n])
		i += n
	}
	return out, retryContext(ctx)
}

// Token preflight allocates no generic maps/arrays. Charges bound serialized
// representations, not process RSS or internal interruptibility of decoders.
type retryJSONScan struct {
	ctx     context.Context
	tokens  int
	events  int
	generic *retryBudget
	typed   *retryBudget
}

func (s *retryJSONScan) token(d *json.Decoder) (json.Token, error) {
	if err := retryContext(s.ctx); err != nil {
		return nil, err
	}
	if s.tokens >= retryTokenLimit {
		return nil, fmt.Errorf("retry JSON exceeds token limit")
	}
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	s.tokens++
	return t, nil
}

func (s *retryJSONScan) add(n int) error {
	if err := s.generic.add(n); err != nil {
		return err
	}
	return s.typed.add(n)
}

func (s *retryJSONScan) str(v string) error {
	if err := s.generic.str(v); err != nil {
		return err
	}
	return s.typed.str(v)
}

func (s *retryJSONScan) read(d *json.Decoder, depth int, eventArray bool, journalRoot bool) error {
	if depth > 64 {
		return fmt.Errorf("retry JSON exceeds nesting depth")
	}
	t, err := s.token(d)
	if err != nil {
		return err
	}
	switch t {
	case json.Delim('{'), json.Delim('['):
		object := t == json.Delim('{')
		if eventArray && object {
			return fmt.Errorf("journal events must be an array")
		}
		if err := s.add(2); err != nil {
			return err
		}
		for d.More() {
			isEvents := false
			if object {
				key, err := s.token(d)
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok {
					return fmt.Errorf("invalid retry object key")
				}
				if err := s.str(name); err != nil {
					return err
				}
				if err := s.add(1); err != nil {
					return err
				}
				isEvents = journalRoot && name == "events"
			} else if eventArray {
				if s.events >= retryEventLimit {
					return fmt.Errorf("retry journal exceeds event limit")
				}
				s.events++
			}
			if err := s.read(d, depth+1, isEvents, false); err != nil {
				return err
			}
			if err := s.add(1); err != nil {
				return err
			}
		}
		end, err := s.token(d)
		if err != nil {
			return err
		}
		if (object && end != json.Delim('}')) || (!object && end != json.Delim(']')) {
			return fmt.Errorf("invalid retry container")
		}
		return nil
	}
	switch v := t.(type) {
	case string:
		return s.str(v)
	case json.Number:
		if err := s.generic.add(len(v)); err != nil {
			return err
		}
		return s.typed.add(max(20, len(v)))
	case bool:
		return s.add(5)
	case nil:
		return s.add(4)
	default:
		return fmt.Errorf("invalid retry token")
	}
}

func (s *retryJSONScan) document(data []byte, journal bool) error {
	if err := retryContext(s.ctx); err != nil {
		return err
	}
	if !utf8.Valid(data) {
		return fmt.Errorf("retry JSON is not UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := s.read(d, 0, false, journal); err != nil {
		return err
	}
	// An EOF probe is not an input token, including at the exact token cap.
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("retry JSON contains trailing data")
	}
	return retryContext(s.ctx)
}

func newRetryScan(ctx context.Context) *retryJSONScan {
	return &retryJSONScan{ctx: ctx, generic: newRetryBudget(ctx), typed: newRetryBudget(ctx)}
}

func preflightRetryInputs(ctx context.Context, policy, journal, stream []byte, mode string) error {
	if err := retryContext(ctx); err != nil {
		return err
	}
	if mode != "stream_prefix" && mode != "sealed_journal" {
		return fmt.Errorf("unsupported retry inspection mode")
	}
	if policy == nil || (mode == "sealed_journal" && (journal == nil || stream == nil)) ||
		(mode == "stream_prefix" && journal != nil) {
		return fmt.Errorf("missing or unexpected retry input")
	}
	for _, raw := range [][]byte{policy, journal, stream} {
		if len(raw) > retryRawLimit {
			return fmt.Errorf("retry input exceeds raw byte limit")
		}
	}
	if err := newRetryScan(ctx).document(policy, false); err != nil {
		return err
	}
	if journal != nil {
		if err := newRetryScan(ctx).document(journal, true); err != nil {
			return err
		}
		// The generic parse is now bounded; raw members are checked before
		// typed inventory admission or DecodeJournal's first replay.
		if err := retryContext(ctx); err != nil {
			return err
		}
		object, err := objectJSON(journal)
		if err != nil {
			return err
		}
		if err := retryContext(ctx); err != nil {
			return err
		}
		array, ok := object["events"].([]any)
		if !ok {
			return fmt.Errorf("retry journal events must be an array")
		}
		for _, value := range array {
			if err := strictRetryEvent(ctx, value); err != nil {
				return err
			}
		}
	}
	scan := newRetryScan(ctx)
	count := 0
	for offset := 0; offset < len(stream); {
		if err := retryContext(ctx); err != nil {
			return err
		}
		n := bytes.IndexByte(stream[offset:], '\n')
		if n < 0 {
			if len(stream)-offset > retryLineLimit {
				return fmt.Errorf("retry torn fragment exceeds line byte limit")
			}
			break
		}
		if n >= retryLineLimit {
			return fmt.Errorf("retry complete line exceeds LF-inclusive byte limit")
		}
		if count >= retryEventLimit {
			return fmt.Errorf("retry stream exceeds event limit")
		}
		count++
		if err := scan.document(stream[offset:offset+n], false); err != nil {
			return err
		}
		offset += n + 1
	}
	return retryContext(ctx)
}

func strictRetryEvent(ctx context.Context, value any) error {
	if err := retryContext(ctx); err != nil {
		return err
	}
	o, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("retry event must be an object")
	}
	names := []string{"sequence", "collection_id", "policy_digest", "type"}
	switch o["type"] {
	case "begin_arm":
		names = append(names, "arm", "eval_id")
	case "cluster_start":
		names = append(names, "cluster_id")
	case "attempt_start":
		names = append(names, "key")
	case "attempt_terminal":
		names = append(names, "attempt")
	case "trial_terminal":
		names = append(names, "arm", "trial")
	case "end_arm":
		names = append(names, "arm", "usage")
	case "collection_end":
	default:
		return fmt.Errorf("unsupported retry event transition")
	}
	if len(o) != len(names) {
		return fmt.Errorf("retry event has fields outside its raw transition")
	}
	for _, name := range names {
		if v, ok := o[name]; !ok || v == nil {
			return fmt.Errorf("retry event requires nonnull %s", name)
		}
	}
	if err := retryContext(ctx); err != nil {
		return err
	}
	if err := requireFields(o, reflect.TypeFor[Event](), ""); err != nil {
		return err
	}
	return retryContext(ctx)
}

func retryStreamEvents(ctx context.Context, stream []byte) ([]Event, int, error) {
	events := []Event{}
	for offset := 0; offset < len(stream); {
		if err := retryContext(ctx); err != nil {
			return nil, 0, err
		}
		n := bytes.IndexByte(stream[offset:], '\n')
		if n < 0 {
			return events, len(stream) - offset, nil
		}
		line := stream[offset : offset+n]
		object, err := objectJSON(line)
		if err != nil {
			return nil, 0, err
		}
		if err := strictRetryEvent(ctx, object); err != nil {
			return nil, 0, err
		}
		var e Event
		d := json.NewDecoder(bytes.NewReader(line))
		d.DisallowUnknownFields()
		if err := retryContext(ctx); err != nil {
			return nil, 0, err
		}
		if err := d.Decode(&e); err != nil {
			return nil, 0, err
		}
		if err := retryContext(ctx); err != nil {
			return nil, 0, err
		}
		events = append(events, e)
		offset += n + 1
	}
	return events, 0, retryContext(ctx)
}

func cloneRetryEvent(ctx context.Context, e Event) (Event, error) {
	if err := retryContext(ctx); err != nil {
		return Event{}, err
	}
	if err := newRetryBudget(ctx).value(e); err != nil {
		return Event{}, err
	}
	if e.Key != nil {
		k := *e.Key
		e.Key = &k
	}
	if e.Attempt != nil {
		a := *e.Attempt
		var err error
		a.Checks, err = cloneRetrySlice(ctx, a.Checks)
		if err != nil {
			return Event{}, err
		}
		a.References, err = cloneRetrySlice(ctx, a.References)
		if err != nil {
			return Event{}, err
		}
		e.Attempt = &a
	}
	if e.Trial != nil {
		trial := *e.Trial
		if trial.FirstPass != nil {
			v := *trial.FirstPass
			trial.FirstPass = &v
		}
		if trial.RetryPass != nil {
			v := *trial.RetryPass
			trial.RetryPass = &v
		}
		e.Trial = &trial
	}
	var err error
	e.Usage, err = cloneRetrySlice(ctx, e.Usage)
	if err != nil {
		return Event{}, err
	}
	for i := range e.Usage {
		if err := retryContext(ctx); err != nil {
			return Event{}, err
		}
		if e.Usage[i].Value != nil {
			v := *e.Usage[i].Value
			e.Usage[i].Value = &v
		}
	}
	e.Runtime, err = cloneRetrySlice(ctx, e.Runtime)
	if err != nil {
		return Event{}, err
	}
	return e, retryContext(ctx)
}

func cloneRetrySlice[T any](ctx context.Context, v []T) ([]T, error) {
	if err := retryContext(ctx); err != nil {
		return nil, err
	}
	if v == nil {
		return nil, nil
	}
	out := make([]T, len(v))
	for i := 0; i < len(v); {
		if err := retryContext(ctx); err != nil {
			return nil, err
		}
		n := min(256, len(v)-i)
		copy(out[i:i+n], v[i:i+n])
		i += n
	}
	return out, retryContext(ctx)
}

type retryBudget struct {
	ctx  context.Context
	used int
}

func newRetryBudget(ctx context.Context) *retryBudget { return &retryBudget{ctx: ctx} }

func (b *retryBudget) add(n int) error {
	if err := retryContext(b.ctx); err != nil {
		return err
	}
	if n < 0 || n > retryRepresentationLimit-b.used {
		return fmt.Errorf("retry representation exceeds 64 MiB")
	}
	b.used += n
	return nil
}

func (b *retryBudget) str(s string) error {
	if err := b.add(2); err != nil {
		return err
	}
	if len(s) > (retryRepresentationLimit-b.used)/6 {
		return fmt.Errorf("retry escaped string exceeds representation limit")
	}
	return b.add(6 * len(s))
}

type retryMember struct {
	name  string
	value any
}

func (b *retryBudget) object(members ...retryMember) error {
	if err := b.add(2); err != nil {
		return err
	}
	for _, m := range members {
		if err := b.str(m.name); err != nil {
			return err
		}
		if err := b.add(2); err != nil { // Colon and conservative comma.
			return err
		}
		if err := b.value(m.value); err != nil {
			return err
		}
	}
	return nil
}

func retryArray[T any](b *retryBudget, array []T) error {
	if array == nil {
		return b.add(4)
	}
	if err := b.add(2); err != nil {
		return err
	}
	for _, v := range array {
		if err := b.value(v); err != nil {
			return err
		}
		if err := b.add(1); err != nil {
			return err
		}
	}
	return nil
}

// The closed typed walker has no reflection, callbacks or caller-owned cycles.
func (b *retryBudget) value(value any) error {
	if err := retryContext(b.ctx); err != nil {
		return err
	}
	switch v := value.(type) {
	case nil:
		return b.add(4)
	case string:
		return b.str(v)
	case Arm:
		return b.str(string(v))
	case int:
		return b.add(20)
	case bool:
		return b.add(5)
	case json.Number:
		return b.add(len(v))
	case *bool:
		if v == nil {
			return b.add(4)
		}
		return b.add(5)
	case *json.Number:
		if v == nil {
			return b.add(4)
		}
		return b.value(*v)
	case *models.EvidenceDigest:
		if v == nil {
			return b.add(4)
		}
		return b.value(*v)
	case models.EvidenceDigest:
		return b.object(retryMember{"sha256", v.SHA256}, retryMember{"encoding", v.Encoding})
	case SampleKey:
		return b.object(retryMember{"cluster_id", v.ClusterID}, retryMember{"task_id", v.TaskID}, retryMember{"trial_ordinal", v.Trial})
	case AttemptKey:
		return b.object(retryMember{"cluster_id", v.ClusterID}, retryMember{"task_id", v.TaskID}, retryMember{"trial_ordinal", v.Trial},
			retryMember{"arm", v.Arm}, retryMember{"eval_id", v.EvalID}, retryMember{"attempt_ordinal", v.Attempt})
	case models.EvidenceOrigin:
		return b.object(retryMember{"eval_id", v.EvalID}, retryMember{"task_id", v.TaskID},
			retryMember{"run_number", v.RunNumber}, retryMember{"attempt_count", v.AttemptCount}, retryMember{"prior_attempts", v.PriorAttempts})
	case models.EvidenceReference:
		m := []retryMember{{"origin", v.Origin}, {"artifact_id", v.ArtifactID}}
		if v.Pointer != "" {
			m = append(m, retryMember{"pointer", v.Pointer})
		}
		return b.object(m...)
	case CheckSummary:
		m := []retryMember{{"scope", v.Scope}, {"grader", v.Grader}, {"passed", v.Passed}, {"score", v.Score}, {"operational_state", v.OperationalState}}
		if v.AfterTurn != 0 {
			m = append(m, retryMember{"after_turn", v.AfterTurn})
		}
		return b.object(m...)
	case RuntimeObservation:
		return b.object(retryMember{"task_id", v.TaskID}, retryMember{"requested_engine", v.RequestedEngine},
			retryMember{"requested_model", v.RequestedModel}, retryMember{"requested_reasoning", v.RequestedReasoning},
			retryMember{"availability", v.Availability}, retryMember{"engine_implementation", v.EngineImplementation},
			retryMember{"model_version", v.ModelVersion}, retryMember{"reason", v.Reason})
	case AttemptSummary:
		return b.object(retryMember{"key", v.Key}, retryMember{"origin", v.Origin}, retryMember{"status", v.Status},
			retryMember{"category", v.Category}, retryMember{"checks", v.Checks}, retryMember{"runtime", v.Runtime}, retryMember{"evidence_references", v.References})
	case TrialSummary:
		return b.object(retryMember{"key", v.Key}, retryMember{"terminal_attempt_ordinal", v.Terminal}, retryMember{"state", v.State},
			retryMember{"first_attempt_pass", v.FirstPass}, retryMember{"retry_policy_pass", v.RetryPass})
	case UsageAxis:
		m := []retryMember{{"axis", v.Axis}, {"availability", v.Availability}, {"observation", v.Observation}}
		if v.Currency != "" {
			m = append(m, retryMember{"currency", v.Currency})
		}
		if v.Value != nil {
			m = append(m, retryMember{"value", v.Value})
		}
		if v.Reason != "" {
			m = append(m, retryMember{"reason", v.Reason})
		}
		return b.object(m...)
	case Event:
		m := []retryMember{{"sequence", v.Sequence}, {"collection_id", v.CollectionID}, {"policy_digest", v.PolicyDigest}, {"type", v.Type}}
		if v.Arm != "" {
			m = append(m, retryMember{"arm", v.Arm})
		}
		if v.EvalID != "" {
			m = append(m, retryMember{"eval_id", v.EvalID})
		}
		if v.ClusterID != "" {
			m = append(m, retryMember{"cluster_id", v.ClusterID})
		}
		if v.Key != nil {
			m = append(m, retryMember{"key", *v.Key})
		}
		if v.Attempt != nil {
			m = append(m, retryMember{"attempt", *v.Attempt})
		}
		if v.Trial != nil {
			m = append(m, retryMember{"trial", *v.Trial})
		}
		if len(v.Usage) != 0 {
			m = append(m, retryMember{"usage", v.Usage})
		}
		if len(v.Runtime) != 0 {
			m = append(m, retryMember{"runtime", v.Runtime})
		}
		return b.object(m...)
	case ResultBinding:
		return b.object(retryMember{"kind", v.Kind}, retryMember{"version", v.Version}, retryMember{"collection_id", v.CollectionID},
			retryMember{"policy_digest", v.PolicyDigest}, retryMember{"arm", v.Arm}, retryMember{"eval_id", v.EvalID},
			retryMember{"resolved_plan_digest", v.PlanDigest}, retryMember{"planned_samples", v.Samples}, retryMember{"trials", v.Trials},
			retryMember{"attempts", v.Attempts}, retryMember{"usage", v.Usage})
	case Receipt:
		m := []retryMember{{"kind", v.Kind}, {"version", v.Version}, {"digest", v.Digest}, {"collection_id", v.CollectionID},
			{"policy_digest", v.PolicyDigest}, {"arm", v.Arm}, {"eval_id", v.EvalID}, {"resolved_plan_digest", v.PlanDigest}, {"state", v.State},
			{"planned_samples", v.Samples}, {"started_samples", v.Started}, {"attempts", v.Attempts}, {"trials", v.Trials},
			{"runtime", v.Runtime}, {"usage", v.Usage}, {"journal_count", v.JournalCount}}
		if v.JournalDigest != nil {
			m = append(m, retryMember{"journal_digest", v.JournalDigest})
		}
		if v.Binding != nil {
			m = append(m, retryMember{"result_binding", *v.Binding})
		}
		if v.BindingDigest != nil {
			m = append(m, retryMember{"result_binding_digest", v.BindingDigest})
		}
		return b.object(m...)
	case RetryPrefixView:
		return b.object(retryMember{"mode", v.Mode}, retryMember{"state", v.State}, retryMember{"policy", v.Policy},
			retryMember{"stream", v.Stream}, retryMember{"journal", v.Journal}, retryMember{"collection_id", v.CollectionID},
			retryMember{"eval_ids", v.EvalIDs}, retryMember{"validated_events", v.Events},
			retryMember{"trailing_fragment_bytes", v.TrailingFragmentBytes}, retryMember{"collection_end_validated", v.CollectionEndValidated})
	case retryPrefixSeal:
		return b.object(retryMember{"domain", v.Domain}, retryMember{"raw_policy", v.RawPolicy}, retryMember{"view", v.View})
	case map[Arm]string:
		if err := b.add(2); err != nil {
			return err
		}
		for k, val := range v {
			if err := b.str(string(k)); err != nil {
				return err
			}
			if err := b.add(2); err != nil {
				return err
			}
			if err := b.str(val); err != nil {
				return err
			}
		}
		return nil
	case []Event:
		return retryArray(b, v)
	case []SampleKey:
		return retryArray(b, v)
	case []AttemptSummary:
		return retryArray(b, v)
	case []TrialSummary:
		return retryArray(b, v)
	case []RuntimeObservation:
		return retryArray(b, v)
	case []UsageAxis:
		return retryArray(b, v)
	case []CheckSummary:
		return retryArray(b, v)
	case []models.EvidenceReference:
		return retryArray(b, v)
	default:
		return fmt.Errorf("unsupported retry representation type %T", value)
	}
}

func preflightReplayExpansion(ctx context.Context, p *Policy, events []Event) error {
	// Two passes, with no PlannedSamples allocation. The common planned set is
	// charged separately to each arm; retry maxima never multiply allocations.
	count := 0
	for _, cluster := range p.Design.Clusters {
		for _, task := range cluster.Tasks {
			for range task.TrialOrdinals {
				if err := retryContext(ctx); err != nil {
					return err
				}
				if count >= retryEventLimit {
					return fmt.Errorf("retry planned samples exceed limit")
				}
				count++
			}
		}
	}
	planned := newRetryBudget(ctx)
	if err := planned.add(2); err != nil {
		return err
	}
	for _, cluster := range p.Design.Clusters {
		for _, task := range cluster.Tasks {
			for _, ordinal := range task.TrialOrdinals {
				if err := planned.value(SampleKey{ClusterID: cluster.ID, TaskID: task.ID, Trial: ordinal}); err != nil {
					return err
				}
				if err := planned.add(1); err != nil {
					return err
				}
			}
		}
	}
	trace := newRetryBudget(ctx)
	// Include both view and seal fixed/header overhead before any trace clones.
	sourceIdentity := models.EvidenceDigest{SHA256: p.Digest.SHA256, Encoding: "source-bytes"}
	v := RetryPrefixView{Mode: "sealed_journal", State: "structurally_complete", Policy: p.Digest,
		Stream: &sourceIdentity, Journal: &sourceIdentity, EvalIDs: map[Arm]string{}, Events: []Event{}}
	if len(events) > 0 {
		v.CollectionID = events[0].CollectionID
	}
	for _, e := range events {
		if e.Type == "begin_arm" {
			v.EvalIDs[e.Arm] = e.EvalID
		}
	}
	if err := trace.value(retryPrefixSeal{Domain: retryPrefixDomain, RawPolicy: sourceIdentity, View: v}); err != nil {
		return err
	}
	for _, e := range events {
		if err := trace.value(e); err != nil {
			return err
		}
		if err := trace.add(1); err != nil {
			return err
		}
	}
	for _, arm := range []Arm{Baseline, Candidate} {
		r := Receipt{Kind: ReceiptKind, Version: Version, CollectionID: v.CollectionID, PolicyDigest: p.Digest,
			Arm: arm, EvalID: v.EvalIDs[arm], PlanDigest: p.Arms[arm].Digest, State: "final", JournalDigest: &p.Digest,
			Samples: []SampleKey{}, Started: []SampleKey{}, Attempts: []AttemptSummary{}, Trials: []TrialSummary{},
			Runtime: []RuntimeObservation{}, Usage: []UsageAxis{}, JournalCount: len(events), BindingDigest: &p.Digest}
		binding := ResultBinding{Kind: BindingKind, Version: Version, CollectionID: r.CollectionID, PolicyDigest: p.Digest,
			Arm: arm, EvalID: r.EvalID, PlanDigest: r.PlanDigest, Samples: []SampleKey{}, Trials: []TrialSummary{},
			Attempts: []AttemptSummary{}, Usage: []UsageAxis{}}
		r.Binding = &binding
		receiptBudget, bindingBudget := newRetryBudget(ctx), newRetryBudget(ctx)
		if err := receiptBudget.value(r); err != nil {
			return err
		}
		if err := bindingBudget.value(binding); err != nil {
			return err
		}
		if err := receiptBudget.add(planned.used - 2); err != nil {
			return err
		}
		if err := receiptBudget.add(planned.used - 2); err != nil {
			return err
		} // Nested binding samples.
		if err := bindingBudget.add(planned.used - 2); err != nil {
			return err
		}
		// Every supplied inventory member is pessimistically charged to EACH
		// arm. This protects DecodeJournal before semantics chooses an arm.
		for _, e := range events {
			if e.Key != nil {
				if err := receiptBudget.value(e.Key.SampleKey); err != nil {
					return err
				}
				if err := receiptBudget.add(1); err != nil {
					return err
				}
			}
			if e.Attempt != nil {
				for _, budget := range []*retryBudget{receiptBudget, receiptBudget, bindingBudget} {
					if err := budget.value(*e.Attempt); err != nil {
						return err
					}
					if err := budget.add(1); err != nil {
						return err
					}
				}
				if err := receiptBudget.value(e.Attempt.Runtime); err != nil {
					return err
				}
				if err := receiptBudget.add(1); err != nil {
					return err
				}
			}
			if e.Trial != nil {
				for _, budget := range []*retryBudget{receiptBudget, receiptBudget, bindingBudget} {
					if err := budget.value(*e.Trial); err != nil {
						return err
					}
					if err := budget.add(1); err != nil {
						return err
					}
				}
			}
			for _, usage := range e.Usage {
				for _, budget := range []*retryBudget{receiptBudget, receiptBudget, bindingBudget} {
					if err := budget.value(usage); err != nil {
						return err
					}
					if err := budget.add(1); err != nil {
						return err
					}
				}
			}
			for _, runtime := range e.Runtime {
				if err := receiptBudget.value(runtime); err != nil {
					return err
				}
				if err := receiptBudget.add(1); err != nil {
					return err
				}
			}
		}
	}
	return retryContext(ctx)
}
