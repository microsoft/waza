// Package readeroracle builds the checksum-pinned 15c reader for native wire
// compatibility tests. It reuses compilation, never observations.
package readeroracle

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

//go:embed testdata/15c/*.original
var originals embed.FS

var digests = map[string]string{
	"nativesnapshot-collection.go":        "f43f5005e7d9102f4f863cda5f1f737d4e449f5f1e64320c33453263c83dc89b",
	"jsonutil-json.go":                    "b293c821e0b9a3e9a766e03762e7e1d9b6ec0939c2f81ba24a4c83cd9b0820a1",
	"models-native_evidence.go":           "3dd9184170658b1dddfab7a8e5fa40638ff03fd325601597e8bc3ce84c2c1f5f",
	"controlledcomparison-native_read.go": "1cd1b54f7f5646ed0285c858573aa5d2327d42deab9ef0895762f80bb093ff13",
	"webapi-release.go":                   "bc2be4ac46418e85465848f9c1cc1921fabd7f39ec7599eae2f32122348224ca",
}

// Build is called once per parity test. The test owns the resulting binary;
// Go's source/flag-keyed build cache may reuse compilation across invocations.
// The oracle is deliberately uncovered: Go 1.26.4 coverage can ignore source
// overlays. Race instrumentation still follows the calling test binary.
func Build(t testing.TB, packageName string) string {
	t.Helper()
	require.Contains(t, []string{"nativesnapshot", "controlledcomparison"}, packageName)
	cwd, err := os.Getwd()
	require.NoError(t, err)
	repository := filepath.Dir(filepath.Dir(cwd))
	scratch := t.TempDir()
	replacements := map[string]string{}
	for name, digest := range digests {
		raw, err := originals.ReadFile("testdata/15c/" + name + ".original")
		require.NoError(t, err)
		require.Equal(t, digest, fmt.Sprintf("%x", sha256.Sum256(raw)), name)
		path := filepath.Join(scratch, name)
		require.NoError(t, os.WriteFile(path, raw, 0600))
		parts := strings.SplitN(name, "-", 2)
		replacements[filepath.Join(repository, "internal", parts[0], parts[1])] = path
	}
	// Current-only unit tests reference helpers absent from the pinned reader.
	empty := filepath.Join(scratch, "current-only-tests.go")
	require.NoError(t, os.WriteFile(empty, []byte("package nativesnapshot\n"), 0600))
	replacements[filepath.Join(repository, "internal", "nativesnapshot", "collection_blob_decode_test.go")] = empty
	overlay, err := json.Marshal(struct{ Replace map[string]string }{replacements})
	require.NoError(t, err)
	overlayPath := filepath.Join(scratch, "reader15c-overlay.json")
	require.NoError(t, os.WriteFile(overlayPath, overlay, 0600))
	executable := filepath.Join(scratch, "reader15c.test.exe")
	args := []string{"test", "-c", "-cover=false", "-buildvcs=false", "-overlay=" + overlayPath, "-o", executable}
	if raceEnabled {
		args = append(args, "-race")
	}
	args = append(args, "./internal/"+packageName)
	command := exec.CommandContext(t.Context(), "go", args...)
	command.Dir = repository
	command.Env = append(os.Environ(), "GOFLAGS=", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off",
		"ENABLE_COPILOT_TESTS=false", "ENABLE_COPILOT_SDK_TESTS=false", "ENABLE_LIVE_COPILOT_TESTS=false",
		"ENABLE_LIVE_TESTS=false", "ENABLE_MODEL_TESTS=false", "NO_COLOR=1")
	output, err := command.CombinedOutput()
	require.NoError(t, err, "building pinned reader (no network or Git required): %s", output)
	t.Logf("compiled frozen15c once: go %s", strings.Join(args, " "))
	return executable
}

func Input(t testing.TB) ([]byte, bool) {
	t.Helper()
	path := os.Getenv("WAZA_NATIVE_READER_ORACLE_INPUT")
	if path == "" {
		return nil, false
	}
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	return raw, true
}

func Output(t testing.TB, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	path := os.Getenv("WAZA_NATIVE_READER_ORACLE_OUTPUT")
	require.NotEmpty(t, path)
	require.NoError(t, os.WriteFile(path, raw, 0600))
}

func Run(t testing.TB, executable, testName string, value any) []byte {
	t.Helper()
	scratch := t.TempDir()
	input, output := filepath.Join(scratch, "input.json"), filepath.Join(scratch, "output.json")
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(input, raw, 0600))
	command := exec.CommandContext(t.Context(), executable, "-test.run=^"+testName+"$", "-test.count=1")
	command.Env = append(os.Environ(), "WAZA_NATIVE_READER_ORACLE_INPUT="+input,
		"WAZA_NATIVE_READER_ORACLE_OUTPUT="+output, "ENABLE_COPILOT_TESTS=false",
		"ENABLE_COPILOT_SDK_TESTS=false", "ENABLE_LIVE_COPILOT_TESTS=false", "ENABLE_LIVE_TESTS=false",
		"ENABLE_MODEL_TESTS=false", "NO_COLOR=1")
	result, err := command.CombinedOutput()
	require.NoError(t, err, "running frozen reader: %s", result)
	raw, err = os.ReadFile(output)
	require.NoError(t, err)
	return raw
}

type Error struct {
	Type     string
	Message  string
	Offset   int64
	Value    string
	Target   string
	Struct   string
	Field    string
	Causes   []Error
	Canceled bool
	Deadline bool
}

// Describe preserves the error chain's concrete classes and decoder metadata,
// not only strings which could conceal a changed offset or cancellation cause.
func Describe(err error) *Error {
	if err == nil {
		return nil
	}
	result := &Error{Type: fmt.Sprintf("%T", err), Message: err.Error(),
		Canceled: errors.Is(err, context.Canceled), Deadline: errors.Is(err, context.DeadlineExceeded)}
	switch typed := err.(type) {
	case *json.SyntaxError:
		result.Offset = typed.Offset
	case *json.UnmarshalTypeError:
		result.Offset, result.Value, result.Target = typed.Offset, typed.Value, typed.Type.String()
		result.Struct, result.Field = typed.Struct, typed.Field
	}
	switch typed := err.(type) {
	case interface{ Unwrap() []error }:
		for _, cause := range typed.Unwrap() {
			if described := Describe(cause); described != nil {
				result.Causes = append(result.Causes, *described)
			}
		}
	case interface{ Unwrap() error }:
		if described := Describe(typed.Unwrap()); described != nil {
			result.Causes = append(result.Causes, *described)
		}
	}
	return result
}
