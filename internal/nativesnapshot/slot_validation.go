package nativesnapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/microsoft/waza/internal/controlledprojection"
	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/releasepolicy"
)

func slotContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("static slots require context")
	}
	return ctx.Err()
}

func validateSlotIDs(ids DeclaredEvalIDs) error {
	for _, id := range []string{ids.Baseline, ids.Candidate} {
		if len(id) == 0 || len(id) > maxDeclaredEvalIDBytes || strings.TrimSpace(id) != id {
			return fmt.Errorf("static slots require bounded exact declared eval IDs")
		}
	}
	if ids.Baseline == ids.Candidate {
		return fmt.Errorf("static slot eval IDs must differ")
	}
	return nil
}

func slotPolicy(raw []byte, ids DeclaredEvalIDs) (*releasepolicy.Policy, error) {
	if len(raw) == 0 || len(raw) > maxSlotPolicyBytes {
		return nil, fmt.Errorf("missing or oversized static slot policy")
	}
	if err := validateSlotIDs(ids); err != nil {
		return nil, err
	}
	p, err := releasepolicy.DecodePolicy(raw)
	if err != nil {
		return nil, fmt.Errorf("admitting static slot policy: %w", err)
	}
	if p.Requirements.Assurance || p.Requirements.Runtime || len(p.Requirements.Billing) != 0 {
		return nil, fmt.Errorf("static slots do not support required assurance, runtime or billing")
	}
	return p, nil
}

func ownSlotPolicy(raw []byte, ids DeclaredEvalIDs) ([]byte, *releasepolicy.Policy, error) {
	if len(raw) == 0 || len(raw) > maxSlotPolicyBytes {
		return nil, nil, fmt.Errorf("missing or oversized static slot policy")
	}
	if err := validateSlotIDs(ids); err != nil {
		return nil, nil, err
	}
	owned := bytes.Clone(raw)
	p, err := slotPolicy(owned, ids)
	if err != nil {
		return nil, nil, err
	}
	return owned, p, nil
}

func buildSlotSet(ctx context.Context, raw []byte, input detachedProjectionInput, p *releasepolicy.Policy, ids DeclaredEvalIDs) (*SlotSet, error) {
	projections, err := reconstructProjection(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("reconstructing static slot sources: %w", err)
	}
	if err := compareSourcePolicy(ctx, raw, p, projections); err != nil {
		return nil, err
	}
	// The non-slot envelope is checked before the two-pass slot allocation.
	set := &SlotSet{rawPolicy: raw, input: input, projections: projections, evalIDs: ids}
	remaining, err := slotSetEnvelope(ctx, set)
	if err != nil {
		return nil, err
	}
	slots, err := deriveSlotsBudget(ctx, p, ids, min(maxSlotJSONBudget, remaining))
	if err != nil {
		return nil, err
	}
	set.slots = slots
	seal, err := sealSlotSet(ctx, set, p)
	if err != nil {
		return nil, err
	}
	set.seal = seal
	return set, nil
}

func compareSourcePolicy(ctx context.Context, rawPolicy []byte, p *releasepolicy.Policy, projections map[releasepolicy.Arm]controlledprojection.SourceProjection) error {
	if err := slotContext(ctx); err != nil {
		return err
	}
	if _, err := boundSlotProjections(ctx, projections); err != nil {
		return err
	}
	value, err := jsonutil.Parse(rawPolicy)
	if err != nil {
		return fmt.Errorf("reading original slot policy: %w", err)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("slot policy must be an object")
	}
	arms, ok := object["arms"].(map[string]any)
	if !ok {
		return fmt.Errorf("slot policy arms must be an object")
	}
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		if err := slotContext(ctx); err != nil {
			return err
		}
		rawArm, ok := arms[string(arm)].(map[string]any)
		if !ok {
			return fmt.Errorf("missing original %s plan", arm)
		}
		rawPlan, ok := rawArm["resolved_plan"].(map[string]any)
		if !ok {
			return fmt.Errorf("missing original %s resolved plan", arm)
		}
		fresh, err := json.Marshal(projections[arm].Arm.Plan)
		if err != nil {
			return fmt.Errorf("encoding reconstructed %s plan: %w", arm, err)
		}
		generic, err := jsonutil.Parse(fresh)
		if err != nil {
			return fmt.Errorf("reading reconstructed %s plan: %w", arm, err)
		}
		if err := equalSlotJSON(rawPlan, generic); err != nil {
			return fmt.Errorf("%s complete source plan differs: %w", arm, err)
		}
		digest, err := evidence.JSONDigest(rawPlan)
		if err != nil {
			return fmt.Errorf("digesting original %s plan: %w", arm, err)
		}
		if *digest != p.Arms[arm].Digest || *digest != projections[arm].Arm.Digest {
			return fmt.Errorf("%s source plan digest differs", arm)
		}
	}
	union, err := controlledprojection.GoldenUnion(projections[releasepolicy.Baseline], projections[releasepolicy.Candidate])
	if err != nil {
		return fmt.Errorf("deriving slot golden union: %w", err)
	}
	if err := equalSlotJSON(object["required_golden_task_ids"], union); err != nil {
		return fmt.Errorf("original golden union differs: %w", err)
	}
	if err := equalSlotJSON(p.GoldenIDs, union); err != nil {
		return fmt.Errorf("admitted golden union differs: %w", err)
	}
	return slotContext(ctx)
}

func equalSlotJSON(a, b any) error {
	left, err := json.Marshal(a)
	if err != nil {
		return err
	}
	right, err := json.Marshal(b)
	if err != nil {
		return err
	}
	if !bytes.Equal(left, right) {
		return fmt.Errorf("ordered JSON-v1 values differ")
	}
	return nil
}

// slotBudget charges conservative escaped JSON sizes without marshaling.
type slotBudget struct{ remaining int }

func (b *slotBudget) add(n int) error {
	if n < 0 || n > b.remaining {
		return fmt.Errorf("static slot encoded JSON budget exceeded")
	}
	b.remaining -= n
	return nil
}

func (b *slotBudget) text(value string, limit int) error {
	if len(value) > limit {
		return fmt.Errorf("static slot string exceeds bounded limit")
	}
	if err := b.add(2); err != nil {
		return err
	}
	if len(value) > b.remaining/6 {
		return fmt.Errorf("static slot escaped string budget exceeded")
	}
	return b.add(6 * len(value))
}

func (b *slotBudget) digest(d models.EvidenceDigest) error {
	if err := b.add(32 + 2*16); err != nil {
		return err
	}
	if err := b.text(d.SHA256, 64); err != nil {
		return err
	}
	return b.text(d.Encoding, 16)
}

func (b *slotBudget) slot(ctx context.Context, s Slot) error {
	if err := slotContext(ctx); err != nil {
		return err
	}
	// Two objects, eight fixed fields, three integer scalars, array delimiter.
	if err := b.add(2*32 + 8*16 + 3*20 + 2); err != nil {
		return err
	}
	for _, id := range []string{s.Key.ClusterID, s.Key.TaskID, s.Key.EvalID} {
		if err := b.text(id, maxSlotIdentifierBytes); err != nil {
			return err
		}
	}
	return b.text(string(s.Key.Arm), 16)
}

func derivePossibleSlots(ctx context.Context, p *releasepolicy.Policy, ids DeclaredEvalIDs) ([]Slot, error) {
	return deriveSlotsBudget(ctx, p, ids, maxSlotJSONBudget)
}

func deriveSlotsBudget(ctx context.Context, p *releasepolicy.Policy, ids DeclaredEvalIDs, limit int) ([]Slot, error) {
	if err := slotContext(ctx); err != nil {
		return nil, err
	}
	if err := validateSlotIDs(ids); err != nil {
		return nil, err
	}
	if p == nil || len(p.Arms) != 2 || len(p.Design.Clusters) == 0 || len(p.Design.Clusters) > maxTasks ||
		len(p.Design.Allocation.Assignments) != len(p.Design.Clusters) {
		return nil, fmt.Errorf("incomplete bounded slot allocation")
	}
	settings := make(map[releasepolicy.Arm]map[string]releasepolicy.Settings, 2)
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		plan, ok := p.Arms[arm]
		if !ok || len(plan.Plan.Tasks) == 0 || len(plan.Plan.Tasks) > maxTasks {
			return nil, fmt.Errorf("missing bounded %s slot plan", arm)
		}
		settings[arm] = make(map[string]releasepolicy.Settings, len(plan.Plan.Tasks))
		for _, task := range plan.Plan.Tasks {
			if err := slotContext(ctx); err != nil {
				return nil, err
			}
			if len(task.ID) == 0 || len(task.ID) > maxSlotIdentifierBytes || task.Settings.MaxAttempts < 1 ||
				task.Settings.Engine != "copilot-sdk" {
				return nil, fmt.Errorf("invalid native slot task settings")
			}
			if _, exists := settings[arm][task.ID]; exists {
				return nil, fmt.Errorf("duplicate native slot task")
			}
			settings[arm][task.ID] = task.Settings
		}
	}
	visit := func(accept func(Slot) error) error {
		index, tasks := 0, 0
		seenClusters, seenTasks := map[string]bool{}, map[string]bool{}
		for ci, cluster := range p.Design.Clusters {
			if err := slotContext(ctx); err != nil {
				return err
			}
			if len(cluster.ID) == 0 || len(cluster.ID) > maxSlotIdentifierBytes || seenClusters[cluster.ID] ||
				len(cluster.Tasks) == 0 || len(cluster.Tasks) > maxTasks-tasks {
				return fmt.Errorf("invalid bounded slot cluster")
			}
			seenClusters[cluster.ID] = true
			for _, task := range cluster.Tasks {
				if len(task.ID) == 0 || len(task.ID) > maxSlotIdentifierBytes || seenTasks[task.ID] {
					return fmt.Errorf("invalid duplicate slot allocation task")
				}
				seenTasks[task.ID] = true
			}
			tasks += len(cluster.Tasks)
			order, err := releasepolicy.ArmOrder(p.Design.Allocation.Seed, cluster.ID)
			if err != nil {
				return err
			}
			assignment := p.Design.Allocation.Assignments[ci]
			if assignment.ClusterID != cluster.ID || !slices.Equal(order, assignment.Order) {
				return fmt.Errorf("slot allocation order differs from seeded order")
			}
			for _, arm := range order {
				eval := ids.Baseline
				if arm == releasepolicy.Candidate {
					eval = ids.Candidate
				}
				for _, task := range cluster.Tasks {
					setting, ok := settings[arm][task.ID]
					if !ok || len(task.TrialOrdinals) == 0 || len(task.TrialOrdinals) > maxPossibleSlots ||
						setting.TrialsPerTask != len(task.TrialOrdinals) {
						return fmt.Errorf("slot task or trial settings differ")
					}
					for ti, trial := range task.TrialOrdinals {
						if trial != ti+1 {
							return fmt.Errorf("invalid stored slot trial ordinal")
						}
						for attempt := 1; ; attempt++ {
							if err := slotContext(ctx); err != nil {
								return err
							}
							// Stop before incrementing even for a maximum-int setting.
							if index >= maxPossibleSlots {
								return fmt.Errorf("possible slot count exceeds bounded limit")
							}
							key := releasepolicy.AttemptKey{SampleKey: releasepolicy.SampleKey{
								ClusterID: cluster.ID, TaskID: task.ID, Trial: trial}, Arm: arm, EvalID: eval, Attempt: attempt}
							if err := accept(Slot{Index: index, Key: key}); err != nil {
								return err
							}
							index++
							if attempt == setting.MaxAttempts {
								break
							}
						}
					}
				}
			}
		}
		for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
			if tasks != len(settings[arm]) {
				return fmt.Errorf("slot plan task multiplicity differs")
			}
		}
		return nil
	}
	count := 0
	budget := slotBudget{min(maxSlotJSONBudget, limit)}
	if err := budget.add(32); err != nil {
		return nil, err
	}
	if err := visit(func(s Slot) error {
		if err := budget.slot(ctx, s); err != nil {
			return err
		}
		count++
		return nil
	}); err != nil {
		return nil, err
	}
	result := make([]Slot, count)
	filled := 0
	if err := visit(func(s Slot) error {
		if filled >= count || s.Index != filled {
			return fmt.Errorf("slot enumeration changed between passes")
		}
		result[filled] = s
		filled++
		return nil
	}); err != nil {
		return nil, err
	}
	if filled != count {
		return nil, fmt.Errorf("slot enumeration count changed")
	}
	if err := slotContext(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

func boundSlotProjections(ctx context.Context, projections map[releasepolicy.Arm]controlledprojection.SourceProjection) (int, error) {
	if err := slotContext(ctx); err != nil {
		return 0, err
	}
	b := slotBudget{maxProjectionJSONBudget}
	if len(projections) != 2 {
		return 0, fmt.Errorf("static slots require both projections")
	}
	if err := b.add(32 + 2*16); err != nil {
		return 0, err
	}
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		projection, ok := projections[arm]
		plan := projection.Arm.Plan
		if !ok || len(plan.Tasks) == 0 || len(plan.Tasks) > maxTasks ||
			len(plan.Identities) > 6*maxTasks+3 || len(projection.GoldenIDs) > maxTasks {
			return 0, fmt.Errorf("static slot projection count exceeds bounded limit")
		}
		if err := b.add(4*32 + 12*16 + 3*32); err != nil {
			return 0, err
		}
		for _, text := range []string{string(arm), plan.Kind, plan.Version} {
			if err := b.text(text, maxSlotIdentifierBytes); err != nil {
				return 0, err
			}
		}
		if err := b.digest(projection.Arm.Digest); err != nil {
			return 0, err
		}
		for _, task := range plan.Tasks {
			if err := slotContext(ctx); err != nil {
				return 0, err
			}
			if err := b.add(3*32 + 20*16 + 5*20 + 2); err != nil {
				return 0, err
			}
			s, r := task.Settings, task.ExpectedRuntime
			for _, text := range []string{task.ID, s.Engine, s.Model, s.ReasoningEffort, s.JudgeModel,
				s.JudgeReasoningEffort, r.Availability, r.EngineImplementation, r.ModelVersion, r.Reason} {
				if err := b.text(text, maxSlotIdentifierBytes); err != nil {
					return 0, err
				}
			}
			if err := b.digest(s.OtherSettingsDigest); err != nil {
				return 0, err
			}
		}
		for _, identity := range plan.Identities {
			if err := slotContext(ctx); err != nil {
				return 0, err
			}
			if err := b.add(32 + 5*16 + 2); err != nil {
				return 0, err
			}
			for _, text := range []string{identity.Domain, identity.TaskID, identity.Availability, identity.Reason} {
				if err := b.text(text, maxSlotIdentifierBytes); err != nil {
					return 0, err
				}
			}
			if identity.Digest != nil {
				if err := b.digest(*identity.Digest); err != nil {
					return 0, err
				}
			}
		}
		for _, id := range projection.GoldenIDs {
			if err := slotContext(ctx); err != nil {
				return 0, err
			}
			if err := b.add(2); err != nil {
				return 0, err
			}
			if err := b.text(id, maxSlotIdentifierBytes); err != nil {
				return 0, err
			}
		}
	}
	return maxProjectionJSONBudget - b.remaining, nil
}

type slotSourceArm struct {
	Arm     releasepolicy.Arm    `json:"arm"`
	Entries []slotSourceIdentity `json:"entries"`
}

type slotSourceIdentity struct {
	Path         string                `json:"path"`
	Roles        []string              `json:"roles"`
	ByteLength   int                   `json:"byte_length"`
	SourceDigest models.EvidenceDigest `json:"source_digest"`
}

func slotSetEnvelope(ctx context.Context, set *SlotSet) (int, error) {
	if err := slotContext(ctx); err != nil {
		return 0, err
	}
	if set == nil || len(set.rawPolicy) == 0 || len(set.rawPolicy) > maxSlotPolicyBytes || len(set.slots) > maxPossibleSlots {
		return 0, fmt.Errorf("missing or oversized static slot set")
	}
	projectionBytes, err := boundSlotProjections(ctx, set.projections)
	if err != nil {
		return 0, err
	}
	b := slotBudget{maxSetSealJSONBudget}
	if err := b.add(32 + 10*16 + projectionBytes + 3*32); err != nil {
		return 0, err
	}
	for _, d := range []models.EvidenceDigest{set.seal, set.input.snapshot, set.input.associations.seal} {
		if err := b.digest(d); err != nil {
			return 0, err
		}
	}
	for _, id := range []string{set.evalIDs.Baseline, set.evalIDs.Candidate} {
		if err := b.text(id, maxDeclaredEvalIDBytes); err != nil {
			return 0, err
		}
	}
	if err := b.text(slotSetDomain, maxSlotIdentifierBytes); err != nil {
		return 0, err
	}
	// Fixed raw-policy, admitted policy and source-table digests.
	if err := b.add(3 * (32 + 2*16 + 2 + 6*64 + 2 + 6*16)); err != nil {
		return 0, err
	}
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		for _, id := range set.projections[arm].GoldenIDs {
			if err := b.add(2); err != nil {
				return 0, err
			}
			if err := b.text(id, maxSlotIdentifierBytes); err != nil {
				return 0, err
			}
		}
	}
	// Also charge the separately digested source table to this envelope.
	if err := preflightSlotInput(ctx, set.input, &b); err != nil {
		return 0, err
	}
	return b.remaining, nil
}

func preflightSlotSet(ctx context.Context, set *SlotSet) error {
	remaining, err := slotSetEnvelope(ctx, set)
	if err != nil {
		return err
	}
	b := slotBudget{min(maxSlotJSONBudget, remaining)}
	if err := b.add(32); err != nil {
		return err
	}
	for _, s := range set.slots {
		if err := b.slot(ctx, s); err != nil {
			return err
		}
	}
	return slotContext(ctx)
}

func preflightSlotInput(ctx context.Context, input detachedProjectionInput, b *slotBudget) error {
	if len(input.canonical) == 0 || len(input.canonical) > maxProjectionBytes ||
		len(input.associations.canonical) == 0 || len(input.associations.canonical) > maxAssociationBytes || len(input.sources) != 2 {
		return fmt.Errorf("invalid bounded slot capture")
	}
	total := 0
	if err := b.add(32); err != nil {
		return err
	}
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		inventory, ok := input.sources[arm]
		if !ok || len(inventory) == 0 || len(inventory) > maxAssociationEvents {
			return fmt.Errorf("incomplete bounded slot sources")
		}
		if err := b.add(32 + 2*16 + 32 + 2); err != nil {
			return err
		}
		if err := b.text(string(arm), 16); err != nil {
			return err
		}
		for _, entry := range inventory {
			if err := slotContext(ctx); err != nil {
				return err
			}
			if len(entry.Roles) > maxAssociationEvents {
				return fmt.Errorf("oversized slot source roles")
			}
			limit := maxSourceBytes
			if slices.Contains(entry.Roles, "executable") {
				limit = maxExecutableBytes
			}
			if len(entry.Bytes) > limit || len(entry.Bytes) > maxTotalBytes-total {
				return fmt.Errorf("oversized slot source bytes")
			}
			total += len(entry.Bytes)
			if err := b.add(32 + 4*16 + 20 + 32 + 2); err != nil {
				return err
			}
			if err := b.text(entry.Path, maxAssociationPath); err != nil {
				return err
			}
			if err := b.digest(entry.Digest); err != nil {
				return err
			}
			for _, role := range entry.Roles {
				if err := slotContext(ctx); err != nil {
					return err
				}
				if err := b.add(2); err != nil {
					return err
				}
				if err := b.text(role, maxAssociationPath); err != nil {
					return err
				}
			}
		}
	}
	return slotContext(ctx)
}

func sealSlotSet(ctx context.Context, set *SlotSet, p *releasepolicy.Policy) (models.EvidenceDigest, error) {
	if err := preflightSlotSet(ctx, set); err != nil {
		return models.EvidenceDigest{}, err
	}
	sources := make([]slotSourceArm, 0, 2)
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		entries := make([]slotSourceIdentity, 0, len(set.input.sources[arm]))
		for _, entry := range set.input.sources[arm] {
			if err := slotContext(ctx); err != nil {
				return models.EvidenceDigest{}, err
			}
			entries = append(entries, slotSourceIdentity{entry.Path, entry.Roles, len(entry.Bytes), entry.Digest})
		}
		sources = append(sources, slotSourceArm{arm, entries})
	}
	sourceSeal, err := evidence.JSONDigest(sources)
	if err != nil {
		return models.EvidenceDigest{}, fmt.Errorf("sealing slot source identities: %w", err)
	}
	union, err := controlledprojection.GoldenUnion(set.projections[releasepolicy.Baseline], set.projections[releasepolicy.Candidate])
	if err != nil {
		return models.EvidenceDigest{}, fmt.Errorf("deriving sealed slot golden union: %w", err)
	}
	seal, err := evidence.JSONDigest(slotSetSeal{slotSetDomain, releasepolicy.SourceDigest(set.rawPolicy), p.Digest,
		set.input.snapshot, set.input.associations.seal, *sourceSeal, set.projections, union, set.evalIDs, set.slots})
	if err != nil {
		return models.EvidenceDigest{}, fmt.Errorf("sealing static slots: %w", err)
	}
	if err := slotContext(ctx); err != nil {
		return models.EvidenceDigest{}, err
	}
	return *seal, nil
}

func revalidateSlotSet(ctx context.Context, set *SlotSet) (*SlotSet, error) {
	if err := preflightSlotSet(ctx, set); err != nil {
		return nil, err
	}
	raw, p, err := ownSlotPolicy(set.rawPolicy, set.evalIDs)
	if err != nil {
		return nil, err
	}
	input, err := validateProjectionInput(ctx, set.input)
	if err != nil {
		return nil, fmt.Errorf("revalidating slot capture: %w", err)
	}
	fresh, err := buildSlotSet(ctx, raw, input, p, set.evalIDs)
	if err != nil {
		return nil, err
	}
	if err := equalSlotJSON(set.projections, fresh.projections); err != nil {
		return nil, fmt.Errorf("retained slot projections changed: %w", err)
	}
	if !slices.Equal(set.slots, fresh.slots) || (set.slots == nil) != (fresh.slots == nil) {
		return nil, fmt.Errorf("retained slot manifest changed")
	}
	if set.seal != fresh.seal {
		return nil, fmt.Errorf("static slot set seal changed")
	}
	if err := slotContext(ctx); err != nil {
		return nil, err
	}
	return fresh, nil
}

func pairSlotJoined(ctx context.Context, set *SlotSet, joined *Joined) error {
	if err := slotContext(ctx); err != nil {
		return err
	}
	if joined == nil || len(joined.canonical) != len(set.input.canonical) ||
		len(joined.canonical) > maxProjectionBytes || len(joined.sources) != 2 {
		return fmt.Errorf("retained primitive join shape differs from slot capture")
	}
	b := slotBudget{64 << 10}
	if err := b.add(32 + 6*16); err != nil {
		return err
	}
	if err := b.slot(ctx, Slot{Key: joined.binding.Key}); err != nil {
		return err
	}
	if err := b.text(joined.binding.CollectionID, 64); err != nil {
		return err
	}
	for _, d := range []models.EvidenceDigest{joined.snapshot, joined.seal, joined.binding.PolicyDigest,
		joined.binding.ProfileDigest, joined.binding.PlanDigest, joined.binding.RequestDigest} {
		if err := b.digest(d); err != nil {
			return err
		}
	}
	if joined.snapshot != set.input.snapshot || !bytes.Equal(joined.canonical, set.input.canonical) {
		return fmt.Errorf("primitive join full snapshot differs from slot capture")
	}
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		actual, ok := joined.sources[arm]
		expected := set.input.sources[arm]
		if !ok || len(actual) != len(expected) {
			return fmt.Errorf("primitive join arm inventory differs from slot capture")
		}
		for i, entry := range actual {
			if err := slotContext(ctx); err != nil {
				return err
			}
			want := expected[i]
			if len(entry.Path) != len(want.Path) || len(entry.Roles) != len(want.Roles) || len(entry.Bytes) != len(want.Bytes) {
				return fmt.Errorf("primitive join source shape differs from slot capture")
			}
			for ri, role := range entry.Roles {
				if err := slotContext(ctx); err != nil {
					return err
				}
				if len(role) != len(want.Roles[ri]) || role != want.Roles[ri] {
					return fmt.Errorf("primitive join source roles differ from slot capture")
				}
			}
			if len(entry.Digest.SHA256) > 64 || len(entry.Digest.Encoding) > 16 ||
				entry.Path != want.Path || entry.Digest != want.Digest || !bytes.Equal(entry.Bytes, want.Bytes) {
				return fmt.Errorf("primitive join full raw source differs from slot capture")
			}
		}
	}
	return slotContext(ctx)
}
