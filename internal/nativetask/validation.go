package nativetask

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"

	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/releasepolicy"
)

func decodeExact(raw []byte, target any) error {
	if len(raw) > 16<<20 {
		return fmt.Errorf("native document exceeds 16 MiB")
	}
	value, err := jsonutil.Parse(raw)
	if err != nil {
		return err
	}
	if err := checkShape(value, reflect.TypeOf(target).Elem()); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decoding native document: %w", err)
	}
	return nil
}

func checkShape(value any, typ reflect.Type) error {
	if typ.Kind() == reflect.Pointer {
		if value == nil {
			return nil
		}
		return checkShape(value, typ.Elem())
	}
	if typ == reflect.TypeFor[json.RawMessage]() {
		if _, ok := value.(map[string]any); !ok {
			return fmt.Errorf("canonical event must be a nonnull object")
		}
		return nil
	}
	if typ == reflect.TypeFor[json.Number]() {
		if _, ok := value.(json.Number); !ok {
			return fmt.Errorf("native number must be a JSON number")
		}
		return nil
	}
	switch typ.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("native object has wrong or null type")
		}
		fields := map[string]reflect.StructField{}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if f.Anonymous && f.Tag.Get("json") == "" {
				for j := 0; j < f.Type.NumField(); j++ {
					child := f.Type.Field(j)
					fields[strings.Split(child.Tag.Get("json"), ",")[0]] = child
				}
			} else if name := strings.Split(f.Tag.Get("json"), ",")[0]; name != "" && name != "-" {
				fields[name] = f
			}
		}
		for name, field := range fields {
			child, exists := object[name]
			if !exists {
				if !strings.Contains(field.Tag.Get("json"), ",omitempty") {
					return fmt.Errorf("native field %s is missing", name)
				}
				continue
			}
			if err := checkShape(child, field.Type); err != nil {
				return fmt.Errorf("native field %s: %w", name, err)
			}
		}
		for name := range object {
			if _, exists := fields[name]; !exists {
				return fmt.Errorf("unknown native field or alias %s", name)
			}
		}
	case reflect.Slice:
		array, ok := value.([]any)
		if !ok {
			return fmt.Errorf("native array has wrong or null type")
		}
		for _, child := range array {
			if err := checkShape(child, typ.Elem()); err != nil {
				return err
			}
		}
	case reflect.Map:
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("native map has wrong or null type")
		}
		for _, child := range object {
			if err := checkShape(child, typ.Elem()); err != nil {
				return err
			}
		}
	case reflect.String:
		if _, ok := value.(string); !ok {
			return fmt.Errorf("native string has wrong or null type")
		}
	case reflect.Bool:
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("native boolean has wrong or null type")
		}
	case reflect.Int, reflect.Int64, reflect.Uint64, reflect.Float64:
		if _, ok := value.(json.Number); !ok {
			return fmt.Errorf("native numeric field has wrong or null type")
		}
	case reflect.Interface:
	default:
		return fmt.Errorf("unsupported native field type %s", typ)
	}
	return nil
}

// DecodeRecord validates supplied draft data; success is not producer admission.
// In particular, no observed runtime, review, billing or assurance is inferred.
func DecodeRecord(raw []byte, admitted *Admitted) (*Record, error) {
	binding, err := admitted.Binding()
	if err != nil {
		return nil, err
	}
	var r Record
	if err := decodeExact(raw, &r); err != nil {
		return nil, err
	}
	expected := Admission{r.CollectionID, r.PolicyDigest, r.ProfileDigest, r.PlanDigest, r.RequestDigest, r.Key}
	if r.Kind != recordKind || r.Version != version || expected != binding {
		return nil, fmt.Errorf("native record binding differs from admission")
	}
	origin := models.EvidenceOrigin{EvalID: r.Key.EvalID, TaskID: r.Key.TaskID, RunNumber: r.Key.Trial, AttemptCount: r.Key.Attempt}
	if r.Origin != origin || r.ActualRow.Origin != origin || r.Summary.Origin != origin || r.Summary.Key != r.Key {
		return nil, fmt.Errorf("native record origin differs from allocated key")
	}
	data, err := admitted.prepared.view()
	if err != nil {
		return nil, err
	}
	if err := releasepolicy.VerifyRunRow(r.Summary, r.ActualRow); err != nil {
		return nil, err
	}
	sessionDigest := r.ActualRow.Run.SessionDigest
	if r.ActualRow.Run.Prompt != data.Request.Message || sessionDigest.SessionID != "" ||
		sessionDigest.Usage != nil || sessionDigest.ToolCallCount != 0 || len(sessionDigest.ToolsUsed) != 0 ||
		len(sessionDigest.ToolCalls) != 0 || len(sessionDigest.Errors) != 0 ||
		sessionDigest.ToolPolicyMode != "" || len(sessionDigest.ToolPolicyDenials) != 0 ||
		len(r.ActualRow.Run.GraderSessions) != 0 || r.ActualRow.Run.Usage != nil {
		return nil, fmt.Errorf("native row contains mismatched prompt or manufactured sessions")
	}
	run := r.ActualRow.Run
	if run.Evidence != nil || run.SnapshotPath != "" || run.WorkspaceDir != "" || run.Responder != nil ||
		len(run.Checkpoints) != 0 || len(run.SkillInvocations) != 0 || len(run.ToolEvents) != 0 ||
		len(run.CommandInvocations) != 0 || len(r.Summary.References) != 0 {
		return nil, fmt.Errorf("native text primitive cannot certify extra capture/dependency evidence")
	}
	if r.Summary.Runtime.Availability != "unavailable" || r.Summary.Runtime.ModelVersion != "" ||
		r.Summary.Runtime.EngineImplementation != "" || r.Summary.Runtime.Reason == "" {
		return nil, fmt.Errorf("native text primitive cannot infer actual runtime versions")
	}
	phases := []Phase{r.Lifecycle.Construction, r.Lifecycle.Initialize, r.Lifecycle.Execute, r.Lifecycle.Grade, r.Lifecycle.Shutdown}
	for _, phase := range phases {
		if !slices.Contains([]string{"completed", "not_attempted", "failed", "cancelled"}, phase.State) || //nolint:misspell // Reviewed draft protocol spelling.
			(phase.State != "completed" && phase.Reason == "") || (phase.State == "completed" && phase.Reason != "") {
			return nil, fmt.Errorf("native lifecycle phase is ambiguous")
		}
	}
	if r.Lifecycle.Initialize.State != "not_attempted" && r.Lifecycle.Construction.State != "completed" ||
		r.Lifecycle.Execute.State != "not_attempted" && r.Lifecycle.Initialize.State != "completed" ||
		r.Lifecycle.Grade.State != "not_attempted" && r.Lifecycle.Execute.State != "completed" {
		return nil, fmt.Errorf("native lifecycle phases are out of order")
	}
	if err := validateOutput(&r, raw); err != nil {
		return nil, err
	}
	if r.Response != nil && !slices.Contains([]string{"completed", "failed", "cancelled"}, r.Lifecycle.Execute.State) || //nolint:misspell // Reviewed draft protocol spelling.
		r.Response == nil && r.Lifecycle.Execute.State == "completed" ||
		r.Output.Availability == "unavailable" && r.Lifecycle.Grade.State != "not_attempted" {
		return nil, fmt.Errorf("native response/output contradicts execution/grading lifecycle")
	}
	var session *models.EvidenceDigest
	if r.Response != nil {
		if r.Response.DurationMS < 0 || r.Response.DurationMS != r.ActualRow.Run.DurationMs {
			return nil, fmt.Errorf("native response duration differs from row")
		}
		session = r.Response.SessionKey
		if (session == nil) != (r.Response.SessionReason != "") {
			return nil, fmt.Errorf("native response identity availability is ambiguous")
		}
		if err := validateUsage(r.Response.Usage); err != nil {
			return nil, err
		}
	}
	ids := map[models.EvidenceDigest]bool{}
	for _, key := range []*models.EvidenceDigest{session, r.Accounting.SessionKey} {
		if key != nil {
			ids[*key] = true
		}
	}
	for _, d := range r.Diagnostics {
		if !validDiagnostic(d) {
			return nil, fmt.Errorf("native diagnostic is not allowlisted")
		}
		if d.SessionKey != nil {
			ids[*d.SessionKey] = true
		}
	}
	if len(ids) > 1 {
		return nil, fmt.Errorf("native response/accounting/diagnostic session identities conflict")
	}
	for key := range ids {
		if !validDigest(key, "json-v1") {
			return nil, fmt.Errorf("native session identity encoding is invalid")
		}
	}
	if err := validateModels(r.EventModels.Models); err != nil {
		return nil, err
	}
	if err := validateModels(r.Accounting.Models); err != nil {
		return nil, err
	}
	if r.EventModels.Complete && (r.EventModels.EventsObserved == 0 || len(r.EventModels.Models) == 0) ||
		r.Accounting.ModelAttributionComplete && len(r.Accounting.Models) == 0 {
		return nil, fmt.Errorf("native complete model observation is empty")
	}
	if !slices.Contains([]string{"rpc", "shutdown", "events", "none"}, r.Accounting.Source) ||
		r.Accounting.ProviderCurrencyState != "unavailable" ||
		r.Accounting.Complete && (r.Lifecycle.Shutdown.State != "completed" || r.Accounting.SessionKey == nil || r.Accounting.Usage == nil ||
			!r.Accounting.ModelAttributionComplete || r.Accounting.Usage.AICredits == nil ||
			(r.Accounting.Source != "rpc" && r.Accounting.Source != "shutdown")) {
		return nil, fmt.Errorf("native final accounting claims are unsupported")
	}
	if err := validateUsage(r.Accounting.Usage); err != nil {
		return nil, err
	}
	if r.Accounting.Source == "none" && (r.Accounting.Usage != nil || r.Accounting.SessionKey != nil ||
		r.Accounting.Complete || r.Accounting.ModelAttributionComplete || len(r.Accounting.Models) != 0) {
		return nil, fmt.Errorf("native accounting absence contradicts retained observation")
	}
	if r.Accounting.Complete {
		if len(r.Accounting.Usage.ModelMetrics) != len(r.Accounting.Models) {
			return nil, fmt.Errorf("native complete model accounting inventory differs")
		}
		for _, model := range r.Accounting.Models {
			metric, exists := r.Accounting.Usage.ModelMetrics[model]
			if !exists || metric.AICredits == nil {
				return nil, fmt.Errorf("native complete model credits are missing")
			}
		}
	}
	if r.Response != nil && r.Response.ErrorMsg != "" && r.ActualRow.Run.ErrorMsg != r.Response.ErrorMsg {
		return nil, fmt.Errorf("native response error differs from retained row")
	}
	behavioral := r.Summary.Category == "behavioral"
	if behavioral {
		for _, phase := range phases {
			if phase.State != "completed" {
				return nil, fmt.Errorf("behavioral row has unfinished or failed phase")
			}
		}
		if r.Response == nil || session == nil || !r.Response.Success || r.Response.ErrorMsg != "" ||
			r.Response.ToolPolicyMode != "deny_all" || len(r.Response.ToolPolicyDenials) > 0 ||
			len(r.Diagnostics) > 0 || r.Output.Availability != "available" {
			return nil, fmt.Errorf("operational native evidence cannot become behavioral")
		}
		if len(r.Summary.Checks) == 0 {
			return nil, fmt.Errorf("native behavioral row needs actual scoped text checks")
		}
		allPassed := true
		for _, check := range r.Summary.Checks {
			grade := run.Validations[check.Grader]
			if !slices.Contains([]string{"eval", "task"}, check.Scope) || check.AfterTurn != 0 ||
				check.OperationalState != "observed" || grade.Type != models.GraderKindText ||
				!finiteNonnegative(grade.Weight) || grade.Weight == 0 || grade.Score < 0 || grade.Score > 1 ||
				grade.DurationMs < 0 {
				return nil, fmt.Errorf("native check is not a completed scoped text grade")
			}
			allPassed = allPassed && check.Passed
		}
		if (r.Summary.Status == "passed") != allPassed {
			return nil, fmt.Errorf("native behavioral status contradicts scoped checks")
		}
	} else if r.Summary.Category != "operational" || r.Summary.Status != "incomplete" || r.ActualRow.Run.ErrorMsg == "" {
		return nil, fmt.Errorf("native operational row needs explicit incomplete/error state")
	}
	return &r, nil
}

func validateOutput(r *Record, raw []byte) error {
	value, err := jsonutil.Parse(raw)
	if err != nil {
		return err
	}
	document, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("native record must be an object")
	}
	object, ok := document["output"].(map[string]any)
	if !ok {
		return fmt.Errorf("native output must be an object")
	}
	_, hasValue := object["value"]
	if r.Output.Availability == "available" {
		if !hasValue || r.Output.Value == nil || r.Output.Reason != "" || r.Response == nil || *r.Output.Value != r.Response.FinalOutput ||
			r.ActualRow.Run.FinalOutput != *r.Output.Value {
			return fmt.Errorf("native output availability contradicts preserved value")
		}
	} else if r.Output.Availability != "unavailable" || hasValue || r.Output.Reason == "" || r.ActualRow.Run.FinalOutput != "" {
		return fmt.Errorf("native unavailable output must not manufacture a row value")
	}
	messages := []Message{}
	if r.Response != nil {
		for i, event := range r.Response.CanonicalEvents {
			parsed, err := jsonutil.Parse(event)
			if err != nil {
				return err
			}
			obj, ok := parsed.(map[string]any)
			if !ok || containsPrivateIdentity(obj) {
				return fmt.Errorf("native canonical event is malformed or exposes private session identity")
			}
			if obj["type"] != "assistant.message" {
				continue
			}
			data, ok := obj["data"].(map[string]any)
			if !ok {
				return fmt.Errorf("native assistant message data is malformed")
			}
			content, ok := data["content"].(string)
			if !ok {
				return fmt.Errorf("native canonical assistant content is malformed")
			}
			presence := "typed_nonempty"
			if content == "" {
				presence = "unknown"
			}
			messages = append(messages, Message{i + 1, content, presence})
		}
	}
	if !reflect.DeepEqual(messages, r.Output.Messages) {
		return fmt.Errorf("native message inventory differs from whole canonical projection")
	}
	var combined strings.Builder
	usable := len(messages) > 0
	for _, m := range messages {
		combined.WriteString(m.Content)
		if m.ContentPresence != "typed_nonempty" {
			usable = false
		}
	}
	if r.Response != nil && combined.String() != r.Response.FinalOutput {
		return fmt.Errorf("native output differs from all contributing messages")
	}
	if r.Output.Availability == "available" && !usable {
		return fmt.Errorf("native empty or missing content presence is unavailable")
	}
	return nil
}

func containsPrivateIdentity(value any) bool {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			normalized := strings.Map(func(r rune) rune {
				if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
					return r
				}
				return -1
			}, key)
			if strings.EqualFold(normalized, "sessionid") || containsPrivateIdentity(child) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if containsPrivateIdentity(child) {
				return true
			}
		}
	}
	return false
}

func validateModels(models []string) error {
	last := ""
	for _, model := range models {
		if model <= last || len(model) > 512 || strings.TrimSpace(model) != model || strings.EqualFold(model, "unknown") || strings.EqualFold(model, "auto") {
			return fmt.Errorf("native model inventory must be known, ordered and unique")
		}
		last = model
	}
	return nil
}

func validateUsage(u *models.UsageStats) error {
	if u == nil {
		return nil
	}
	if u.InputTokens < 0 || u.OutputTokens < 0 || u.CacheReadTokens < 0 || u.CacheWriteTokens < 0 || u.Turns < 0 ||
		math.IsNaN(u.PremiumRequests) || math.IsInf(u.PremiumRequests, 0) || u.PremiumRequests < 0 {
		return fmt.Errorf("native usage contains invalid measurements")
	}
	if u.AICredits != nil && (!finiteNonnegative(*u.AICredits)) {
		return fmt.Errorf("native credits are invalid")
	}
	for _, metric := range u.ModelMetrics {
		if metric.InputTokens < 0 || metric.OutputTokens < 0 || metric.CacheReadTokens < 0 || metric.CacheWriteTokens < 0 ||
			!finiteNonnegative(metric.RequestCount) || !finiteNonnegative(metric.RequestCost) ||
			metric.AICredits != nil && !finiteNonnegative(*metric.AICredits) {
			return fmt.Errorf("native model usage is invalid")
		}
	}
	return nil
}

func finiteNonnegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

func validDiagnostic(d Diagnostic) bool {
	return slices.Contains([]string{"initialize", "execute", "disconnect", "ephemeral_delete", "usage_metrics", "shutdown_usage", "shutdown_delete", "client_stop", "commandmock_cleanup", "git_cleanup", "workspace_cleanup"}, d.Stage) &&
		slices.Contains([]string{"provider_failed", "start_failed", "auth_failed", "create_failed", "resume_failed", "send_failed", "execute_failed", "cleanup_failed", "expected_skill_cancellation", "usage_missing_or_partial", "model_attribution_missing", "shutdown_rpc_failed", "history_failed", "fallback_disconnect_failed", "observer_failed", "engine_closed", "session_busy"}, d.Code)
}
