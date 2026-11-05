package controlledcomparison

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/graders"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/nativesnapshot"
	"github.com/microsoft/waza/internal/nativetask"
	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/microsoft/waza/internal/rootedfile"
)

type nativeStop struct {
	Kind    string `json:"kind"`
	Version string `json:"version"`
	Reason  string `json:"reason"`
}

var errNativeIncomplete = errors.New("native collection is an incomplete prefix; no resume or repair is permitted")
var errNativeStopped = errors.New("native collection has a retained failure")

// ReadSelectedDecision dispatches only an explicit raw selected policy. Native
// envelopes cannot be admitted by the legacy core-only reader.
func ReadSelectedDecision(ctx context.Context, directory string, selected []byte) (decision releasepolicy.Decision, err error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return releasepolicy.InitialDecision(), err
	}
	defer func() {
		err = errors.Join(err, root.Close(), ctx.Err())
		if err != nil {
			decision.Accepted = false
		}
	}()
	actual, err := nativeArtifact(ctx, root, "policy.json", 16<<20)
	if err != nil {
		return releasepolicy.InitialDecision(), err
	}
	if !bytes.Equal(actual, selected) {
		// Whitespace is not policy identity, but the independently admitted
		// selected digest must still agree with the persisted commitment.
		if IsNativePolicy(selected) {
			left, _, _, err := DecodeNativePolicy(selected)
			if err != nil {
				return releasepolicy.InitialDecision(), err
			}
			right, _, _, err := DecodeNativePolicy(actual)
			if err != nil || right.Digest != left.Digest {
				mismatch := releasepolicy.InitialDecision()
				mismatch.Compatibility.State = "mismatched"
				return mismatch, fmt.Errorf("selected native policy differs from collection")
			}
		} else {
			left, err := releasepolicy.DecodePolicy(selected)
			if err != nil {
				return releasepolicy.InitialDecision(), err
			}
			right, err := releasepolicy.DecodePolicy(actual)
			if err != nil || right.Digest != left.Digest {
				mismatch := releasepolicy.InitialDecision()
				mismatch.Compatibility.State = "mismatched"
				return mismatch, fmt.Errorf("selected policy differs from collection")
			}
		}
	}
	if !IsNativePolicy(actual) {
		policy, err := releasepolicy.DecodePolicy(selected)
		if err != nil {
			return releasepolicy.InitialDecision(), err
		}
		return releasepolicy.ReadSelectedDecisionRootContext(ctx, root, policy.Digest)
	}
	return readDecisionRootContext(ctx, root, actual)
}

func ReadDecisionRootContext(ctx context.Context, root *os.Root) (decision releasepolicy.Decision, err error) {
	raw, err := nativeArtifact(ctx, root, "policy.json", 16<<20)
	if err != nil {
		return releasepolicy.InitialDecision(), err
	}
	return readDecisionRootContext(ctx, root, raw)
}

// readDecisionRootContext assesses the exact policy bytes already admitted by
// the caller. It must not reopen policy.json after selection has been checked.
func readDecisionRootContext(ctx context.Context, root *os.Root, raw []byte) (decision releasepolicy.Decision, err error) {
	if !IsNativePolicy(raw) {
		return releasepolicy.ReadDecisionRootContext(ctx, root)
	}
	decision = releasepolicy.InitialDecision()
	decision.Native = &releasepolicy.NativeDecision{
		Profile: "copilot_sdk_single_turn_text", Version: "1.0", Attempts: []releasepolicy.NativeAttemptView{},
		RuntimeVersion: "unavailable", ProviderCurrency: "unavailable", Assurance: "unsupported",
		ClientToolPolicy: "deny_all_client_policy_not_server_isolation",
		BudgetContract:   "per_arm_final_observed_limits_best_effort_global_start_checks_async_overshoot_possible",
	}
	defer func() {
		err = errors.Join(err, ctx.Err())
		if err != nil {
			decision.Accepted = false
			if !errors.Is(err, errNativeIncomplete) && !errors.Is(err, errNativeStopped) {
				decision.Compatibility.State = "invalid"
				decision.Compatibility.Reasons = append(decision.Compatibility.Reasons, err.Error())
			}
		}
	}()
	native, policy, profile, err := DecodeNativePolicy(raw)
	if err != nil {
		return decision, err
	}
	coreDecision, coreArtifacts, coreErr := releasepolicy.ReadCoreDecisionRetainedRootContext(ctx, root, policy.Digest)
	core, err := coreArtifacts.Artifact("core-policy.json")
	if err != nil {
		return coreDecision, errors.Join(coreErr, err)
	}
	if !bytes.Equal(core, native.Policy) {
		return decision, fmt.Errorf("native envelope and core policy bytes differ")
	}
	captured, err := nativeArtifact(ctx, root, "native-capture.json", nativesnapshot.CollectionWireBytes)
	if err != nil {
		return decision, err
	}
	capture, err := nativesnapshot.ReadCollectionCapture(ctx, captured)
	if err != nil {
		return decision, err
	}
	if err := verifyNativeCapture(ctx, native, policy, capture, captured); err != nil {
		return decision, err
	}
	coreDecision.Native = decision.Native
	decision = coreDecision
	var stopped *nativeStop
	if _, err := root.Lstat("native-stop.json"); err == nil {
		raw, err := nativeArtifact(ctx, root, "native-stop.json", 16<<20)
		if err != nil {
			return decision, err
		}
		if err := models.ValidateNativeJSONKeys(raw, nativeStop{}); err != nil {
			return decision, err
		}
		if _, err := jsonutil.Parse(raw); err != nil {
			return decision, err
		}
		var stop nativeStop
		if err := json.Unmarshal(raw, &stop); err != nil {
			return decision, err
		}
		if stop.Kind != "waza.release-native-stop" || stop.Version != "1.0" || stop.Reason == "" {
			return decision, fmt.Errorf("native stop artifact is invalid")
		}
		stopped = &stop
	} else if !errors.Is(err, os.ErrNotExist) {
		return decision, err
	}
	stream, err := coreArtifacts.Artifact("journal.ndjson")
	if err != nil {
		return decision, errors.Join(coreErr, err)
	}
	retry, err := releasepolicy.InspectRetryPrefix(ctx, native.Policy, stream)
	if err != nil {
		return decision, err
	}
	view, err := retry.View(ctx)
	if err != nil {
		return decision, err
	}
	admissions := []*nativetask.Admitted{}
	keys := []releasepolicy.AttemptKey{}
	coreAttempts := []releasepolicy.AttemptSummary{}
	for _, event := range view.Events {
		if event.Type == "attempt_start" {
			key := *event.Key
			primitive, err := capture.Primitive(ctx, key.Arm, key.TaskID)
			if err != nil {
				return decision, err
			}
			admitted, err := nativetask.Admit(ctx, primitive, profile, key)
			if err != nil {
				return decision, err
			}
			if err := capture.Join(ctx, admitted, key); err != nil {
				return decision, err
			}
			admissions = append(admissions, admitted)
			keys = append(keys, key)
		}
		if event.Type == "attempt_terminal" {
			coreAttempts = append(coreAttempts, *event.Attempt)
		}
	}
	for _, key := range keys {
		decision.Native.Attempts = append(decision.Native.Attempts, unknownNativeAttempt(key))
	}
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		axes := nativeUsage(nil, &arm)
		for i := range axes {
			axes[i].Value, axes[i].Availability, axes[i].Observation = nil, "unavailable", "unknown"
			axes[i].Reason = "Native payload/terminal evidence cannot be verified."
		}
		decision.Usage[arm] = axes
	}
	nativeInfo, err := root.Lstat("native")
	if err != nil {
		return decision, errors.Join(coreErr, err)
	}
	if !nativeInfo.IsDir() || nativeInfo.Mode()&os.ModeSymlink != 0 {
		return decision, fmt.Errorf("native attempt directory is not a regular directory")
	}
	directory, err := root.OpenRoot("native")
	if err != nil {
		return decision, errors.Join(coreErr, err)
	}
	openedNative, statErr := directory.Stat(".")
	if statErr != nil || !os.SameFile(nativeInfo, openedNative) {
		return decision, errors.Join(fmt.Errorf("native attempt directory changed while opening"), statErr, directory.Close())
	}
	events, eventErr := nativeArtifact(ctx, directory, "native-task-events.ndjson", 16<<20)
	rows, rowErr := nativeArtifact(ctx, directory, "native-task-rows.ndjson", 16<<20)
	closeErr := directory.Close()
	if err := errors.Join(eventErr, rowErr, closeErr); err != nil {
		return decision, err
	}
	prefix, err := nativetask.InspectCollectionPrefix(ctx, events, rows, admissions)
	if err != nil {
		return decision, err
	}
	decision.Native.Attempts = []releasepolicy.NativeAttemptView{}
	records := prefix.Records
	if view.CollectionID != profile.Digest.SHA256 || len(coreAttempts) > len(records) {
		return decision, fmt.Errorf("native/core attempt inventory or collection identity differs")
	}
	for i, record := range records {
		if err := releasepolicy.ValidateAttempt(policy, record.Summary); err != nil {
			return decision, fmt.Errorf("native terminal contradicts selected policy: %w", err)
		}
		if err := authorizeNativeStart(policy, view.EvalIDs, records[:i], record.Key); err != nil {
			return decision, err
		}
		if err := checkNativeBudget(policy, records[:i]); err != nil {
			return decision, fmt.Errorf("native attempt violates prestart budget policy: %w", err)
		}
		if i >= len(keys) || record.Key != keys[i] || i < len(coreAttempts) && !reflect.DeepEqual(record.Summary, coreAttempts[i]) {
			return decision, fmt.Errorf("native payload/terminal/core terminal are not reconciled")
		}
		if err := verifyNativeGrades(ctx, capture, record); err != nil {
			return decision, err
		}
		decision.Native.Attempts = append(decision.Native.Attempts, nativeAttemptView(record))
	}
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		decision.Usage[arm] = nativeUsage(records, &arm)
	}
	if repeatedNativeSession(records) {
		return decision, fmt.Errorf("native attempts reused a session identity; authoritative usage is unavailable")
	}
	if len(keys) > len(records) {
		if len(keys) != len(records)+1 {
			return decision, fmt.Errorf("native prefix omits earlier starts")
		}
		if err := authorizeNativeStart(policy, view.EvalIDs, records, keys[len(records)]); err != nil {
			return decision, err
		}
		if err := checkNativeBudget(policy, records); err != nil {
			return decision, err
		}
		key := keys[len(records)]
		decision.Native.Attempts = append(decision.Native.Attempts, unknownNativeAttempt(key))
		for i := range decision.Usage[key.Arm] {
			axis := &decision.Usage[key.Arm][i]
			axis.Value, axis.Availability, axis.Observation = nil, "unavailable", "unknown"
			axis.Reason = "A started native attempt lacks final authoritative usage."
		}
	}
	if prefix.Pending != nil || len(records) != len(admissions) || len(records) != len(coreAttempts) || !view.CollectionEndValidated {
		decision.Accepted = false
		decision.Completeness.State, decision.Operations.State, decision.Statistics.State = "partial", "incomplete", "inconclusive"
		if stopped != nil {
			decision.Operations.Reasons = append(decision.Operations.Reasons, stopped.Reason)
		}
		return decision, errors.Join(errNativeIncomplete, coreErr)
	}
	if stopped != nil {
		decision.Accepted = false
		if decision.Operations.State == "observed" {
			decision.Operations.State = "failed"
		}
		decision.Operations.Reasons = append(decision.Operations.Reasons, stopped.Reason)
		return decision, errNativeStopped
	}
	if coreErr != nil {
		return decision, coreErr
	}
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		finalData, err := coreArtifacts.Artifact(string(arm) + ".final.json")
		if err != nil {
			return decision, err
		}
		final, err := releasepolicy.DecodeReceipt(finalData)
		if err != nil {
			return decision, err
		}
		if !reflect.DeepEqual(final.Usage, nativeUsage(records, &arm)) {
			return decision, fmt.Errorf("final core usage differs from every retained native session")
		}
		resultData, err := coreArtifacts.Artifact(string(arm) + ".results.json")
		if err != nil {
			return decision, err
		}
		if err := verifyNativeRows(resultData, arm, records); err != nil {
			return decision, err
		}
	}
	return decision, nil
}

func unknownNativeAttempt(key releasepolicy.AttemptKey) releasepolicy.NativeAttemptView {
	return releasepolicy.NativeAttemptView{
		Key: key, Status: "incomplete", Category: "operational", Error: "Durable attempt start lacks a verified complete native terminal record.",
		OutputAvailability: "unavailable", EventModels: []string{}, AccountingModels: []string{},
		Lifecycle: map[string]releasepolicy.NativePhaseView{}}
}

func verifyNativeGrades(ctx context.Context, capture *nativesnapshot.CollectionCapture, record nativetask.Record) error {
	if record.Lifecycle.Grade.State != "completed" {
		if len(record.Summary.Checks) != 0 || len(record.ActualRow.Run.Validations) != 0 {
			return fmt.Errorf("native incomplete grading carries observed checks")
		}
		return nil
	}
	if record.Output.Value == nil || record.Output.Availability != "available" {
		return fmt.Errorf("native grading lacks available output")
	}
	spec, task, err := capture.Task(ctx, record.Key.Arm, record.Key.TaskID)
	if err != nil {
		return err
	}
	expected, err := graders.RunAll(ctx, spec.Graders, task, &graders.Context{
		TestCase: task, Output: *record.Output.Value, OutputPresent: true, DurationMS: record.ActualRow.Run.DurationMs}, "", "", false)
	if err != nil {
		return fmt.Errorf("verifying native retained text grades: %w", err)
	}
	if len(expected) != len(record.ActualRow.Run.Validations) || len(expected) != len(record.Summary.Checks) {
		return fmt.Errorf("native grade inventory differs from retained declarations")
	}
	scopes := map[string]string{}
	for _, g := range spec.Graders {
		scopes[g.Identifier] = "eval"
	}
	for _, g := range task.Validators {
		scopes[g.Identifier] = "task"
	}
	for _, check := range record.Summary.Checks {
		want, exists := expected[check.Grader]
		got := record.ActualRow.Run.Validations[check.Grader]
		if !exists || check.Scope != scopes[check.Grader] {
			return fmt.Errorf("native scoped check differs from retained declaration")
		}
		want.DurationMs, got.DurationMs = 0, 0
		left, err := evidence.JSONDigest(want)
		if err != nil {
			return err
		}
		right, err := evidence.JSONDigest(got)
		if err != nil || *left != *right || got.Type != models.GraderKindText {
			return fmt.Errorf("native retained text grade differs from available output")
		}
	}
	return nil
}

func nativeAttemptView(record nativetask.Record) releasepolicy.NativeAttemptView {
	view := releasepolicy.NativeAttemptView{Key: record.Key, Status: record.Summary.Status,
		Category: record.Summary.Category, Error: record.ActualRow.Run.ErrorMsg,
		OutputAvailability: record.Output.Availability, Output: record.Output.Value,
		EventModels: record.EventModels.Models, AccountingModels: record.Accounting.Models, UsageComplete: record.Accounting.Complete,
		Lifecycle: map[string]releasepolicy.NativePhaseView{}}
	for name, phase := range map[string]nativetask.Phase{"construction": record.Lifecycle.Construction, "initialize": record.Lifecycle.Initialize,
		"execute": record.Lifecycle.Execute, "grade": record.Lifecycle.Grade, "shutdown": record.Lifecycle.Shutdown} {
		view.Lifecycle[name] = releasepolicy.NativePhaseView{State: phase.State, Reason: phase.Reason}
	}
	return view
}

func verifyNativeRows(raw []byte, arm releasepolicy.Arm, records []nativetask.Record) error {
	value, err := jsonutil.Parse(raw)
	if err != nil {
		return err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("native results must be an object")
	}
	rows, ok := object["rows"].([]any)
	if !ok {
		return fmt.Errorf("native results must contain actual rows")
	}
	next := 0
	for _, record := range records {
		if record.Key.Arm != arm {
			continue
		}
		if next >= len(rows) {
			return fmt.Errorf("native/core actual row count differs")
		}
		got, err := evidence.JSONDigest(rows[next])
		if err != nil {
			return err
		}
		want, err := evidence.JSONDigest(record.ActualRow)
		if err != nil || got == nil || want == nil || *got != *want {
			return fmt.Errorf("native/core full actual row differs")
		}
		next++
	}
	if next != len(rows) {
		return fmt.Errorf("extra core actual rows are not native attempts")
	}
	return nil
}

func nativeArtifact(ctx context.Context, root *os.Root, name string, maximum int) ([]byte, error) {
	return nativeArtifactWithOpener(ctx, root, name, maximum, rootedfile.Open)
}

func nativeArtifactWithOpener(ctx context.Context, root *os.Root, name string, maximum int, open func(*os.Root, string) (*os.File, error)) (_ []byte, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if root == nil || name == "." || filepath.Base(name) != name || maximum < 1 || maximum > nativesnapshot.CollectionWireBytes {
		return nil, fmt.Errorf("invalid bounded native artifact read")
	}
	before, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("native artifact %s is not a regular file", name)
	}
	if before.Size() > int64(maximum) {
		return nil, fmt.Errorf("native artifact %s exceeds byte limit", name)
	}
	file, err := open(root, name)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, file.Close(), ctx.Err()) }()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, opened) || !opened.Mode().IsRegular() || opened.Size() > int64(maximum) {
		return nil, fmt.Errorf("native artifact %s changed or exceeds byte limit", name)
	}
	reader := io.LimitReader(file, int64(maximum)+1)
	raw := make([]byte, 0, min(maximum, 64<<10))
	buffer := make([]byte, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := reader.Read(buffer)
		raw = append(raw, buffer[:n]...)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return nil, err
			}
			break
		}
	}
	if len(raw) > maximum {
		return nil, fmt.Errorf("native artifact %s exceeds byte limit", name)
	}
	retained, err := file.Stat()
	if err != nil {
		return nil, err
	}
	after, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) || before.Size() != after.Size() ||
		!before.ModTime().Equal(after.ModTime()) || retained.Size() != opened.Size() ||
		!retained.ModTime().Equal(opened.ModTime()) || len(raw) != int(retained.Size()) {
		return nil, fmt.Errorf("native artifact %s changed while reading", name)
	}
	return raw, nil
}
