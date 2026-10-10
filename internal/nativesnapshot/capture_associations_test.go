package nativesnapshot

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/stretchr/testify/require"
)

// The oracle compiles the pinned original capture/files/native constructor
// against these very same actual fixture files before their roots are closed.
func TestG2CaptureOldIdentityAndRootFreeReconstruction(t *testing.T) {
	for _, name := range []string{"file", "directory", "empty_directory", "default_context",
		"relative_context", "task_context", "absolute_task_context", "disabled_prompt", "numeric_extremes", "empty_lock", "missing_literal",
		"zero_first_timeout", "ordered_instructions", "ordered_golden_tasks", "nil_inventory", "root_deleted", "multi_role_prompt"} {
		t.Run(name, func(t *testing.T) {
			base, locations := fixture(t)
			for _, prefix := range []string{"baseline", "candidate"} {
				task := snapshotTask
				switch name {
				case "file":
					task = "id: task\ninputs:\n  prompt: hello\n  context: {fixture: fixture.txt}\n"
					write(t, base, prefix+"/fixture.txt", "actual fixture")
				case "directory", "empty_directory":
					task = "id: task\ninputs:\n  prompt: hello\n  context: {fixture: fixture}\n"
					require.NoError(t, os.MkdirAll(filepath.Join(base, prefix, "fixture", "nested", "empty"), 0700))
					if name == "directory" {
						write(t, base, prefix+"/fixture/z", "z")
						write(t, base, prefix+"/fixture/nested/a", "a")
					}
				case "default_context":
					write(t, base, prefix+"/fixtures/a.txt", prefix)
					write(t, base, prefix+"/fixtures/instruction.md", "exact instruction\n")
					location := locations[releasepolicy.Arm(prefix)]
					location.ContextDir = ""
					locations[releasepolicy.Arm(prefix)] = location
				case "relative_context":
					// The supplied CLI context remains relative to captured CWD.
				case "task_context":
					task += "context_dir: " + prefix + "/context\n"
				case "absolute_task_context":
					task += "context_dir: " + filepath.Join(base, prefix, "context") + "\n"
				case "disabled_prompt":
					write(t, base, prefix+"/tasks/disabled.yaml", "id: disabled\nenabled: false\ninputs: {prompt_file: disabled.txt}\n")
					write(t, base, prefix+"/tasks/disabled.txt", "disabled prompt bytes")
				case "numeric_extremes":
					task += "  # actual parser values\n"
					task = strings.Replace(task, "  prompt_file:", "  context:\n    large: 18446744073709551615\n    negative: -9223372036854775808\n    nested: [9223372036854775807, -9007199254740993]\n  prompt_file:", 1)
				case "empty_lock":
					write(t, base, prefix+"/waza.lock", "")
				case "missing_literal":
					write(t, base, prefix+"/eval.yaml", strings.Replace(snapshotEval, `tasks: ["tasks/*.yaml"]`, `tasks: ["tasks/missing.yaml", "tasks/task.yaml"]`, 1))
				case "zero_first_timeout":
					task += "first_event_timeout_seconds: 0\n"
					write(t, base, prefix+"/eval.yaml", strings.Replace(snapshotEval, "  timeout_seconds: 30", "  timeout_seconds: 30\n  first_event_timeout_seconds: 5", 1))
				case "ordered_instructions":
					write(t, base, prefix+"/context/eval.md", "eval instruction")
					write(t, base, prefix+"/eval.yaml", strings.Replace(snapshotEval, "  timeout_seconds: 30", "  timeout_seconds: 30\n  instruction_files: [eval.md]", 1))
				case "ordered_golden_tasks":
					task += "golden: true\n"
					write(t, base, prefix+"/tasks/z.yaml", "id: first\ngolden: true\ninputs: {prompt: hello}\n")
					write(t, base, prefix+"/eval.yaml", strings.Replace(snapshotEval, `tasks: ["tasks/*.yaml"]`, `tasks: ["tasks/z.yaml", "tasks/task.yaml"]`, 1))
				case "nil_inventory":
					task = "id: task\ninputs: {prompt: hello}\n"
				case "multi_role_prompt":
					task = "id: task\ninputs: {prompt_file: task.yaml}\n"
				}
				write(t, base, prefix+"/tasks/task.yaml", task)
			}
			original := frozenOriginalCapture(t, locations)
			old, err := Prepare(context.Background(), locations)
			require.NoError(t, err)
			capture, err := prepareProjectionCapture(context.Background(), locations)
			require.NoError(t, err)
			require.Equal(t, old.canonical, capture.prepared.canonical)
			require.Equal(t, old.seal, capture.prepared.seal)
			require.Equal(t, old.sources, capture.prepared.sources)
			require.Equal(t, original.Canonical, capture.prepared.canonical)
			require.Equal(t, original.Sources, capture.prepared.sources)
			input, err := detachProjectionInput(context.Background(), capture)
			require.NoError(t, err)
			expected := original.Projection
			// Close the actual roots without changing the fixture cleanup owner.
			require.NoError(t, locations[releasepolicy.Baseline].Root.Close())
			if name == "root_deleted" {
				require.NoError(t, os.RemoveAll(base))
			} else {
				require.NoError(t, os.Rename(base, base+"-detached"))
				t.Cleanup(func() { require.NoError(t, os.Rename(base+"-detached", base)) })
			}
			t.Chdir(t.TempDir())
			got, err := reconstructProjection(context.Background(), input)
			require.NoError(t, err)
			require.Equal(t, expected, got)
			capture.prepared.canonical[0] = '!'
			capture.associations.canonical[0] = '!'
			capture.prepared.sources[releasepolicy.Baseline][0].Bytes[0] ^= 1
			again, err := reconstructProjection(context.Background(), input)
			require.NoError(t, err)
			require.Equal(t, got, again)
		})
	}
}

func resealG2(t *testing.T, capture *projectionCapture, mutate func(*captureAssociations)) {
	t.Helper()
	var associated captureAssociations
	require.NoError(t, json.Unmarshal(capture.associations.canonical, &associated))
	mutate(&associated)
	raw, err := json.Marshal(associated)
	require.NoError(t, err)
	capture.associations.canonical = raw
	digest, err := evidence.JSONDigest(json.RawMessage(raw))
	require.NoError(t, err)
	capture.associations.seal = *digest
}

func TestG2OwnedTraceRejectsTamperingAndNoPartialOutput(t *testing.T) {
	for _, name := range []string{"missing", "old_version", "new_version", "wrong_snapshot",
		"extra_event", "missing_event", "reordered_event", "ordinal", "scope", "phase", "role",
		"source_index", "node_index", "source_path", "source_bytes", "client_bytes", "node_extra",
		"node_missing", "kind_changed", "source_extra", "disabled_flip"} {
		t.Run(name, func(t *testing.T) {
			_, locations := fixture(t)
			capture, err := prepareProjectionCapture(context.Background(), locations)
			require.NoError(t, err)
			switch name {
			case "missing":
				capture.associations = retainedAssociations{}
			case "source_bytes":
				capture.prepared.sources[releasepolicy.Baseline][0].Bytes[0] ^= 1
			case "client_bytes", "disabled_flip":
				var snapshots map[releasepolicy.Arm]armSnapshot
				require.NoError(t, json.Unmarshal(capture.prepared.canonical, &snapshots))
				arm := snapshots[releasepolicy.Baseline]
				if name == "client_bytes" {
					arm.Tasks[0].Request = json.RawMessage(`{"Message":"changed"}`)
				} else {
					arm.Tasks[0].Enabled = false
				}
				snapshots[releasepolicy.Baseline] = arm
				capture.prepared.canonical, err = json.Marshal(snapshots)
				require.NoError(t, err)
				seal, err := evidence.JSONDigest(json.RawMessage(capture.prepared.canonical))
				require.NoError(t, err)
				capture.prepared.seal = *seal
				resealG2(t, capture, func(a *captureAssociations) { a.Snapshot = *seal })
			default:
				resealG2(t, capture, func(a *captureAssociations) {
					arm := a.Arms[releasepolicy.Baseline]
					switch name {
					case "old_version":
						a.Version = "0"
					case "new_version":
						a.Version = "2"
					case "wrong_snapshot":
						a.Snapshot.SHA256 = strings.Repeat("0", 64)
					case "extra_event":
						arm.Events = append(arm.Events, arm.Events[0])
					case "missing_event":
						arm.Events = arm.Events[1:]
					case "reordered_event":
						arm.Events[0], arm.Events[1] = arm.Events[1], arm.Events[0]
					case "ordinal":
						arm.Events[0].Ordinal = 9
					case "scope":
						arm.Events[0].ScopeIndex = 1
					case "phase":
						arm.Events[0].Phase = taskPhase
					case "role":
						arm.Events[0].Role = "executable"
					case "source_index":
						value := uint32(9999)
						arm.Events[0].Source = &value
					case "node_index":
						value := uint32(9999)
						arm.Events[1].Node = &value
					case "source_path":
						arm.Events[0].Path = "evaluator"
					case "node_extra":
						arm.Nodes = append(arm.Nodes, capturedNode{Path: "zz-unused", State: missingNode})
					case "node_missing":
						arm.Nodes = arm.Nodes[1:]
					case "kind_changed":
						for i := range arm.Nodes {
							if arm.Nodes[i].Kind != nil {
								value := regularNode
								arm.Nodes[i].Kind = &value
								break
							}
						}
					case "source_extra":
						value := uint32(0)
						arm.Events[1].Source = &value
					}
					a.Arms[releasepolicy.Baseline] = arm
				})
			}
			input, err := detachProjectionInput(context.Background(), capture)
			if err != nil {
				require.Equal(t, detachedProjectionInput{}, input)
				return
			}
			result, err := reconstructProjection(context.Background(), input)
			require.Error(t, err)
			require.Nil(t, result)
		})
	}
}

func TestG2StrictSupplementTokens(t *testing.T) {
	_, locations := fixture(t)
	capture, err := prepareProjectionCapture(context.Background(), locations)
	require.NoError(t, err)
	original := string(capture.associations.canonical)
	for name, raw := range map[string]string{
		"unknown":          strings.Replace(original, `"kind":`, `"extra":true,"kind":`, 1),
		"duplicate":        strings.Replace(original, `"version":"1"`, `"version":"1","version":"1"`, 1),
		"null_kind":        strings.Replace(original, `"kind":"directory"`, `"kind":null`, 1),
		"null_nodes":       strings.Replace(original, `"nodes":[`, `"nodes":null,"discard":[`, 1),
		"wrong_integer":    strings.Replace(original, `"ordinal":1`, `"ordinal":"1"`, 1),
		"negative_integer": strings.Replace(original, `"ordinal":1`, `"ordinal":-1`, 1),
		"fraction":         strings.Replace(original, `"ordinal":1`, `"ordinal":1.0`, 1),
		"trailing":         original + "{}",
		"missing_required": strings.Replace(original, `"scope_index":0,`, "", 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := decodeAssociations(context.Background(), []byte(raw))
			require.Error(t, err)
		})
	}
}

func TestG2NilCanceledBoundaries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	capture, err := prepareProjectionCapture(ctx, nil)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, capture)
	input, err := detachProjectionInput(ctx, nil)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, detachedProjectionInput{}, input)
	input, err = detachProjectionInput(context.Background(), nil)
	require.Error(t, err)
	require.Equal(t, detachedProjectionInput{}, input)
	result, err := reconstructProjection(ctx, detachedProjectionInput{})
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, result)
	capture, err = prepareProjectionCapture(nil, nil) //nolint:staticcheck // Explicit nil-context boundary.
	require.Error(t, err)
	require.Nil(t, capture)
	input, err = detachProjectionInput(nil, nil) //nolint:staticcheck // Explicit nil-context boundary.
	require.Error(t, err)
	require.Equal(t, detachedProjectionInput{}, input)
	var total int
	snapshot, sources, associated, err := captureArmWithAssociations(context.Background(), boundLocation{}, &total)
	require.Error(t, err)
	require.Equal(t, armSnapshot{}, snapshot)
	require.Nil(t, sources)
	require.Equal(t, armAssociations{}, associated)
	canonical, inventories, arms, err := recaptureWithAssociations(ctx, nil)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, canonical)
	require.Nil(t, inventories)
	require.Nil(t, arms)
	canonical, inventories, arms, err = recaptureWithAssociations(context.Background(), nil)
	require.Error(t, err)
	require.Nil(t, canonical)
	require.Nil(t, inventories)
	require.Nil(t, arms)
}

func TestG2ReconstructionCancellationAtEarlyMiddleAndFinalBoundary(t *testing.T) {
	_, locations := fixture(t)
	capture, err := prepareProjectionCapture(context.Background(), locations)
	require.NoError(t, err)
	input, err := detachProjectionInput(context.Background(), capture)
	require.NoError(t, err)
	observed := &readContext{Context: context.Background(), onCheck: func(int) error { return nil }}
	_, err = reconstructProjection(observed, input)
	require.NoError(t, err)
	require.Greater(t, observed.calls, 3)
	for _, at := range []int{2, observed.calls / 2, observed.calls - 1} {
		ctx := &readContext{Context: context.Background(), onCheck: func(call int) error {
			if call >= at {
				return context.Canceled
			}
			return nil
		}}
		result, err := reconstructProjection(ctx, input)
		require.ErrorIs(t, err, context.Canceled)
		require.Nil(t, result)
	}
}

func TestG2ValidRangeScopeReassignmentStillRejectsConsumerMismatch(t *testing.T) {
	for _, name := range []string{"task_scope", "discovery_scope"} {
		t.Run(name, func(t *testing.T) {
			base, locations := fixture(t)
			write(t, base, "baseline/tasks/z.yaml", "id: z\ninputs: {prompt: hello}\n")
			write(t, base, "baseline/eval.yaml", strings.Replace(snapshotEval, `tasks: ["tasks/*.yaml"]`, `tasks: ["tasks/task.yaml", "tasks/z.yaml"]`, 1))
			capture, err := prepareProjectionCapture(context.Background(), locations)
			require.NoError(t, err)
			resealG2(t, capture, func(all *captureAssociations) {
				arm := all.Arms[releasepolicy.Baseline]
				for i, event := range arm.Events {
					if name == "task_scope" && event.Phase == requestPhase && event.ScopeIndex == 0 ||
						name == "discovery_scope" && event.Phase == discoveryPhase && event.ScopeIndex == 0 {
						arm.Events[i].ScopeIndex = 1
						break
					}
				}
				all.Arms[releasepolicy.Baseline] = arm
			})
			input, err := detachProjectionInput(context.Background(), capture)
			require.NoError(t, err, "the reassigned scope is in range; semantic replay must reject it")
			result, err := reconstructProjection(context.Background(), input)
			require.ErrorContains(t, err, "consumer mismatch")
			require.Nil(t, result)
		})
	}
}
