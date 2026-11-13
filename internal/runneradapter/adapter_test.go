package runneradapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func exampleRequest(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	return bytes.TrimSpace(raw)
}

func descriptor(t *testing.T, operation string, extra string) []byte {
	t.Helper()
	body := `{"kind":"waza.adapter-work-descriptor","schema_version":"1.0","work_id":"queue/item-7","operation":"` + operation + `"`
	if operation != OperationStatus {
		body += `,"request":` + string(exampleRequest(t))
	}
	return []byte(body + extra + "}")
}

func TestMain(m *testing.M) {
	if os.Getenv("RUNNERADAPTER_PUBLIC_CLI_HELPER") == "1" {
		input, _ := io.ReadAll(os.Stdin)
		args := strings.Join(os.Args[1:], " ")
		if args != "runner submit --store dir" || !strings.Contains(string(input), `"kind":"waza.execution-submission"`) {
			fmt.Fprintf(os.Stderr, "unexpected call %q", args)
			os.Exit(5)
		}
		fmt.Print(`{"kind":"waza.execution-submission-result","schema_version":"1.0","status":"rejected","exit_code":6,"engine_calls":0,` +
			`"diagnostics":[{"code":"submission_input_invalid","message":"invalid"}]}` + "\n")
		os.Exit(6)
	}
	os.Exit(m.Run())
}

func TestProcessContractRunsPublicCLIOnceWithExactInput(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	t.Setenv("RUNNERADAPTER_PUBLIC_CLI_HELPER", "1")
	translation := Translate(descriptor(t, OperationSubmit, ""))
	contract := ProcessContract{Executable: executable, StoreArgs: []string{"--store", "dir"}}
	result := Dispatch(t.Context(), contract, translation, nil)
	require.Equal(t, StatusRejected, result.Status, "%+v", result.Diagnostics)
	require.Equal(t, 6, result.ExitCode)
	require.True(t, result.Dispatched)

	missing := Dispatch(t.Context(), ProcessContract{Executable: executable + "-missing"}, translation, nil)
	require.Equal(t, StatusContractError, missing.Status)
	require.Equal(t, CodeContractUnavailable, missing.Diagnostics[0].Code)
}

func TestTranslateForwardsRequestAndPolicyBytesExactly(t *testing.T) {
	request := exampleRequest(t)
	policy := `{"mode": "distinct", "parent": {"execution_id": "exec-` + strings.Repeat("a", 64) + `", "relationship": "retry"}}`
	translation := Translate(descriptor(t, OperationSubmit, `,"policy": `+policy))
	require.Equal(t, "translated", translation.Status, "%+v", translation.Diagnostics)
	want := `{"kind":"waza.execution-submission","schema_version":"1.0","request":` + string(request) + `,"policy":` + policy + `}`
	require.Equal(t, want, translation.Call.Input)
	require.Equal(t, digest([]byte(want)), translation.Call.InputSHA256)
	require.Equal(t, digest(request), translation.Summary.RequestSHA256)
	require.Equal(t, []string{"runner", "submit"}, translation.Call.Command)
	require.Equal(t, HTTPRoute{Method: "POST", Path: "/api/v1/submissions"}, translation.Call.HTTP)
	require.Equal(t, "distinct", translation.Summary.Policy)
	require.Equal(t, "retry", translation.Summary.Parent.Relationship)
	require.Equal(t, &Selection{Shard: "shard-a", Task: "selected", Trial: 2, Attempt: 2}, translation.Summary.Selection)
	require.Equal(t, "queue/item-7", translation.WorkID)
	_, err := EncodeTranslation(translation)
	require.NoError(t, err)

	again := Translate(descriptor(t, OperationSubmit, `,"policy": `+policy))
	require.Equal(t, translation, again, "translation must be deterministic so redelivery is byte-identical")
}

func TestTranslateOmittedPolicyInjectsNoDefault(t *testing.T) {
	translation := Translate(descriptor(t, OperationCheckpoint, ""))
	require.Equal(t, "translated", translation.Status)
	require.NotContains(t, translation.Call.Input, `"policy"`)
	require.Equal(t, "omitted", translation.Summary.Policy)
	require.Equal(t, []string{"runner", "checkpoint", "create"}, translation.Call.Command)
}

func TestTranslateResumeAndStatusRoutes(t *testing.T) {
	checkpoint := strings.Repeat("b", 64)
	resume := Translate(descriptor(t, OperationResume, `,"checkpoint_sha256":"`+checkpoint+`"`))
	require.Equal(t, "translated", resume.Status, "%+v", resume.Diagnostics)
	require.Equal(t, []string{"runner", "checkpoint", "resume", checkpoint}, resume.Call.Command)
	require.Equal(t, "/api/v1/checkpoints/"+checkpoint+"/resume", resume.Call.HTTP.Path)

	run, work := "run-"+strings.Repeat("c", 64), "trial-"+strings.Repeat("d", 64)
	status := Translate(descriptor(t, OperationStatus, `,"logical":{"run":"`+run+`","work":"`+work+`"}`))
	require.Equal(t, "translated", status.Status, "%+v", status.Diagnostics)
	require.Equal(t, []string{"runner", "lineage", "--run", run, "--work", work}, status.Call.Command)
	require.Equal(t, HTTPRoute{Method: "GET", Path: "/api/v1/lineage?run=" + run + "&work=" + work}, status.Call.HTTP)
	require.Empty(t, status.Call.Input)
	_, err := EncodeTranslation(status)
	require.NoError(t, err)
}

func TestTranslateRejectsAmbiguousOrLossyDescriptors(t *testing.T) {
	checkpoint := `,"checkpoint_sha256":"` + strings.Repeat("b", 64) + `"`
	logical := `,"logical":{"run":"run-` + strings.Repeat("c", 64) + `","work":"trial-` + strings.Repeat("d", 64) + `"}`
	cases := map[string]struct {
		data []byte
		code string
	}{
		"scheduler priority":     {descriptor(t, OperationSubmit, `,"priority":5`), CodeDescriptorUnsupported},
		"scheduler retry policy": {descriptor(t, OperationSubmit, `,"max_retries":3,"queue":"gpu"`), CodeDescriptorUnsupported},
		"duplicate key":          {descriptor(t, OperationSubmit, `,"work_id":"other"`), CodeDescriptorInvalid},
		"trailing data":          {append(descriptor(t, OperationSubmit, ""), []byte(` {}`)...), CodeDescriptorInvalid},
		"not an object":          {[]byte(`[]`), CodeDescriptorInvalid},
		"submit with checkpoint": {descriptor(t, OperationSubmit, checkpoint), CodeDescriptorAmbiguous},
		"submit with logical":    {descriptor(t, OperationSubmit, logical), CodeDescriptorAmbiguous},
		"resume without digest":  {descriptor(t, OperationResume, ""), CodeDescriptorIncomplete},
		"status without logical": {descriptor(t, OperationStatus, ""), CodeDescriptorIncomplete},
		"status with policy":     {descriptor(t, OperationStatus, logical+`,"policy":{"mode":"distinct"}`), CodeDescriptorAmbiguous},
		"unknown operation":      {descriptor(t, "schedule", ""), CodeDescriptorInvalid},
		"negotiation-only request": {[]byte(`{"kind":"waza.adapter-work-descriptor","schema_version":"1.0","work_id":"w","operation":"submit",` +
			`"request":{"schema_version":"1.0","versions":["1.0"],"required_capabilities":["exact-unit"],"optional_capabilities":[]}}`), CodeDescriptorInvalid},
		"fractional trial": {bytes.Replace(descriptor(t, OperationSubmit, ""), []byte(`"trial": 2`), []byte(`"trial": 2.5`), 1), CodeDescriptorInvalid},
		"invalid work id":  {bytes.Replace(descriptor(t, OperationSubmit, ""), []byte(`queue/item-7`), []byte(` spaced`), 1), CodeDescriptorInvalid},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			translation := Translate(tc.data)
			require.Equal(t, "rejected", translation.Status)
			require.Len(t, translation.Diagnostics, 1)
			require.Equal(t, tc.code, translation.Diagnostics[0].Code, translation.Diagnostics[0].Message)
			require.Nil(t, translation.Call)
			_, err := EncodeTranslation(translation)
			require.NoError(t, err)
			result := Dispatch(t.Context(), failingContract{t}, translation, nil)
			require.Equal(t, StatusRejected, result.Status)
			require.Equal(t, 6, result.ExitCode)
			require.False(t, result.Dispatched)
			_, err = EncodeResult(result)
			require.NoError(t, err)
		})
	}
}

type failingContract struct{ t *testing.T }

func (f failingContract) Call(context.Context, Call) (Reply, error) {
	f.t.Fatal("rejected descriptors must make no public call")
	return Reply{}, nil
}

type recordingContract struct {
	calls []Call
	reply Reply
	err   error
}

func (r *recordingContract) Call(_ context.Context, call Call) (Reply, error) {
	r.calls = append(r.calls, call)
	return r.reply, r.err
}

func submissionReply(t *testing.T, status string, exit int) []byte {
	t.Helper()
	value := map[string]any{"kind": "waza.execution-submission-result", "schema_version": "1.0", "status": status,
		"exit_code": exit, "engine_calls": 0, "diagnostics": []any{}}
	if status == "rejected" {
		value["diagnostics"] = []any{map[string]string{"code": "submission_input_invalid", "message": "invalid"}}
		data, err := json.Marshal(value)
		require.NoError(t, err)
		return data
	}
	data, err := os.ReadFile("testdata/submission-completed.json")
	require.NoError(t, err)
	return data
}

func TestDispatchMapsPublicResultsOneToOne(t *testing.T) {
	translation := Translate(descriptor(t, OperationSubmit, ""))
	rejectedReply := &recordingContract{reply: Reply{ExitCode: 6, Body: submissionReply(t, "rejected", 6)}}
	result := Dispatch(t.Context(), rejectedReply, translation, nil)
	require.Len(t, rejectedReply.calls, 1)
	require.Equal(t, StatusRejected, result.Status)
	require.Equal(t, 6, result.ExitCode)
	require.True(t, result.Dispatched)
	require.Equal(t, "not_applicable", result.Provenance.State)
	_, err := EncodeResult(result)
	require.NoError(t, err)

	contradiction := &recordingContract{reply: Reply{ExitCode: 0, Body: submissionReply(t, "rejected", 6)}}
	result = Dispatch(t.Context(), contradiction, translation, nil)
	require.Equal(t, StatusContractError, result.Status)
	require.Equal(t, CodeContractViolation, result.Diagnostics[0].Code)

	wrongKind := &recordingContract{reply: Reply{Body: []byte(`{"kind":"waza.execution-lineage","schema_version":"1.0"}`)}}
	result = Dispatch(t.Context(), wrongKind, translation, nil)
	require.Equal(t, StatusContractError, result.Status)

	unavailable := &recordingContract{err: errors.New("spawn failed")}
	result = Dispatch(t.Context(), unavailable, translation, nil)
	require.Len(t, unavailable.calls, 1, "the adapter never retries")
	require.Equal(t, StatusContractError, result.Status)
	require.Equal(t, 2, result.ExitCode)
	require.Equal(t, CodeContractUnavailable, result.Diagnostics[0].Code)
	_, err = EncodeResult(result)
	require.NoError(t, err)
}

func TestDispatchRejectsInternallyInconsistentSubmission(t *testing.T) {
	translation := Translate(descriptor(t, OperationSubmit, ""))
	completed := string(submissionReply(t, "completed", 0))
	for name, body := range map[string]string{
		"completion execution": strings.Replace(completed, `"waza.execution-completion","schema_version":"1.0","execution_id":"exec-215`,
			`"waza.execution-completion","schema_version":"1.0","execution_id":"exec-315`, 1),
		"terminal exit code": strings.Replace(completed, `"outcome":"success","exit_code":0`, `"outcome":"success","exit_code":1`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			require.NotEqual(t, completed, body)
			contract := &recordingContract{reply: Reply{ExitCode: 0, Body: []byte(body)}}
			result := Dispatch(t.Context(), contract, translation, nil)
			require.Equal(t, StatusContractError, result.Status)
			require.Equal(t, CodeContractViolation, result.Diagnostics[0].Code)
			require.Empty(t, result.ManifestDigest)
		})
	}
}

func TestHTTPContractNeverFollowsRedirects(t *testing.T) {
	translation := Translate(descriptor(t, OperationSubmit, ""))
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Redirect(w, r, "/api/v1/elsewhere", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	_, err := HTTPContract{BaseURL: server.URL, Client: server.Client()}.Call(t.Context(), *translation.Call)
	require.ErrorContains(t, err, "redirect")
	require.Equal(t, 1, calls, "a redirect must not replay the state-changing call")
}

func TestHTTPContractForwardsExactBodyAndExitHeader(t *testing.T) {
	translation := Translate(descriptor(t, OperationSubmit, ""))
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/submissions", r.URL.Path)
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		gotBody, _ = readAll(r)
		w.Header().Set("X-Waza-Exit-Code", "6")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(submissionReply(t, "rejected", 6))
	}))
	defer server.Close()
	reply, err := HTTPContract{BaseURL: server.URL}.Call(t.Context(), *translation.Call)
	require.NoError(t, err)
	require.Equal(t, translation.Call.Input, string(gotBody))
	require.Equal(t, 6, reply.ExitCode)

	bare := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer bare.Close()
	_, err = HTTPContract{BaseURL: bare.URL}.Call(t.Context(), *translation.Call)
	require.ErrorContains(t, err, "HTTP 403")
}

func readAll(r *http.Request) ([]byte, error) {
	var buffer bytes.Buffer
	_, err := buffer.ReadFrom(r.Body)
	return buffer.Bytes(), err
}

type stubVerifier struct{ state string }

func (s stubVerifier) VerifyProvenance(context.Context, string, json.RawMessage) Provenance {
	return Provenance{State: s.state, Detail: "hook"}
}

func TestProvenanceHookIsOptionalAndRecordedOnly(t *testing.T) {
	translation := Translate(descriptor(t, OperationSubmit, ""))
	completed := &recordingContract{reply: Reply{ExitCode: 0, Body: submissionReply(t, "completed", 0)}}
	result := Dispatch(t.Context(), completed, translation, nil)
	require.Equal(t, StatusTerminal, result.Status, "%+v", result.Diagnostics)
	require.Equal(t, "not_configured", result.Provenance.State)
	require.NotEmpty(t, result.ManifestDigest)
	verified := Dispatch(t.Context(), completed, translation, stubVerifier{"verified"})
	require.Equal(t, "verified", verified.Provenance.State)
	failed := Dispatch(t.Context(), completed, translation, stubVerifier{"failed"})
	require.Equal(t, StatusTerminal, failed.Status, "a provenance failure is recorded, not turned into a retry or a new outcome")
	require.Equal(t, "failed", failed.Provenance.State)
	unknown := Dispatch(t.Context(), completed, translation, stubVerifier{"trusted"})
	require.Equal(t, "unavailable", unknown.Provenance.State)
	for _, value := range []Result{result, verified, failed, unknown} {
		_, err := EncodeResult(value)
		require.NoError(t, err)
	}
}
