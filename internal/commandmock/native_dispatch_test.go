package commandmock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/models"
)

var errNativeSentinel = errors.New("native sentinel")

func nativeTestSession(t *testing.T, calls int) (string, *Session) {
	t.Helper()
	root := commandMockTestRoot(t)
	s, err := NewNativeSession(context.Background(), t.TempDir(), []models.CommandMockConfig{{
		Name: "az", ExpectCalls: intPointer(calls),
		Responses: []models.CommandMockResponse{{Args: []string{}, Stdout: "baseline"}},
	}}, t.TempDir(), func(context.Context, NativePrepareRequest) (json.RawMessage, error) {
		return json.RawMessage(`{"opaque":{"future":[1,true,"value"]}}`), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !s.closed {
			if err := os.RemoveAll(s.dir); err != nil {
				t.Error(err)
			}
		}
	})
	return root, s
}

func emitNative(output NativeOutput) NativeDispatch {
	return func(_ context.Context, _ NativeDispatchRequest, emit func(NativeOutput) error, receipt func() error) (int, error) {
		if err := emit(output); err != nil {
			return 1, err
		}
		return output.ExitCode, receipt()
	}
}

func TestNativeNilCompatibility(t *testing.T) {
	root := commandMockTestRoot(t)
	s, err := NewNativeSession(context.Background(), t.TempDir(), []models.CommandMockConfig{{
		Name: "az", ExpectCalls: intPointer(3),
		Responses: []models.CommandMockResponse{{Args: []string{}, ExitCode: 7}, {Args: []string{}, ExitCode: 9}},
	}}, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(s.dir, "config.json"))
	if err != nil || bytes.Contains(data, []byte("native_config")) {
		t.Fatalf("legacy config changed: %s, %v", data, err)
	}
	result, err := Invoke(root, s.ID(), "az", nil, s.workspace)
	if err != nil || result.ExitCode != 7 {
		t.Fatalf("first match changed: %+v, %v", result, err)
	}
	code, err := RunNativeCommand(context.Background(), root, s.ID(), "az", nil, s.workspace, NativeDispatchOptions{})
	if err != nil || code != 7 {
		t.Fatalf("nil native path: %d, %v", code, err)
	}
	t.Chdir(s.workspace)
	t.Setenv(SessionEnvironmentVariable, s.ID())
	if code := RunCommand(root, "az", nil); code != 7 {
		t.Fatalf("legacy shim path changed: %d", code)
	}
	if got, err := s.Close(); err != nil || len(got) != 3 {
		t.Fatalf("legacy records: %+v, %v", got, err)
	}
}

func TestNativePreparationOrderingAndCleanup(t *testing.T) {
	commandMockTestRoot(t)
	for _, failure := range []string{"prepare", "nil-config-materialize", "invalid-json", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var dir string
			_, err := NewNativeSession(ctx, t.TempDir(), []models.CommandMockConfig{{
				Name: "az", Responses: []models.CommandMockResponse{{
					Args: []string{}, WorkDir: "./root", Fixture: "missing.json",
				}},
			}}, t.TempDir(), func(ctx context.Context, req NativePrepareRequest) (json.RawMessage, error) {
				dir = req.StateDir
				if _, err := os.Stat(dir); err != nil {
					t.Fatal(err)
				}
				if !bytes.Contains(req.Mock, []byte("./root")) {
					t.Fatalf("preparation received normalized mock: %s", req.Mock)
				}
				if failure == "prepare" {
					return nil, errNativeSentinel
				}
				if failure == "invalid-json" {
					return json.RawMessage("{"), nil
				}
				if failure == "nil-config-materialize" {
					return nil, nil
				}
				if failure == "cancel" {
					cancel()
				}
				return json.RawMessage(`{}`), nil
			})
			if err == nil || dir == "" {
				t.Fatalf("preparation did not precede materialization: %v, %q", err, dir)
			}
			if failure == "prepare" && !errors.Is(err, errNativeSentinel) {
				t.Fatalf("lost preparation error: %v", err)
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("setup leaked task directory: %v", err)
			}
		})
	}
}

func TestNativeBaselineValidationBeforePrepare(t *testing.T) {
	called := false
	_, err := NewNativeSession(context.Background(), ".", []models.CommandMockConfig{{Name: "../invalid"}}, ".",
		func(context.Context, NativePrepareRequest) (json.RawMessage, error) {
			called = true
			return json.RawMessage(`{}`), nil
		})
	if err == nil || called {
		t.Fatalf("baseline validation bypassed: called=%v, err=%v", called, err)
	}
}

func TestNativeOpaqueRoundtripAndActualSanitizedReceipt(t *testing.T) {
	root, s := nativeTestSession(t, 1)
	t.Setenv("CLIENT_SECRET", "hidden-value")
	args := []string{"--client-secret", "hidden-value", "actual"}
	var stdout, stderr bytes.Buffer
	output := NativeOutput{Stdout: []byte("real stdout"), Stderr: "real stderr", ExitCode: 23, ResponseIndex: 4}
	dispatch := func(ctx context.Context, req NativeDispatchRequest, emit func(NativeOutput) error, receipt func() error) (int, error) {
		if req.StateDir != s.dir || req.Workspace != s.workspace || req.CWD != s.workspace || !reflect.DeepEqual(req.Args, args) {
			t.Fatalf("wrong dispatch context: %+v", req)
		}
		if string(req.Configuration) != `{"opaque":{"future":[1,true,"value"]}}` ||
			!bytes.Contains(req.Mock, []byte(`"name":"az"`)) {
			t.Fatalf("lost opaque or selected configuration: %+v", req)
		}
		if len(s.Invocations()) != 0 {
			t.Fatal("receipt existed before emission")
		}
		if err := emit(output); err != nil {
			return 1, err
		}
		if stdout.String() != "real stdout" || stderr.String() != "real stderr" || len(s.Invocations()) != 0 {
			t.Fatal("emission was not actual or recorded prematurely")
		}
		return output.ExitCode, receipt()
	}
	code, err := RunNativeCommand(context.Background(), root, s.ID(), "az", args, s.workspace,
		NativeDispatchOptions{Dispatch: dispatch, Stdout: &stdout, Stderr: &stderr})
	if code != 23 || err != nil {
		t.Fatalf("dispatch: %d, %v", code, err)
	}
	records, err := s.Close()
	if err != nil || len(records) != 1 || records[0].ExitCode != 23 || records[0].ResponseIndex != 4 ||
		!reflect.DeepEqual(records[0].Args, []string{redactedArg, redactedArg, "actual"}) {
		t.Fatalf("receipt did not reflect sanitized actual output: %+v, %v", records, err)
	}
}

type nativeFailWriter struct{ short bool }

func (w nativeFailWriter) Write(data []byte) (int, error) {
	if w.short {
		return len(data) - 1, nil
	}
	return 0, errNativeSentinel
}

func TestNativeWriteFailuresNoReceipts(t *testing.T) {
	for _, stream := range []string{"stdout", "stderr"} {
		for _, short := range []bool{false, true} {
			t.Run(stream+map[bool]string{false: "-error", true: "-short"}[short], func(t *testing.T) {
				root, s := nativeTestSession(t, 1)
				var stdout, stderr bytes.Buffer
				options := NativeDispatchOptions{
					Stdout: &stdout, Stderr: &stderr,
					Dispatch: emitNative(NativeOutput{Stdout: []byte("out"), Stderr: "err"}),
				}
				if stream == "stdout" {
					options.Stdout = nativeFailWriter{short}
				} else {
					options.Stderr = nativeFailWriter{short}
				}
				code, err := RunNativeCommand(context.Background(), root, s.ID(), "az", nil, s.workspace, options)
				want := errNativeSentinel
				if short {
					want = io.ErrShortWrite
				}
				if code == 0 || !errors.Is(err, want) || len(s.Invocations()) != 0 {
					t.Fatalf("write failure certified: %d, %v, %+v", code, err, s.Invocations())
				}
				if stream == "stderr" && stdout.String() != "out" {
					t.Fatal("actual stdout write not preserved on stderr failure")
				}
				_, err = s.Close()
				if err == nil || strings.Contains(err.Error(), "expected 1 call") {
					t.Fatalf("attempt was not counted independently: %v", err)
				}
			})
		}
	}
}

func TestNativeFailClosedAndCancellation(t *testing.T) {
	for _, mode := range []string{"missing", "no-emit", "pre-cancel", "callback-cancel", "post-emit-cancel", "dispatcher-error", "receipt-first"} {
		t.Run(mode, func(t *testing.T) {
			root, s := nativeTestSession(t, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var stdout bytes.Buffer
			called := false
			dispatch := func(ctx context.Context, req NativeDispatchRequest, emit func(NativeOutput) error, receipt func() error) (int, error) {
				called = true
				switch mode {
				case "no-emit":
					return 0, nil
				case "dispatcher-error":
					return 0, errNativeSentinel
				case "receipt-first":
					_ = receipt()
					return 0, nil
				case "callback-cancel":
					cancel()
					_ = emit(NativeOutput{Stdout: []byte("not written")})
					return 0, nil
				case "post-emit-cancel":
					if err := emit(NativeOutput{Stdout: []byte("written")}); err != nil {
						return 1, err
					}
					cancel()
					_ = receipt()
					return 0, nil
				default:
					t.Fatal("dispatcher must not run")
					return 0, nil
				}
			}
			if mode == "pre-cancel" {
				cancel()
			}
			if mode == "missing" {
				dispatch = nil
				if _, err := Invoke(root, s.ID(), "az", nil, s.workspace); err == nil {
					t.Fatal("legacy Invoke accepted private configuration")
				}
			}
			code, err := RunNativeCommand(ctx, root, s.ID(), "az", nil, s.workspace,
				NativeDispatchOptions{Dispatch: dispatch, Stdout: &stdout, Stderr: io.Discard})
			if code == 0 || err == nil || len(s.Invocations()) != 0 {
				t.Fatalf("undelivered step certified: %d, %v", code, err)
			}
			if strings.Contains(mode, "cancel") && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
			if (mode == "pre-cancel" || mode == "missing") && called {
				t.Fatal("unexpected dispatch")
			}
			if mode == "post-emit-cancel" && stdout.String() != "written" {
				t.Fatal("emitted bytes rolled back")
			}
			_, err = s.Close()
			if err == nil || strings.Contains(err.Error(), "expected 1 call") {
				t.Fatalf("lost attempt accounting: %v", err)
			}
		})
	}
}

func TestNativeReceiptFailureConsumesWithoutReemission(t *testing.T) {
	root, s := nativeTestSession(t, 2)
	var stdout bytes.Buffer
	dispatch := func(_ context.Context, _ NativeDispatchRequest, emit func(NativeOutput) error, receipt func() error) (int, error) {
		if err := emit(NativeOutput{Stdout: []byte("once")}); err != nil {
			return 1, err
		}
		if err := os.RemoveAll(s.logDir); err != nil {
			t.Fatal(err)
		}
		if err := receipt(); err == nil {
			t.Fatal("receipt unexpectedly succeeded")
		}
		// A dispatcher cannot bypass failures by ignoring callbacks or retrying.
		_ = emit(NativeOutput{Stdout: []byte("twice")})
		_ = receipt()
		return 0, nil
	}
	code, err := RunNativeCommand(context.Background(), root, s.ID(), "az", nil, s.workspace,
		NativeDispatchOptions{Dispatch: dispatch, Stdout: &stdout, Stderr: io.Discard})
	if code == 0 || err == nil || stdout.String() != "once" {
		t.Fatalf("receipt failure retried emission: %d, %v, %q", code, err, stdout.String())
	}
	if err := os.Mkdir(s.logDir, 0700); err != nil {
		t.Fatal(err)
	}
	attempts, err := os.ReadDir(filepath.Join(s.dir, "attempts"))
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts: %v, %v", attempts, err)
	}
	data, err := os.ReadFile(filepath.Join(s.dir, "attempts", attempts[0].Name()))
	var attempt nativeAttempt
	if err != nil || json.Unmarshal(data, &attempt) != nil || !attempt.Emitted || attempt.Receipt != "" {
		t.Fatalf("successful emission confused with receipt: %s, %v", data, err)
	}
	code, err = RunNativeCommand(context.Background(), root, s.ID(), "az", nil, s.workspace,
		NativeDispatchOptions{Dispatch: emitNative(NativeOutput{}), Stdout: &stdout, Stderr: io.Discard})
	if code != 0 || err != nil {
		t.Fatalf("second attempt: %d, %v", code, err)
	}
	records, err := s.Close()
	if len(records) != 1 || err == nil || strings.Contains(err.Error(), "expected 2 call") {
		t.Fatalf("attempts conflated with receipts: %+v, %v", records, err)
	}
}

func TestNativeExactAttemptCountsAndReceiptGuard(t *testing.T) {
	for _, mode := range []string{"wrong-count", "exit-mismatch", "duplicate-receipt", "post-receipt-error"} {
		t.Run(mode, func(t *testing.T) {
			expected := 1
			if mode == "wrong-count" {
				expected = 2
			}
			root, s := nativeTestSession(t, expected)
			var stdout bytes.Buffer
			dispatch := func(_ context.Context, _ NativeDispatchRequest, emit func(NativeOutput) error, receipt func() error) (int, error) {
				if err := emit(NativeOutput{Stdout: []byte("once"), ExitCode: 7}); err != nil {
					return 1, err
				}
				if err := receipt(); err != nil {
					return 1, err
				}
				switch mode {
				case "exit-mismatch":
					return 0, nil
				case "duplicate-receipt":
					_ = receipt()
				case "post-receipt-error":
					return 7, errNativeSentinel
				}
				return 7, nil
			}
			code, dispatchErr := RunNativeCommand(context.Background(), root, s.ID(), "az", nil, s.workspace,
				NativeDispatchOptions{Dispatch: dispatch, Stdout: &stdout, Stderr: io.Discard})
			if stdout.String() != "once" || len(s.Invocations()) != 1 {
				t.Fatal("delivered receipt rolled back or duplicated")
			}
			if mode == "wrong-count" {
				if code != 7 || dispatchErr != nil {
					t.Fatalf("dispatch: %d, %v", code, dispatchErr)
				}
			} else if code == 0 || dispatchErr == nil {
				t.Fatalf("ignored dispatcher/capture failure: %d, %v", code, dispatchErr)
			}
			_, closeErr := s.Close()
			if closeErr == nil {
				t.Fatal("invalid count or capture certified")
			}
			if mode == "wrong-count" && !strings.Contains(closeErr.Error(), "expected 2 call(s), got 1") {
				t.Fatalf("incorrect exact attempt count: %v", closeErr)
			}
		})
	}
}

func TestNativeCaptureLossIsOperationalError(t *testing.T) {
	for _, loss := range []string{"receipt", "attempt", "corrupt-receipt", "corrupt-attempt", "pending-attempt"} {
		t.Run(loss, func(t *testing.T) {
			root, s := nativeTestSession(t, 1)
			_, err := RunNativeCommand(context.Background(), root, s.ID(), "az", nil, s.workspace,
				NativeDispatchOptions{Dispatch: emitNative(NativeOutput{}), Stdout: io.Discard, Stderr: io.Discard})
			if err != nil {
				t.Fatal(err)
			}
			dir := s.logDir
			if strings.Contains(loss, "attempt") {
				dir = filepath.Join(s.dir, "attempts")
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 {
				t.Fatalf("capture: %v, %v", entries, err)
			}
			path := filepath.Join(dir, entries[0].Name())
			if strings.HasPrefix(loss, "corrupt") {
				err = os.WriteFile(path, []byte("{"), 0600)
			} else if loss == "pending-attempt" {
				err = os.WriteFile(path, []byte(`{"command":"az","done":false}`), 0600)
			} else {
				err = os.Remove(path)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Close(); err == nil {
				t.Fatal("lost capture certified exact count")
			} else if (loss == "attempt" || loss == "corrupt-attempt") && strings.Contains(err.Error(), "got 0") {
				t.Fatalf("lost data treated as a known exact count: %v", err)
			}
		})
	}
}

func TestNativeAttemptCaptureFailureMarked(t *testing.T) {
	root, s := nativeTestSession(t, 0)
	dir := filepath.Join(s.dir, "attempts")
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	called := false
	dispatch := func(context.Context, NativeDispatchRequest, func(NativeOutput) error, func() error) (int, error) {
		called = true
		return 0, nil
	}
	code, err := RunNativeCommand(context.Background(), root, s.ID(), "az", nil, s.workspace,
		NativeDispatchOptions{Dispatch: dispatch})
	if code == 0 || err == nil || called {
		t.Fatalf("dispatch ran without attempted-call capture: %d, %v, %v", code, err, called)
	}
	if _, err := os.Stat(filepath.Join(s.dir, "native-capture-error")); err != nil {
		t.Fatalf("capture failure not marked: %v", err)
	}
	if _, err := s.Close(); err == nil {
		t.Fatal("zero expectation certified from failed attempt capture")
	}
}

func TestNativePreparationSnapshotSkipsLegacyMaterialization(t *testing.T) {
	for _, change := range []string{"mutate", "remove"} {
		t.Run(change, func(t *testing.T) {
			root := commandMockTestRoot(t)
			baseDir := t.TempDir()
			path := filepath.Join(baseDir, "declared.json")
			if err := os.WriteFile(path, []byte("immutable snapshot"), 0600); err != nil {
				t.Fatal(err)
			}
			s, err := NewNativeSession(context.Background(), t.TempDir(), []models.CommandMockConfig{{
				Name: "az", ExpectCalls: intPointer(1),
				Responses: []models.CommandMockResponse{{Args: []string{}, Fixture: "declared.json", WorkDir: "./root"}},
			}}, baseDir, func(_ context.Context, req NativePrepareRequest) (json.RawMessage, error) {
				var baseline models.CommandMockConfig
				if err := json.Unmarshal(req.Mock, &baseline); err != nil {
					return nil, err
				}
				if baseline.Responses[0].WorkDir != "./root" {
					t.Fatalf("preparation received normalized baseline: %+v", baseline)
				}
				data, err := os.ReadFile(filepath.Join(baseDir, baseline.Responses[0].Fixture))
				if err != nil {
					return nil, err
				}
				if change == "mutate" {
					err = os.WriteFile(path, []byte("mutable replacement"), 0600)
				} else {
					err = os.Remove(path)
				}
				if err != nil {
					return nil, err
				}
				return json.Marshal(map[string]string{"captured": string(data)})
			})
			if err != nil {
				t.Fatalf("private snapshot triggered shadowed legacy fixture I/O: %v", err)
			}
			t.Cleanup(func() {
				if !s.closed {
					if _, err := s.Close(); err != nil {
						t.Error(err)
					}
				}
			})
			config, err := readStoredConfig(filepath.Join(s.dir, "config.json"))
			if err != nil || len(config.Mocks) != 1 || len(config.Mocks[0].Responses) != 0 {
				t.Fatalf("private responses were materialized: %+v, %v", config, err)
			}
			var stdout bytes.Buffer
			dispatch := func(_ context.Context, req NativeDispatchRequest, emit func(NativeOutput) error, receipt func() error) (int, error) {
				var snapshot map[string]string
				if err := json.Unmarshal(req.Configuration, &snapshot); err != nil {
					return 1, err
				}
				if err := emit(NativeOutput{Stdout: []byte(snapshot["captured"])}); err != nil {
					return 1, err
				}
				return 0, receipt()
			}
			code, err := RunNativeCommand(context.Background(), root, s.ID(), "az", nil, s.workspace,
				NativeDispatchOptions{Dispatch: dispatch, Stdout: &stdout, Stderr: io.Discard})
			if code != 0 || err != nil || stdout.String() != "immutable snapshot" {
				t.Fatalf("snapshot was not immutable: %d, %v, %q", code, err, stdout.String())
			}
			if _, err := s.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeExitCodeRangeBeforeEmission(t *testing.T) {
	for _, exitCode := range []int{-1, 0, 255, 256} {
		t.Run(strconv.Itoa(exitCode), func(t *testing.T) {
			root, s := nativeTestSession(t, 1)
			var stdout, stderr bytes.Buffer
			output := NativeOutput{Stdout: []byte("stdout"), Stderr: "stderr", ExitCode: exitCode}
			code, err := RunNativeCommand(context.Background(), root, s.ID(), "az", nil, s.workspace,
				NativeDispatchOptions{Dispatch: emitNative(output), Stdout: &stdout, Stderr: &stderr})
			if exitCode < 0 || exitCode > 255 {
				if code == 0 || err == nil || !strings.Contains(err.Error(), "between 0 and 255") {
					t.Fatalf("invalid exit status accepted: %d, %v", code, err)
				}
				if stdout.Len() != 0 || stderr.Len() != 0 || len(s.Invocations()) != 0 {
					t.Fatal("invalid exit status emitted bytes or published a receipt")
				}
				if _, err := s.Close(); err == nil {
					t.Fatal("invalid exit status capture certified")
				}
				return
			}
			if code != exitCode || err != nil || stdout.String() != "stdout" || stderr.String() != "stderr" {
				t.Fatalf("valid exit boundary rejected: %d, %v", code, err)
			}
			if records, err := s.Close(); err != nil || len(records) != 1 || records[0].ExitCode != exitCode {
				t.Fatalf("boundary receipt incorrect: %+v, %v", records, err)
			}
		})
	}
}

func TestNativeReceiptRejectsCorruptionWithoutFabricatedInvocations(t *testing.T) {
	tests := []struct {
		name  string
		field string
		value any
		omit  bool
	}{
		{name: "missing-exit", field: "exit_code", omit: true},
		{name: "null-exit", field: "exit_code"},
		{name: "float-exit", field: "exit_code", value: 1.5},
		{name: "string-exit", field: "exit_code", value: "0"},
		{name: "negative-exit", field: "exit_code", value: -1},
		{name: "overflow-exit", field: "exit_code", value: 256},
		{name: "missing-index", field: "response_index", omit: true},
		{name: "null-index", field: "response_index"},
		{name: "negative-index", field: "response_index", value: -1},
		{name: "float-index", field: "response_index", value: 1.5},
		{name: "string-index", field: "response_index", value: "0"},
		{name: "missing-command", field: "command", omit: true},
		{name: "null-command", field: "command"},
		{name: "empty-command", field: "command", value: ""},
		{name: "missing-stamp", field: "recorded_at", omit: true},
		{name: "null-stamp", field: "recorded_at"},
		{name: "zero-stamp", field: "recorded_at", value: "0001-01-01T00:00:00Z"},
		{name: "invalid-stamp", field: "recorded_at", value: "invalid"},
	}
	for _, duplicate := range []string{"exit_code", "response_index", "command", "recorded_at", "invocation"} {
		tests = append(tests, struct {
			name  string
			field string
			value any
			omit  bool
		}{name: "duplicate-" + duplicate, field: duplicate})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, s := nativeTestSession(t, 1)
			_, err := RunNativeCommand(context.Background(), root, s.ID(), "az", nil, s.workspace,
				NativeDispatchOptions{Dispatch: emitNative(NativeOutput{ExitCode: 7, ResponseIndex: 2}), Stdout: io.Discard, Stderr: io.Discard})
			if err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(s.logDir)
			if err != nil || len(entries) != 1 {
				t.Fatalf("receipts: %v, %v", entries, err)
			}
			path := filepath.Join(s.logDir, entries[0].Name())
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(tt.name, "duplicate-") {
				needle := `"` + tt.field + `":`
				data = []byte(strings.Replace(string(data), needle, needle+`null,`+needle, 1))
			} else {
				var outer map[string]json.RawMessage
				if err := json.Unmarshal(data, &outer); err != nil {
					t.Fatal(err)
				}
				var target map[string]json.RawMessage
				if tt.field == "recorded_at" {
					target = outer
				} else {
					if err := json.Unmarshal(outer["invocation"], &target); err != nil {
						t.Fatal(err)
					}
				}
				if tt.omit {
					delete(target, tt.field)
				} else {
					value, err := json.Marshal(tt.value)
					if err != nil {
						t.Fatal(err)
					}
					target[tt.field] = value
				}
				if tt.field != "recorded_at" {
					outer["invocation"], err = json.Marshal(target)
					if err != nil {
						t.Fatal(err)
					}
				}
				data, err = json.Marshal(outer)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if records := s.Invocations(); len(records) != 0 {
				t.Fatalf("accessor returned corrupt native receipt: %+v", records)
			}
			records, err := s.Close()
			if err == nil || len(records) != 0 {
				t.Fatalf("corrupt native receipt certified or fabricated invocation: %+v, %v", records, err)
			}
		})
	}
}

func TestNativeReceiptRequiresRealCanonicalAttempt(t *testing.T) {
	for _, name := range []string{"call-stray.json", "attempt-orphan.json"} {
		t.Run(name, func(t *testing.T) {
			root, s := nativeTestSession(t, 1)
			_, err := RunNativeCommand(context.Background(), root, s.ID(), "az", nil, s.workspace,
				NativeDispatchOptions{Dispatch: emitNative(NativeOutput{}), Stdout: io.Discard, Stderr: io.Discard})
			if err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(s.logDir)
			if err != nil || len(entries) != 1 {
				t.Fatalf("receipts: %v, %v", entries, err)
			}
			if err := os.Rename(filepath.Join(s.logDir, entries[0].Name()), filepath.Join(s.logDir, name)); err != nil {
				t.Fatal(err)
			}
			if records, err := s.Close(); err == nil || len(records) != 0 {
				t.Fatalf("stray native receipt masqueraded as delivery: %+v, %v", records, err)
			}
		})
	}
}

func TestNativeRetainedCallbacksRejectAfterDispatch(t *testing.T) {
	for _, beforeReturn := range []string{"none", "emit", "receipt"} {
		t.Run(beforeReturn, func(t *testing.T) {
			root, s := nativeTestSession(t, 1)
			var retainedEmit func(NativeOutput) error
			var retainedReceipt func() error
			var stdout, stderr bytes.Buffer
			dispatch := func(_ context.Context, _ NativeDispatchRequest, emit func(NativeOutput) error, receipt func() error) (int, error) {
				retainedEmit, retainedReceipt = emit, receipt
				if beforeReturn != "none" {
					if err := emit(NativeOutput{Stdout: []byte("once"), Stderr: "once"}); err != nil {
						return 1, err
					}
				}

				if beforeReturn == "receipt" {
					if err := receipt(); err != nil {
						return 1, err
					}
				}
				return 1, errNativeSentinel
			}
			_, err := RunNativeCommand(context.Background(), root, s.ID(), "az", nil, s.workspace,
				NativeDispatchOptions{Dispatch: dispatch, Stdout: &stdout, Stderr: &stderr})
			if !errors.Is(err, errNativeSentinel) {
				t.Fatalf("dispatcher failure lost: %v", err)
			}
			out, diagnostic, receiptCount := stdout.String(), stderr.String(), len(s.Invocations())
			if err := retainedEmit(NativeOutput{Stdout: []byte("late"), Stderr: "late"}); err == nil || !strings.Contains(err.Error(), "active dispatch") {
				t.Fatalf("retained emit accepted: %v", err)
			}
			if err := retainedReceipt(); err == nil || !strings.Contains(err.Error(), "active dispatch") {
				t.Fatalf("retained receipt accepted: %v", err)
			}
			if stdout.String() != out || stderr.String() != diagnostic || len(s.Invocations()) != receiptCount {
				t.Fatal("retained callbacks mutated actual streams or receipts")
			}
			if _, err := s.Close(); err == nil {
				t.Fatal("failed dispatch capture certified")
			}
		})
	}
}

func TestNativeNegativeResponseIndexRejectedBeforeEmission(t *testing.T) {
	root, s := nativeTestSession(t, 1)
	var stdout, stderr bytes.Buffer
	code, err := RunNativeCommand(context.Background(), root, s.ID(), "az", nil, s.workspace,
		NativeDispatchOptions{Dispatch: emitNative(NativeOutput{Stdout: []byte("out"), Stderr: "err", ResponseIndex: -1}), Stdout: &stdout, Stderr: &stderr})
	if code == 0 || err == nil || stdout.Len() != 0 || stderr.Len() != 0 || len(s.Invocations()) != 0 {
		t.Fatalf("negative response index emitted or fabricated receipt: %d, %v", code, err)
	}
	if _, err := s.Close(); err == nil {
		t.Fatal("failed negative-index capture certified")
	}
}

func TestNativeCapturePreservesLegacyDecoding(t *testing.T) {
	_, s := nativeTestSession(t, 0)
	data := []byte(`{"recorded_at":"2026-10-09T00:00:00Z","invocation":{"command":"gh","args":[]}}`)
	if err := os.WriteFile(filepath.Join(s.logDir, "call-legacy.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	records, err := s.Close()
	if err != nil || len(records) != 1 || records[0].Command != "gh" || records[0].ExitCode != 0 {
		t.Fatalf("legacy decoding changed in mixed capture: %+v, %v", records, err)
	}
}
