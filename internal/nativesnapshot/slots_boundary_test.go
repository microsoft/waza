package nativesnapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/assurance"
	"github.com/microsoft/waza/internal/controlledprojection"
	"github.com/microsoft/waza/internal/nativetask"
	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/stretchr/testify/require"
)

func freshSlotSet(t *testing.T, set *SlotSet) *SlotSet {
	t.Helper()
	fresh, err := revalidateSlotSet(context.Background(), set)
	require.NoError(t, err)
	return fresh
}

func resealTestSlotSet(t *testing.T, set *SlotSet) {
	t.Helper()
	p, err := releasepolicy.DecodePolicy(set.rawPolicy)
	require.NoError(t, err)
	seal, err := sealSlotSet(context.Background(), set, p)
	require.NoError(t, err)
	set.seal = seal
}

func cloneSlotCapture(t *testing.T, f slotFixtureResult) *projectionCapture {
	t.Helper()
	input, err := detachProjectionInput(context.Background(), f.capture)
	require.NoError(t, err)
	return &projectionCapture{prepared: &Prepared{canonical: input.canonical, seal: input.snapshot, sources: input.sources},
		associations: input.associations}
}

func TestSlotSetCount65536And65537WithoutProductOverflow(t *testing.T) {
	f := slotFixture(t, "file")
	p, err := releasepolicy.DecodePolicy(f.raw)
	require.NoError(t, err)
	// Actual complete original G1 plans are the base for this offline helper
	// fixture; only effective counts change. This is not source admission.
	ids := DeclaredEvalIDs{Baseline: "b", Candidate: "c"}
	p.Design.Clusters[0].ID = "c"
	order, err := releasepolicy.ArmOrder(p.Design.Allocation.Seed, "c")
	require.NoError(t, err)
	p.Design.Allocation.Assignments[0] = releasepolicy.Assignment{ClusterID: "c", Order: order}
	setAttempts := func(b, c int) {
		for arm, count := range map[releasepolicy.Arm]int{releasepolicy.Baseline: b, releasepolicy.Candidate: c} {
			a := p.Arms[arm]
			a.Plan.Tasks[0].Settings.MaxAttempts = count
			p.Arms[arm] = a
		}
	}
	setAttempts(32768, 32768)
	slots, err := derivePossibleSlots(context.Background(), p, ids)
	require.NoError(t, err)
	require.Len(t, slots, 65536)
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		a := p.Arms[arm]
		a.Digest = slotTestDigest(t, a.Plan)
		p.Arms[arm] = a
	}
	raw, err := releasepolicy.SealJSON(p)
	require.NoError(t, err)
	require.Equal(t, originalSlotOrder(t, raw, ids), slots, "every maximum-count key has an independent collector-order oracle")
	b := slotBudget{maxSlotJSONBudget}
	require.NoError(t, b.add(32))
	for i, slot := range slots {
		require.Equal(t, i, slot.Index)
		require.NoError(t, b.slot(context.Background(), slot))
	}
	helperSet := freshSlotSet(t, f.set)
	helperSet.slots = slots
	require.NoError(t, preflightSlotSet(context.Background(), helperSet), "full 128MiB envelope also fits")
	setAttempts(32769, 32768)
	slots, err = derivePossibleSlots(context.Background(), p, ids)
	require.ErrorContains(t, err, "count")
	require.Nil(t, slots)
	setAttempts(int(^uint(0)>>1), int(^uint(0)>>1))
	slots, err = derivePossibleSlots(context.Background(), p, ids)
	require.ErrorContains(t, err, "count")
	require.Nil(t, slots)
	setAttempts(32768, 32768)
	for _, id := range []string{strings.Repeat("x", 65536), strings.Repeat("<", 4096)} {
		p.Design.Clusters[0].ID = id
		order, err := releasepolicy.ArmOrder(p.Design.Allocation.Seed, id)
		require.NoError(t, err)
		p.Design.Allocation.Assignments[0] = releasepolicy.Assignment{ClusterID: id, Order: order}
		slots, err = derivePossibleSlots(context.Background(), p, ids)
		require.Error(t, err, "identifier or expanded JSON cap must fail before slot allocation")
		require.Nil(t, slots)
	}
}

func TestSlotSetExactExpanded64MiBBudget(t *testing.T) {
	f := slotFixture(t, "file")
	p, err := releasepolicy.DecodePolicy(f.raw)
	require.NoError(t, err)
	cluster, task := strings.Repeat("<", 4096), strings.Repeat("\x00", 4096)
	ids := DeclaredEvalIDs{Baseline: strings.Repeat("b", 4096), Candidate: strings.Repeat("c", 1433)}
	p.Design.Clusters[0].ID = cluster
	p.Design.Clusters[0].Tasks[0].ID = task
	order, err := releasepolicy.ArmOrder(p.Design.Allocation.Seed, cluster)
	require.NoError(t, err)
	p.Design.Allocation.Assignments[0] = releasepolicy.Assignment{ClusterID: cluster, Order: order}
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		a := p.Arms[arm]
		a.Plan.Tasks[0].ID = task
		a.Plan.Tasks[0].Settings.MaxAttempts = 508
		p.Arms[arm] = a
	}
	// 32 array bytes + 508 pairs * (626 fixed + 6*21913 identifier bytes)
	// reaches the original declared 64MiB conservative budget exactly.
	slots, err := derivePossibleSlots(context.Background(), p, ids)
	require.NoError(t, err)
	require.Len(t, slots, 1016)
	b := slotBudget{maxSlotJSONBudget}
	require.NoError(t, b.add(32))
	for _, slot := range slots {
		require.NoError(t, b.slot(context.Background(), slot))
	}
	require.Zero(t, b.remaining)
	require.Error(t, b.add(1))
	envelope := freshSlotSet(t, f.set)
	envelope.slots = slots
	require.NoError(t, preflightSlotSet(context.Background(), envelope), "the complete 128MiB envelope independently fits")
	ids.Candidate += "c"
	slots, err = derivePossibleSlots(context.Background(), p, ids)
	require.ErrorContains(t, err, "budget")
	require.Nil(t, slots)
}

func TestSlotSetCheckedEscapedJSONBudgets(t *testing.T) {
	for _, text := range []string{"", "abc", "\x00\n\t", "<>&", "€😀", strings.Repeat("a", 4096)} {
		n := 2 + 6*len(text)
		b := slotBudget{n}
		require.NoError(t, b.text(text, 4096))
		require.Zero(t, b.remaining)
		raw, err := json.Marshal(text)
		require.NoError(t, err)
		require.LessOrEqual(t, len(raw), n)
		b = slotBudget{n - 1}
		require.Error(t, b.text(text, 4096))
	}
	for _, cap := range []int{maxSlotJSONBudget, maxSetSealJSONBudget, maxProjectionJSONBudget} {
		b := slotBudget{cap}
		require.NoError(t, b.add(cap))
		require.Error(t, b.add(1))
	}
	b := slotBudget{int(^uint(0) >> 1)}
	require.Error(t, b.add(-1))
	require.Error(t, b.text(strings.Repeat("a", 4097), 4096))
}

func TestSlotSetAllocationLookupGuards(t *testing.T) {
	f := slotFixture(t, "multi")
	for _, name := range []string{"missing_plan", "unknown_arm", "duplicate_plan", "missing_task", "zero_attempts",
		"wrong_engine", "wrong_trials", "zero_trials", "ordinal", "order", "cluster", "duplicate_task", "duplicate_cluster",
		"extra_task", "long_task", "nil", "no_clusters"} {
		t.Run(name, func(t *testing.T) {
			p, err := releasepolicy.DecodePolicy(f.raw)
			require.NoError(t, err)
			a := p.Arms[releasepolicy.Baseline]
			switch name {
			case "missing_plan":
				delete(p.Arms, releasepolicy.Baseline)
			case "unknown_arm":
				delete(p.Arms, releasepolicy.Baseline)
				p.Arms["other"] = a
			case "duplicate_plan":
				a.Plan.Tasks[1].ID = a.Plan.Tasks[0].ID
				p.Arms[releasepolicy.Baseline] = a
			case "missing_task":
				p.Design.Clusters[0].Tasks[0].ID = "not-a-task"
			case "zero_attempts":
				a.Plan.Tasks[0].Settings.MaxAttempts = 0
				p.Arms[releasepolicy.Baseline] = a
			case "wrong_engine":
				a.Plan.Tasks[0].Settings.Engine = "mock"
				p.Arms[releasepolicy.Baseline] = a
			case "wrong_trials":
				a.Plan.Tasks[0].Settings.TrialsPerTask++
				p.Arms[releasepolicy.Baseline] = a
			case "zero_trials":
				p.Design.Clusters[0].Tasks[0].TrialOrdinals = nil
			case "ordinal":
				p.Design.Clusters[0].Tasks[0].TrialOrdinals[0] = 2
			case "order":
				p.Design.Allocation.Assignments[0].Order = []releasepolicy.Arm{releasepolicy.Baseline}
			case "cluster":
				p.Design.Allocation.Assignments[0].ClusterID += "different"
			case "duplicate_task":
				p.Design.Clusters[0].Tasks[1].ID = p.Design.Clusters[0].Tasks[0].ID
			case "duplicate_cluster":
				p.Design.Clusters = append(p.Design.Clusters, p.Design.Clusters[0])
				p.Design.Allocation.Assignments = append(p.Design.Allocation.Assignments, p.Design.Allocation.Assignments[0])
			case "extra_task":
				a.Plan.Tasks = append(a.Plan.Tasks, releasepolicy.TaskPlan{ID: "extra", Settings: a.Plan.Tasks[0].Settings})
				p.Arms[releasepolicy.Baseline] = a
			case "long_task":
				p.Design.Clusters[0].Tasks[0].ID = strings.Repeat("a", 4097)
			case "nil":
				p = nil
			case "no_clusters":
				p.Design.Clusters = nil
				p.Design.Allocation.Assignments = nil
			}
			slots, err := derivePossibleSlots(context.Background(), p, slotTestIDs)
			require.Error(t, err)
			require.Nil(t, slots)
		})
	}
}

func TestSlotSetRetainedMutationAndLocalReseals(t *testing.T) {
	f := slotFixture(t, "multi")
	for _, reseal := range []bool{false, true} {
		for _, name := range []string{"raw", "eval", "slot_index", "slot_key", "projection", "golden",
			"source_bytes", "roles", "canonical", "supplement", "seal"} {
			t.Run(fmt.Sprintf("%s/reseal=%t", name, reseal), func(t *testing.T) {
				set := freshSlotSet(t, f.set)
				switch name {
				case "raw":
					set.rawPolicy = resealSlotPolicyObject(t, set.rawPolicy, func(o map[string]any) {
						arms, ok := o["arms"].(map[string]any)
						require.True(t, ok)
						for _, arm := range []string{"baseline", "candidate"} {
							a, ok := arms[arm].(map[string]any)
							require.True(t, ok)
							plan, ok := a["resolved_plan"].(map[string]any)
							require.True(t, ok)
							tasks, ok := plan["tasks"].([]any)
							require.True(t, ok)
							for _, task := range tasks {
								entry, ok := task.(map[string]any)
								require.True(t, ok)
								runtime, ok := entry["expected_runtime"].(map[string]any)
								require.True(t, ok)
								runtime["reason"] = "another unavailable reason"
							}
							a["resolved_plan_digest"] = slotTestDigest(t, plan)
						}
					})
				case "eval":
					set.evalIDs.Baseline += "-changed"
				case "slot_index":
					set.slots[0].Index++
				case "slot_key":
					set.slots[0].Key.Attempt++
				case "projection":
					p := set.projections[releasepolicy.Baseline]
					p.Arm.Plan.Tasks[0].Settings.Model += "-changed"
					p.Arm.Digest = slotTestDigest(t, p.Arm.Plan)
					set.projections[releasepolicy.Baseline] = p
				case "golden":
					p := set.projections[releasepolicy.Baseline]
					p.GoldenIDs = []string{"zeta"}
					set.projections[releasepolicy.Baseline] = p
				case "source_bytes":
					set.input.sources[releasepolicy.Candidate][0].Bytes[0] ^= 1
				case "roles":
					set.input.sources[releasepolicy.Candidate][0].Roles[0] = "changed"
				case "canonical":
					set.input.canonical[0] = '!'
				case "supplement":
					set.input.associations.canonical[0] = '!'
				case "seal":
					set.seal.SHA256 = strings.Repeat("0", 64)
				}
				if reseal && name != "seal" {
					// Sealing local tables cannot authorize a contradictory capture.
					resealTestSlotSet(t, set)
				}
				slots, err := set.Slots(context.Background())
				require.Error(t, err)
				require.Nil(t, slots)
			})
		}
	}
	// Completely consistent declarations may change: there is no authenticity,
	// uniqueness or old-declaration authorization claim.
	ids := DeclaredEvalIDs{"another-baseline", "another-candidate"}
	set, err := PrepareSlotSet(context.Background(), f.raw, f.capture, ids)
	require.NoError(t, err)
	require.NotEqual(t, f.set.seal, set.seal)
	for _, slot := range set.slots {
		require.Contains(t, []string{ids.Baseline, ids.Candidate}, slot.Key.EvalID)
	}
	// Input policy/capture ownership is independent of the returned set.
	f.raw[0] = '!'
	f.capture.prepared.canonical[0] = '!'
	f.capture.associations.canonical[0] = '!'
	f.capture.prepared.sources[releasepolicy.Baseline][0].Bytes[0] ^= 1
	_, err = set.Slots(context.Background())
	require.NoError(t, err)
}

func TestSlotSetSupplementAndRawSourceContradictions(t *testing.T) {
	f := slotFixture(t, "disabled_prompt")
	for _, name := range []string{"nil", "nil_prepared", "missing", "null", "old", "new", "snapshot", "event",
		"node", "query", "source", "role", "prompt", "disabled", "client"} {
		t.Run(name, func(t *testing.T) {
			capture := cloneSlotCapture(t, f)
			switch name {
			case "nil":
				capture = nil
			case "nil_prepared":
				capture.prepared = nil
			case "missing":
				capture.associations = retainedAssociations{}
			case "null":
				capture.associations.canonical = []byte("null")
				capture.associations.seal = slotTestDigest(t, json.RawMessage("null"))
			case "source", "role", "prompt", "disabled":
				for i := range capture.prepared.sources[releasepolicy.Baseline] {
					s := &capture.prepared.sources[releasepolicy.Baseline][i]
					match := name == "source" || name == "role" ||
						name == "prompt" && strings.HasSuffix(s.Path, "/prompt.txt") ||
						name == "disabled" && strings.HasSuffix(s.Path, "/disabled.yaml")
					if match {
						if name == "role" {
							s.Roles[0] = "changed"
						} else {
							s.Bytes = append(s.Bytes, 'x')
							s.Digest = releasepolicy.SourceDigest(s.Bytes)
						}

						break
					}
				}
			case "client":
				var snapshots map[releasepolicy.Arm]armSnapshot
				require.NoError(t, json.Unmarshal(capture.prepared.canonical, &snapshots))
				a := snapshots[releasepolicy.Baseline]
				a.Tasks[0].Request = json.RawMessage(`{"Message":"changed"}`)
				snapshots[releasepolicy.Baseline] = a
				raw, err := json.Marshal(snapshots)
				require.NoError(t, err)
				capture.prepared.canonical = raw
				capture.prepared.seal = slotTestDigest(t, json.RawMessage(raw))
				resealG2(t, capture, func(a *captureAssociations) { a.Snapshot = capture.prepared.seal })
			default:
				resealG2(t, capture, func(a *captureAssociations) {
					b := a.Arms[releasepolicy.Baseline]
					switch name {
					case "old":
						a.Version = "0"
					case "new":
						a.Version = "2"
					case "snapshot":
						a.Snapshot.SHA256 = strings.Repeat("0", 64)
					case "event":
						b.Events = append(b.Events, b.Events[0])
					case "node":
						b.Nodes = b.Nodes[1:]
					case "query":
						b.Events[0].Path += "-changed"
					}
					a.Arms[releasepolicy.Baseline] = b
				})
			}
			set, err := PrepareSlotSet(context.Background(), f.raw, capture, slotTestIDs)
			require.Error(t, err)
			require.Nil(t, set)
		})
	}
}

func TestSlotSetFullyResealedSourcesDoNotReplaceOriginalRawParsers(t *testing.T) {
	f := slotFixture(t, "disabled_prompt")
	for _, name := range []string{"eval", "task", "prompt", "instruction", "disabled"} {
		t.Run(name, func(t *testing.T) {
			capture := cloneSlotCapture(t, f)
			path := map[string]string{"eval": "baseline/eval.yaml", "task": "baseline/tasks/task.yaml",
				"prompt": "baseline/tasks/prompt.txt", "instruction": "baseline/context/instruction.md",
				"disabled": "baseline/tasks/disabled.yaml"}[name]
			index := -1
			for i := range capture.prepared.sources[releasepolicy.Baseline] {
				entry := &capture.prepared.sources[releasepolicy.Baseline][i]
				if entry.Path != path {
					continue
				}
				index = i
				switch name {
				case "eval":
					entry.Bytes = bytes.Replace(entry.Bytes, []byte("offline-model"), []byte("different-model"), 1)
				case "task":
					entry.Bytes = bytes.Replace(entry.Bytes, []byte("name: task-name"), []byte("name: changed-name"), 1)
				case "disabled":
					entry.Bytes = bytes.Replace(entry.Bytes, []byte("enabled: false"), []byte("enabled: true"), 1)
				default:
					entry.Bytes = []byte("changed input")
				}
				entry.Digest = releasepolicy.SourceDigest(entry.Bytes)
				break
			}
			require.NotEqual(t, -1, index)
			var snapshots map[releasepolicy.Arm]armSnapshot
			require.NoError(t, json.Unmarshal(capture.prepared.canonical, &snapshots))
			a := snapshots[releasepolicy.Baseline]
			entry := capture.prepared.sources[releasepolicy.Baseline][index]
			a.Sources[index].ByteLength = len(entry.Bytes)
			a.Sources[index].Digest = entry.Digest
			snapshots[releasepolicy.Baseline] = a
			raw, err := json.Marshal(snapshots)
			require.NoError(t, err)
			capture.prepared.canonical = raw
			capture.prepared.seal = slotTestDigest(t, json.RawMessage(raw))
			resealG2(t, capture, func(a *captureAssociations) { a.Snapshot = capture.prepared.seal })
			_, err = detachProjectionInput(context.Background(), capture)
			require.NoError(t, err, "all local source/snapshot/association seals are internally consistent")
			set, err := PrepareSlotSet(context.Background(), f.raw, capture, slotTestIDs)
			require.Error(t, err, "actual raw replay and full CLIENT/G1 parity must still fail")
			require.Nil(t, set)
		})
	}
}

func TestSlotSetRetainedPreflight(t *testing.T) {
	f := slotFixture(t, "file")
	for _, name := range []string{"slots_count", "slot_text", "raw_policy", "projection_count", "projection_text",
		"projection_digest", "identity_count", "golden_count", "source_count", "source_bytes", "source_path",
		"roles_count", "role_text", "canonical", "supplement", "snapshot_digest", "eval"} {
		t.Run(name, func(t *testing.T) {
			set := freshSlotSet(t, f.set)
			switch name {
			case "slots_count":
				set.slots = make([]Slot, 65537)
			case "slot_text":
				set.slots[0].Key.TaskID = strings.Repeat("a", 4097)
			case "raw_policy":
				set.rawPolicy = make([]byte, maxSlotPolicyBytes+1)
			case "projection_count":
				p := set.projections[releasepolicy.Baseline]
				p.Arm.Plan.Tasks = make([]releasepolicy.TaskPlan, maxTasks+1)
				set.projections[releasepolicy.Baseline] = p
			case "projection_text":
				p := set.projections[releasepolicy.Baseline]
				p.Arm.Plan.Tasks[0].Settings.Model = strings.Repeat("a", 4097)
				set.projections[releasepolicy.Baseline] = p
			case "projection_digest":
				p := set.projections[releasepolicy.Baseline]
				p.Arm.Digest.SHA256 = strings.Repeat("a", 65)
				set.projections[releasepolicy.Baseline] = p
			case "identity_count":
				p := set.projections[releasepolicy.Baseline]
				p.Arm.Plan.Identities = make([]releasepolicy.Identity, 6*maxTasks+4)
				set.projections[releasepolicy.Baseline] = p
			case "golden_count":
				p := set.projections[releasepolicy.Baseline]
				p.GoldenIDs = make([]string, maxTasks+1)
				set.projections[releasepolicy.Baseline] = p
			case "source_count":
				set.input.sources[releasepolicy.Baseline] = make([]source, maxAssociationEvents+1)
			case "source_bytes":
				set.input.sources[releasepolicy.Baseline][0].Bytes = make([]byte, maxExecutableBytes+1)
			case "source_path":
				set.input.sources[releasepolicy.Baseline][0].Path = strings.Repeat("a", 4097)
			case "roles_count":
				set.input.sources[releasepolicy.Baseline][0].Roles = make([]string, maxAssociationEvents+1)
			case "role_text":
				set.input.sources[releasepolicy.Baseline][0].Roles[0] = strings.Repeat("a", 4097)
			case "canonical":
				set.input.canonical = make([]byte, maxProjectionBytes+1)
			case "supplement":
				set.input.associations.canonical = make([]byte, maxAssociationBytes+1)
			case "snapshot_digest":
				set.input.snapshot.Encoding = strings.Repeat("a", 17)
			case "eval":
				set.evalIDs.Baseline = strings.Repeat("a", 4097)
			}
			require.Error(t, preflightSlotSet(context.Background(), set))
			slots, err := set.Slots(context.Background())
			require.Error(t, err)
			require.Nil(t, slots)
		})
	}
}

func TestSlotJoinedPreflightPairingAndSubstitution(t *testing.T) {
	f := slotFixture(t, "file")
	admitted := admitSlot(t, f, f.set.slots[0].Key, nil)
	for _, name := range []string{"nil", "canonical_size", "canonical_valid_size", "extra_arm", "source_count",
		"source_bytes", "source_path", "source_roles", "opposite_bytes", "collection_id", "digest", "arm_text",
		"key_text", "set_seal", "slot_index", "join_seal", "binding_request", "binding_profile", "binding_collection"} {
		t.Run(name, func(t *testing.T) {
			j, err := CheckSlotJoin(context.Background(), f.set, 0, admitted)
			require.NoError(t, err)
			switch name {
			case "nil":
				j.joined = nil
			case "canonical_size":
				j.joined.canonical = make([]byte, maxProjectionBytes+1)
			case "canonical_valid_size":
				j.joined.canonical = append([]byte(`"`), bytes.Repeat([]byte("a"), maxProjectionBytes)...)
				j.joined.canonical = append(j.joined.canonical, '"')
			case "extra_arm":
				j.joined.sources["other"] = []source{}
			case "source_count":
				j.joined.sources[releasepolicy.Baseline] = append(j.joined.sources[releasepolicy.Baseline], source{})
			case "source_bytes":
				j.joined.sources[releasepolicy.Baseline][0].Bytes = make([]byte, maxExecutableBytes+1)
			case "source_path":
				j.joined.sources[releasepolicy.Baseline][0].Path = strings.Repeat("a", 4097)
			case "source_roles":
				j.joined.sources[releasepolicy.Baseline][0].Roles = make([]string, maxAssociationEvents+1)
			case "opposite_bytes":
				other := releasepolicy.Baseline
				if j.slot.Key.Arm == other {
					other = releasepolicy.Candidate
				}
				j.joined.sources[other][0].Bytes[0] ^= 1
			case "collection_id":
				j.joined.binding.CollectionID = strings.Repeat("a", 65)
			case "digest":
				j.joined.binding.ProfileDigest.SHA256 = strings.Repeat("a", 65)
			case "arm_text":
				j.joined.binding.Key.Arm = releasepolicy.Arm(strings.Repeat("a", 17))
			case "key_text":
				j.joined.binding.Key.EvalID = strings.Repeat("a", 4097)
			case "set_seal":
				j.setSeal.SHA256 = strings.Repeat("0", 64)
			case "slot_index":
				j.slot.Index = len(j.set.slots)
			case "join_seal":
				j.seal.SHA256 = strings.Repeat("0", 64)
			case "binding_request", "binding_profile", "binding_collection":
				switch name {
				case "binding_request":
					j.joined.binding.RequestDigest = slotTestDigest(t, "another request")
				case "binding_profile":
					j.joined.binding.ProfileDigest = slotTestDigest(t, "another profile")
				case "binding_collection":
					j.joined.binding.CollectionID = strings.Repeat("b", 64)
				}
			}
			slot, binding, err := j.Binding(context.Background())
			require.Error(t, err)
			require.Equal(t, Slot{}, slot)
			require.Equal(t, nativetask.Admission{}, binding)
		})
	}
	t.Run("individually_valid_CLIENT_reseal", func(t *testing.T) {
		j, err := CheckSlotJoin(context.Background(), f.set, 0, admitted)
		require.NoError(t, err)
		var snapshots map[releasepolicy.Arm]armSnapshot
		require.NoError(t, json.Unmarshal(j.joined.canonical, &snapshots))
		a := snapshots[j.slot.Key.Arm]
		var request map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(a.Tasks[0].Request, &request))
		if string(request["SkillPaths"]) == "null" {
			request["SkillPaths"] = json.RawMessage("[]")
		} else {
			request["SkillPaths"] = json.RawMessage("null")
		}
		a.Tasks[0].Request, err = json.Marshal(request)
		require.NoError(t, err)
		snapshots[j.slot.Key.Arm] = a
		j.joined.canonical, err = json.Marshal(snapshots)
		require.NoError(t, err)
		j.joined.snapshot = slotTestDigest(t, json.RawMessage(j.joined.canonical))
		j.joined.seal = slotTestDigest(t, joinSeal{j.joined.snapshot, j.joined.binding})
		_, err = j.joined.Binding()
		require.NoError(t, err, "the substituted primitive is independently seal-valid")
		j.seal = slotTestDigest(t, slotJoinSeal{slotJoinDomain, j.setSeal, j.slot, j.joined.snapshot, j.joined.seal, j.joined.binding})
		_, _, err = j.Binding(context.Background())
		require.ErrorContains(t, err, "slot capture")
	})
	t.Run("different_actual_root", func(t *testing.T) {
		other := slotFixture(t, "file")
		j, err := CheckSlotJoin(context.Background(), f.set, 0, admitted)
		require.NoError(t, err)
		substitute, err := CheckPrimitiveJoin(context.Background(), other.capture.prepared, admitted, j.slot.Key)
		require.NoError(t, err)
		require.Equal(t, j.joined.binding, substitute.binding)
		require.NotEqual(t, j.joined.snapshot, substitute.snapshot)
		j.joined = substitute
		j.seal = slotTestDigest(t, slotJoinSeal{slotJoinDomain, j.setSeal, j.slot, substitute.snapshot, substitute.seal, substitute.binding})
		_, _, err = j.Binding(context.Background())
		require.Error(t, err)
	})
}

// A deterministic context cancels inside loops without racing caller mutation.
type slotCancelContext struct {
	context.Context
	remaining int
}

func (c *slotCancelContext) Err() error {
	c.remaining--
	if c.remaining <= 0 {
		return context.Canceled
	}
	return nil
}

func TestSlotSetCancelAndNilBoundaries(t *testing.T) {
	f := slotFixture(t, "multi")
	admitted := admitSlot(t, f, f.set.slots[0].Key, nil)
	j, err := CheckSlotJoin(context.Background(), f.set, 0, admitted)
	require.NoError(t, err)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{nil, canceled} {
		set, err := PrepareSlotSet(ctx, f.raw, f.capture, slotTestIDs)
		require.Error(t, err)
		require.Nil(t, set)
		slots, err := f.set.Slots(ctx)
		require.Error(t, err)
		require.Nil(t, slots)
		joined, err := CheckSlotJoin(ctx, f.set, 0, admitted)
		require.Error(t, err)
		require.Nil(t, joined)
		slot, binding, err := j.Binding(ctx)
		require.Error(t, err)
		require.Equal(t, Slot{}, slot)
		require.Equal(t, nativetask.Admission{}, binding)
	}
	for _, n := range []int{2, 20, 100, 300, 1000} {
		ctx := &slotCancelContext{context.Background(), n}
		set, err := PrepareSlotSet(ctx, f.raw, f.capture, slotTestIDs)
		require.ErrorIs(t, err, context.Canceled)
		require.Nil(t, set)
		ctx = &slotCancelContext{context.Background(), n}
		slots, err := f.set.Slots(ctx)
		require.ErrorIs(t, err, context.Canceled)
		require.Nil(t, slots)
		ctx = &slotCancelContext{context.Background(), n}
		joined, err := CheckSlotJoin(ctx, f.set, 0, admitted)
		require.ErrorIs(t, err, context.Canceled)
		require.Nil(t, joined)
		ctx = &slotCancelContext{context.Background(), n}
		slot, binding, err := j.Binding(ctx)
		require.ErrorIs(t, err, context.Canceled)
		require.Equal(t, Slot{}, slot)
		require.Equal(t, nativetask.Admission{}, binding)
	}
	for _, operation := range []func(context.Context) (bool, error){
		func(ctx context.Context) (bool, error) {
			set, err := PrepareSlotSet(ctx, f.raw, f.capture, slotTestIDs)
			return set != nil, err
		},
		func(ctx context.Context) (bool, error) {
			slots, err := f.set.Slots(ctx)
			return slots != nil, err
		},
		func(ctx context.Context) (bool, error) {
			joined, err := CheckSlotJoin(ctx, f.set, 0, admitted)
			return joined != nil, err
		},
		func(ctx context.Context) (bool, error) {
			slot, binding, err := j.Binding(ctx)
			return slot != (Slot{}) || binding != (nativetask.Admission{}), err
		},
	} {
		counted := &slotCancelContext{context.Background(), 1000000}
		usable, err := operation(counted)
		require.NoError(t, err)
		require.True(t, usable)
		// Cancel on the last observed check, including after no-context Binding.
		usable, err = operation(&slotCancelContext{context.Background(), 1000000 - counted.remaining})
		require.ErrorIs(t, err, context.Canceled)
		require.False(t, usable)
	}
	p, err := releasepolicy.DecodePolicy(f.raw)
	require.NoError(t, err)
	// 33 pre-visitor checks and 3 checks/slot make cancellation observable in
	// both passes. Find the exact boundary using an uncanceled counted run.
	counter := &slotCancelContext{context.Background(), 100000}
	_, err = derivePossibleSlots(counter, p, slotTestIDs)
	require.NoError(t, err)
	calls := 100000 - counter.remaining
	for _, n := range []int{calls / 3, calls - 2, calls} {
		slots, err := derivePossibleSlots(&slotCancelContext{context.Background(), n}, p, slotTestIDs)
		require.ErrorIs(t, err, context.Canceled)
		require.Nil(t, slots)
	}
	var nilSet *SlotSet
	_, err = nilSet.Slots(context.Background())
	require.Error(t, err)
	var nilJoin *SlotJoined
	_, _, err = nilJoin.Binding(context.Background())
	require.Error(t, err)
	for _, ids := range []DeclaredEvalIDs{{}, {"same", "same"}, {" leading", "candidate"},
		{"baseline", "trailing "}, {strings.Repeat("a", 4097), "candidate"}} {
		set, err := PrepareSlotSet(context.Background(), f.raw, f.capture, ids)
		require.Error(t, err)
		require.Nil(t, set)
	}
	for _, raw := range [][]byte{nil, {}, []byte("{}"), bytes.Repeat([]byte(" "), maxSlotPolicyBytes+1)} {
		set, err := PrepareSlotSet(context.Background(), raw, f.capture, slotTestIDs)
		require.Error(t, err)
		require.Nil(t, set)
	}
}

func TestSlotSetUnsupportedOriginalCaptureDeclarations(t *testing.T) {
	for _, name := range []string{"float", "duplicate_instruction", "noncanonical_context"} {
		t.Run(name, func(t *testing.T) {
			base, locations := fixture(t)
			switch name {
			case "float":
				write(t, base, "baseline/tasks/task.yaml", "id: task\ninputs: {prompt: hello, context: {value: 1.0}}\n")
			case "duplicate_instruction":
				write(t, base, "baseline/tasks/task.yaml", snapshotTask+"\ninstruction_files: [instruction.md, instruction.md]\n")
			case "noncanonical_context":
				a := locations[releasepolicy.Baseline]
				a.ContextDir = "./baseline/context"
				locations[releasepolicy.Baseline] = a
			}
			original, originalErr := Prepare(context.Background(), locations)
			capture, captureErr := prepareProjectionCapture(context.Background(), locations)
			require.Error(t, originalErr)
			require.Nil(t, original)
			require.Error(t, captureErr)
			require.Nil(t, capture)
		})
	}
}

func TestSlotSetPrimitiveBoundsRemainIndependentPerArm(t *testing.T) {
	f := slotFixture(t, "file")
	slot := f.set.slots[0]
	admitted := admitSlot(t, f, slot.Key, nil)
	other := releasepolicy.Baseline
	if slot.Key.Arm == other {
		other = releasepolicy.Candidate
	}
	// A comment-only raw change preserves G1 plans but exceeds the primitive
	// raw cap on the opposite arm. G2 reconstruction still retains both arms.
	write(t, f.base, string(other)+"/eval.yaml", snapshotEval+"\n#"+strings.Repeat("x", assurance.MaxLabelBytes)+"\n")
	oracle := frozenOriginalCapture(t, f.locations)
	capture, err := prepareProjectionCapture(context.Background(), f.locations)
	require.NoError(t, err)
	require.Equal(t, oracle.Projection, f.original.Projection)
	set, err := PrepareSlotSet(context.Background(), f.raw, capture, slotTestIDs)
	require.NoError(t, err)
	joined, err := CheckSlotJoin(context.Background(), set, 0, admitted)
	require.NoError(t, err)
	_, _, err = joined.Binding(context.Background())
	require.NoError(t, err)
	request, err := capture.prepared.Request(other, "task")
	require.NoError(t, err)
	sources := []nativetask.Source{}
	for _, entry := range capture.prepared.sources[other] {
		sources = append(sources, nativetask.Source{Root: f.locations[other].Root, Path: entry.Path})
	}
	primitive, err := nativetask.Prepare(context.Background(), other, "task", request, sources)
	require.Error(t, err)
	require.Nil(t, primitive)
	// The exact selected original raw bound also fails without any new cap.
	write(t, f.base, string(slot.Key.Arm)+"/eval.yaml", snapshotEval+"\n#"+strings.Repeat("x", assurance.MaxLabelBytes)+"\n")
	capture, err = prepareProjectionCapture(context.Background(), f.locations)
	require.NoError(t, err)
	set, err = PrepareSlotSet(context.Background(), f.raw, capture, slotTestIDs)
	require.NoError(t, err)
	joined, err = CheckSlotJoin(context.Background(), set, 0, admitted)
	require.Error(t, err)
	require.Nil(t, joined)
}

func TestSlotSetSourcePolicyShapeAndProjectionBudgets(t *testing.T) {
	f := slotFixture(t, "file")
	p, err := releasepolicy.DecodePolicy(f.raw)
	require.NoError(t, err)
	for _, raw := range [][]byte{[]byte("null"), []byte("{}"), []byte(`{"arms":{}}`),
		[]byte(`{"arms":{"baseline":{"resolved_plan":[]}}}`)} {
		require.Error(t, compareSourcePolicy(context.Background(), raw, p, f.original.Projection))
	}
	for _, name := range []string{"missing_arm", "digest", "identity_digest", "identity_text", "plan_kind"} {
		t.Run(name, func(t *testing.T) {
			set := freshSlotSet(t, f.set)
			projection := set.projections[releasepolicy.Baseline]
			switch name {
			case "missing_arm":
				delete(set.projections, releasepolicy.Candidate)
			case "digest":
				projection.Arm.Digest.Encoding = strings.Repeat("a", 17)
			case "identity_digest":
				projection.Arm.Plan.Identities[0].Digest.SHA256 = strings.Repeat("a", 65)
			case "identity_text":
				projection.Arm.Plan.Identities[0].Reason = strings.Repeat("a", 4097)
			case "plan_kind":
				projection.Arm.Plan.Kind = strings.Repeat("a", 4097)
			}
			set.projections[releasepolicy.Baseline] = projection
			_, err := boundSlotProjections(context.Background(), set.projections)
			require.Error(t, err)
		})
	}
	// Fill permitted typed fields to exceed the aggregate projection budget
	// without violating an individual string/count cap or using reflection.
	projections := map[releasepolicy.Arm]controlledprojection.SourceProjection{}
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		row := f.original.Projection[arm]
		identities := make([]releasepolicy.Identity, 6*maxTasks+3)
		for i := range identities {
			identities[i] = releasepolicy.Identity{Domain: strings.Repeat("<", 4096),
				TaskID: strings.Repeat("\x00", 4096), Reason: strings.Repeat("€", 1365)}
		}
		row.Arm.Plan.Identities = identities
		projections[arm] = row
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = boundSlotProjections(ctx, projections)
	require.ErrorContains(t, err, "budget")
}

func TestSlotPrimitiveOriginalExactEncodedBoundary(t *testing.T) {
	f := slotFixture(t, "file")
	packageDir, err := os.Getwd()
	require.NoError(t, err)
	repository := filepath.Dir(filepath.Dir(packageDir))
	scratch := t.TempDir()
	var snapshots map[releasepolicy.Arm]armSnapshot
	require.NoError(t, json.Unmarshal(f.original.Canonical, &snapshots))
	requestJSON := snapshots[releasepolicy.Baseline].Tasks[0].Request
	requestPath := filepath.Join(scratch, "request.json")
	require.NoError(t, os.WriteFile(requestPath, requestJSON, 0600))
	sourcePaths := []string{}
	for _, entry := range f.original.Sources[releasepolicy.Baseline] {
		sourcePaths = append(sourcePaths, entry.Path)
	}
	pathsJSON, err := json.Marshal(sourcePaths)
	require.NoError(t, err)
	// Inspect the existing private primitive payload in its own test package,
	// using its pinned originalPrepare oracle. No copied request projection,
	// production getter, reflection or altered primitive limit is introduced.
	const boundaryOracle = `package nativetask
import (
 "bytes"
 "context"
 "encoding/json"
 "os"
 "strings"
 "testing"
 "github.com/microsoft/waza/internal/assurance"
 "github.com/microsoft/waza/internal/execution"
 "github.com/microsoft/waza/internal/releasepolicy"
 "github.com/stretchr/testify/require"
)
func TestSlotOriginalPrimitiveBoundaryOracle(t *testing.T) {
 raw, err := os.ReadFile(os.Getenv("WAZA_SLOT_BOUNDARY_REQUEST"))
 require.NoError(t, err)
 var req execution.ExecutionRequest
 decoder := json.NewDecoder(bytes.NewReader(raw))
 decoder.UseNumber()
 require.NoError(t, decoder.Decode(&req))
 root, err := os.OpenRoot(os.Getenv("WAZA_SLOT_BOUNDARY_ROOT"))
 require.NoError(t, err)
 t.Cleanup(func() { require.NoError(t, root.Close()) })
 var paths []string
 require.NoError(t, json.Unmarshal([]byte(os.Getenv("WAZA_SLOT_BOUNDARY_PATHS")), &paths))
 sources := []Source{}
 for _, path := range paths { sources = append(sources, Source{Root: root, Path: path}) }
 original, err := originalPrepare(context.Background(), releasepolicy.Baseline, "task", &req, sources)
 require.NoError(t, err)
 require.Empty(t, req.TaskDescription)
 req.TaskDescription = strings.Repeat("x", assurance.MaxLabelBytes-len(original.data))
 original, err = originalPrepare(context.Background(), releasepolicy.Baseline, "task", &req, sources)
 require.NoError(t, err)
 require.Len(t, original.data, assurance.MaxLabelBytes)
 actual, err := Prepare(context.Background(), releasepolicy.Baseline, "task", &req, sources)
 require.NoError(t, err)
 require.Equal(t, original.data, actual.data)
 require.Equal(t, original.seal, actual.seal)
 req.TaskDescription += "x"
 original, err = originalPrepare(context.Background(), releasepolicy.Baseline, "task", &req, sources)
 require.ErrorContains(t, err, "encoded")
 require.Nil(t, original)
 actual, err = Prepare(context.Background(), releasepolicy.Baseline, "task", &req, sources)
 require.ErrorContains(t, err, "encoded")
 require.Nil(t, actual)
 // An independently unmet raw cap is a nonpass even if encoded is also too
 // large; neither primitive bound is relaxed for static slots.
 req.TaskDescription = ""
 req.Resources = []execution.ResourceFile{{Path: "large", Content: make([]byte, assurance.MaxLabelBytes+1)}}
 original, err = originalPrepare(context.Background(), releasepolicy.Baseline, "task", &req, sources)
 require.ErrorContains(t, err, "bounded inventory")
 require.Nil(t, original)
 actual, err = Prepare(context.Background(), releasepolicy.Baseline, "task", &req, sources)
 require.ErrorContains(t, err, "bounded inventory")
 require.Nil(t, actual)
}
func TestSlotOriginalIdentityOracle(t *testing.T) {
 for _, name := range []string{"eval", "task", "trial", "attempt"} {
  t.Run("origin_"+name, func(t *testing.T) {
   _, _, admitted, record := fixture(t)
   raw, err := json.Marshal(record)
   require.NoError(t, err)
   _, err = DecodeRecord(raw, admitted)
   require.NoError(t, err)
   switch name {
   case "eval": record.Origin.EvalID += "-other"
   case "task": record.Origin.TaskID += "-other"
   case "trial": record.Origin.RunNumber++
   case "attempt": record.Origin.AttemptCount++
   }
   raw, err = json.Marshal(record)
   require.NoError(t, err)
   _, err = DecodeRecord(raw, admitted)
   require.Error(t, err)
  })
 }
 for _, name := range []string{"collection", "profile", "request", "policy", "plan"} {
  t.Run("retained_"+name, func(t *testing.T) {
   _, _, admitted, _ := fixture(t)
   switch name {
   case "collection": admitted.binding.CollectionID = strings.Repeat("b", 64)
   case "profile": admitted.binding.ProfileDigest.SHA256 = strings.Repeat("b", 64)
   case "request": admitted.binding.RequestDigest.SHA256 = strings.Repeat("b", 64)
   case "policy": admitted.binding.PolicyDigest.SHA256 = strings.Repeat("b", 64)
   case "plan": admitted.binding.PlanDigest.SHA256 = strings.Repeat("b", 64)
   }
   _, err := admitted.Binding()
   require.Error(t, err)
  })
 }
}
`
	sourcePath := filepath.Join(scratch, "boundary_test.go")
	require.NoError(t, os.WriteFile(sourcePath, []byte(boundaryOracle), 0600))
	overlay, err := json.Marshal(struct{ Replace map[string]string }{map[string]string{
		filepath.Join(repository, "internal", "nativetask", "slot_boundary_oracle_test.go"): sourcePath}})
	require.NoError(t, err)
	overlayPath := filepath.Join(scratch, "boundary-overlay.json")
	require.NoError(t, os.WriteFile(overlayPath, overlay, 0600))
	command := exec.CommandContext(t.Context(), "go", "test", "-overlay="+overlayPath, "./internal/nativetask",
		"-run=^TestSlotOriginal(PrimitiveBoundary|Identity)Oracle$", "-count=1")
	command.Dir = repository
	command.Env = append(os.Environ(), "WAZA_SLOT_BOUNDARY_REQUEST="+requestPath, "WAZA_SLOT_BOUNDARY_ROOT="+f.base,
		"WAZA_SLOT_BOUNDARY_PATHS="+string(pathsJSON), "ENABLE_COPILOT_TESTS=false", "NO_COLOR=1")
	output, err := command.CombinedOutput()
	require.NoError(t, err, "%s", output)
}
