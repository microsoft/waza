package telemetry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// EvalInfo carries top-level identifiers that label the root eval span.
type EvalInfo struct {
	Name   string
	Skill  string
	Engine string
	Model  string
	RunID  string
}

// TaskInfo carries identifiers for a single test case execution.
type TaskInfo struct {
	TestID      string
	DisplayName string
}

// TurnInfo carries identifiers for one Execute call (initial prompt,
// follow-up, or responder reply).
type TurnInfo struct {
	Number       int    // 1-indexed turn within the conversation (initial=1)
	Trial        int    // optional trial number when the same task is re-run
	Kind         string // "initial" | "follow_up" | "responder_reply"
	Model        string
	SessionID    string
	WorkspaceDir string
	Prompt       string
}

// ToolCallInfo carries identifiers for one tool invocation.
type ToolCallInfo struct {
	Name      string
	CallID    string
	Arguments string
	Result    string
	Success   bool
}

// ModelCallInfo carries identifiers for one model invocation. Engines that
// expose per-call telemetry should emit one of these per chat completion;
// engines that only surface aggregate usage can emit a single ModelCall at
// turn end.
type ModelCallInfo struct {
	System        string
	RequestModel  string
	ResponseModel string
	InputTokens   int
	OutputTokens  int
	CacheReads    int
	PremiumReqs   float64
}

// StartEvalSpan opens the root span for an eval run. The returned span
// should be ended with End() (and SetStatus on failure) when the eval
// finishes.
func StartEvalSpan(ctx context.Context, p *Provider, info EvalInfo) (context.Context, trace.Span) {
	tr := p.Tracer()
	attrs := []attribute.KeyValue{
		AttrWazaEvalName.String(info.Name),
		AttrWazaEvalSkill.String(info.Skill),
		AttrWazaEvalEngine.String(info.Engine),
		AttrWazaEvalRunID.String(info.RunID),
	}
	if info.Model != "" {
		attrs = append(attrs, AttrGenAIRequestModel.String(info.Model))
	}
	return tr.Start(ctx, "waza.eval", trace.WithAttributes(attrs...))
}

// StartTaskSpan opens a child span for one test case.
func StartTaskSpan(ctx context.Context, p *Provider, info TaskInfo) (context.Context, trace.Span) {
	tr := p.Tracer()
	return tr.Start(ctx, "waza.task", trace.WithAttributes(
		AttrWazaTaskID.String(info.TestID),
		AttrWazaTaskName.String(info.DisplayName),
	))
}

// StartTurnSpan opens a child span for one Execute call. The caller is
// expected to redact the prompt unless p.Config().IncludePayloads is true;
// this helper does the redaction for them.
func StartTurnSpan(ctx context.Context, p *Provider, info TurnInfo) (context.Context, trace.Span) {
	tr := p.Tracer()
	attrs := []attribute.KeyValue{
		AttrGenAISystem.String(GenAISystemCopilot),
		AttrGenAIOperationName.String(GenAIOperationChat),
		AttrWazaTurnNumber.Int(info.Number),
	}
	if info.Trial > 0 {
		attrs = append(attrs, AttrWazaTurnTrial.Int(info.Trial))
	}
	if info.Kind != "" {
		attrs = append(attrs, AttrWazaTurnKind.String(info.Kind))
	}
	if info.Model != "" {
		attrs = append(attrs, AttrGenAIRequestModel.String(info.Model))
	}
	if info.SessionID != "" {
		attrs = append(attrs, AttrWazaSessionID.String(info.SessionID))
	}
	if info.WorkspaceDir != "" {
		attrs = append(attrs, AttrWazaWorkspaceDir.String(info.WorkspaceDir))
	}
	if info.Prompt != "" {
		attrs = append(attrs, payloadAttr(AttrGenAIPromptText, AttrWazaPromptHash, AttrWazaPromptLength, info.Prompt, p.Config().IncludePayloads)...)
	}
	return tr.Start(ctx, "waza.turn", trace.WithAttributes(attrs...))
}

// RecordModelCall emits a child span describing one model invocation. The
// span is started and ended in this call because waza records model usage
// after the fact rather than wrapping the request itself.
func RecordModelCall(ctx context.Context, p *Provider, info ModelCallInfo) {
	tr := p.Tracer()
	system := info.System
	if system == "" {
		system = GenAISystemCopilot
	}
	attrs := []attribute.KeyValue{
		AttrGenAISystem.String(system),
		AttrGenAIOperationName.String(GenAIOperationChat),
		AttrGenAIUsageInputTokens.Int(info.InputTokens),
		AttrGenAIUsageOutputTokens.Int(info.OutputTokens),
	}
	if info.RequestModel != "" {
		attrs = append(attrs, AttrGenAIRequestModel.String(info.RequestModel))
	}
	if info.ResponseModel != "" {
		attrs = append(attrs, AttrGenAIResponseModel.String(info.ResponseModel))
	}
	if info.CacheReads != 0 {
		attrs = append(attrs, AttrGenAIUsageCacheReadTokens.Int(info.CacheReads))
	}
	if info.PremiumReqs != 0 {
		attrs = append(attrs, AttrWazaPremiumReqs.Float64(info.PremiumReqs))
	}
	_, span := tr.Start(ctx, "waza.model_call", trace.WithAttributes(attrs...))
	span.End()
}

// RecordToolCall emits a child span describing one tool invocation. As with
// RecordModelCall the span is opened and closed in one go because waza only
// learns about the call after the engine reports it.
func RecordToolCall(ctx context.Context, p *Provider, info ToolCallInfo) {
	tr := p.Tracer()
	attrs := []attribute.KeyValue{
		AttrGenAIOperationName.String(GenAIOperationTool),
		AttrGenAIToolName.String(info.Name),
		AttrWazaToolSuccess.Bool(info.Success),
	}
	if info.CallID != "" {
		attrs = append(attrs, AttrGenAIToolCallID.String(info.CallID))
	}
	include := p.Config().IncludePayloads
	if info.Arguments != "" {
		attrs = append(attrs, payloadAttr(AttrGenAIToolArguments, AttrWazaToolArgsHash, AttrWazaToolArgsLength, info.Arguments, include)...)
	}
	if info.Result != "" {
		attrs = append(attrs, payloadAttr(AttrGenAIToolResult, AttrWazaToolResultHash, AttrWazaToolResultLength, info.Result, include)...)
	}
	_, span := tr.Start(ctx, "waza.tool_call", trace.WithAttributes(attrs...))
	if !info.Success {
		span.SetStatus(codes.Error, "tool call reported failure")
	}
	span.End()
}

// RecordCompletion attaches the assistant's final output to an in-flight
// turn span. Honors the IncludePayloads policy.
func RecordCompletion(span trace.Span, p *Provider, output string) {
	if span == nil || output == "" {
		return
	}
	for _, kv := range payloadAttr(AttrGenAICompletionText, AttrWazaCompletionHash, AttrWazaCompletionLength, output, p.Config().IncludePayloads) {
		span.SetAttributes(kv)
	}
}

// payloadAttr returns the attribute(s) that represent a payload string,
// honoring the redaction policy. When include is true the raw value is
// emitted under rawKey. When include is false the raw value is dropped and
// a SHA-256 hash + length are emitted under hashKey / lengthKey so backends
// can still group identical payloads. Hash/length keys are per-payload-slot
// so multiple redacted payloads on the same span (e.g. tool args + result)
// do not collide.
func payloadAttr(rawKey, hashKey, lengthKey attribute.Key, value string, include bool) []attribute.KeyValue {
	if include {
		return []attribute.KeyValue{rawKey.String(value)}
	}
	sum := sha256.Sum256([]byte(value))
	return []attribute.KeyValue{
		hashKey.String(hex.EncodeToString(sum[:])),
		lengthKey.Int(len(value)),
	}
}
