package nativesnapshot

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/controlledprojection"
	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/nativetask"
	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/stretchr/testify/require"
)

var slotTestIDs = DeclaredEvalIDs{Baseline: "declared-baseline", Candidate: "declared-candidate"}

func slotTestDigest(t *testing.T, value any) models.EvidenceDigest {
	t.Helper()
	d, err := evidence.JSONDigest(value)
	require.NoError(t, err)
	return *d
}

func slotTestPolicy(t *testing.T, projections map[releasepolicy.Arm]controlledprojection.SourceProjection) []byte {
	t.Helper()
	union, err := controlledprojection.GoldenUnion(projections[releasepolicy.Baseline], projections[releasepolicy.Candidate])
	require.NoError(t, err)
	p := &releasepolicy.Policy{Kind: releasepolicy.PolicyKind, Version: releasepolicy.Version,
		GoldenRule: "both_arms_first_attempt_pass", FamilyRule: "single_contrast_external_family_bound",
		Changes: []releasepolicy.AllowedChange{}, GoldenIDs: union,
		Requirements: releasepolicy.Requirements{IdentityDomains: []string{
			"task_definition", "resolved_prompt", "fixture_inventory", "instruction_inventory",
			"grader_configuration", "dependency_mode", "grader_implementation", "rubric_inventory", "lock_inventory"},
			Billing: []releasepolicy.BillingRequirement{}},
		Arms: map[releasepolicy.Arm]releasepolicy.PolicyArm{},
		Design: releasepolicy.Design{Estimand: "fixed_suite_repeated_execution", Endpoint: "first_attempt_pass",
			Alpha: "0.05", FamilySize: 1, MaximumHalfWidth: "1", Margin: "1", Accept: "noninferiority",
			Independence: releasepolicy.Independence{Assessment: "unjustified",
				Justification: "Offline fixture declaration, not independent observed executions.",
				Limitations:   []string{"Static reservations grant no execution or release acceptance."}},
			Allocation: releasepolicy.Allocation{Mechanism: "sha256_seed_cluster_first_bit", Seed: strings.Repeat("ab", 32)},
		},
	}
	tasks := []releasepolicy.PlannedTask{}
	plan := projections[releasepolicy.Baseline].Arm.Plan
	// Collector task order deliberately differs from the full G1 owning order.
	for i := len(plan.Tasks) - 1; i >= 0; i-- {
		task := plan.Tasks[i]
		trials := make([]int, task.Settings.TrialsPerTask)
		for j := range trials {
			trials[j] = j + 1
		}
		tasks = append(tasks, releasepolicy.PlannedTask{ID: task.ID,
			Weight: json.Number(strconv.FormatFloat(1/float64(len(plan.Tasks)), 'g', -1, 64)), TrialOrdinals: trials})
	}
	p.Design.Clusters = []releasepolicy.Cluster{{ID: "cluster", Weight: "1", Tasks: tasks}}
	order, err := releasepolicy.ArmOrder(p.Design.Allocation.Seed, "cluster")
	require.NoError(t, err)
	p.Design.Allocation.Assignments = []releasepolicy.Assignment{{ClusterID: "cluster", Order: order}}
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		p.Arms[arm] = projections[arm].Arm
	}
	raw, err := releasepolicy.SealJSON(p)
	require.NoError(t, err)
	_, err = releasepolicy.DecodePolicy(raw)
	require.NoError(t, err, "test-only policy must first pass the actual independent raw admission")
	return raw
}

type slotFixtureResult struct {
	base      string
	locations map[releasepolicy.Arm]ArmLocation
	capture   *projectionCapture
	original  originalCaptureResult
	raw       []byte
	set       *SlotSet
}

func slotFixture(t *testing.T, variant string) slotFixtureResult {
	t.Helper()
	base, locations := fixture(t)
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		prefix := string(arm)
		write(t, base, prefix+"/context/a.txt", "identical fixture bytes")
		task := snapshotTask
		switch variant {
		case "directory", "empty_directory":
			task = "id: task\ninputs:\n  prompt: hello\n  context: {fixture: fixture}\n"
			require.NoError(t, os.MkdirAll(filepath.Join(base, prefix, "fixture/nested/empty"), 0700))
			if variant == "directory" {
				write(t, base, prefix+"/fixture/z", "z")
				write(t, base, prefix+"/fixture/nested/a", "a")
			}
		case "default_context":
			write(t, base, prefix+"/fixtures/a.txt", "same")
			write(t, base, prefix+"/fixtures/instruction.md", "instruction")
			location := locations[arm]
			location.ContextDir = ""
			locations[arm] = location
		case "relative_context":
			location := locations[arm]
			location.ContextDir = prefix + "/context"
			locations[arm] = location
		case "disabled_prompt":
			write(t, base, prefix+"/tasks/disabled.yaml", "id: disabled\nenabled: false\ngolden: true\ninputs: {prompt_file: disabled.txt}\n")
			write(t, base, prefix+"/tasks/disabled.txt", "source-bound disabled prompt")
		case "numeric":
			task = strings.Replace(task, "  prompt_file:", "  context:\n    large: 18446744073709551615\n    negative: -9223372036854775808\n    nested: [9223372036854775807, -9007199254740993]\n  prompt_file:", 1)
		case "empty_lock":
			write(t, base, prefix+"/waza.lock", "")
		case "present_lock":
			write(t, base, prefix+"/waza.lock", "{}")
		case "ordered_instructions":
			task = strings.Replace(task, `["instruction.md"]`, `["second.md", "instruction.md"]`, 1)
			write(t, base, prefix+"/context/second.md", "second")
		case "no_inventory":
			task = "id: task\ninputs: {prompt: hello}\n"
		case "multi_role":
			task = "id: task\ninputs: {prompt_file: task.yaml}\n"
		case "multi", "golden":
			task += "golden: true\n"
			write(t, base, prefix+"/tasks/z.yaml", "id: zeta\ngolden: true\ninputs: {prompt: hello}\n")
			eval := strings.Replace(snapshotEval, `tasks: ["tasks/*.yaml"]`, `tasks: ["tasks/z.yaml", "tasks/task.yaml"]`, 1)
			eval = strings.Replace(eval, "  trials_per_task: 1", "  trials_per_task: 2\n  max_attempts: 2", 1)
			write(t, base, prefix+"/eval.yaml", eval)
		}
		write(t, base, prefix+"/tasks/task.yaml", task)
	}
	// No duplicated reconstruction oracle: run the pinned original overlay on
	// these exact roots and bytes, then author the test policy from its plans.
	original := frozenOriginalCapture(t, locations)
	capture, err := prepareProjectionCapture(context.Background(), locations)
	require.NoError(t, err)
	require.Equal(t, original.Canonical, capture.prepared.canonical)
	require.Equal(t, original.Sources, capture.prepared.sources)
	raw := slotTestPolicy(t, original.Projection)
	set, err := PrepareSlotSet(context.Background(), raw, capture, slotTestIDs)
	require.NoError(t, err)
	require.Equal(t, original.Projection, set.projections)
	require.Equal(t, original.Canonical, set.input.canonical)
	require.Equal(t, original.Sources, set.input.sources)
	return slotFixtureResult{base, locations, capture, original, raw, set}
}

// Independent pinned collector/journal nesting, not the slot visitor.
func originalSlotOrder(t *testing.T, raw []byte, ids DeclaredEvalIDs) []Slot {
	t.Helper()
	p, err := releasepolicy.DecodePolicy(raw)
	require.NoError(t, err)
	result := []Slot{}
	for _, cluster := range p.Design.Clusters {
		order, err := releasepolicy.ArmOrder(p.Design.Allocation.Seed, cluster.ID)
		require.NoError(t, err)
		for _, arm := range order {
			evalID := ids.Baseline
			if arm == releasepolicy.Candidate {
				evalID = ids.Candidate
			}
			for _, task := range cluster.Tasks {
				var settings releasepolicy.Settings
				found := false
				for _, row := range p.Arms[arm].Plan.Tasks {
					if row.ID == task.ID {
						settings, found = row.Settings, true
					}
				}
				require.True(t, found)
				for _, trial := range task.TrialOrdinals {
					for attempt := 1; attempt <= settings.MaxAttempts; attempt++ {
						result = append(result, Slot{Index: len(result), Key: releasepolicy.AttemptKey{
							SampleKey: releasepolicy.SampleKey{ClusterID: cluster.ID, TaskID: task.ID, Trial: trial},
							Arm:       arm, EvalID: evalID, Attempt: attempt}})
					}
				}
			}
		}
	}
	return result
}

func admitSlot(t *testing.T, f slotFixtureResult, key releasepolicy.AttemptKey, mutate func(*nativetask.Profile)) *nativetask.Admitted {
	t.Helper()
	p, err := releasepolicy.DecodePolicy(f.raw)
	require.NoError(t, err)
	profile := &nativetask.Profile{Kind: "waza.release-native-task-profile", Version: "1.0",
		Nonce: strings.Repeat("a", 64), Selection: nativetask.Selection{Operation: "native_text_task", Version: "1.0"},
		PolicyDigest: p.Digest, PermissionMode: "deny_all", Arms: map[releasepolicy.Arm]nativetask.ArmPlan{}}
	primitives := map[releasepolicy.Arm]*nativetask.Prepared{}
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		request, err := f.capture.prepared.Request(arm, key.TaskID)
		require.NoError(t, err)
		sources := []nativetask.Source{}
		for _, source := range f.original.Sources[arm] {
			sources = append(sources, nativetask.Source{Root: f.locations[arm].Root, Path: source.Path})
		}
		primitive, err := nativetask.Prepare(context.Background(), arm, key.TaskID, request, sources)
		require.NoError(t, err)
		primitives[arm] = primitive
		profile.Arms[arm] = nativetask.ArmPlan{PlanDigest: f.original.Projection[arm].Arm.Digest,
			Requests: []nativetask.RequestPlan{{TaskID: key.TaskID, RequestDigest: primitive.RequestDigest()}}}
	}
	if mutate != nil {
		mutate(profile)
	}
	raw, err := releasepolicy.SealJSON(profile)
	require.NoError(t, err)
	profile, err = nativetask.DecodeProfile(raw)
	require.NoError(t, err)
	admitted, err := nativetask.Admit(context.Background(), primitives[key.Arm], profile, key)
	require.NoError(t, err)
	return admitted
}

func TestSlotSetOriginalSourceParityAndEveryPrimitive(t *testing.T) {
	for _, variant := range []string{"file", "directory", "empty_directory", "default_context", "relative_context",
		"disabled_prompt", "numeric", "empty_lock", "present_lock", "ordered_instructions", "no_inventory", "multi_role", "multi"} {
		t.Run(variant, func(t *testing.T) {
			f := slotFixture(t, variant)
			expected := originalSlotOrder(t, f.raw, slotTestIDs)
			slots, err := f.set.Slots(context.Background())
			require.NoError(t, err)
			require.Equal(t, expected, slots)
			for i, slot := range expected {
				admitted := admitSlot(t, f, slot.Key, nil)
				joined, err := CheckSlotJoin(context.Background(), f.set, i, admitted)
				require.NoError(t, err, "each slot requires a separate actual primitive join")
				gotSlot, gotAdmission, err := joined.Binding(context.Background())
				require.NoError(t, err)
				wantAdmission, err := admitted.Binding()
				require.NoError(t, err)
				require.Equal(t, slot, gotSlot)
				require.Equal(t, wantAdmission, gotAdmission)
				gotSlot.Key.EvalID = "mutated returned copy"
				gotAdmission.CollectionID = "mutated returned copy"
				again, admission, err := joined.Binding(context.Background())
				require.NoError(t, err)
				require.Equal(t, slot, again)
				require.Equal(t, wantAdmission, admission)
			}
			slots[0].Key.Attempt = 999
			again, err := f.set.Slots(context.Background())
			require.NoError(t, err)
			require.Equal(t, expected, again)
		})
	}
}

func TestSlotSetStaticOrderBothSeedBits(t *testing.T) {
	f := slotFixture(t, "multi")
	p, err := releasepolicy.DecodePolicy(f.raw)
	require.NoError(t, err)
	seen := map[releasepolicy.Arm]bool{}
	for n := 0; len(seen) < 2 && n < 100; n++ {
		p.Design.Allocation.Seed = fmt.Sprintf("%064x", n)
		order, err := releasepolicy.ArmOrder(p.Design.Allocation.Seed, "cluster")
		require.NoError(t, err)
		p.Design.Allocation.Assignments[0].Order = order
		raw, err := releasepolicy.SealJSON(p)
		require.NoError(t, err)
		set, err := PrepareSlotSet(context.Background(), raw, f.capture, slotTestIDs)
		require.NoError(t, err)
		slots, err := set.Slots(context.Background())
		require.NoError(t, err)
		require.Equal(t, originalSlotOrder(t, raw, slotTestIDs), slots)
		require.Len(t, slots, 16)
		// There is no outcome/start/skip input: all possible retries remain.
		require.Equal(t, 2, slots[1].Key.Attempt)
		seen[order[0]] = true
	}
	require.Len(t, seen, 2)
}

func TestSlotSetRootFreeAfterCloseRenameDeleteAndCWD(t *testing.T) {
	for _, action := range []string{"close", "rename", "delete", "live_mutation"} {
		t.Run(action, func(t *testing.T) {
			f := slotFixture(t, "file")
			expected := originalSlotOrder(t, f.raw, slotTestIDs)
			admissions := make([]*nativetask.Admitted, len(expected))
			for i, slot := range expected {
				admissions[i] = admitSlot(t, f, slot.Key, nil)
			}
			switch action {
			case "live_mutation":
				write(t, f.base, "baseline/tasks/prompt.txt", "changed live bytes")
			default:
				require.NoError(t, f.locations[releasepolicy.Baseline].Root.Close())
				switch action {
				case "rename":
					require.NoError(t, os.Rename(f.base, f.base+"-detached"))
					t.Cleanup(func() { require.NoError(t, os.Rename(f.base+"-detached", f.base)) })
				case "delete":
					require.NoError(t, os.RemoveAll(f.base))
				}
			}
			t.Chdir(t.TempDir())
			slots, err := f.set.Slots(context.Background())
			require.NoError(t, err)
			require.Equal(t, expected, slots)
			for i, slot := range expected {
				joined, err := CheckSlotJoin(context.Background(), f.set, i, admissions[i])
				require.NoError(t, err)
				got, _, err := joined.Binding(context.Background())
				require.NoError(t, err)
				require.Equal(t, slot, got)
			}
		})
	}
}

func resealSlotPolicyObject(t *testing.T, raw []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	value, err := jsonutil.Parse(raw)
	require.NoError(t, err)
	object := slotTestObject(t, value)
	mutate(object)
	raw, err = releasepolicy.SealJSON(object)
	require.NoError(t, err)
	return raw
}

func slotTestObject(t *testing.T, value any) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	require.True(t, ok)
	return object
}

func slotTestArray(t *testing.T, value any) []any {
	t.Helper()
	array, ok := value.([]any)
	require.True(t, ok)
	return array
}

func TestSlotSetRawPolicyPlansNumbersGoldenAndRequirements(t *testing.T) {
	f := slotFixture(t, "golden")
	for _, name := range []string{"runtime", "assurance", "input_tokens", "output_tokens", "ai_credits", "provider_currency",
		"runtime_reason", "mock_runtime", "model_version", "dependency", "other_settings", "plan_number_fraction", "plan_number_exponent",
		"plan_order", "identity_order", "golden_missing", "golden_extra", "golden_reorder", "golden_duplicate", "golden_null", "golden_omitted"} {
		t.Run(name, func(t *testing.T) {
			raw := resealSlotPolicyObject(t, f.raw, func(o map[string]any) {
				req := slotTestObject(t, o["requirements"])
				switch name {
				case "runtime", "assurance":
					req[name] = true
				case "input_tokens", "output_tokens", "ai_credits", "provider_currency":
					billing := map[string]any{"axis": name, "maximum": json.Number("1")}
					if name == "provider_currency" {
						billing["currency"] = "USD"
					}
					req["billing"] = []any{billing}
				case "golden_missing":
					o["required_golden_task_ids"] = []string{"task"}
				case "golden_extra":
					o["required_golden_task_ids"] = []string{"task", "zeta", "extra"}
				case "golden_reorder":
					o["required_golden_task_ids"] = []string{"zeta", "task"}
				case "golden_duplicate":
					o["required_golden_task_ids"] = []string{"task", "task", "zeta"}
				case "golden_null":
					o["required_golden_task_ids"] = nil
				case "golden_omitted":
					delete(o, "required_golden_task_ids")
				default:
					for _, arm := range []string{"baseline", "candidate"} {
						a := slotTestObject(t, slotTestObject(t, o["arms"])[arm])
						plan := slotTestObject(t, a["resolved_plan"])
						tasks := slotTestArray(t, plan["tasks"])
						for _, task := range tasks {
							row := slotTestObject(t, task)
							runtime := slotTestObject(t, row["expected_runtime"])
							settings := slotTestObject(t, row["settings"])
							switch name {
							case "runtime_reason":
								runtime["reason"] = "another unavailable reason"
							case "mock_runtime":
								runtime["availability"], runtime["reason"] = "expected", ""
								runtime["engine_implementation"], runtime["model_version"] = "mock", "mock_no_provider"
							case "model_version":
								runtime["model_version"] = "invented"
							case "other_settings":
								settings["other_settings_digest"] = slotTestDigest(t, "changed metadata")
							case "plan_number_fraction":
								settings["timeout_seconds"] = json.Number("30.0")
							case "plan_number_exponent":
								settings["timeout_seconds"] = json.Number("3e1")
							}
						}
						if name == "plan_order" {
							slices.Reverse(tasks)
						}
						identities := slotTestArray(t, plan["identities"])
						if name == "identity_order" {
							identities[0], identities[1] = identities[1], identities[0]
						}
						if name == "dependency" {
							for _, identity := range identities {
								id := slotTestObject(t, identity)
								if id["domain"] == "dependency_mode" {
									id["digest"] = slotTestDigest(t, "invented SDK/server isolation")
								}
							}
						}
						a["resolved_plan_digest"] = slotTestDigest(t, plan)
					}
				}
			})
			set, err := PrepareSlotSet(context.Background(), raw, f.capture, slotTestIDs)
			require.Error(t, err)
			require.Nil(t, set)
		})
	}
	for _, token := range []string{"1", "1.0", "1e0"} {
		t.Run("raw_design_number_"+token, func(t *testing.T) {
			raw := resealSlotPolicyObject(t, f.raw, func(o map[string]any) {
				slotTestObject(t, o["design"])["noninferiority_margin"] = json.Number(token)
			})
			set, err := PrepareSlotSet(context.Background(), raw, f.capture, slotTestIDs)
			require.NoError(t, err)
			require.Equal(t, raw, set.rawPolicy)
			p, err := releasepolicy.DecodePolicy(raw)
			require.NoError(t, err)
			require.Equal(t, token, p.Design.Margin.String())
		})
	}
	var pretty strings.Builder
	var compact any
	decoder := json.NewDecoder(strings.NewReader(string(f.raw)))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&compact))
	formatted, err := json.MarshalIndent(compact, "", "  ")
	require.NoError(t, err)
	pretty.Write(formatted)
	set, err := PrepareSlotSet(context.Background(), []byte(pretty.String()), f.capture, slotTestIDs)
	require.NoError(t, err)
	require.Equal(t, f.set.projections, set.projections)
	require.NotEqual(t, f.set.seal, set.seal, "raw source identity remains separate from JSON-v1 plan identity")
}

func TestSlotSetWrongPrimitiveIdentityAndIndex(t *testing.T) {
	f := slotFixture(t, "multi")
	slot := f.set.slots[0]
	for _, name := range []string{"cluster", "eval", "trial", "attempt", "arm", "policy", "plan", "task", "nil", "request"} {
		t.Run(name, func(t *testing.T) {
			key := slot.Key
			var mutate func(*nativetask.Profile)
			switch name {
			case "cluster":
				key.ClusterID += "-other"
			case "eval":
				key.EvalID += "-other"
			case "trial":
				key.Trial++
			case "attempt":
				key.Attempt++
			case "arm":
				if key.Arm == releasepolicy.Baseline {
					key.Arm = releasepolicy.Candidate
				} else {
					key.Arm = releasepolicy.Baseline
				}
			case "task":
				if key.TaskID == "task" {
					key.TaskID = "zeta"
				} else {
					key.TaskID = "task"
				}
			case "policy":
				mutate = func(p *nativetask.Profile) { p.PolicyDigest = slotTestDigest(t, "different policy") }
			case "plan":
				mutate = func(p *nativetask.Profile) {
					a := p.Arms[key.Arm]
					a.PlanDigest = slotTestDigest(t, "different full plan")
					p.Arms[key.Arm] = a
				}
			}
			admitted := admitSlot(t, f, key, mutate)
			if name == "nil" {
				admitted = nil
			}
			if name == "request" {
				// A source-valid declaration with a different request cannot use
				// the original primitive, even when policy/plan remain original.
				fresh, err := revalidateSlotSet(context.Background(), f.set)
				require.NoError(t, err)
				fresh.input.canonical[0] = '!'
				joined, err := CheckSlotJoin(context.Background(), fresh, 0, admitted)
				require.Error(t, err)
				require.Nil(t, joined)
				return
			}
			joined, err := CheckSlotJoin(context.Background(), f.set, 0, admitted)
			require.Error(t, err)
			require.Nil(t, joined)
		})
	}
	admitted := admitSlot(t, f, slot.Key, nil)
	for _, index := range []int{-1, len(f.set.slots), int(^uint(0) >> 1)} {
		joined, err := CheckSlotJoin(context.Background(), f.set, index, admitted)
		require.Error(t, err)
		require.Nil(t, joined)
	}
}

func TestSlotSetOldHandlesCompileIneligible(t *testing.T) {
	packageDir, err := os.Getwd()
	require.NoError(t, err)
	repository := filepath.Dir(filepath.Dir(packageDir))
	for _, expression := range []string{"&Prepared{}", "func() *Prepared { p := &Prepared{}; copy := *p; return &copy }()",
		"&Joined{}", "detachedProjectionInput{}", "map[string]any{}"} {
		t.Run(expression, func(t *testing.T) {
			scratch := t.TempDir()
			source := filepath.Join(scratch, "negative.go")
			require.NoError(t, os.WriteFile(source, []byte("package nativesnapshot\nimport \"context\"\nvar _, _ = PrepareSlotSet(context.Background(), nil, "+expression+", DeclaredEvalIDs{})\n"), 0600))
			overlay, err := json.Marshal(struct{ Replace map[string]string }{map[string]string{
				filepath.Join(packageDir, "slot_compile_negative_test.go"): source}})
			require.NoError(t, err)
			overlayPath := filepath.Join(scratch, "negative-overlay.json")
			require.NoError(t, os.WriteFile(overlayPath, overlay, 0600))
			command := exec.CommandContext(t.Context(), "go", "test", "-overlay="+overlayPath, "./internal/nativesnapshot", "-run=^$", "-count=1")
			command.Dir = repository
			command.Env = append(os.Environ(), "ENABLE_COPILOT_TESTS=false", "NO_COLOR=1")
			output, err := command.CombinedOutput()
			require.Error(t, err)
			require.Contains(t, string(output), "cannot use")
			require.Contains(t, string(output), "*projectionCapture")
		})
	}
}
