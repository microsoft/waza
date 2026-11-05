package nativesnapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/nativetask"
	"github.com/microsoft/waza/internal/releasepolicy"
)

const retryJoinDomain = "waza.internal.native-retry-join.v1"

type SuppliedTape struct {
	Present bool                   `json:"present"`
	Digest  *models.EvidenceDigest `json:"digest"`
}

type JoinedAttempt struct {
	Slot        Slot                          `json:"slot"`
	Core        *releasepolicy.AttemptSummary `json:"core_terminal"`
	NativeState string                        `json:"native_state"`
	Admission   *nativetask.Admission         `json:"admission"`
	Native      *nativetask.Record            `json:"native_record"`
}

// RetryJoinView reports supplied consistency only. There is deliberately no
// pass/permission field, usage total or inferred runtime/assurance capability.
type RetryJoinView struct {
	State                  string                 `json:"state"`
	CoreState              string                 `json:"core_state"`
	CoreMode               string                 `json:"core_mode"`
	CollectionID           string                 `json:"collection_id"`
	CoreStream             *models.EvidenceDigest `json:"core_stream"`
	CoreJournal            *models.EvidenceDigest `json:"core_journal"`
	TrailingFragmentBytes  int                    `json:"trailing_fragment_bytes"`
	CollectionEndValidated bool                   `json:"collection_end_validated"`
	NativeEvents           SuppliedTape           `json:"native_events"`
	NativeRows             SuppliedTape           `json:"native_rows"`
	Attempts               []JoinedAttempt        `json:"attempts"`
}

// RetryJoined retains no roots or execution capabilities. Callers must
// independently recheck source currentness; supplied order is not fsync proof.
type RetryJoined struct {
	set        *SlotSet
	retry      *releasepolicy.RetryPrefix
	events     []byte
	rows       []byte
	admissions []*nativetask.Admitted
	seal       models.EvidenceDigest
}

type retryJoinSeal struct {
	Domain       string                 `json:"domain"`
	Set          models.EvidenceDigest  `json:"set"`
	Retry        models.EvidenceDigest  `json:"retry"`
	NativeEvents SuppliedTape           `json:"native_events"`
	NativeRows   SuppliedTape           `json:"native_rows"`
	Admissions   []nativetask.Admission `json:"admissions"`
}

// CheckRetryJoin joins actual supplied starts, not every possible policy slot.
// Admissions must cover exactly the ordered native starts. Missing native
// evidence stays incomplete; contradictory or torn evidence returns no handle.
func CheckRetryJoin(ctx context.Context, set *SlotSet, retry *releasepolicy.RetryPrefix, events, rows []byte, admissions []*nativetask.Admitted) (*RetryJoined, error) {
	if err := slotContext(ctx); err != nil {
		return nil, err
	}
	// Bound and admit before copying any caller-sized bytes or slice.
	native, err := nativetask.InspectPrefix(ctx, events, rows, admissions)
	if err != nil {
		return nil, err
	}
	fresh, err := revalidateSlotSet(ctx, set)
	if err != nil {
		return nil, err
	}
	j := &RetryJoined{set: fresh, retry: retry, events: bytes.Clone(events),
		rows: bytes.Clone(rows), admissions: slices.Clone(admissions)}
	_, seal, err := j.derive(ctx, fresh, native)
	if err != nil {
		return nil, err
	}
	j.seal = seal
	return j, nil
}

func (j *RetryJoined) View(ctx context.Context) (RetryJoinView, error) {
	if err := slotContext(ctx); err != nil {
		return RetryJoinView{}, err
	}
	if j == nil {
		return RetryJoinView{}, fmt.Errorf("missing native retry join")
	}
	native, err := nativetask.InspectPrefix(ctx, j.events, j.rows, j.admissions)
	if err != nil {
		return RetryJoinView{}, err
	}
	fresh, err := revalidateSlotSet(ctx, j.set)
	if err != nil {
		return RetryJoinView{}, err
	}
	v, seal, err := j.derive(ctx, fresh, native)
	if err != nil {
		return RetryJoinView{}, err
	}
	if seal != j.seal {
		return RetryJoinView{}, fmt.Errorf("native retry join seal changed")
	}
	return v, nil
}

func suppliedTape(raw []byte) SuppliedTape {
	if raw == nil {
		return SuppliedTape{}
	}
	d := releasepolicy.SourceDigest(raw)
	return SuppliedTape{Present: true, Digest: &d}
}

// Only freshly revalidated canonical input reaches this per-derivation index.
// Decode the full capture once, never once per distinct started task.
func retryJoinRequests(ctx context.Context, canonical []byte) (map[releasepolicy.Arm]map[string]*execution.ExecutionRequest, error) {
	if err := slotContext(ctx); err != nil {
		return nil, err
	}
	var snapshots map[releasepolicy.Arm]armSnapshot
	if err := json.Unmarshal(canonical, &snapshots); err != nil {
		return nil, fmt.Errorf("decoding retained retry capture: %w", err)
	}
	requests := make(map[releasepolicy.Arm]map[string]*execution.ExecutionRequest, len(snapshots))
	for arm, snapshot := range snapshots {
		requests[arm] = make(map[string]*execution.ExecutionRequest, len(snapshot.Tasks))
		for _, task := range snapshot.Tasks {
			if err := slotContext(ctx); err != nil {
				return nil, err
			}
			if !task.Enabled {
				continue
			}
			request, err := decodeSnapshotRequest(task.Request)
			if err != nil {
				return nil, fmt.Errorf("decoding retained retry request %s/%s: %w", arm, task.ID, err)
			}
			requests[arm][task.ID] = request
		}
	}
	return requests, nil
}

func (j *RetryJoined) derive(ctx context.Context, set *SlotSet, native *nativetask.Prefix) (RetryJoinView, models.EvidenceDigest, error) {
	fail := func(err error) (RetryJoinView, models.EvidenceDigest, error) {
		return RetryJoinView{}, models.EvidenceDigest{}, err
	}
	core, retrySeal, err := j.retry.ViewForPolicy(ctx, set.rawPolicy)
	if err != nil {
		return fail(err)
	}
	policy, err := releasepolicy.DecodePolicy(set.rawPolicy)
	if err != nil {
		return fail(err)
	}
	for arm, id := range core.EvalIDs {
		want := set.evalIDs.Baseline
		if arm == releasepolicy.Candidate {
			want = set.evalIDs.Candidate
		}
		if id != want {
			return fail(fmt.Errorf("retry arm eval identity differs from static slots"))
		}
	}
	index := make(map[releasepolicy.AttemptKey]Slot, len(set.slots))
	for _, slot := range set.slots {
		if err := slotContext(ctx); err != nil {
			return fail(err)
		}
		index[slot.Key] = slot
	}
	v := RetryJoinView{State: "incomplete", CoreState: core.State, CoreMode: core.Mode,
		CollectionID: core.CollectionID, CoreStream: core.Stream, CoreJournal: core.Journal,
		TrailingFragmentBytes: core.TrailingFragmentBytes, CollectionEndValidated: core.CollectionEndValidated,
		NativeEvents: suppliedTape(j.events), NativeRows: suppliedTape(j.rows), Attempts: []JoinedAttempt{}}
	for _, event := range core.Events {
		if err := slotContext(ctx); err != nil {
			return fail(err)
		}
		switch event.Type {
		case "attempt_start":
			slot, ok := index[*event.Key]
			if !ok {
				return fail(fmt.Errorf("retry start absent from static source slots"))
			}
			v.Attempts = append(v.Attempts, JoinedAttempt{Slot: slot, NativeState: "missing"})
		case "attempt_terminal":
			if len(v.Attempts) == 0 || v.Attempts[len(v.Attempts)-1].Slot.Key != event.Attempt.Key {
				return fail(fmt.Errorf("retry terminal lacks its matching start"))
			}
			v.Attempts[len(v.Attempts)-1].Core = event.Attempt
		}
	}
	if len(j.admissions) > len(v.Attempts) {
		return fail(fmt.Errorf("native starts exceed accepted core starts"))
	}
	bindings := make([]nativetask.Admission, 0, len(j.admissions))
	budget := slotBudget{remaining: maxSlotJSONBudget}
	requests, err := retryJoinRequests(ctx, set.input.canonical)
	if err != nil {
		return fail(err)
	}
	sources := map[releasepolicy.Arm][]nativetask.CapturedSource{}
	for i, admitted := range j.admissions {
		if err := slotContext(ctx); err != nil {
			return fail(err)
		}
		binding, err := admitted.Binding()
		if err != nil {
			return fail(fmt.Errorf("reading native retry admission: %w", err))
		}
		actual := &v.Attempts[i]
		key := actual.Slot.Key
		if binding.Key != key || binding.PolicyDigest != core.Policy ||
			binding.CollectionID != core.CollectionID || binding.CollectionID != binding.ProfileDigest.SHA256 ||
			binding.PlanDigest != set.projections[key.Arm].Arm.Digest ||
			(i > 0 && binding.ProfileDigest != bindings[0].ProfileDigest) {
			return fail(fmt.Errorf("native admission differs from retry policy, profile, order or source plan"))
		}
		if err := budget.slot(ctx, actual.Slot); err != nil {
			return fail(err)
		}
		for _, digest := range []models.EvidenceDigest{binding.PolicyDigest, binding.ProfileDigest, binding.PlanDigest, binding.RequestDigest} {
			if err := budget.digest(digest); err != nil {
				return fail(err)
			}
		}
		if err := budget.text(binding.CollectionID, 64); err != nil {
			return fail(err)
		}
		if sources[key.Arm] == nil {
			for _, source := range set.input.sources[key.Arm] {
				sources[key.Arm] = append(sources[key.Arm], nativetask.CapturedSource{
					Path: source.Path, Bytes: string(source.Bytes), Digest: source.Digest})
			}
		}
		request := requests[key.Arm][key.TaskID]
		if request == nil {
			return fail(fmt.Errorf("native selected retry task %q is unavailable", key.TaskID))
		}
		if err := nativetask.MatchSnapshot(ctx, admitted, key, request, sources[key.Arm]); err != nil {
			return fail(fmt.Errorf("matching retry source/request: %w", err))
		}
		actual.Admission = &binding
		actual.NativeState = "pending"
		if i < len(native.Records) {
			record := native.Records[i]
			if err := releasepolicy.ValidateAttempt(policy, record.Summary); err != nil {
				return fail(fmt.Errorf("native terminal violates frozen retry policy: %w", err))
			}
			if actual.Core != nil {
				if err := equalSlotJSON(record.Summary, *actual.Core); err != nil {
					return fail(fmt.Errorf("native/core complete terminal summary differs: %w", err))
				}
			}
			actual.Native, actual.NativeState = &record, "terminal"
		}
		bindings = append(bindings, binding)
	}
	if core.CollectionEndValidated && v.NativeEvents.Present && v.NativeRows.Present &&
		len(native.Records) == len(v.Attempts) {
		v.State = "structurally_matched"
	}
	if err := slotContext(ctx); err != nil {
		return fail(err)
	}
	seal, err := evidence.JSONDigest(retryJoinSeal{retryJoinDomain, set.seal, retrySeal,
		v.NativeEvents, v.NativeRows, bindings})
	if err != nil {
		return fail(fmt.Errorf("sealing native retry join: %w", err))
	}
	if err := slotContext(ctx); err != nil {
		return fail(err)
	}
	return v, *seal, nil
}
