package commandmock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/models"
)

// NativePrepareRequest contains the typed, serialized baseline mock before
// normalization, not the retained raw source or a public schema extension.
// Serialization's omitempty tags lose explicit empty slices and zero values.
// Callers requiring source presence must retain it separately in a closure or
// private envelope. Preparation owns any additional private state.
type NativePrepareRequest struct {
	StateDir  string
	Workspace string
	Mock      json.RawMessage
}

type NativePrepare func(context.Context, NativePrepareRequest) (json.RawMessage, error)

// NativeDispatchRequest carries the selected stored mock and opaque preparation
// result. Neither value is interpreted as a fault schema by commandmock.
type NativeDispatchRequest struct {
	StateDir      string
	Workspace     string
	CWD           string
	Name          string
	Args          []string
	Mock          json.RawMessage
	Configuration json.RawMessage
}

type NativeOutput struct {
	Stdout        []byte
	Stderr        string
	ExitCode      int
	ResponseIndex int
}

// NativeDispatch must call emit and then receipt synchronously, at most once.
// It owns cancellable I/O; this hook does not install process-signal handlers.
// Receipt failure after emission must never cause retry, re-emission or rollback.
type NativeDispatch func(context.Context, NativeDispatchRequest, func(NativeOutput) error, func() error) (int, error)

type NativeDispatchOptions struct {
	Dispatch NativeDispatch
	Stdout   io.Writer
	Stderr   io.Writer
}

type nativeAttempt struct {
	Command string `json:"command"`
	Done    bool   `json:"done"`
	Emitted bool   `json:"emitted"`
	Receipt string `json:"receipt,omitempty"`
	Error   string `json:"error,omitempty"`
}

// RunNativeCommand is an opt-in shim entry point. With no private configuration
// it uses RunCommand unchanged; configured mocks never fall through to the host.
// Nil writers use the actual process streams.
func RunNativeCommand(ctx context.Context, root, sessionID, name string, args []string, cwd string, options NativeDispatchOptions) (exitCode int, resultErr error) {
	config, mock, err := findMock(root, sessionID, name, cwd)
	if err != nil {
		return 1, err
	}
	if mock == nil || mock.NativeConfig == nil {
		result, err := Invoke(root, sessionID, name, args, cwd)
		if err != nil {
			return 127, err
		}
		return runInvocationResult(root, name, args, result), nil
	}
	stateDir := filepath.Dir(config.LogDir)
	file, err := os.CreateTemp(filepath.Join(stateDir, "attempts"), "attempt-*.json")
	if err != nil {
		markErr := os.WriteFile(filepath.Join(stateDir, "native-capture-error"), []byte("native attempt capture failed\n"), 0600)
		if markErr != nil {
			markErr = fmt.Errorf("marking native capture failure: %w", markErr)
		}
		return 1, errors.Join(fmt.Errorf("recording native command attempt: %w", err), markErr)
	}
	attempt := nativeAttempt{Command: mock.Name}
	path := file.Name()
	data, err := json.Marshal(attempt)
	if err != nil {
		return 1, errors.Join(err, file.Close())
	}
	n, writeErr := file.Write(data)
	if writeErr == nil && n != len(data) {
		writeErr = io.ErrShortWrite
	}
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return 1, fmt.Errorf("recording native command attempt: %w", err)
	}
	defer func() {
		attempt.Done = true
		if resultErr != nil {
			// Do not persist arbitrary dispatcher diagnostics, which may contain
			// private configuration or raw arguments.
			attempt.Error = "native dispatch or capture failed"
		}
		data, err := json.Marshal(attempt)
		if err == nil {
			err = os.WriteFile(path, data, 0600)
		}
		if err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("finalizing native command attempt: %w", err))
			exitCode = 1
		}
	}()
	if err := ctx.Err(); err != nil {
		return 1, err
	}
	if options.Dispatch == nil {
		return 1, fmt.Errorf("command mock %q requires a private native dispatcher", name)
	}
	raw, err := json.Marshal(mock)
	if err != nil {
		return 1, fmt.Errorf("encoding selected command mock: %w", err)
	}
	stdout, stderr := options.Stdout, options.Stderr
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	var output NativeOutput
	var callbackErr error
	var active atomic.Bool
	active.Store(true)
	defer active.Store(false)
	emitCalled, emitted, receiptCalled := false, false, false
	fail := func(err error) error {
		callbackErr = errors.Join(callbackErr, err)
		return err
	}
	emit := func(value NativeOutput) error {
		if !active.Load() {
			return errors.New("native command emission requires active dispatch")
		}
		if emitCalled {
			return fail(errors.New("native command emission already attempted"))
		}
		emitCalled = true
		if value.ExitCode < 0 || value.ExitCode > 255 {
			return fail(fmt.Errorf("native command exit code must be between 0 and 255, got %d", value.ExitCode))
		}
		if value.ResponseIndex < 0 {
			return fail(errors.New("native command response index must be nonnegative"))
		}
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if err := writeNative(stdout, value.Stdout); err != nil {
			return fail(fmt.Errorf("emitting native stdout: %w", err))
		}
		if err := writeNative(stderr, []byte(value.Stderr)); err != nil {
			return fail(fmt.Errorf("emitting native stderr: %w", err))
		}
		output, emitted = value, true
		attempt.Emitted = true
		return nil
	}
	receipt := func() error {
		if !active.Load() {
			return errors.New("native command receipt requires active dispatch")
		}
		if receiptCalled {
			return fail(errors.New("native command receipt already attempted"))
		}
		receiptCalled = true
		if !emitted {
			return fail(errors.New("native command receipt requires successful emission"))
		}
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		invocation := models.CommandInvocation{
			Command:  sanitizeArgs([]string{name}, os.Environ())[0],
			Args:     sanitizeArgs(args, os.Environ()),
			ExitCode: output.ExitCode, ResponseIndex: output.ResponseIndex,
		}
		record := invocationRecord{RecordedAt: time.Now().UTC(), Invocation: invocation}
		data, err := json.Marshal(record)
		if err != nil {
			return fail(fmt.Errorf("encoding native command receipt: %w", err))
		}
		// A unique attempt ID also identifies its receipt. No retry can publish
		// a second receipt or emit output again for this dispatch.
		receiptName := filepath.Base(path)
		if err := publishNativeReceipt(config.LogDir, receiptName, data); err != nil {
			return fail(fmt.Errorf("publishing native command receipt: %w", err))
		}
		attempt.Receipt = receiptName
		return nil
	}
	code, dispatchErr := options.Dispatch(ctx, NativeDispatchRequest{
		StateDir: stateDir, Workspace: config.Workspace, CWD: cwd,
		Name: name, Args: append([]string(nil), args...), Mock: raw,
		Configuration: append(json.RawMessage(nil), mock.NativeConfig...),
	}, emit, receipt)
	active.Store(false)
	resultErr = errors.Join(dispatchErr, callbackErr, ctx.Err())
	if !emitted {
		resultErr = errors.Join(resultErr, errors.New("native command completed without successful emission"))
	}
	if attempt.Receipt == "" {
		resultErr = errors.Join(resultErr, errors.New("native command capture incomplete: missing delivered receipt"))
	}
	if emitted && code != output.ExitCode {
		resultErr = errors.Join(resultErr, errors.New("native command exit code disagrees with delivered output"))
	}
	if resultErr != nil {
		return 1, resultErr
	}
	return code, nil
}

func writeNative(writer io.Writer, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	n, err := writer.Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return nil
}

func publishNativeReceipt(dir, name string, data []byte) (resultErr error) {
	file, err := os.CreateTemp(dir, ".receipt-*")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.Remove(file.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
			resultErr = errors.Join(resultErr, fmt.Errorf("removing unpublished native receipt: %w", err))
		}
	}()
	n, writeErr := file.Write(data)
	if writeErr == nil && n != len(data) {
		writeErr = io.ErrShortWrite
	}
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(dir, name))
}

func parseNativeReceipt(data []byte) (invocationRecord, error) {
	value, err := jsonutil.Parse(data)
	if err != nil {
		return invocationRecord{}, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return invocationRecord{}, errors.New("native receipt must be an object")
	}
	stamp, ok := object["recorded_at"].(string)
	if !ok {
		return invocationRecord{}, errors.New("native receipt requires recorded_at")
	}
	recordedAt, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil || recordedAt.IsZero() {
		return invocationRecord{}, errors.New("native receipt requires valid recorded_at")
	}
	invocation, ok := object["invocation"].(map[string]any)
	if !ok {
		return invocationRecord{}, errors.New("native receipt requires invocation")
	}
	command, ok := invocation["command"].(string)
	if !ok || command == "" {
		return invocationRecord{}, errors.New("native receipt requires command")
	}
	exitToken, ok := invocation["exit_code"].(json.Number)
	if !ok {
		return invocationRecord{}, errors.New("native receipt requires integer exit_code")
	}
	exitCode, err := exitToken.Int64()
	if err != nil || exitCode < 0 || exitCode > 255 {
		return invocationRecord{}, errors.New("native receipt exit_code must be an integer between 0 and 255")
	}
	indexToken, ok := invocation["response_index"].(json.Number)
	if !ok {
		return invocationRecord{}, errors.New("native receipt requires integer response_index")
	}
	index, err := indexToken.Int64()
	if err != nil || index < 0 || int64(int(index)) != index {
		return invocationRecord{}, errors.New("native receipt response_index must be a nonnegative integer")
	}
	var args []string
	if value := invocation["args"]; value != nil {
		values, ok := value.([]any)
		if !ok {
			return invocationRecord{}, errors.New("native receipt args must be an array")
		}
		args = make([]string, len(values))
		for i, value := range values {
			arg, ok := value.(string)
			if !ok {
				return invocationRecord{}, errors.New("native receipt args must contain strings")
			}
			args[i] = arg
		}
	}
	return invocationRecord{RecordedAt: recordedAt, Invocation: models.CommandInvocation{
		Command: command, Args: args, ExitCode: int(exitCode), ResponseIndex: int(index),
	}}, nil
}

func (s *Session) isNativeCommand(name string) bool {
	for command := range s.native {
		if sameCommand(name, sanitizeArgs([]string{command}, os.Environ())[0]) {
			return true
		}
	}
	return false
}

// nativeCapture deliberately refuses to certify exact counts from unreadable,
// unfinished, or lost capture data. Attempts remain distinct from deliveries.
func (s *Session) nativeCapture() ([]models.CommandInvocation, map[string]int, error) {
	counts := make(map[string]int)
	countsComplete := true
	var captureErr error
	if _, err := os.Stat(filepath.Join(s.dir, "native-capture-error")); err == nil {
		captureErr = errors.New("native command capture failed: attempt data may be incomplete")
		countsComplete = false
	} else if !errors.Is(err, os.ErrNotExist) {
		captureErr = fmt.Errorf("checking native capture failure: %w", err)
		countsComplete = false
	}
	receipts := make(map[string]invocationRecord)
	var records []invocationRecord
	entries, err := os.ReadDir(s.logDir)
	if err != nil {
		captureErr = errors.Join(captureErr, fmt.Errorf("reading native receipts: %w", err))
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.logDir, entry.Name()))
		var record invocationRecord
		nativeReceipt := strings.HasPrefix(entry.Name(), "attempt-")
		if err == nil {
			if nativeReceipt {
				record, err = parseNativeReceipt(data)
			} else {
				err = json.Unmarshal(data, &record)
			}
		}
		if err != nil {
			captureErr = errors.Join(captureErr, fmt.Errorf("reading native receipt %q: %w", entry.Name(), err))
			continue
		}
		if !nativeReceipt {
			if s.isNativeCommand(record.Invocation.Command) {
				captureErr = errors.Join(captureErr, errors.New("native command receipt lacks canonical attempt filename"))
				continue
			}
			records = append(records, record)
		} else {
			receipts[entry.Name()] = record
		}
	}
	attempts, err := os.ReadDir(filepath.Join(s.dir, "attempts"))
	if err != nil {
		captureErr = errors.Join(captureErr, fmt.Errorf("reading native attempts: %w", err))
		countsComplete = false
	}
	for _, entry := range attempts {
		data, err := os.ReadFile(filepath.Join(s.dir, "attempts", entry.Name()))
		var attempt nativeAttempt
		if err == nil {
			err = json.Unmarshal(data, &attempt)
		}
		if err != nil {
			captureErr = errors.Join(captureErr, fmt.Errorf("reading native attempt %q: %w", entry.Name(), err))
			countsComplete = false
			continue
		}
		if !s.native[attempt.Command] {
			captureErr = errors.Join(captureErr, errors.New("native command capture contains unknown attempt"))
			countsComplete = false
			continue
		}
		counts[attempt.Command]++
		if !attempt.Done || attempt.Error != "" {
			captureErr = errors.Join(captureErr, errors.New("native command capture incomplete or failed"))
		}
		record, ok := receipts[attempt.Receipt]
		if attempt.Receipt == "" || !ok {
			captureErr = errors.Join(captureErr, errors.New("native command capture incomplete: missing delivered receipt"))
		} else if !attempt.Emitted || attempt.Receipt != entry.Name() || record.Invocation.Command != sanitizeArgs([]string{attempt.Command}, os.Environ())[0] {
			captureErr = errors.Join(captureErr, errors.New("native command capture contains inconsistent receipt"))
		} else {
			records = append(records, record)
		}
		delete(receipts, attempt.Receipt)
	}
	for name := range receipts {
		if strings.HasPrefix(name, "attempt-") {
			captureErr = errors.Join(captureErr, errors.New("native command capture incomplete: missing attempt"))
			countsComplete = false
		}
	}
	sort.SliceStable(records, func(i, j int) bool {
		return records[i].RecordedAt.Before(records[j].RecordedAt)
	})
	invocations := make([]models.CommandInvocation, len(records))
	for i, record := range records {
		invocations[i] = record.Invocation
	}
	if !countsComplete {
		counts = nil
	}
	return invocations, counts, captureErr
}
