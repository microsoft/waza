package controlledcomparison

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	copilot "github.com/github/copilot-sdk/go"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/graders"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/nativetask"
	"github.com/microsoft/waza/internal/releasepolicy"
)

// CollectNative is the paid-capable, explicitly selected public producer.
// Ordinary Collect remains mock-only; no result/cache/resume adapter is accepted.
func CollectNative(ctx context.Context, policyData []byte, baseline, candidate Source, directory string) error {
	return collectNative(ctx, policyData, baseline, candidate, directory, nil)
}

// NativeCollectorOptions supports the same SDK client factory seam as the
// execution engine. A supplied client does not establish authenticated evidence.
type NativeCollectorOptions struct {
	NewCopilotClient func(*copilot.ClientOptions) execution.CopilotClient
}

func CollectNativeWithOptions(ctx context.Context, policyData []byte, baseline, candidate Source, directory string, options NativeCollectorOptions) error {
	return collectNative(ctx, policyData, baseline, candidate, directory, options.NewCopilotClient)
}

func collectNative(ctx context.Context, policyData []byte, baseline, candidate Source, directory string,
	newClient func(*copilot.ClientOptions) execution.CopilotClient) (err error) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return fmt.Errorf("native durable collection is unsupported on %s; collection requires Linux or macOS", runtime.GOOS)
	}
	native, policy, profile, err := DecodeNativePolicy(policyData)
	if err != nil {
		return err
	}
	if err := nativeEnvironment(); err != nil {
		return err
	}
	capture, closeCapture, err := captureNative(ctx, baseline, candidate)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, closeCapture()) }()
	captureData, err := capture.Marshal(ctx)
	if err != nil {
		return err
	}
	if err := verifyNativeCapture(ctx, native, policy, capture, captureData); err != nil {
		return err
	}
	if err := capture.Recheck(ctx); err != nil {
		return err
	}
	var tape *nativetask.Tape
	var retainedRoot *os.Root
	defer func() {
		err = errors.Join(err, tape.Close())
		if retainedRoot == nil {
			return
		}
		if err != nil {
			raw, encodeErr := json.Marshal(nativeStop{Kind: "waza.release-native-stop", Version: "1.0", Reason: err.Error()})
			if encodeErr == nil {
				encodeErr = releasepolicy.WriteCollectionArtifact(retainedRoot, "native-stop.json", raw)
			}
			err = errors.Join(err, encodeErr)
		}
		err = errors.Join(err, retainedRoot.Close())
	}()
	records := []nativetask.Record{}
	admitted := map[releasepolicy.AttemptKey]*nativetask.Admitted{}
	evalIDs := map[releasepolicy.Arm]string{}
	var pending *nativetask.Record
	collector := releasepolicy.Collector{
		PolicyEnvelope: policyData, CollectionID: profile.Digest.SHA256,
		Initialize: func(context.Context) error { return nil },
		Shutdown:   func(context.Context) error { return nil },
		Open: func(ctx context.Context, root *os.Root, _ string, ids map[releasepolicy.Arm]string) error {
			evalIDs = ids
			var err error
			retainedRoot, err = root.OpenRoot(".")
			if err != nil {
				return err
			}
			if err := releasepolicy.WriteCollectionArtifact(root, "native-capture.json", captureData); err != nil {
				return err
			}
			tape, err = nativetask.CreateCollectionTape(ctx, root, "native")
			return err
		},
		BeforeAttempt: func(ctx context.Context, key releasepolicy.AttemptKey) error {
			if err := nativeEnvironment(); err != nil {
				return err
			}
			if err := capture.Recheck(ctx); err != nil {
				return err
			}
			if err := authorizeNativeStart(policy, evalIDs, records, key); err != nil {
				return err
			}
			return checkNativeBudget(policy, records)
		},
		Attempt: func(ctx context.Context, key releasepolicy.AttemptKey) (releasepolicy.AttemptObservation, error) {
			primitive, err := capture.Primitive(ctx, key.Arm, key.TaskID)
			if err != nil {
				return releasepolicy.AttemptObservation{}, err
			}
			slot, err := nativetask.Admit(ctx, primitive, profile, key)
			if err != nil {
				return releasepolicy.AttemptObservation{}, err
			}
			if err := capture.Join(ctx, slot, key); err != nil {
				return releasepolicy.AttemptObservation{}, err
			}
			if err := tape.Start(ctx, slot); err != nil {
				return releasepolicy.AttemptObservation{}, err
			}
			admitted[key] = slot
			request, err := capture.Request(ctx, key.Arm, key.TaskID)
			if err != nil {
				return releasepolicy.AttemptObservation{}, err
			}
			spec, task, err := capture.Task(ctx, key.Arm, key.TaskID)
			if err != nil {
				return releasepolicy.AttemptObservation{}, err
			}
			record, err := executeNativeAttempt(ctx, slot, request, spec, task, newClient)
			if err != nil {
				return releasepolicy.AttemptObservation{}, err
			}
			pending = record
			return releasepolicy.AttemptObservation{Summary: record.Summary, Result: record.ActualRow}, nil
		},
		PersistAttempt: func(ctx context.Context, observation releasepolicy.AttemptObservation) error {
			if pending == nil || !reflect.DeepEqual(pending.Summary, observation.Summary) ||
				!reflect.DeepEqual(pending.ActualRow, observation.Result) {
				return fmt.Errorf("native attempt changed before persistence")
			}
			raw, err := json.Marshal(pending)
			if err != nil {
				return err
			}
			if _, err := nativetask.DecodeRecord(raw, admitted[pending.Key]); err != nil {
				return fmt.Errorf("admitting owned native record: %w", err)
			}
			if err := tape.Complete(ctx, raw); err != nil {
				return err
			}
			records = append(records, *pending)
			pending = nil
			if repeatedNativeSession(records) {
				return fmt.Errorf("native attempts reused an observed session identity; complete payloads retained but accounting is unavailable")
			}
			return nil
		},
		Usage: func(_ context.Context, arm releasepolicy.Arm) ([]releasepolicy.UsageAxis, error) {
			return nativeUsage(records, &arm), nil
		},
		BeforePublish: func(ctx context.Context) error {
			if err := tape.Close(); err != nil {
				return err
			}
			return capture.Recheck(ctx)
		},
	}
	if err := releasepolicy.Collect(ctx, native.Policy, directory, collector); err != nil {
		return err
	}
	for _, record := range records {
		if record.Summary.Category != "behavioral" {
			return fmt.Errorf("native collection retained operationally incomplete attempts; inspect lifecycle evidence before reassessment")
		}
	}
	return nil
}

// The scheduler and the independent admitted-record state must agree. Static
// possible slots and imported retry prefixes are never execution permission.
func authorizeNativeStart(policy *releasepolicy.Policy, evalIDs map[releasepolicy.Arm]string, records []nativetask.Record, key releasepolicy.AttemptKey) error {
	next := 0
	for ci, cluster := range policy.Design.Clusters {
		for _, arm := range policy.Design.Allocation.Assignments[ci].Order {
			for _, task := range cluster.Tasks {
				maximum := 0
				for _, planned := range policy.Arms[arm].Plan.Tasks {
					if planned.ID == task.ID {
						maximum = planned.Settings.MaxAttempts
					}
				}
				for _, trial := range task.TrialOrdinals {
					for attempt := 1; attempt <= maximum; attempt++ {
						expected := releasepolicy.AttemptKey{SampleKey: releasepolicy.SampleKey{
							ClusterID: cluster.ID, TaskID: task.ID, Trial: trial}, Arm: arm, EvalID: evalIDs[arm], Attempt: attempt}
						if next == len(records) {
							if expected != key {
								return fmt.Errorf("native start is not authorized by frozen allocation and accepted attempts")
							}
							return nil
						}
						record := records[next]
						if record.Key != expected {
							return fmt.Errorf("retained native attempts contradict frozen allocation")
						}
						next++
						if record.Summary.Status == "passed" || record.Summary.Category != "behavioral" {
							break
						}
					}
				}
			}
		}
	}
	return fmt.Errorf("native allocation is exhausted")
}

func checkNativeBudget(policy *releasepolicy.Policy, records []nativetask.Record) error {
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		if err := checkNativeArmBudget(policy, records, arm); err != nil {
			return err
		}
	}
	return nil
}

func checkNativeArmBudget(policy *releasepolicy.Policy, records []nativetask.Record, arm releasepolicy.Arm) error {
	axes := nativeUsage(records, &arm)
	for _, required := range policy.Requirements.Billing {
		for _, observed := range axes {
			if observed.Axis != required.Axis {
				continue
			}
			if observed.Value == nil {
				return fmt.Errorf("required prior %s usage is unavailable; no further native attempt may start", required.Axis)
			}
			value, ok := new(big.Rat).SetString(observed.Value.String())
			limit, limitOK := new(big.Rat).SetString(required.Maximum.String())
			if !ok || !limitOK {
				return fmt.Errorf("invalid exact native budget observation")
			}
			if value.Cmp(limit) >= 0 {
				return fmt.Errorf("observed %s budget is reached/exceeded; no further native attempt may start (in-flight overshoot is possible)", required.Axis)
			}
		}
	}
	return nil
}

func nativeUsage(records []nativetask.Record, arm *releasepolicy.Arm) []releasepolicy.UsageAxis {
	complete := !repeatedNativeSession(records)
	input, output, credits := new(big.Rat), new(big.Rat), new(big.Rat)
	sessions := map[models.EvidenceDigest]bool{}
	for _, record := range records {
		if arm != nil && record.Key.Arm != *arm {
			continue
		}
		account := record.Accounting
		if !account.Complete || account.SessionKey == nil || account.Usage == nil || account.Usage.AICredits == nil ||
			sessions[*account.SessionKey] {
			complete = false
			continue
		}
		sessions[*account.SessionKey] = true
		input.Add(input, new(big.Rat).SetInt64(int64(account.Usage.InputTokens)))
		output.Add(output, new(big.Rat).SetInt64(int64(account.Usage.OutputTokens)))
		credit, ok := new(big.Rat).SetString(strconv.FormatFloat(*account.Usage.AICredits, 'g', -1, 64))
		if !ok {
			complete = false
			continue
		}
		credits.Add(credits, credit)
	}
	result := []releasepolicy.UsageAxis{}
	for _, item := range []struct {
		name  string
		value *big.Rat
	}{{"input_tokens", input}, {"output_tokens", output}, {"ai_credits", credits}} {
		axis := releasepolicy.UsageAxis{Axis: item.name, Availability: "unavailable", Observation: "unknown",
			Reason: "One or more fresh attempts lack complete final authoritative session usage."}
		if complete {
			// Input observations are integers or finite decimal tokens; summing
			// their rational values introduces no presentation rounding.
			value := json.Number(exactDecimal(item.value))
			axis.Availability, axis.Observation, axis.Value, axis.Reason = "available", "final_complete_attributable", &value, ""
		}
		result = append(result, axis)
	}
	return result
}

func repeatedNativeSession(records []nativetask.Record) bool {
	seen := map[models.EvidenceDigest]bool{}
	for _, record := range records {
		session := record.Accounting.SessionKey
		if record.Response != nil && record.Response.SessionKey != nil {
			session = record.Response.SessionKey
		}
		if session == nil {
			continue
		}
		if seen[*session] {
			return true
		}
		seen[*session] = true
	}
	return false
}

func exactDecimal(value *big.Rat) string {
	if value.IsInt() {
		return value.Num().String()
	}
	denominator := new(big.Int).Set(value.Denom())
	two, five := big.NewInt(2), big.NewInt(5)
	twos, fives := 0, 0
	for new(big.Int).Mod(denominator, two).Sign() == 0 {
		denominator.Quo(denominator, two)
		twos++
	}
	for new(big.Int).Mod(denominator, five).Sign() == 0 {
		denominator.Quo(denominator, five)
		fives++
	}
	return value.FloatString(max(twos, fives))
}

func executeNativeAttempt(ctx context.Context, admitted *nativetask.Admitted, request *execution.ExecutionRequest,
	spec *models.EvalSpec, task *models.TestCase, newClient func(*copilot.ClientOptions) execution.CopilotClient) (*nativetask.Record, error) {
	binding, err := admitted.Binding()
	if err != nil {
		return nil, err
	}
	key := binding.Key
	origin := models.EvidenceOrigin{EvalID: key.EvalID, TaskID: key.TaskID, RunNumber: key.Trial, AttemptCount: key.Attempt}
	notAttempted := nativetask.Phase{State: "not_attempted", Reason: "Prior lifecycle prerequisite did not complete."}
	record := &nativetask.Record{Kind: "waza.release-native-task-attempt", Version: "2.0",
		CollectionID: binding.CollectionID, PolicyDigest: binding.PolicyDigest, ProfileDigest: binding.ProfileDigest,
		PlanDigest: binding.PlanDigest, RequestDigest: binding.RequestDigest, Key: key, Origin: origin,
		Lifecycle: nativetask.Lifecycle{Construction: nativetask.Phase{State: "completed"},
			Initialize: notAttempted, Execute: notAttempted, Grade: notAttempted, Shutdown: notAttempted},
		Output:      nativetask.Output{Availability: "unavailable", Reason: "No unambiguous output observation.", Messages: []nativetask.Message{}},
		Diagnostics: []nativetask.Diagnostic{}, EventModels: nativetask.EventModels{Models: []string{}},
		Accounting: nativetask.Accounting{Source: "none", Models: []string{}, ProviderCurrencyState: "unavailable",
			Reason: "No final authoritative session usage observation."},
		Summary: releasepolicy.AttemptSummary{Key: key, Origin: origin, Status: "incomplete", Category: "operational",
			Checks: []releasepolicy.CheckSummary{}, References: []models.EvidenceReference{},
			Runtime: releasepolicy.RuntimeObservation{TaskID: key.TaskID, RequestedEngine: "copilot-sdk",
				RequestedModel: request.ModelID, RequestedReasoning: request.ReasoningEffort, Availability: "unavailable",
				Reason: "SDK event/usage model labels do not establish engine or provider model versions."}},
		ActualRow: releasepolicy.ActualRunRow{Origin: origin, Run: models.RunResult{RunNumber: key.Trial, Attempts: key.Attempt,
			Prompt: request.Message, Status: models.StatusError, Validations: map[string]models.GraderResults{},
			SessionDigest: models.SessionDigest{ToolsUsed: []string{}, Errors: []string{}}}},
	}
	var mu sync.Mutex
	diagnostics := []execution.ExecutionDiagnostic{}
	engine := execution.NewCopilotEngineBuilder(request.ModelID, &execution.CopilotEngineBuilderOptions{
		OwnClient: true, DisableCommandMocks: true, NewCopilotClient: newClient, DiagnosticObserver: func(d execution.ExecutionDiagnostic) error {
			mu.Lock()
			defer mu.Unlock()
			diagnostics = append(diagnostics, d)
			return nil
		},
	}).Build()
	timeout := spec.Config.TimeoutSec
	if task.TimeoutSec != nil {
		timeout = *task.TimeoutSec
	}
	attemptCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	initErr := engine.Initialize(attemptCtx)
	record.Lifecycle.Initialize = nativePhase(attemptCtx, initErr)
	var response *execution.ExecutionResponse
	var executeErr, gradeErr error
	if initErr == nil && attemptCtx.Err() == nil {
		// The profile owns fresh sessions; caller declarations cannot request
		// retention or reuse of the remote session.
		ownedRequest := *request
		ownedRequest.EphemeralSession = true
		response, executeErr = engine.Execute(attemptCtx, &ownedRequest)
		record.Lifecycle.Execute = nativePhase(attemptCtx, executeErr)
	}
	if response != nil {
		record.ActualRow.Run.DurationMs = response.DurationMs
		if err := preserveNativeResponse(record, response); err != nil {
			executeErr = errors.Join(executeErr, err)
			record.Lifecycle.Execute = nativePhase(attemptCtx, executeErr)
		}
	}
	if record.Output.Availability == "available" && executeErr == nil && response != nil && response.Success && attemptCtx.Err() == nil {
		validations, err := graders.RunAll(attemptCtx, spec.Graders, task, &graders.Context{
			TestCase: task, Output: *record.Output.Value, OutputPresent: true, DurationMS: response.DurationMs}, "", "", false)
		gradeErr = err
		record.Lifecycle.Grade = nativePhase(attemptCtx, gradeErr)
		if gradeErr == nil {
			record.ActualRow.Run.Validations = validations
			names := make([]string, 0, len(validations))
			for name := range validations {
				names = append(names, name)
			}
			slices.Sort(names)
			for _, name := range names {
				grade := validations[name]
				scope := "eval"
				for _, validator := range task.Validators {
					if validator.Identifier == name {
						scope = "task"
					}
				}
				record.Summary.Checks = append(record.Summary.Checks, releasepolicy.CheckSummary{Scope: scope,
					Grader: name, Passed: grade.Passed, Score: releaseNumber(grade.Score), OperationalState: "observed"})
			}
		}
	}
	finalizeCtx, stopFinalize := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	shutdownErr := engine.Shutdown(finalizeCtx)
	record.Lifecycle.Shutdown = nativePhase(finalizeCtx, shutdownErr)
	stopFinalize()
	mu.Lock()
	observedDiagnostics := slices.Clone(diagnostics)
	mu.Unlock()
	sessionID := ""
	if response != nil {
		sessionID = response.SessionID
	}
	for _, d := range observedDiagnostics {
		if sessionID == "" {
			sessionID = d.SessionID
		}
		var session *models.EvidenceDigest
		if d.SessionID != "" {
			session, err = evidence.JSONDigest(d.SessionID)
			if err != nil {
				return nil, err
			}
		}
		record.Diagnostics = append(record.Diagnostics, nativetask.Diagnostic{Stage: string(d.Stage), Code: string(d.Code), SessionKey: session})
	}
	if sessionID != "" {
		session, err := evidence.JSONDigest(sessionID)
		if err != nil {
			return nil, err
		}
		observed := engine.SessionUsageObservation(sessionID)
		events := engine.SessionEventModelObservation(sessionID)
		record.EventModels = nativetask.EventModels{Models: nonnilStrings(events.Models), EventsObserved: events.EventsObserved, Complete: events.Complete}
		usage := engine.SessionUsage(sessionID)
		source := observed.Source
		if source == "" {
			source = "none"
		}
		if source != "none" {
			record.Accounting = nativetask.Accounting{SessionKey: session, Source: source,
				Complete:                 observed.Complete && observed.ModelAttributionComplete && shutdownErr == nil,
				ModelAttributionComplete: observed.ModelAttributionComplete, Models: nonnilStrings(observed.Models), Usage: usage,
				ProviderCurrencyState: "unavailable", Reason: "Final SDK usage; no currency conversion or requested-model inference."}
		}
	}
	operationErr := errors.Join(initErr, executeErr, gradeErr, shutdownErr, ctx.Err())
	if response != nil && response.ErrorMsg != "" {
		operationErr = errors.Join(errors.New(response.ErrorMsg), operationErr)
	}
	for _, model := range append(slices.Clone(record.EventModels.Models), record.Accounting.Models...) {
		if model != request.ModelID {
			operationErr = errors.Join(operationErr, fmt.Errorf("observed SDK model differs from fixed requested model"))
		}
	}
	if len(record.Diagnostics) > 0 {
		operationErr = errors.Join(operationErr, fmt.Errorf("native lifecycle diagnostics prevent a behavioral observation"))
	}
	if record.Output.Availability != "available" || response == nil || !response.Success ||
		record.Lifecycle.Grade.State != "completed" {
		operationErr = errors.Join(operationErr, fmt.Errorf("native execution/output/grading evidence is unavailable"))
	}
	if operationErr != nil {
		record.ActualRow.Run.ErrorMsg = operationErr.Error()
	} else {
		record.Summary.Category, record.Summary.Status, record.ActualRow.Run.Status = "behavioral", "passed", models.StatusPassed
		for _, check := range record.Summary.Checks {
			if !check.Passed {
				record.Summary.Status, record.ActualRow.Run.Status = "failed", models.StatusFailed
			}
		}
	}
	return record, nil
}

func nonnilStrings(values []string) []string {
	return append([]string{}, values...)
}

func nativePhase(ctx context.Context, err error) nativetask.Phase {
	if err == nil && ctx.Err() == nil {
		return nativetask.Phase{State: "completed"}
	}
	state := "failed"
	if ctx.Err() != nil {
		state = "cancelled" //nolint:misspell // Native lifecycle wire spelling.
	}
	return nativetask.Phase{State: state, Reason: errors.Join(err, ctx.Err()).Error()}
}

func preserveNativeResponse(record *nativetask.Record, response *execution.ExecutionResponse) error {
	var session *models.EvidenceDigest
	var err error
	reason := "SDK response did not expose a session identity."
	if response.SessionID != "" {
		session, err = evidence.JSONDigest(response.SessionID)
		if err != nil {
			return err
		}
		reason = ""
	}
	native := &nativetask.Response{SessionKey: session, SessionReason: reason, Success: response.Success,
		ErrorMsg: response.ErrorMsg, FinalOutput: response.FinalOutput, DurationMS: response.DurationMs,
		CanonicalEvents: []json.RawMessage{}, ToolPolicyMode: response.ToolPolicyMode,
		ToolPolicyDenials: []models.ToolPolicyDenial{},
		UsageIsCumulative: response.UsageIsCumulative, UsageRevision: response.UsageRevision, Usage: response.Usage}
	for _, denial := range response.ToolPolicyDenials {
		native.ToolPolicyDenials = append(native.ToolPolicyDenials, models.ToolPolicyDenial{Tool: denial.Tool, Kind: denial.Kind, Reason: denial.Reason})
	}
	usable := true
	var combined strings.Builder
	for i, event := range response.Events {
		raw, err := json.Marshal(event.Raw())
		if err != nil {
			return fmt.Errorf("retaining native event: %w", err)
		}
		value, err := jsonutil.Parse(raw)
		if err != nil {
			return err
		}
		redactNativeSessionIDs(value)
		raw, err = json.Marshal(value)
		if err != nil {
			return err
		}
		native.CanonicalEvents = append(native.CanonicalEvents, raw)
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("native event is not an object")
		}
		if object["type"] != "assistant.message" {
			continue
		}
		data, ok := object["data"].(map[string]any)
		if !ok {
			return fmt.Errorf("native assistant event data is unavailable")
		}
		content, typed := data["content"].(string)
		presence := "typed_nonempty"
		if !typed || content == "" {
			presence, usable = "unknown", false
		}
		combined.WriteString(content)
		record.Output.Messages = append(record.Output.Messages, nativetask.Message{EventOrdinal: i + 1, Content: content, ContentPresence: presence})
	}
	record.Response = native
	if combined.String() != response.FinalOutput {
		return fmt.Errorf("native response output differs from contributing event messages")
	}
	if usable && len(record.Output.Messages) > 0 {
		output := response.FinalOutput
		record.Output.Availability, record.Output.Value, record.Output.Reason = "available", &output, ""
		record.ActualRow.Run.FinalOutput = output
	}
	return nil
}

func redactNativeSessionIDs(value any) {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			normalized := strings.Map(func(r rune) rune {
				if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
					return r
				}
				return -1
			}, key)
			if strings.EqualFold(normalized, "sessionid") {
				delete(value, key)
			} else {
				redactNativeSessionIDs(child)
			}
		}
	case []any:
		for _, child := range value {
			redactNativeSessionIDs(child)
		}
	}
}
