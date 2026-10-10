package nativesnapshot

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/assurance"
	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/nativetask"
	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/stretchr/testify/require"
)

func joinFixture(t *testing.T) (string, *Prepared, *nativetask.Admitted, releasepolicy.AttemptKey) {
	t.Helper()
	base, locations := fixture(t)
	prepared, err := Prepare(context.Background(), locations)
	require.NoError(t, err)
	primitives := map[releasepolicy.Arm]*nativetask.Prepared{}
	profile := &nativetask.Profile{
		Kind: "waza.release-native-task-profile", Version: "1.0",
		Nonce:          "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Selection:      nativetask.Selection{Operation: "native_text_task", Version: "1.0"},
		PermissionMode: "deny_all", Arms: map[releasepolicy.Arm]nativetask.ArmPlan{},
	}
	policy, err := evidence.JSONDigest("synthetic policy, not an allocation")
	require.NoError(t, err)
	profile.PolicyDigest = *policy
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		req, err := prepared.Request(arm, "task")
		require.NoError(t, err)
		sources := make([]nativetask.Source, 0, len(prepared.sources[arm]))
		for _, source := range prepared.sources[arm] {
			sources = append(sources, nativetask.Source{Root: locations[arm].Root, Path: source.Path})
		}
		primitive, err := nativetask.Prepare(context.Background(), arm, "task", req, sources)
		require.NoError(t, err)
		primitives[arm] = primitive
		plan, err := evidence.JSONDigest(string(arm))
		require.NoError(t, err)
		profile.Arms[arm] = nativetask.ArmPlan{
			PlanDigest: *plan, Requests: []nativetask.RequestPlan{{TaskID: "task", RequestDigest: primitive.RequestDigest()}},
		}
	}
	raw, err := releasepolicy.SealJSON(profile)
	require.NoError(t, err)
	profile, err = nativetask.DecodeProfile(raw)
	require.NoError(t, err)
	key := releasepolicy.AttemptKey{
		SampleKey: releasepolicy.SampleKey{ClusterID: "provided-cluster", TaskID: "task", Trial: 2},
		Arm:       releasepolicy.Baseline, EvalID: "provided-eval", Attempt: 3,
	}
	admitted, err := nativetask.Admit(context.Background(), primitives[key.Arm], profile, key)
	require.NoError(t, err)
	return base, prepared, admitted, key
}

func TestCheckPrimitiveJoinIsDetachedAndInspectionOnly(t *testing.T) {
	base, prepared, admitted, key := joinFixture(t)
	joined, err := CheckPrimitiveJoin(context.Background(), prepared, admitted, key)
	require.NoError(t, err)
	before, err := joined.Binding()
	require.NoError(t, err)
	actual, err := admitted.Binding()
	require.NoError(t, err)
	require.Equal(t, actual, before)
	require.Equal(t, prepared.canonical, joined.canonical)
	require.Equal(t, prepared.sources, joined.sources)
	// No live source state is assessed by the join.
	write(t, base, "baseline/tasks/prompt.txt", "changed after capture")
	_, err = CheckPrimitiveJoin(context.Background(), prepared, admitted, key)
	require.NoError(t, err)
	_, err = Recheck(context.Background(), prepared)
	require.Error(t, err)
	// The join owns complete detached state, not mutable preparation pointers.
	prepared.canonical[0] = '!'
	prepared.sources[key.Arm][0].Bytes[0] ^= 1
	prepared.sources[key.Arm][0].Roles[0] = "caller mutation"
	after, err := joined.Binding()
	require.NoError(t, err)
	require.Equal(t, before, after)
	var record nativetask.Record
	require.Equal(t, nativetask.Capabilities{
		FullToolTape: "not_assessed", Session: "not_assessed", RuntimeVersion: "not_assessed",
		ProviderCurrency: "not_assessed", Assurance: "not_assessed",
	}, record.Capabilities())
}

func TestCheckPrimitiveJoinRejectsRetainedMutations(t *testing.T) {
	for _, name := range []string{"snapshot", "missing_arm", "subset", "extra", "order", "alias", "bytes", "digest", "roles", "other_arm", "wrong_key", "nil_admitted"} {
		t.Run(name, func(t *testing.T) {
			_, prepared, admitted, key := joinFixture(t)
			arm := key.Arm
			switch name {
			case "snapshot":
				prepared.canonical[0] = '!'
			case "missing_arm":
				delete(prepared.sources, releasepolicy.Candidate)
			case "subset":
				prepared.sources[arm] = prepared.sources[arm][1:]
			case "extra":
				prepared.sources[arm] = append(prepared.sources[arm], source{Path: "extra"})
			case "order":
				prepared.sources[arm][0], prepared.sources[arm][1] = prepared.sources[arm][1], prepared.sources[arm][0]
			case "alias":
				prepared.sources[arm][0].Path = "./" + prepared.sources[arm][0].Path
			case "bytes":
				prepared.sources[arm][0].Bytes[0] ^= 1
			case "digest":
				prepared.sources[arm][0].Digest.Encoding = "json-v1"
			case "roles":
				prepared.sources[arm][0].Roles[0] = "changed"
			case "other_arm":
				prepared.sources[releasepolicy.Candidate][0].Bytes[0] ^= 1
			case "wrong_key":
				key.ClusterID += "-other"
			case "nil_admitted":
				admitted = nil
			}
			_, err := CheckPrimitiveJoin(context.Background(), prepared, admitted, key)
			require.Error(t, err)
		})
	}
	_, err := CheckPrimitiveJoin(context.Background(), nil, nil, releasepolicy.AttemptKey{})
	require.Error(t, err)
	_, prepared, admitted, key := joinFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = CheckPrimitiveJoin(ctx, prepared, admitted, key)
	require.ErrorIs(t, err, context.Canceled)
}

func TestJoinedBindingRejectsMutation(t *testing.T) {
	for _, name := range []string{"canonical", "binding", "seal", "raw_sources", "roles"} {
		t.Run(name, func(t *testing.T) {
			_, prepared, admitted, key := joinFixture(t)
			joined, err := CheckPrimitiveJoin(context.Background(), prepared, admitted, key)
			require.NoError(t, err)
			switch name {
			case "canonical":
				joined.canonical[0] = '!'
			case "binding":
				joined.binding.Key.Attempt++
			case "seal":
				joined.seal.Encoding = "source-bytes"
			case "raw_sources":
				joined.sources[key.Arm][0].Bytes[0] ^= 1
			case "roles":
				joined.sources[key.Arm][0].Roles[0] = "changed"
			}

			_, err = joined.Binding()
			require.Error(t, err)
		})
	}
	var joined *Joined
	_, err := joined.Binding()
	require.Error(t, err)
}

func TestJoinSelectedArmCapIsNotPairedAdmission(t *testing.T) {
	base, prepared, admitted, key := joinFixture(t)
	locations := map[releasepolicy.Arm]ArmLocation{}
	for arm, binding := range prepared.bindings {
		locations[arm] = binding.location
	}
	write(t, base, "candidate/tasks/prompt.txt", strings.Repeat("x", assurance.MaxLabelBytes+1))
	current, err := Prepare(context.Background(), locations)
	require.NoError(t, err)
	joined, err := CheckPrimitiveJoin(context.Background(), current, admitted, key)
	require.NoError(t, err, "opposite arm retains its independent resolver bounds")
	binding, err := joined.Binding()
	require.NoError(t, err)
	require.Equal(t, key, binding.Key)
	key.Arm = releasepolicy.Candidate
	_, err = CheckPrimitiveJoin(context.Background(), current, admitted, key)
	require.Error(t, err, "retaining the other arm does not admit its key")
	write(t, base, "evaluator", strings.Repeat("x", assurance.MaxLabelBytes+1))
	current, err = Prepare(context.Background(), locations)
	require.NoError(t, err, "resolver can retain inputs larger than primitive preparation")
	key.Arm = releasepolicy.Baseline
	_, err = CheckPrimitiveJoin(context.Background(), current, admitted, key)
	require.ErrorContains(t, err, "primitive bounded inventory")
}

func TestJoinCanonicalDeclarationsDoNotReplaceRawSources(t *testing.T) {
	base, prepared, admitted, key := joinFixture(t)
	before, err := prepared.Request(key.Arm, key.TaskID)
	require.NoError(t, err)
	write(t, base, "baseline/eval.yaml", snapshotEval+"\n# same declaration, different source bytes\n")
	locations := map[releasepolicy.Arm]ArmLocation{}
	for arm, binding := range prepared.bindings {
		locations[arm] = binding.location
	}
	changed, err := Prepare(context.Background(), locations)
	require.NoError(t, err)
	after, err := changed.Request(key.Arm, key.TaskID)
	require.NoError(t, err)
	require.Equal(t, before, after)
	_, err = CheckPrimitiveJoin(context.Background(), changed, admitted, key)
	require.ErrorContains(t, err, "source differs")
}

func TestFullCLIENTIdentityRemainsIndependentOfIntent(t *testing.T) {
	_, prepared, admitted, key := joinFixture(t)
	original, err := CheckPrimitiveJoin(context.Background(), prepared, admitted, key)
	require.NoError(t, err)
	var snapshots map[releasepolicy.Arm]armSnapshot
	require.NoError(t, json.Unmarshal(prepared.canonical, &snapshots))
	arm := snapshots[key.Arm]
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(arm.Tasks[0].Request, &fields))
	// This is a private fixture variant, not a public supplied-seal API.
	// Empty SkillPaths is omitted by requestIntent, but retained by CLIENT JSON.
	if string(fields["SkillPaths"]) == "null" {
		fields["SkillPaths"] = json.RawMessage("[]")
	} else {
		require.Equal(t, "[]", string(fields["SkillPaths"]))
		fields["SkillPaths"] = json.RawMessage("null")
	}
	arm.Tasks[0].Request, err = json.Marshal(fields)
	require.NoError(t, err)
	snapshots[key.Arm] = arm
	prepared.canonical, err = json.Marshal(snapshots)
	require.NoError(t, err)
	seal, err := evidence.JSONDigest(json.RawMessage(prepared.canonical))
	require.NoError(t, err)
	prepared.seal = *seal
	changed, err := CheckPrimitiveJoin(context.Background(), prepared, admitted, key)
	require.NoError(t, err)
	require.Equal(t, original.binding.RequestDigest, changed.binding.RequestDigest)
	require.NotEqual(t, original.snapshot, changed.snapshot)
	require.NotEqual(t, original.seal, changed.seal)
	_, err = original.Binding()
	require.NoError(t, err)
	_, err = changed.Binding()
	require.NoError(t, err)
}
