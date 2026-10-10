package nativesnapshot

import (
	"context"
	"fmt"
	"slices"

	"github.com/microsoft/waza/internal/controlledprojection"
	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/nativetask"
	"github.com/microsoft/waza/internal/releasepolicy"
)

const (
	maxPossibleSlots        = 65536
	maxSlotPolicyBytes      = 16 << 20
	maxDeclaredEvalIDBytes  = 4096
	maxSlotIdentifierBytes  = 4096
	maxSlotJSONBudget       = 64 << 20
	maxSetSealJSONBudget    = 128 << 20
	maxProjectionJSONBudget = 16 << 20
	slotSetDomain           = "waza.internal.native-static-slots.v1"
	slotJoinDomain          = "waza.internal.native-static-slot-join.v1"
)

type DeclaredEvalIDs struct {
	Baseline  string `json:"baseline"`
	Candidate string `json:"candidate"`
}

type Slot struct {
	Index int                      `json:"index"`
	Key   releasepolicy.AttemptKey `json:"key"`
}

// SlotSet is a root-free static manifest, not execution or retry authority.
type SlotSet struct {
	rawPolicy   []byte
	input       detachedProjectionInput
	projections map[releasepolicy.Arm]controlledprojection.SourceProjection
	evalIDs     DeclaredEvalIDs
	slots       []Slot
	seal        models.EvidenceDigest
}

type slotSetSeal struct {
	Domain       string                                                      `json:"domain"`
	RawPolicy    models.EvidenceDigest                                       `json:"raw_policy"`
	Policy       models.EvidenceDigest                                       `json:"policy"`
	Snapshot     models.EvidenceDigest                                       `json:"snapshot"`
	Associations models.EvidenceDigest                                       `json:"associations"`
	Sources      models.EvidenceDigest                                       `json:"sources"`
	Projections  map[releasepolicy.Arm]controlledprojection.SourceProjection `json:"projections"`
	GoldenUnion  []string                                                    `json:"golden_union"`
	EvalIDs      DeclaredEvalIDs                                             `json:"eval_ids"`
	Slots        []Slot                                                      `json:"slots"`
}

type SlotJoined struct {
	set     *SlotSet
	setSeal models.EvidenceDigest
	slot    Slot
	joined  *Joined
	seal    models.EvidenceDigest
}

type slotJoinSeal struct {
	Domain        string                `json:"domain"`
	Set           models.EvidenceDigest `json:"set"`
	Slot          Slot                  `json:"slot"`
	Snapshot      models.EvidenceDigest `json:"snapshot"`
	PrimitiveJoin models.EvidenceDigest `json:"primitive_join"`
	Admission     nativetask.Admission  `json:"admission"`
}

// PrepareSlotSet accepts only the distinct associated capture, never Prepared.
func PrepareSlotSet(ctx context.Context, rawPolicy []byte, capture *projectionCapture, evalIDs DeclaredEvalIDs) (*SlotSet, error) {
	if err := slotContext(ctx); err != nil {
		return nil, err
	}
	raw, p, err := ownSlotPolicy(rawPolicy, evalIDs)
	if err != nil {
		return nil, err
	}
	input, err := detachProjectionInput(ctx, capture)
	if err != nil {
		return nil, fmt.Errorf("detaching slot capture: %w", err)
	}
	return buildSlotSet(ctx, raw, input, p, evalIDs)
}

func (s *SlotSet) Slots(ctx context.Context) ([]Slot, error) {
	fresh, err := revalidateSlotSet(ctx, s)
	if err != nil {
		return nil, err
	}
	result := slices.Clone(fresh.slots)
	if err := slotContext(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

// CheckSlotJoin inspects one independently admitted primitive at a derived index.
func CheckSlotJoin(ctx context.Context, set *SlotSet, index int, admitted *nativetask.Admitted) (*SlotJoined, error) {
	if err := slotContext(ctx); err != nil {
		return nil, err
	}
	if index < 0 || admitted == nil {
		return nil, fmt.Errorf("missing slot admission or negative index")
	}
	fresh, err := revalidateSlotSet(ctx, set)
	if err != nil {
		return nil, err
	}
	if index >= len(fresh.slots) {
		return nil, fmt.Errorf("slot index outside derived manifest")
	}
	slot := fresh.slots[index]
	if err := slotContext(ctx); err != nil {
		return nil, err
	}
	admission, err := admitted.Binding()
	if err != nil {
		return nil, fmt.Errorf("reading slot admission: %w", err)
	}
	if err := slotContext(ctx); err != nil {
		return nil, err
	}
	if err := matchSlotAdmission(fresh, slot, admission); err != nil {
		return nil, err
	}
	// Only freshly G2-validated input supplies this ephemeral primitive adapter.
	// CheckPrimitiveJoin owns its result; no Prepared or root is retained here.
	adapter := &Prepared{canonical: fresh.input.canonical, seal: fresh.input.snapshot, sources: fresh.input.sources}
	joined, err := CheckPrimitiveJoin(ctx, adapter, admitted, slot.Key)
	if err != nil {
		return nil, fmt.Errorf("joining static slot: %w", err)
	}
	if err := pairSlotJoined(ctx, fresh, joined); err != nil {
		return nil, err
	}
	actual, err := joined.Binding()
	if err != nil {
		return nil, fmt.Errorf("reading primitive join: %w", err)
	}
	if err := slotContext(ctx); err != nil {
		return nil, err
	}
	if actual != admission {
		return nil, fmt.Errorf("slot admission changed during join")
	}
	if err := pairSlotJoined(ctx, fresh, joined); err != nil {
		return nil, err
	}
	result := &SlotJoined{set: fresh, setSeal: fresh.seal, slot: slot, joined: joined}
	seal, err := digestSlotJoin(ctx, result, actual)
	if err != nil {
		return nil, err
	}
	result.seal = seal
	return result, nil
}

func (j *SlotJoined) Binding(ctx context.Context) (Slot, nativetask.Admission, error) {
	fail := func(err error) (Slot, nativetask.Admission, error) {
		return Slot{}, nativetask.Admission{}, err
	}
	if err := slotContext(ctx); err != nil {
		return fail(err)
	}
	if j == nil {
		return fail(fmt.Errorf("missing static slot join"))
	}
	fresh, err := revalidateSlotSet(ctx, j.set)
	if err != nil {
		return fail(err)
	}
	index := j.slot.Index
	if j.setSeal != fresh.seal || index < 0 || index >= len(fresh.slots) || j.slot != fresh.slots[index] {
		return fail(fmt.Errorf("static slot join manifest changed"))
	}
	// Bound and pair retained state before the existing no-context Binding.
	if err := pairSlotJoined(ctx, fresh, j.joined); err != nil {
		return fail(err)
	}
	admission, err := j.joined.Binding()
	if err != nil {
		return fail(fmt.Errorf("reading retained primitive join: %w", err))
	}
	if err := slotContext(ctx); err != nil {
		return fail(err)
	}
	if err := matchSlotAdmission(fresh, j.slot, admission); err != nil {
		return fail(err)
	}
	if err := pairSlotJoined(ctx, fresh, j.joined); err != nil {
		return fail(err)
	}
	seal, err := digestSlotJoin(ctx, j, admission)
	if err != nil {
		return fail(err)
	}
	if seal != j.seal {
		return fail(fmt.Errorf("static slot join seal changed"))
	}
	return j.slot, admission, nil
}

func matchSlotAdmission(set *SlotSet, slot Slot, admission nativetask.Admission) error {
	p, err := slotPolicy(set.rawPolicy, set.evalIDs)
	if err != nil {
		return err
	}
	if admission.Key != slot.Key || admission.PolicyDigest != p.Digest ||
		admission.PlanDigest != set.projections[slot.Key.Arm].Arm.Digest {
		return fmt.Errorf("primitive admission differs from derived slot policy, plan or key")
	}
	return nil
}

func digestSlotJoin(ctx context.Context, j *SlotJoined, admission nativetask.Admission) (models.EvidenceDigest, error) {
	if err := slotContext(ctx); err != nil {
		return models.EvidenceDigest{}, err
	}
	digest, err := evidence.JSONDigest(slotJoinSeal{slotJoinDomain, j.setSeal, j.slot, j.joined.snapshot, j.joined.seal, admission})
	if err != nil {
		return models.EvidenceDigest{}, fmt.Errorf("sealing static slot join: %w", err)
	}
	if err := slotContext(ctx); err != nil {
		return models.EvidenceDigest{}, err
	}
	return *digest, nil
}
