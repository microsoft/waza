package controlledcomparison_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/microsoft/waza/internal/controlledcomparison"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/microsoft/waza/internal/testutil/readeroracle"
	"github.com/microsoft/waza/internal/webapi"
	"github.com/stretchr/testify/require"
)

type frozenDecisionInput struct {
	Directory string `json:"directory"`
	Policy    []byte `json:"policy"`
}

type frozenDecisionObservation struct {
	Selected      releasepolicy.Decision `json:"selected"`
	SelectedError string                 `json:"selected_error"`
	SelectedClass *readeroracle.Error    `json:"selected_error_class"`
	Root          releasepolicy.Decision `json:"root"`
	RootError     string                 `json:"root_error"`
	RootClass     *readeroracle.Error    `json:"root_error_class"`
	APIStatus     int                    `json:"api_status"`
	API           json.RawMessage        `json:"api"`
}

func observeFrozenNativeDecision(t *testing.T, input frozenDecisionInput) frozenDecisionObservation {
	t.Helper()
	var result frozenDecisionObservation
	var err error
	require.NotPanics(t, func() {
		result.Selected, err = controlledcomparison.ReadSelectedDecision(t.Context(), input.Directory, input.Policy)
	})
	if err != nil {
		result.SelectedError = err.Error()
		result.SelectedClass = readeroracle.Describe(err)
	}
	root, err := os.OpenRoot(input.Directory)
	require.NoError(t, err)
	require.NotPanics(t, func() {
		result.Root, err = controlledcomparison.ReadDecisionRootContext(t.Context(), root)
	})
	if err != nil {
		result.RootError = err.Error()
		result.RootClass = readeroracle.Describe(err)
	}
	require.NoError(t, root.Close())
	mux := http.NewServeMux()
	webapi.RegisterReleaseRoutes(mux, filepath.Dir(input.Directory))
	response := httptest.NewRecorder()
	require.NotPanics(t, func() {
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/release-collections", nil))
	})
	result.APIStatus = response.Code
	require.Equal(t, http.StatusOK, response.Code)
	require.True(t, json.Valid(response.Body.Bytes()))
	result.API = append(json.RawMessage(nil), response.Body.Bytes()...)
	return result
}

func frozenNativeDecisionProcess(t *testing.T, raw []byte) {
	var input frozenDecisionInput
	require.NoError(t, json.Unmarshal(raw, &input))
	result := observeFrozenNativeDecision(t, input)
	readeroracle.Output(t, result)
}

func TestNativeReaderSuppliedDecisionProcess(t *testing.T) {
	oracle := readeroracle.Build(t, "controlledcomparison")
	executable, err := os.Executable()
	require.NoError(t, err)
	directory := filepath.Join(t.TempDir(), "supplied")
	require.NoError(t, os.Mkdir(directory, 0700))
	policy := []byte(`{"kind":"waza.release-native-policy"}`)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "policy.json"), policy, 0600))
	input := frozenDecisionInput{Directory: directory, Policy: policy}
	frozen := readeroracle.Run(t, oracle, "TestNativeReaderFrozen15cDecisionParity", input)
	raw := readeroracle.Run(t, executable, "TestNativeReaderFrozen15cDecisionParity", input)
	require.Equal(t, frozen, raw, "full selected/root/API payloads and error classes must match frozen15c")
	var got frozenDecisionObservation
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, "native policy requires its exact six members", got.SelectedError)
	require.Equal(t, got.SelectedError, got.RootError)
	require.NotNil(t, got.SelectedClass)
	require.Equal(t, got.SelectedError, got.SelectedClass.Message)
	require.NotNil(t, got.RootClass)
	require.Equal(t, got.RootError, got.RootClass.Message)
	require.False(t, got.Selected.Accepted)
	require.Equal(t, "invalid", got.Selected.Compatibility.State)
	require.NotNil(t, got.Selected.Native)
	require.Empty(t, got.Selected.Native.Attempts)
	require.Equal(t, got.Selected, got.Root)
	var api []webapi.ReleaseCollection
	require.NoError(t, json.Unmarshal(got.API, &api))
	require.Len(t, api, 1)
	require.Equal(t, "supplied", api[0].Path)
	require.Equal(t, got.RootError, api[0].Error)
	require.False(t, api[0].Decision.Accepted)
	require.Equal(t, got.Root.Compatibility, api[0].Decision.Compatibility)
	require.Equal(t, got.Root.Native, api[0].Decision.Native)
}

func TestNativeReaderFrozen15cDecisionParity(t *testing.T) {
	if raw, subprocess := readeroracle.Input(t); subprocess {
		frozenNativeDecisionProcess(t, raw)
		return
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("native durable creation requires Linux or macOS; supplied decision reading remains covered")
	}
	oracle := readeroracle.Build(t, "controlledcomparison")
	binaryBefore, err := os.Stat(oracle)
	require.NoError(t, err)
	source := nativeFixture(t, "fixed-model")
	policy, err := controlledcomparison.PlanNative(t.Context(), source, source, nativeDesign(), nativeRequirements(), nil)
	require.NoError(t, err)
	policy = fixedNativePolicy(t, policy)
	directory := filepath.Join(t.TempDir(), "native")
	calls := 0
	require.NoError(t, controlledcomparison.CollectNativeWithOptions(t.Context(), policy, source, source, directory,
		controlledcomparison.NativeCollectorOptions{NewCopilotClient: nativeFactory(t, &calls)}))
	require.Equal(t, 2, calls)
	path := filepath.Join(directory, "native-capture.json")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	t.Logf("same actual production capture bytes=%d sha256=%x", len(raw), sha256.Sum256(raw))
	start, end, found, err := jsonutil.ObjectMemberRange(raw, "blobs")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, byte('{'), raw[start])
	require.Equal(t, byte('}'), raw[end-1])
	keyEnd := bytes.IndexByte(raw[start+2:], '"') + start + 2
	require.Greater(t, keyEnd, start+2)
	key := bytes.Clone(raw[start+1 : keyEnd+1])
	before := bytes.TrimSpace(raw[1 : start-len(`"blobs":`)])
	after := bytes.TrimSpace(raw[end : len(raw)-1])
	cases := []struct {
		Name string
		Raw  func() []byte
		Want string
	}{
		{"writer", func() []byte { return raw }, ""},
		{"whitespace", func() []byte {
			return append(append(append([]byte(nil), " \r\n\t"...), raw...), "\r\n "...)
		}, ""},
		{"escaped_member", func() []byte {
			escaped := bytes.Replace(raw, []byte(`"blobs":`), []byte(`"\u0062lobs":`), 1)
			require.NotEqual(t, raw, escaped)
			return escaped
		}, ""},
		{"member_order", func() []byte {
			reordered := append([]byte(`{"blobs":`), raw[start:end]...)
			reordered = append(reordered, ',')
			remainder := bytes.Clone(bytes.Trim(before, " ,\r\n\t"))
			if len(remainder) > 0 && len(bytes.Trim(after, " ,\r\n\t")) > 0 {
				remainder = append(remainder, ',')
			}
			remainder = append(remainder, bytes.Trim(after, " ,\r\n\t")...)
			reordered = append(reordered, remainder...)
			reordered = append(reordered, '}')
			require.True(t, json.Valid(reordered), "top-level member reorder must retain valid raw nested JSON")
			return reordered
		}, ""},
		{"trailing_document", func() []byte {
			return append(bytes.Clone(raw), []byte("{}")...)
		}, "invalid JSON"},
		{"duplicate_blob", func() []byte {
			duplicate := append(bytes.Clone(raw[:end-1]), ',')
			duplicate = append(duplicate, key...)
			duplicate = append(duplicate, []byte(`:""`)...)
			return append(duplicate, raw[end-1:]...)
		}, "duplicate JSON object key"},
		{"unsafe_truncated_object", func() []byte { return []byte(`{"blobs":{"a":"`) }, "invalid JSON"},
		{"unicode_before_syntax", func() []byte { return []byte(`{"kind":"\ud800",`) }, "unpaired Unicode surrogate"},
		{"blob_failure_before_unknown", func() []byte {
			return []byte(`{"blobs":{"a":"!!!"},"unknown":true}`)
		}, "illegal base64"},
		{"unknown_before_blob_failure", func() []byte {
			return []byte(`{"unknown":true,"blobs":{"a":"!!!"}}`)
		}, `unknown field "unknown"`},
	}
	input := frozenDecisionInput{Directory: directory, Policy: policy}
	observations := map[string]frozenDecisionObservation{}
	for _, test := range cases {
		t.Run(test.Name, func(t *testing.T) {
			require.NoError(t, os.WriteFile(path, test.Raw(), 0600))
			frozen := readeroracle.Run(t, oracle, "TestNativeReaderFrozen15cDecisionParity", input)
			var expected frozenDecisionObservation
			require.NoError(t, json.Unmarshal(frozen, &expected))
			if test.Want == "" {
				require.Empty(t, expected.SelectedError)
				require.Empty(t, expected.RootError)
				require.Equal(t, "passed", expected.Selected.Golden.State)
				require.Len(t, expected.Selected.Native.Attempts, 2)
				for _, attempt := range expected.Selected.Native.Attempts {
					require.Equal(t, "passed", attempt.Status)
					require.NotNil(t, attempt.Output)
					require.Equal(t, "hello", *attempt.Output)
					require.True(t, attempt.UsageComplete)
				}
			} else {
				require.Contains(t, expected.SelectedError, test.Want)
				require.Contains(t, expected.RootError, test.Want)
				require.False(t, expected.Selected.Accepted)
				require.Equal(t, "invalid", expected.Selected.Compatibility.State)
				require.Empty(t, expected.Selected.Native.Attempts)
			}
			got := observeFrozenNativeDecision(t, frozenDecisionInput{Directory: directory, Policy: policy})
			expectedEncoded, err := json.Marshal(expected)
			require.NoError(t, err)
			gotEncoded, err := json.Marshal(got)
			require.NoError(t, err)
			require.Equal(t, expectedEncoded, gotEncoded, "selected/root/API actual decision payload and first error must match frozen15c")
			var api []webapi.ReleaseCollection
			require.NoError(t, json.Unmarshal(got.API, &api))
			require.Len(t, api, 1)
			require.Equal(t, got.Root.Accepted, api[0].Decision.Accepted)
			require.Equal(t, got.Root.Golden, api[0].Decision.Golden)
			require.Equal(t, got.Root.Native, api[0].Decision.Native)
			if test.Want == "" {
				require.Empty(t, api[0].Error)
			} else {
				require.Equal(t, got.RootError, api[0].Error)
			}
			observations[test.Name] = expected
		})
	}
	require.Len(t, observations, len(cases))
	require.NoError(t, os.WriteFile(path, raw, 0600))
	control := observeFrozenNativeDecision(t, frozenDecisionInput{Directory: directory, Policy: policy})
	require.Empty(t, control.SelectedError)
	require.Equal(t, "passed", control.Selected.Golden.State)
	binaryAfter, err := os.Stat(oracle)
	require.NoError(t, err)
	require.True(t, os.SameFile(binaryBefore, binaryAfter))
	require.Equal(t, binaryBefore.ModTime(), binaryAfter.ModTime())
	if out := os.Getenv("WAZA_READER_PROOF_DIR"); out != "" {
		encoded, err := json.Marshal(observations)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(out, "decision-frozen15c-observations.json"), encoded, 0600))
		require.NoError(t, os.WriteFile(filepath.Join(out, "actual-production-capture.json"), raw, 0600))
	}
}
