package nativesnapshot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/microsoft/waza/internal/controlledprojection"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/stretchr/testify/require"
)

type originalCaptureLocation struct {
	Root       string
	EvalPath   string
	CWD        string
	ContextDir string
	Executable string
}

type originalCaptureResult struct {
	Canonical  []byte
	Sources    map[releasepolicy.Arm][]source
	Projection map[releasepolicy.Arm]controlledprojection.SourceProjection
}

var originalCaptureHarness struct {
	sync.Mutex
	directory  string
	executable string
}

func TestMain(m *testing.M) {
	directory, err := os.MkdirTemp("", "waza-original-capture-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "creating original capture harness:", err)
		os.Exit(1)
	}
	originalCaptureHarness.directory = directory
	code := m.Run()
	if err := os.RemoveAll(directory); err != nil {
		fmt.Fprintln(os.Stderr, "cleaning original capture harness:", err)
		code = 1
	}
	os.Exit(code)
}

// This test runs in a Go source overlay containing the exact pinned ORIGINAL
// capture/files/constructor. It cannot call the new seam or reconstruction.
func TestG2OriginalSourceOracle(t *testing.T) {
	raw := os.Getenv("WAZA_G2_ORIGINAL_LOCATIONS")
	if raw == "" {
		t.Skip("executed only by the original-source overlay harness")
	}
	require.Equal(t, "false", os.Getenv("ENABLE_COPILOT_TESTS"))
	var requested map[releasepolicy.Arm]originalCaptureLocation
	require.NoError(t, json.Unmarshal([]byte(raw), &requested))
	locations := map[releasepolicy.Arm]ArmLocation{}
	for arm, location := range requested {
		root, err := os.OpenRoot(location.Root)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, root.Close()) })
		locations[arm] = ArmLocation{Root: root, EvalPath: location.EvalPath, CWD: location.CWD, ContextDir: location.ContextDir, Executable: location.Executable}
	}
	prepared, err := Prepare(context.Background(), locations)
	require.NoError(t, err)
	result := originalCaptureResult{Canonical: prepared.canonical, Sources: prepared.sources,
		Projection: projectionFromOriginalCapture(t, prepared)}
	output, err := json.Marshal(result)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(os.Getenv("WAZA_G2_ORIGINAL_OUTPUT"), output, 0600))
}

func projectionFromOriginalCapture(t *testing.T, prepared *Prepared) map[releasepolicy.Arm]controlledprojection.SourceProjection {
	t.Helper()
	var snapshots map[releasepolicy.Arm]armSnapshot
	require.NoError(t, json.Unmarshal(prepared.canonical, &snapshots))
	result := map[releasepolicy.Arm]controlledprojection.SourceProjection{}
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		snapshot := snapshots[arm]
		raw := map[string][]byte{}
		for _, source := range prepared.sources[arm] {
			raw[source.Path] = source.Bytes
		}
		spec, err := models.ParseCapturedEvalSpecOffline(raw[snapshot.EvalPath], filepath.Join(snapshot.RootPath, snapshot.EvalPath))
		require.NoError(t, err)
		suite := controlledprojection.SuiteInput{Spec: *spec, Tasks: []controlledprojection.TaskInput{},
			Executable: raw[snapshot.Executable]}
		suite.LockBytes, suite.LockPresent = raw[filepath.Join(filepath.Dir(snapshot.EvalPath), models.LockfileName)]
		for _, row := range snapshot.Tasks {
			if !row.Enabled {
				continue
			}
			task, err := models.ParseCapturedTestCaseOffline(raw[row.Path], filepath.Join(snapshot.RootPath, row.Path), func(absolute string) ([]byte, error) {
				label, err := filepath.Rel(snapshot.RootPath, absolute)
				if err != nil {
					return nil, err
				}
				return raw[label], nil
			})
			require.NoError(t, err)
			request, err := prepared.Request(arm, row.ID)
			require.NoError(t, err)
			suite.Tasks = append(suite.Tasks, controlledprojection.TaskInput{Definition: *task, Request: *request})
		}
		projection, err := controlledprojection.NativePlan(suite)
		require.NoError(t, err)
		result[arm] = projection
	}
	return result
}

func frozenOriginalCaptureBinary(t *testing.T) string {
	t.Helper()
	originalCaptureHarness.Lock()
	defer originalCaptureHarness.Unlock()
	if originalCaptureHarness.executable != "" {
		return originalCaptureHarness.executable
	}
	packageDir, err := os.Getwd()
	require.NoError(t, err)
	repository := filepath.Dir(filepath.Dir(packageDir))
	dataDir := filepath.Join(packageDir, "testdata", "g2-original")
	// Every pinned file must remain byte-identical, rather than evolving with
	// the implementation under test.
	hashes := map[string]string{
		"nativesnapshot-files.go":        "6853cc4a0636acc7193ed8301a8b873b53ff67534c150f688dbb04eefd08bea3",
		"nativesnapshot-join.go":         "c7cc757f9e05671031815db292cab26d57b1c796e2530ba271c6b142ec9a873f",
		"nativesnapshot-snapshot.go":     "e47b64cde60eb5b4989fb7ae232ce896c0ce08fbf9bcf9e4ddc0e91b2c51e37a",
		"orchestration-request_files.go": "7500d6cb75626a225c4e706c96e278f77c8581a04e1e9e9b4f7561b059e39da3",
		"orchestration-runner.go":        "a0f9f345c6a59328cd0fd977fa83aa71f495debed5bf08d2916ed527e7dd62f5",
	}
	scratch := originalCaptureHarness.directory
	replacements := map[string]string{}
	for name, digest := range hashes {
		source := filepath.Join(dataDir, name+".original")
		raw, err := os.ReadFile(source)
		require.NoError(t, err)
		hash := sha256.Sum256(raw)
		require.Equal(t, digest, hex.EncodeToString(hash[:]))
		if name == "nativesnapshot-snapshot.go" {
			// Retarget only renamed parser entrypoints; the pinned oracle and
			// its capture logic remain unchanged.
			for _, parser := range []string{"EvalSpec", "TestCase"} {
				old := "models.Parse" + parser + "Offline("
				require.Equal(t, 1, strings.Count(string(raw), old))
				raw = bytes.ReplaceAll(raw, []byte(old), []byte("models.ParseCaptured"+parser+"Offline("))
			}
			source = filepath.Join(scratch, name)
			require.NoError(t, os.WriteFile(source, raw, 0600))
		}
		parts := strings.SplitN(name, "-", 2)
		replacements[filepath.Join(repository, "internal", parts[0], parts[1])] = source
	}
	for _, packageName := range []string{"nativesnapshot", "orchestration"} {
		empty := filepath.Join(scratch, packageName+".go")
		require.NoError(t, os.WriteFile(empty, []byte("package "+packageName+"\n"), 0600))
		files := []string{"request_inputs.go", "request_inputs_test.go", "request_inputs_unix_test.go", "g2_original_constructor_test.go", "inline.go"}
		if packageName == "nativesnapshot" {
			files = []string{"capture_associations.go", "association_validation.go", "projection_reconstruction.go", "collection.go", "collection_wire_test.go", "capture_associations_test.go", "g2_discovery_test.go", "g2_association_limits_test.go", "g2_capture_races_test.go", "g2_capture_races_unix_test.go", "g2_capture_windows_test.go"}
			files = append(files, "slots.go", "slot_validation.go", "slots_test.go", "slots_boundary_test.go")
			files = append(files, "retry_join.go", "retry_join_test.go")
			files = append(files, "reader_parity_test.go", "collection_blob_decode_test.go")
		}
		for _, name := range files {
			target := filepath.Join(repository, "internal", packageName, name)
			if _, err := os.Stat(target); err == nil {
				replacements[target] = empty
			}
		}
	}
	overlay, err := json.Marshal(struct{ Replace map[string]string }{replacements})
	require.NoError(t, err)
	overlayPath := filepath.Join(scratch, "original-overlay.json")
	require.NoError(t, os.WriteFile(overlayPath, overlay, 0600))
	executable := filepath.Join(scratch, "original-capture.test.exe")
	command := exec.CommandContext(t.Context(), "go", "test", "-c", "-overlay="+overlayPath, "-o", executable, "./internal/nativesnapshot")
	command.Dir = repository
	command.Env = append(os.Environ(), "ENABLE_COPILOT_TESTS=false", "NO_COLOR=1")
	output, err := command.CombinedOutput()
	require.NoError(t, err, "%s", output)
	originalCaptureHarness.executable = executable
	return executable
}

func frozenOriginalCapture(t *testing.T, locations map[releasepolicy.Arm]ArmLocation) originalCaptureResult {
	t.Helper()
	executable := frozenOriginalCaptureBinary(t)
	scratch := t.TempDir()
	requested := map[releasepolicy.Arm]originalCaptureLocation{}
	for arm, location := range locations {
		requested[arm] = originalCaptureLocation{location.Root.Name(), location.EvalPath, location.CWD, location.ContextDir, location.Executable}
	}
	rawLocations, err := json.Marshal(requested)
	require.NoError(t, err)
	outputPath := filepath.Join(scratch, "original-output.json")
	// Reuse compilation only: each invocation opens the current roots and runs
	// the pinned capture in a fresh process, with a distinct output artifact.
	command := exec.CommandContext(t.Context(), executable, "-test.run=^TestG2OriginalSourceOracle$", "-test.count=1", "-test.timeout=10m")
	command.Env = append(os.Environ(), "WAZA_G2_ORIGINAL_LOCATIONS="+string(rawLocations), "WAZA_G2_ORIGINAL_OUTPUT="+outputPath,
		"ENABLE_COPILOT_TESTS=false", "NO_COLOR=1")
	output, err := command.CombinedOutput()
	require.NoError(t, err, "%s", output)
	raw, err := os.ReadFile(outputPath)
	require.NoError(t, err)
	var result originalCaptureResult
	require.NoError(t, json.Unmarshal(raw, &result))
	return result
}

func TestG2OriginalOracleReusesOnlyCompilation(t *testing.T) {
	base, locations := fixture(t)
	executable := frozenOriginalCaptureBinary(t)
	before, err := os.Stat(executable)
	require.NoError(t, err)
	first := frozenOriginalCapture(t, locations)
	write(t, base, "baseline/tasks/prompt.txt", "changed current prompt")
	second := frozenOriginalCapture(t, locations)
	require.NotEqual(t, first.Canonical, second.Canonical)
	require.NotEqual(t, first.Sources, second.Sources)
	require.NotEqual(t, first.Projection, second.Projection)
	var snapshots map[releasepolicy.Arm]armSnapshot
	require.NoError(t, json.Unmarshal(second.Canonical, &snapshots))
	var request struct{ Message string }
	require.NoError(t, json.Unmarshal(snapshots[releasepolicy.Baseline].Tasks[0].Request, &request))
	require.Equal(t, "changed current prompt", request.Message)
	require.Equal(t, first.Projection[releasepolicy.Candidate], second.Projection[releasepolicy.Candidate])
	_, otherLocations := fixture(t)
	other := frozenOriginalCapture(t, otherLocations)
	require.NotEqual(t, first.Canonical, other.Canonical, "different historical roots must not reuse captured results")
	require.Equal(t, executable, frozenOriginalCaptureBinary(t))
	after, err := os.Stat(executable)
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after), "all captures must reuse the same compiled oracle")
	require.Equal(t, before.ModTime(), after.ModTime())
}

func TestG2PreparedAndCopiedPreparedAreCompileIneligible(t *testing.T) {
	packageDir, err := os.Getwd()
	require.NoError(t, err)
	repository := filepath.Dir(filepath.Dir(packageDir))
	for _, expression := range []string{"&Prepared{}", "func() *Prepared { original := &Prepared{}; copied := *original; return &copied }()"} {
		t.Run(expression, func(t *testing.T) {
			scratch := t.TempDir()
			source := filepath.Join(scratch, "negative.go")
			require.NoError(t, os.WriteFile(source, []byte("package nativesnapshot\nimport \"context\"\nvar _, _ = detachProjectionInput(context.Background(), "+expression+")\n"), 0600))
			overlay, err := json.Marshal(struct{ Replace map[string]string }{map[string]string{
				filepath.Join(packageDir, "g2_compile_negative_test.go"): source,
			}})
			require.NoError(t, err)
			overlayPath := filepath.Join(scratch, "negative-overlay.json")
			require.NoError(t, os.WriteFile(overlayPath, overlay, 0600))
			command := exec.CommandContext(t.Context(), "go", "test", "-overlay="+overlayPath, "./internal/nativesnapshot", "-run=^$", "-count=1")
			command.Dir = repository
			command.Env = append(os.Environ(), "ENABLE_COPILOT_TESTS=false", "NO_COLOR=1")
			output, err := command.CombinedOutput()
			require.Error(t, err)
			require.Contains(t, string(output), "cannot use")
			require.Contains(t, string(output), "*Prepared")
			require.Contains(t, string(output), "*projectionCapture")
		})
	}
}
