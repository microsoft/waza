// Package judgeevidence freezes the bounded data supplied to independent judges.
package judgeevidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"unicode/utf8"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/copilotevents"
	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/snapshot"
)

const maxEvents = 4096

func diagnostic(code string) models.EvidenceDiagnostic {
	return models.EvidenceDiagnostic{Category: "prompt_judging", Code: code,
		Message: "Required judge evidence is unusable; review the recorded coverage and capture selection."}
}

func Selection(configs []models.GraderConfig, task *models.TestCase) (models.PromptEvidenceContext, error) {
	selection, err := selectionUnion(configs, task)
	if err == nil && len(selection.Files) > 0 && runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return selection, errors.New("evidence_context selected-file capture requires Linux or macOS")
	}
	return selection, err
}

// ValidateSelection checks the final-grader union without requiring live file capture.
func ValidateSelection(configs []models.GraderConfig, task *models.TestCase) error {
	_, err := selectionUnion(configs, task)
	return err
}

func selectionUnion(configs []models.GraderConfig, task *models.TestCase) (models.PromptEvidenceContext, error) {
	parameters := make([]models.GraderParameters, 0, len(configs))
	for _, config := range configs {
		parameters = append(parameters, config.Parameters)
	}
	if task != nil {
		for _, validator := range task.Validators {
			parameters = append(parameters, validator.Parameters)
		}
	}
	files, excluded := map[string]bool{}, map[string]bool{}
	for _, parameter := range parameters {
		p, ok := parameter.(models.PromptGraderParameters)
		if !ok || p.EvidenceContext == nil {
			continue
		}
		if err := p.ValidateEvidenceContext(); err != nil {
			return models.PromptEvidenceContext{}, err
		}
		for _, name := range p.EvidenceContext.Files {
			files[name] = true
		}
		for _, name := range p.EvidenceContext.EvaluatorOnly {
			excluded[name] = true
		}
	}
	selection := models.PromptEvidenceContext{Files: []string{}, EvaluatorOnly: []string{}}
	if len(files) > models.JudgeEvidenceMaxFiles || len(excluded) > models.JudgeEvidenceMaxFiles {
		return selection, errors.New("evidence_context: run union exceeds 64 files or exclusions")
	}
	for name := range files {
		for exclusion := range excluded {
			if name == exclusion || strings.HasPrefix(name, exclusion+"/") {
				return selection, errors.New("evidence_context: selected files conflict with evaluator-only exclusions")
			}
		}
		selection.Files = append(selection.Files, name)
	}
	for name := range excluded {
		selection.EvaluatorOnly = append(selection.EvaluatorOnly, name)
	}
	slices.Sort(selection.Files)
	slices.Sort(selection.EvaluatorOnly)
	return selection, nil
}

func CaptureFiles(root string, selection models.PromptEvidenceContext) *models.JudgeFileCapture {
	capture := &models.JudgeFileCapture{Files: []models.JudgeEvidenceFile{},
		Redaction: models.EvidenceRedaction{Policy: "default", Limitations: []string{"Redaction does not certify secret absence."}}}
	if len(selection.Files) == 0 {
		return capture
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		capture.Diagnostics = []models.EvidenceDiagnostic{diagnostic("unsupported_platform")}
		return capture
	}
	files, accounting, err := snapshot.CaptureWorkspaceWithAccounting(root, selection.Files, selection.EvaluatorOnly, nil,
		snapshot.WorkspaceLimits{MaxFileBytes: models.JudgeEvidenceMaxFileBytes, MaxTotalBytes: models.JudgeEvidenceMaxBytes})
	capture.Redaction.MatchCount = accounting.RedactionCount
	capture.Redaction.AppliedRules = accounting.AppliedRules
	if err != nil {
		code := "file_capture_failed"
		if strings.Contains(err.Error(), "limit") {
			code = "limit_exceeded"
		}
		capture.Diagnostics = append(capture.Diagnostics, diagnostic(code))
	}
	if accounting.RedactionCount != 0 {
		capture.Diagnostics = append(capture.Diagnostics, diagnostic("redacted_required"))
	}
	for _, file := range files {
		capture.Files = append(capture.Files, models.JudgeEvidenceFile{Path: file.Path, Content: file.Content, SHA256: file.SHA256})
	}
	return capture
}

type CaptureInput struct {
	Origin        models.EvidenceOrigin
	Task          string
	Output        string
	OutputPresent bool
	Events        []copilot.SessionEvent
	Files         *models.JudgeFileCapture
	Selection     models.PromptEvidenceContext
	ExecutionMode string
}

func complete[T any](items T) models.JudgeEvidenceSection[T] {
	return models.JudgeEvidenceSection[T]{Availability: "captured", Completeness: "complete", Items: &items}
}

func unavailable[T any](reason string) models.JudgeEvidenceSection[T] {
	return models.JudgeEvidenceSection[T]{Availability: "unavailable", Completeness: "unknown", Reason: reason}
}

// Capture never returns rejected source content in an error or diagnostic.
func Capture(input CaptureInput) *models.JudgeEvidence {
	bundle := &models.JudgeEvidence{
		Version: models.JudgeEvidenceVersion, Origin: input.Origin, Boundary: "final",
		ExecutionMode: input.ExecutionMode,
		Task:          unavailable[string]("unobserved"), Output: unavailable[string]("unobserved"),
		Messages:   unavailable[[]models.JudgeEvidenceMessage]("unobserved"),
		ToolEvents: unavailable[[]models.ToolEvent]("unobserved"),
		Files:      unavailable[[]models.JudgeEvidenceFile]("unobserved"),
		Redaction:  models.EvidenceRedaction{Policy: "default", Limitations: []string{"Redaction does not certify secret absence."}},
		Limits: models.JudgeEvidenceLimits{MaxFiles: models.JudgeEvidenceMaxFiles,
			MaxFileBytes: models.JudgeEvidenceMaxFileBytes, MaxBundleBytes: models.JudgeEvidenceMaxBytes},
	}
	policy := snapshot.DefaultPolicy()
	budget := projectionBudget{bytes: models.JudgeEvidenceMaxBytes, nodes: 16384}
	if len(input.Selection.Files) > models.JudgeEvidenceMaxFiles ||
		len(input.Selection.EvaluatorOnly) > models.JudgeEvidenceMaxFiles ||
		budget.inspect(reflect.ValueOf(input.Selection), 0) != nil {
		bundle.Diagnostics = []models.EvidenceDiagnostic{diagnostic("limit_exceeded")}
		if err := seal(bundle); err != nil {
			bundle.Diagnostics = append(bundle.Diagnostics, diagnostic("invalid_evidence"))
		}
		return bundle
	}
	copyValue := func(value any, target any) error {
		if err := budget.inspect(reflect.ValueOf(value), 0); err != nil {
			return err
		}
		raw, err := json.Marshal(value)
		if err != nil || len(raw) > models.JudgeEvidenceMaxBytes {
			return errors.New("limit_exceeded")
		}
		parsed, err := jsonutil.Parse(raw)
		if err != nil {
			return errors.New("invalid_evidence")
		}
		sanitized := policy.RedactAny(parsed)
		clean, err := json.Marshal(sanitized)
		if err != nil || len(clean) > models.JudgeEvidenceMaxBytes {
			return errors.New("limit_exceeded")
		}
		if !bytes.Equal(raw, clean) {
			// Encoding order may change, so compare normalized source bytes.
			normalized, err := json.Marshal(parsed)
			if err != nil {
				return errors.New("invalid_evidence")
			}
			if !bytes.Equal(normalized, clean) {
				return errors.New("redacted_required")
			}
		}
		decoder := json.NewDecoder(bytes.NewReader(clean))
		decoder.UseNumber()
		return decoder.Decode(target)
	}
	identity := []string{input.Origin.EvalID, input.Origin.TaskID}
	for _, value := range identity {
		if len(value) > 1024 || !utf8.ValidString(value) || policy.RedactString(value) != value {
			bundle.Origin.EvalID, bundle.Origin.TaskID = "", ""
			bundle.Diagnostics = append(bundle.Diagnostics, diagnostic("invalid_binding"))
			break
		}
	}
	for _, section := range []struct {
		value   string
		present bool
		target  *models.JudgeEvidenceSection[string]
	}{
		{input.Task, true, &bundle.Task},
		{input.Output, input.OutputPresent, &bundle.Output},
	} {
		if !section.present {
			bundle.Diagnostics = append(bundle.Diagnostics, diagnostic("unobserved"))
			continue
		}
		var text string
		if err := copyValue(section.value, &text); err != nil {
			*section.target = unavailable[string](err.Error())
			bundle.Diagnostics = append(bundle.Diagnostics, diagnostic(err.Error()))
		} else {
			*section.target = complete(text)
		}
	}
	if len(input.Events) > maxEvents {
		bundle.Diagnostics = append(bundle.Diagnostics, diagnostic("limit_exceeded"))
	} else if len(input.Events) > 0 {
		messages, tools, err := projectEvents(input.Events)
		if err == nil {
			var copiedMessages []models.JudgeEvidenceMessage
			var copiedTools []models.ToolEvent
			err = copyValue(messages, &copiedMessages)
			if err == nil {
				bundle.Messages = complete(copiedMessages)
				err = copyValue(tools, &copiedTools)
			}
			if err == nil {
				bundle.ToolEvents = complete(copiedTools)
			}
		}
		if err != nil {
			bundle.ToolEvents = unavailable[[]models.ToolEvent](err.Error())
			if bundle.Messages.Items == nil {
				bundle.Messages = unavailable[[]models.JudgeEvidenceMessage](err.Error())
			}
			bundle.Diagnostics = append(bundle.Diagnostics, diagnostic(err.Error()))
		}
	} else {
		bundle.Diagnostics = append(bundle.Diagnostics, diagnostic("unobserved"))
	}
	if len(input.Selection.Files) == 0 {
		bundle.Files = complete([]models.JudgeEvidenceFile{})
	} else if input.Files != nil {
		valid := len(input.Files.Files) <= models.JudgeEvidenceMaxFiles &&
			len(input.Files.Diagnostics) <= models.JudgeEvidenceMaxFiles &&
			len(input.Files.Redaction.AppliedRules) <= len(policy.Rules)+1 &&
			input.Files.Redaction.MatchCount >= 0 &&
			input.Files.Redaction.MatchCount <= models.JudgeEvidenceMaxBytes &&
			budget.inspect(reflect.ValueOf(input.Files), 0) == nil
		if valid {
			for _, file := range input.Files.Files {
				if len(file.Content) > models.JudgeEvidenceMaxFileBytes {
					valid = false
					break
				}
			}
			knownRules := map[string]bool{"sensitive_key": true}
			for _, rule := range policy.Rules {
				knownRules[rule.Name] = true
			}
			for _, rule := range input.Files.Redaction.AppliedRules {
				valid = valid && knownRules[rule]
			}
		}
		if valid {
			// CaptureFiles already sanitized contents, including structured keys.
			bundle.Files = complete(slices.Clone(input.Files.Files))
			for _, source := range input.Files.Diagnostics {
				code := source.Code
				switch code {
				case "file_capture_failed", "limit_exceeded", "redacted_required", "unsupported_platform":
				default:
					code = "invalid_evidence"
				}
				bundle.Diagnostics = append(bundle.Diagnostics, diagnostic(code))
			}
			if len(input.Files.Diagnostics) > 0 {
				bundle.Files.Completeness = "incomplete"
			}
			bundle.Redaction.MatchCount += input.Files.Redaction.MatchCount
			bundle.Redaction.AppliedRules = slices.Clone(input.Files.Redaction.AppliedRules)
			if input.Files.Redaction.MatchCount > 0 && len(input.Files.Diagnostics) == 0 {
				bundle.Diagnostics = append(bundle.Diagnostics, diagnostic("redacted_required"))
			}
		} else {
			bundle.Diagnostics = append(bundle.Diagnostics, diagnostic("invalid_file_capture"))
		}
	} else {
		bundle.Diagnostics = append(bundle.Diagnostics, diagnostic("unobserved_file_capture"))
	}
	bundle.Redaction.MatchCount += policy.MatchCount()
	bundle.Redaction.AppliedRules = append(bundle.Redaction.AppliedRules, policy.MatchedRules()...)
	slices.Sort(bundle.Redaction.AppliedRules)
	bundle.Redaction.AppliedRules = slices.Compact(bundle.Redaction.AppliedRules)
	bundle.Selection.Files = slices.Clone(input.Selection.Files)
	bundle.Selection.EvaluatorOnly = slices.Clone(input.Selection.EvaluatorOnly)
	if err := seal(bundle); err != nil {
		bundle.Task = unavailable[string]("limit_exceeded")
		bundle.Output = unavailable[string]("limit_exceeded")
		bundle.Messages = unavailable[[]models.JudgeEvidenceMessage]("limit_exceeded")
		bundle.ToolEvents = unavailable[[]models.ToolEvent]("limit_exceeded")
		bundle.Files = unavailable[[]models.JudgeEvidenceFile]("limit_exceeded")
		bundle.Selection = models.PromptEvidenceContext{}
		bundle.Diagnostics = []models.EvidenceDiagnostic{diagnostic("limit_exceeded")}
		// The bounded metadata-only failure cannot exceed the bundle limit.
		bundle.SHA256 = ""
		if digest, err := evidence.JSONDigest(bundle); err == nil {
			bundle.SHA256 = digest.SHA256
		} else {
			bundle.Diagnostics = append(bundle.Diagnostics, diagnostic("invalid_evidence"))
		}
	}
	return bundle
}

func projectEvents(events []copilot.SessionEvent) ([]models.JudgeEvidenceMessage, []models.ToolEvent, error) {
	messages := []models.JudgeEvidenceMessage{}
	tools := []models.ToolEvent{}
	byID := map[string]int{}
	completed := map[string]bool{}
	turn, sawUser, sawAssistant := 1, false, false
	for _, event := range events {
		switch event.Type() {
		case copilot.SessionEventTypeUserMessage, copilot.SessionEventTypeAssistantMessage:
			text, present := copilotevents.Content(event)
			if !present {
				return nil, nil, errors.New("incomplete_conversation")
			}
			role := "user"
			if event.Type() == copilot.SessionEventTypeAssistantMessage {
				role, sawAssistant = "assistant", true
			} else {
				if sawUser {
					turn++
				}
				sawUser = true
			}
			messages = append(messages, models.JudgeEvidenceMessage{Role: role, Content: text})
		case copilot.SessionEventTypeToolExecutionStart:
			start, ok := copilotevents.ToolStart(event)
			if !ok || start.ToolCallID == "" || start.ToolName == "" {
				return nil, nil, errors.New("missing_tool_result")
			}
			if _, duplicate := byID[start.ToolCallID]; duplicate {
				return nil, nil, errors.New("missing_tool_result")
			}
			byID[start.ToolCallID] = len(tools)
			tools = append(tools, models.ToolEvent{ToolCallID: start.ToolCallID, ToolName: start.ToolName,
				Turn: turn, Sequence: len(tools) + 1, Args: start.Arguments})
		case copilot.SessionEventTypeToolExecutionComplete:
			result, ok := copilotevents.ToolComplete(event)
			if !ok || result.Result == nil {
				return nil, nil, errors.New("missing_tool_result")
			}
			index, found := byID[result.ToolCallID]
			if !found || completed[result.ToolCallID] {
				return nil, nil, errors.New("missing_tool_result")
			}
			completed[result.ToolCallID] = true
			tools[index].Result, tools[index].Success = result.Result, result.Success
		}
	}
	if !sawUser || !sawAssistant {
		return nil, nil, errors.New("incomplete_conversation")
	}
	if len(completed) != len(tools) {
		return nil, nil, errors.New("missing_tool_result")
	}
	return messages, tools, nil
}

type projectionBudget struct{ bytes, nodes int }

func (b *projectionBudget) inspect(value reflect.Value, depth int) error {
	b.nodes--
	if depth > 32 || b.nodes < 0 || b.bytes < 0 {
		return errors.New("limit_exceeded")
	}
	if !value.IsValid() {
		return nil
	}
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		if !value.IsNil() {
			return b.inspect(value.Elem(), depth+1)
		}
	case reflect.String:
		b.bytes -= value.Len()
		if b.bytes < 0 || !utf8.ValidString(value.String()) {
			return errors.New("limit_exceeded")
		}
	case reflect.Array, reflect.Slice:
		if value.Len() > b.nodes {
			return errors.New("limit_exceeded")
		}
		for i := 0; i < value.Len(); i++ {
			if err := b.inspect(value.Index(i), depth+1); err != nil {
				return err
			}
		}
	case reflect.Map:
		if value.Len() > b.nodes || value.Type().Key().Kind() != reflect.String {
			return errors.New("limit_exceeded")
		}
		iter := value.MapRange()
		for iter.Next() {
			if err := b.inspect(iter.Key(), depth+1); err != nil {
				return err
			}
			if err := b.inspect(iter.Value(), depth+1); err != nil {
				return err
			}
		}
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			if value.Type().Field(i).IsExported() {
				if err := b.inspect(value.Field(i), depth+1); err != nil {
					return err
				}
			}
		}
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
		b.bytes -= 32
	default:
		return errors.New("invalid_evidence")
	}
	return nil
}

func seal(bundle *models.JudgeEvidence) error {
	bundle.SHA256 = ""
	digest, err := evidence.JSONDigest(bundle)
	if err != nil {
		return err
	}
	bundle.SHA256 = digest.SHA256
	data, err := json.Marshal(bundle)
	if err != nil || len(data) > models.JudgeEvidenceMaxBytes {
		return errors.New("limit_exceeded")
	}
	return nil
}

// VerifyIntegrity admits bound metadata without claiming that incomplete
// evidence is usable for judging.
func VerifyIntegrity(bundle *models.JudgeEvidence, origin models.EvidenceOrigin) error {
	if bundle == nil {
		return errors.New("judge evidence is missing; record a new run with evidence_context enabled")
	}
	if bundle.Version != models.JudgeEvidenceVersion || bundle.Origin != origin || origin.EvalID == "" ||
		origin.TaskID == "" || origin.RunNumber < 1 || origin.AttemptCount < 1 || bundle.Boundary != "final" {
		return errors.New("judge evidence: invalid_binding")
	}
	if bundle.Limits != (models.JudgeEvidenceLimits{MaxFiles: models.JudgeEvidenceMaxFiles,
		MaxFileBytes: models.JudgeEvidenceMaxFileBytes, MaxBundleBytes: models.JudgeEvidenceMaxBytes}) ||
		origin.AttemptCount == 1 && origin.PriorAttempts != "none" ||
		origin.AttemptCount > 1 && origin.PriorAttempts != "not_preserved" {
		return errors.New("judge evidence: invalid capture limits or attempt lineage")
	}
	encoded, err := json.Marshal(bundle)
	if err != nil || len(encoded) > models.JudgeEvidenceMaxBytes {
		return errors.New("judge evidence: limit_exceeded")
	}
	var admitted models.JudgeEvidence
	if err := json.Unmarshal(encoded, &admitted); err != nil {
		return errors.New("judge evidence: invalid structure")
	}
	copy := *bundle
	copy.SHA256 = ""
	digest, err := evidence.JSONDigest(copy)
	if err != nil || digest.SHA256 != bundle.SHA256 {
		return errors.New("judge evidence: digest mismatch")
	}
	return nil
}

func Verify(bundle *models.JudgeEvidence, origin models.EvidenceOrigin) error {
	if err := VerifyIntegrity(bundle, origin); err != nil {
		return err
	}
	if bundle.ExecutionMode != "live" && bundle.ExecutionMode != "mock" {
		return errors.New("judge evidence: execution mode is unavailable")
	}
	if len(bundle.Diagnostics) != 0 || bundle.Redaction.MatchCount != 0 || bundle.Redaction.Policy != "default" {
		return errors.New("judge evidence: required capture is incomplete or redacted")
	}
	sections := []struct {
		availability, completeness string
		present                    bool
	}{
		{bundle.Task.Availability, bundle.Task.Completeness, bundle.Task.Items != nil},
		{bundle.Output.Availability, bundle.Output.Completeness, bundle.Output.Items != nil},
		{bundle.Messages.Availability, bundle.Messages.Completeness, bundle.Messages.Items != nil},
		{bundle.ToolEvents.Availability, bundle.ToolEvents.Completeness, bundle.ToolEvents.Items != nil},
		{bundle.Files.Availability, bundle.Files.Completeness, bundle.Files.Items != nil},
	}
	for _, section := range sections {
		if section.availability != "captured" || section.completeness != "complete" || !section.present {
			return errors.New("judge evidence: a required section is unavailable or incomplete")
		}
	}
	if len(*bundle.Messages.Items) == 0 || len(*bundle.ToolEvents.Items) > maxEvents ||
		len(*bundle.Files.Items) != len(bundle.Selection.Files) || len(bundle.Selection.Files) > models.JudgeEvidenceMaxFiles {
		return errors.New("judge evidence: required coverage does not match selection")
	}
	users, assistants := 0, 0
	for _, message := range *bundle.Messages.Items {
		switch message.Role {
		case "user":
			users++
		case "assistant":
			assistants++
		}
	}
	if users == 0 || assistants == 0 {
		return errors.New("judge evidence: incomplete_conversation")
	}
	if err := (models.PromptGraderParameters{EvidenceContext: &bundle.Selection}).ValidateEvidenceContext(); err != nil {
		return errors.New("judge evidence: invalid selection")
	}
	calls := map[string]bool{}
	for i, tool := range *bundle.ToolEvents.Items {
		if tool.Result == nil || tool.ToolCallID == "" || calls[tool.ToolCallID] || tool.ToolName == "" ||
			tool.Sequence != i+1 || tool.Turn < 1 || tool.Turn > users {
			return errors.New("judge evidence: missing_tool_result")
		}
		calls[tool.ToolCallID] = true
	}
	seen := map[string]bool{}
	for _, file := range *bundle.Files.Items {
		if models.ValidateJudgeEvidencePath(file.Path) != nil || !slices.Contains(bundle.Selection.Files, file.Path) ||
			seen[file.Path] || len(file.Content) > models.JudgeEvidenceMaxFileBytes || !utf8.ValidString(file.Content) {
			return errors.New("judge evidence: invalid selected file")
		}
		seen[file.Path] = true
		for _, excluded := range bundle.Selection.EvaluatorOnly {
			if file.Path == excluded || strings.HasPrefix(file.Path, excluded+"/") {
				return errors.New("judge evidence: selected file is evaluator-only")
			}
		}
		sum := sha256.Sum256([]byte(file.Content))
		if hex.EncodeToString(sum[:]) != file.SHA256 {
			return errors.New("judge evidence: selected file digest mismatch")
		}
	}
	return nil
}

// Delivery returns a detached subset. It never exposes other graders' files or
// their evaluator-only declarations, even though the stored bundle is a union.
func Delivery(bundle *models.JudgeEvidence, origin models.EvidenceOrigin, selection models.PromptEvidenceContext) (*models.JudgeEvidence, error) {
	if err := Verify(bundle, origin); err != nil {
		return nil, err
	}
	if err := (models.PromptGraderParameters{EvidenceContext: &selection}).ValidateEvidenceContext(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(bundle)
	if err != nil {
		return nil, errors.New("judge evidence cannot be encoded")
	}
	var delivery models.JudgeEvidence
	if err := json.Unmarshal(data, &delivery); err != nil {
		return nil, err
	}
	files := []models.JudgeEvidenceFile{}
	for _, name := range selection.Files {
		found := false
		for _, file := range *delivery.Files.Items {
			if name == file.Path {
				for _, excluded := range selection.EvaluatorOnly {
					if name == excluded || strings.HasPrefix(name, excluded+"/") {
						return nil, errors.New("judge evidence: selected file is evaluator-only")
					}
				}
				files = append(files, file)
				found = true
			}
		}
		if !found {
			return nil, errors.New("judge evidence: newly selected file was not captured; record a new run")
		}
	}
	delivery.Files = complete(files)
	delivery.Selection = models.PromptEvidenceContext{Files: slices.Clone(selection.Files), EvaluatorOnly: slices.Clone(selection.EvaluatorOnly)}
	if err := seal(&delivery); err != nil {
		return nil, fmt.Errorf("judge evidence delivery: %w", err)
	}
	return &delivery, nil
}
