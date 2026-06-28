package orchestration

import (
	"context"
	"encoding/json"

	"go.opentelemetry.io/otel/trace"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/telemetry"
)

// emitChildSpans records tool_call and model_call child spans for the
// given execution response under the supplied turn span context. It is
// safe to call with a nil provider; the underlying tracer will be a
// no-op and nothing is recorded.
//
// The function also attaches the assistant's final output to the turn
// span (honoring the redaction policy) so that consumers can correlate
// a turn span with the model's reply.
func emitChildSpans(turnCtx context.Context, p *telemetry.Provider, turnSpan trace.Span, resp *execution.ExecutionResponse, requestModel string) {
	if resp == nil {
		return
	}
	telemetry.RecordCompletion(turnSpan, p, resp.FinalOutput)

	for _, tc := range resp.ToolCalls {
		args := marshalArgs(tc.Arguments)
		result := marshalResult(tc.Result)
		telemetry.RecordToolCall(turnCtx, p, telemetry.ToolCallInfo{
			Name:      tc.Name,
			CallID:    tc.ID,
			Arguments: args,
			Result:    result,
			Success:   tc.Success,
		})
	}

	// Emit one model_call span per recorded per-model usage entry when the
	// engine surfaces it. Otherwise fall back to a single span carrying the
	// aggregate usage so token totals still appear in the trace.
	usage := resp.Usage
	model := requestModel
	if resp.ModelID != "" {
		model = resp.ModelID
	}
	if usage != nil && len(usage.ModelMetrics) > 0 {
		for name, m := range usage.ModelMetrics {
			telemetry.RecordModelCall(turnCtx, p, telemetry.ModelCallInfo{
				RequestModel:  model,
				ResponseModel: name,
				InputTokens:   m.InputTokens,
				OutputTokens:  m.OutputTokens,
				CacheReads:    m.CacheReadTokens,
				PremiumReqs:   m.RequestCount,
			})
		}
		return
	}
	if usage != nil && !usage.IsZero() {
		telemetry.RecordModelCall(turnCtx, p, telemetry.ModelCallInfo{
			RequestModel:  model,
			ResponseModel: model,
			InputTokens:   usage.InputTokens,
			OutputTokens:  usage.OutputTokens,
			CacheReads:    usage.CacheReadTokens,
			PremiumReqs:   usage.PremiumRequests,
		})
	}
}

func marshalArgs(args models.ToolCallArgs) string {
	b, err := json.Marshal(args)
	if err != nil {
		return ""
	}
	return string(b)
}

func marshalResult(result any) string {
	if result == nil {
		return ""
	}
	b, err := json.Marshal(result)
	if err != nil {
		return ""
	}
	return string(b)
}
