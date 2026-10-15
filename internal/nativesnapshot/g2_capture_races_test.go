package nativesnapshot

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/stretchr/testify/require"
)

func TestG2CaptureActualMissingAndSourceChangesAtFrozenBoundary(t *testing.T) {
	for _, name := range []string{"missing_lock_present", "missing_literal_present", "source_changed", "directory_changed", "cancel_frozen", "physical_alias"} {
		t.Run(name, func(t *testing.T) {
			base, locations := fixture(t)
			location := locations[releasepolicy.Baseline]
			if name == "missing_literal_present" {
				write(t, base, "baseline/eval.yaml", strings.Replace(snapshotEval, `tasks: ["tasks/*.yaml"]`, `tasks: ["tasks/missing.yaml", "tasks/task.yaml"]`, 1))
			}
			if name == "directory_changed" {
				write(t, base, "baseline/tasks/task.yaml", "id: task\ninputs: {prompt: hello, context: {fixture: fixture}}\n")
				write(t, base, "baseline/fixture/input", "original")
			}
			if name == "physical_alias" {
				require.NoError(t, os.Remove(filepath.Join(base, "baseline/context/instruction.md")))
				require.NoError(t, os.Link(filepath.Join(base, "baseline/context/a.txt"), filepath.Join(base, "baseline/context/instruction.md")))
			}
			binding, err := bind(context.Background(), location)
			require.NoError(t, err)
			ctx := &readContext{Context: context.Background()}
			recorder := newAssociationRecorder(ctx, base, &associationBudget{})
			mutated := false
			ctx.onCheck = func(int) error {
				if recorder.phase != requestPhase || mutated || name == "physical_alias" {
					return nil
				}
				mutated = true
				switch name {
				case "missing_lock_present":
					write(t, base, "baseline/waza.lock", "")
				case "missing_literal_present":
					write(t, base, "baseline/tasks/missing.yaml", "id: formerly-absent\ninputs: {prompt: hello}\n")
				case "source_changed":
					write(t, base, "baseline/context/a.txt", "changed source size")
				case "directory_changed":
					write(t, base, "baseline/fixture/new", "new directory entry")
				case "cancel_frozen":
					return context.Canceled
				}
				return nil
			}
			total := 0
			_, inventory, err := captureArmRecorded(ctx, binding, &total, recorder)
			require.Error(t, err)
			require.Nil(t, inventory)
			if name != "physical_alias" {
				require.True(t, mutated)
			}
			if name == "cancel_frozen" {
				require.ErrorIs(t, err, context.Canceled)
			}
		})
	}
}

func TestG2MissingRecheckIsFiniteAndRejectsOnlyObservedChanges(t *testing.T) {
	files, base := reader(t, context.Background())
	recorder := newAssociationRecorder(context.Background(), base, &associationBudget{})
	recorder.set(lockPhase, 0)
	info, observedErr := files.check("missing-lock")
	require.ErrorIs(t, observedErr, fs.ErrNotExist)
	require.NoError(t, recorder.observe("missing-lock", "present_lock", info, observedErr))
	write(t, base, "not-observed", "not an absence claim")
	require.NoError(t, recorder.recheckMissing(files))
	write(t, base, "missing-lock", "")
	require.ErrorContains(t, recorder.recheckMissing(files), "missing observation changed")
}

func TestG2MissingFinalRecheckPreservesCancellation(t *testing.T) {
	files, base := reader(t, context.Background())
	recorder := newAssociationRecorder(context.Background(), base, &associationBudget{})
	recorder.set(lockPhase, 0)
	info, observedErr := files.check("waza.lock")
	require.ErrorIs(t, observedErr, fs.ErrNotExist)
	require.NoError(t, recorder.observe("waza.lock", "present_lock", info, observedErr))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	files.ctx, recorder.ctx = ctx, ctx
	require.ErrorIs(t, recorder.recheckMissing(files), context.Canceled)
}

func TestG2SingleArmAssociatedCaptureReturnsNoPartialValues(t *testing.T) {
	base, locations := fixture(t)
	binding, err := bind(context.Background(), locations[releasepolicy.Baseline])
	require.NoError(t, err)
	total := 0
	snapshot, sources, associated, err := captureArmWithAssociations(context.Background(), binding, &total)
	require.NoError(t, err)
	require.NotEmpty(t, snapshot.Tasks)
	require.NotEmpty(t, sources)
	require.NotEmpty(t, associated.Events)
	require.NoError(t, os.Remove(filepath.Join(base, "baseline/context/a.txt")))
	total = 0
	snapshot, sources, associated, err = captureArmWithAssociations(context.Background(), binding, &total)
	require.Error(t, err)
	require.Equal(t, armSnapshot{}, snapshot)
	require.Nil(t, sources)
	require.Equal(t, armAssociations{}, associated)
}

func TestG2CaptureOriginalDeclarationErrorsRemainErrors(t *testing.T) {
	for _, name := range []string{"missing_resource", "duplicate_destination", "duplicate_disabled_id", "no_enabled", "invalid_fixture_type", "empty_prompt", "bad_pattern"} {
		t.Run(name, func(t *testing.T) {
			base, locations := fixture(t)
			switch name {
			case "missing_resource":
				require.NoError(t, os.Remove(filepath.Join(base, "baseline/context/a.txt")))
			case "duplicate_destination":
				write(t, base, "baseline/tasks/task.yaml", snapshotTask+"\n")
				write(t, base, "baseline/eval.yaml", strings.Replace(snapshotEval, "  timeout_seconds: 30", "  timeout_seconds: 30\n  instruction_files: [instruction.md]", 1))
			case "duplicate_disabled_id":
				write(t, base, "baseline/tasks/disabled.yaml", "id: task\nenabled: false\ninputs: {prompt: hello}\n")
			case "no_enabled":
				write(t, base, "baseline/tasks/task.yaml", "id: task\nenabled: false\ninputs: {prompt: hello}\n")
			case "invalid_fixture_type":
				write(t, base, "baseline/tasks/task.yaml", "id: task\ninputs: {prompt: hello, context: {fixture: 1}}\n")
			case "empty_prompt":
				write(t, base, "baseline/tasks/prompt.txt", "")
			case "bad_pattern":
				write(t, base, "baseline/eval.yaml", strings.Replace(snapshotEval, `tasks: ["tasks/*.yaml"]`, `tasks: ["tasks/["]`, 1))
			}
			old, originalErr := Prepare(context.Background(), locations)
			require.Error(t, originalErr)
			require.Nil(t, old)
			capture, err := prepareProjectionCapture(context.Background(), locations)
			require.Error(t, err)
			require.Nil(t, capture)
		})
	}
}
