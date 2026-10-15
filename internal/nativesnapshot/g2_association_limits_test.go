package nativesnapshot

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/stretchr/testify/require"
)

func TestG2AssociationInclusiveCountersAndPath(t *testing.T) {
	for _, name := range []string{"events", "nodes", "directories", "bytes"} {
		t.Run(name, func(t *testing.T) {
			budget := &associationBudget{}
			counter, limit := &budget.events, maxAssociationEvents
			switch name {
			case "nodes":
				counter, limit = &budget.nodes, maxAssociationNodes
			case "directories":
				counter, limit = &budget.directories, maxAssociationDirectories
			case "bytes":
				budget.bytes = maxAssociationBytes - 1
			}
			if name != "bytes" {
				*counter = limit - 1
			}
			size := 0
			if name == "bytes" {
				size = 1
			}
			require.NoError(t, budget.charge(counter, limit, size))
			before := *budget
			require.Error(t, budget.charge(counter, limit, size))
			require.Equal(t, before, *budget)
		})
	}
	require.NoError(t, associationPath(strings.Repeat("a", maxAssociationPath)))
	require.Error(t, associationPath(strings.Repeat("a", maxAssociationPath+1)))
	for _, invalid := range []string{"", "..", "../outside", "/absolute", "a//b", "a/./b", "a\\b"} {
		require.Error(t, associationPath(invalid), invalid)
	}
	recorder := newAssociationRecorder(context.Background(), "/lexical", &associationBudget{events: maxAssociationEvents})
	err := recorder.read("input", "eval")
	require.Error(t, err)
	require.Empty(t, recorder.events)
	recorder.budget.nodes = maxAssociationNodes
	require.Error(t, recorder.addNode("input", regularNode, false))
	require.Empty(t, recorder.nodes)
}

func TestG2BoundedTokenCountersBeforeTypedDecode(t *testing.T) {
	for _, limit := range []int{maxAssociationEvents, maxAssociationNodes, maxAssociationDirectories} {
		shape := &associationWireShape{kind: 'a', item: &associationWireShape{kind: 's'}, count: new(int), limit: limit}
		raw := "[" + strings.Repeat(`"x",`, limit-1) + `"x"]`
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		require.NoError(t, scanAssociationValue(context.Background(), decoder, shape, 0))
		require.Equal(t, limit, *shape.count)
		*shape.count = 0
		decoder = json.NewDecoder(strings.NewReader("[" + strings.Repeat(`"x",`, limit) + `"x"]`))
		decoder.UseNumber()
		require.Error(t, scanAssociationValue(context.Background(), decoder, shape, 0))
		require.Equal(t, limit, *shape.count)
	}
	_, err := decodeAssociations(context.Background(), make([]byte, maxAssociationBytes+1))
	require.Error(t, err)
}

func TestG2BoundedEncodingExactWireAndInclusiveBytes(t *testing.T) {
	_, locations := fixture(t)
	capture, err := prepareProjectionCapture(context.Background(), locations)
	require.NoError(t, err)
	associated, err := decodeAssociations(context.Background(), capture.associations.canonical)
	require.NoError(t, err)
	marshaled, err := json.Marshal(associated)
	require.NoError(t, err)
	bounded, err := marshalCaptureAssociations(context.Background(), associated)
	require.NoError(t, err)
	require.Equal(t, marshaled, bounded)
	// Exercise the exact encoded-byte boundary independently of the path/counter
	// guards; the encoder must not grow its aggregate buffer past that boundary.
	associated = captureAssociations{Kind: associationKind, Version: associationVersion,
		Arms: map[releasepolicy.Arm]armAssociations{
			releasepolicy.Baseline:  {Nodes: []capturedNode{}, Directories: []capturedDirectory{}, Events: []associationEvent{}},
			releasepolicy.Candidate: {Nodes: []capturedNode{}, Directories: []capturedDirectory{}, Events: []associationEvent{}},
		}}
	raw, err := marshalCaptureAssociations(context.Background(), associated)
	require.NoError(t, err)
	associated.Kind = strings.Repeat("a", maxAssociationBytes-len(raw)+len(associationKind))
	raw, err = marshalCaptureAssociations(context.Background(), associated)
	require.NoError(t, err)
	require.Len(t, raw, maxAssociationBytes)
	require.LessOrEqual(t, cap(raw), maxAssociationBytes)
	associated.Kind += "a"
	raw, err = marshalCaptureAssociations(context.Background(), associated)
	require.Error(t, err)
	require.Nil(t, raw)
}

func TestG2PatternDepthAndCancellation(t *testing.T) {
	reader := newRetainedArmInputs(context.Background(), "/lexical", armAssociations{}, nil)
	matches, err := matchCapturedPattern(context.Background(), strings.Repeat("*/", 10002)+"file", &retainedDiscoveryInputs{reader})
	require.Error(t, err)
	require.Nil(t, matches)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	matches, err = matchCapturedPattern(ctx, "file", &retainedDiscoveryInputs{reader})
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, matches)
	for _, op := range []associationOperation{kindOperation, readOperation, directoryOperation} {
		t.Run(fmt.Sprint(op), func(t *testing.T) {
			reader := newRetainedArmInputs(ctx, "/lexical", armAssociations{}, nil)
			_, err := reader.next("file", op)
			require.ErrorIs(t, err, context.Canceled)
			require.Zero(t, reader.position)
		})
	}

}

func TestG2StringsAreBoundedBeforeTokenDecoding(t *testing.T) {
	for _, text := range []string{
		`"` + strings.Repeat("a", maxAssociationPath) + `"`,
		`"` + strings.Repeat(`\u0061`, maxAssociationPath) + `"`,
		`"` + strings.Repeat(`\u20ac`, maxAssociationPath/3) + `"`,
		`"` + strings.Repeat(`\ud83d\ude00`, maxAssociationPath/4) + `"`,
		`"` + strings.Repeat("€", maxAssociationPath/3) + `"`,
	} {
		require.NoError(t, boundAssociationStrings(context.Background(), []byte(text)))
	}
	for _, text := range []string{
		`"` + strings.Repeat("a", maxAssociationPath+1) + `"`,
		`"` + strings.Repeat(`\u0061`, maxAssociationPath+1) + `"`,
		`"` + strings.Repeat(`\u20ac`, maxAssociationPath/3+1) + `"`,
		`"` + strings.Repeat(`\ud83d\ude00`, maxAssociationPath/4+1) + `"`,
		`"` + strings.Repeat("€", maxAssociationPath/3+1) + `"`,
		`"` + strings.Repeat(`\ud800`, maxAssociationPath/3+1) + `"`,
		string(append(append([]byte{'"'}, []byte(strings.Repeat("\xff", maxAssociationPath/3+1))...), '"')),
		`"\u000`,
		`"\uzzzz"`,
		`"unterminated`,
	} {
		require.Error(t, boundAssociationStrings(context.Background(), []byte(text)))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, boundAssociationStrings(ctx, []byte(`"bounded"`)), context.Canceled)
}

func TestG2SnapshotLabelsCannotCauseAmbientPathResolution(t *testing.T) {
	root := filepath.Join(t.TempDir(), "historical", "root")
	for _, contextDir := range []string{"", ".", "fixtures", filepath.Join(root, "fixtures")} {
		require.NoError(t, validateSnapshotLabels(armSnapshot{
			RootPath: root, CWD: root, EvalPath: "eval.yaml", Executable: "evaluator", ContextDir: contextDir,
		}))
	}

}

func TestG2ActualDirectoryRowBoundaryDoesNotCapOldPrepare(t *testing.T) {
	base, locations := fixture(t)
	write(t, base, "baseline/tasks/task.yaml", "id: task\ninputs: {prompt: hello, context: {fixture: fixture}}\n")
	for i := 0; i < maxAssociationDirectories-3; i++ {
		require.NoError(t, os.MkdirAll(filepath.Join(base, "baseline", "fixture", fmt.Sprintf("d%05d", i)), 0700))
	}
	original := frozenOriginalCapture(t, locations)
	capture, err := prepareProjectionCapture(context.Background(), locations)
	require.NoError(t, err)
	associated, err := decodeAssociations(context.Background(), capture.associations.canonical)
	require.NoError(t, err)
	require.Equal(t, maxAssociationDirectories, len(associated.Arms[releasepolicy.Baseline].Directories)+len(associated.Arms[releasepolicy.Candidate].Directories))
	require.Equal(t, original.Canonical, capture.prepared.canonical)
	input, err := detachProjectionInput(context.Background(), capture)
	require.NoError(t, err)
	result, err := reconstructProjection(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, original.Projection, result)
	require.NoError(t, os.Mkdir(filepath.Join(base, "baseline/fixture/z-extra"), 0700))
	old, err := Prepare(context.Background(), locations)
	require.NoError(t, err, "the original directory-entry cap still permits this fixture")
	require.NotNil(t, old)
	capture, err = prepareProjectionCapture(context.Background(), locations)
	require.ErrorContains(t, err, "association limit exceeded")
	require.Nil(t, capture)
}

func TestG2ActualQueriedPathBoundaryDoesNotCapOldPrepare(t *testing.T) {
	base, locations := fixture(t)
	prefix := "baseline/missing/"
	label := prefix + strings.Repeat("a", maxAssociationPath-len(prefix))
	pattern := strings.TrimPrefix(label, "baseline/")
	encoded, err := json.Marshal([]string{pattern, "tasks/task.yaml"})
	require.NoError(t, err)
	write(t, base, "baseline/eval.yaml", strings.Replace(snapshotEval, `tasks: ["tasks/*.yaml"]`, "tasks: "+string(encoded), 1))
	original := frozenOriginalCapture(t, locations)
	capture, err := prepareProjectionCapture(context.Background(), locations)
	require.NoError(t, err)
	associated, err := decodeAssociations(context.Background(), capture.associations.canonical)
	require.NoError(t, err)
	found := false
	for _, node := range associated.Arms[releasepolicy.Baseline].Nodes {
		if node.Path == label {
			found = true
			require.Equal(t, missingNode, node.State)
		}
	}
	require.True(t, found, "the original finite Stat query supplies this missing observation")
	require.Equal(t, original.Canonical, capture.prepared.canonical)
	input, err := detachProjectionInput(context.Background(), capture)
	require.NoError(t, err)
	result, err := reconstructProjection(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, original.Projection, result)
	encoded, err = json.Marshal([]string{pattern + "a", "tasks/task.yaml"})
	require.NoError(t, err)
	write(t, base, "baseline/eval.yaml", strings.Replace(snapshotEval, `tasks: ["tasks/*.yaml"]`, "tasks: "+string(encoded), 1))
	old, err := Prepare(context.Background(), locations)
	require.NoError(t, err)
	require.NotNil(t, old)
	capture, err = prepareProjectionCapture(context.Background(), locations)
	require.ErrorContains(t, err, "path exceeds bounded limit")
	require.Nil(t, capture)
}

func TestG2ActualEventBoundaryDoesNotCapOldPrepare(t *testing.T) {
	base, locations := fixture(t)
	for i := 0; i < 5949; i++ {
		require.NoError(t, os.MkdirAll(filepath.Join(base, "baseline", "fixture", fmt.Sprintf("d%05d", i)), 0700))
	}
	for i := 0; i < 10; i++ {
		write(t, base, fmt.Sprintf("baseline/tasks/t%02d.yaml", i),
			fmt.Sprintf("id: t%02d\ninputs: {prompt: hello, context: {fixture: fixture}}\n", i))
	}
	task := "id: task\ninputs: {prompt: hello, context: {fixture: fixture}}\ninstruction_files: [i0.md, i1.md, i2.md, i3.md, i4.md, i5.md, i6.md, i7.md]\n"
	write(t, base, "baseline/tasks/task.yaml", task)
	for i := 0; i < 8; i++ {
		write(t, base, fmt.Sprintf("baseline/context/i%d.md", i), fmt.Sprintf("instruction %d", i))
	}
	original := frozenOriginalCapture(t, locations)
	capture, err := prepareProjectionCapture(context.Background(), locations)
	require.NoError(t, err)
	associated, err := decodeAssociations(context.Background(), capture.associations.canonical)
	require.NoError(t, err)
	require.Equal(t, maxAssociationEvents, len(associated.Arms[releasepolicy.Baseline].Events)+len(associated.Arms[releasepolicy.Candidate].Events))
	require.Equal(t, original.Canonical, capture.prepared.canonical)
	input, err := detachProjectionInput(context.Background(), capture)
	require.NoError(t, err)
	result, err := reconstructProjection(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, original.Projection, result)
	write(t, base, "baseline/context/i8.md", "one additional instruction")
	write(t, base, "baseline/tasks/task.yaml", strings.Replace(task, "i7.md]", "i7.md, i8.md]", 1))
	old, err := Prepare(context.Background(), locations)
	require.NoError(t, err)
	require.NotNil(t, old)
	capture, err = prepareProjectionCapture(context.Background(), locations)
	require.ErrorContains(t, err, "association limit exceeded")
	require.Nil(t, capture)
}
func TestG2InvalidSnapshotLabelsCannotResolveAmbientPaths(t *testing.T) {
	root := filepath.Join(t.TempDir(), "historical", "root")
	for _, name := range []string{"relative_root", "relative_cwd", "outside_cwd", "noncanonical_root",
		"outside_context", "relative_escape_context", "noncanonical_context", "absolute_eval", "relative_escape_executable"} {
		t.Run(name, func(t *testing.T) {
			snapshot := armSnapshot{RootPath: root, CWD: root, EvalPath: "eval.yaml", Executable: "evaluator"}
			switch name {
			case "relative_root":
				snapshot.RootPath = "historical/root"
			case "relative_cwd":
				snapshot.CWD = "."
			case "outside_cwd":
				snapshot.CWD = filepath.Join(filepath.Dir(root), "outside")
			case "noncanonical_root":
				snapshot.RootPath = filepath.Dir(root) + string(filepath.Separator) + "." + string(filepath.Separator) + "root"
			case "outside_context":
				snapshot.ContextDir = filepath.Join(filepath.Dir(root), "outside")
			case "relative_escape_context":
				snapshot.ContextDir = "../outside"
			case "noncanonical_context":
				snapshot.ContextDir = root + string(filepath.Separator) + "." + string(filepath.Separator) + "fixtures"
			case "absolute_eval":
				snapshot.EvalPath = "/eval.yaml"
			case "relative_escape_executable":
				snapshot.Executable = "../evaluator"
			}
			require.Error(t, validateSnapshotLabels(snapshot))
		})
	}
}

func TestG2ActualNodeBoundaryDoesNotCapOldPrepare(t *testing.T) {
	base, locations := fixture(t)
	patterns := make([]string, 0, maxAssociationNodes/2-1)
	for i := 0; i < maxAssociationNodes/2-2; i++ {
		patterns = append(patterns, fmt.Sprintf("tasks/m%05d.yaml", i))
	}
	patterns = append(patterns, "tasks/task.yaml")
	encoded, err := json.Marshal(patterns)
	require.NoError(t, err)
	eval := strings.Replace(snapshotEval, `tasks: ["tasks/*.yaml"]`, "tasks: "+string(encoded), 1)
	write(t, base, "baseline/eval.yaml", eval)
	write(t, base, "candidate/eval.yaml", eval)
	original := frozenOriginalCapture(t, locations)
	capture, err := prepareProjectionCapture(context.Background(), locations)
	require.NoError(t, err)
	associated, err := decodeAssociations(context.Background(), capture.associations.canonical)
	require.NoError(t, err)
	require.Equal(t, maxAssociationNodes, len(associated.Arms[releasepolicy.Baseline].Nodes)+len(associated.Arms[releasepolicy.Candidate].Nodes))
	require.Equal(t, original.Canonical, capture.prepared.canonical)
	input, err := detachProjectionInput(context.Background(), capture)
	require.NoError(t, err)
	result, err := reconstructProjection(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, original.Projection, result)
	patterns = append(patterns, "tasks/one-extra-missing.yaml")
	encoded, err = json.Marshal(patterns)
	require.NoError(t, err)
	write(t, base, "baseline/eval.yaml", strings.Replace(snapshotEval, `tasks: ["tasks/*.yaml"]`, "tasks: "+string(encoded), 1))
	old, err := Prepare(context.Background(), locations)
	require.NoError(t, err)
	require.NotNil(t, old)
	capture, err = prepareProjectionCapture(context.Background(), locations)
	require.ErrorContains(t, err, "association limit exceeded")
	require.Nil(t, capture)
}
