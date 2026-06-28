package telemetry

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// newRecordingProvider returns a Provider backed by an in-memory exporter
// so tests can assert on the spans waza emits without needing an OTLP
// collector. The returned cleanup func flushes the provider.
func newRecordingProvider(t *testing.T, cfg Config) (*Provider, *tracetest.InMemoryExporter) {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	p := &Provider{
		cfg:      cfg,
		tracer:   tp.Tracer(TracerName),
		shutdown: tp.Shutdown,
	}
	// Do NOT call Shutdown in cleanup: the InMemoryExporter resets its
	// captured spans on Shutdown, which would race with the test assertions.
	return p, exp
}

func attrMap(span sdktrace.ReadOnlySpan) map[attribute.Key]attribute.Value {
	m := map[attribute.Key]attribute.Value{}
	for _, kv := range span.Attributes() {
		m[kv.Key] = kv.Value
	}
	return m
}

func findSpan(t *testing.T, spans []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, s := range spans {
		if s.Name() == name {
			return s
		}
	}
	t.Fatalf("no span named %q in %d emitted spans", name, len(spans))
	return nil
}

func TestSpanHierarchyAndGenAIAttributes(t *testing.T) {
	p, exp := newRecordingProvider(t, Config{Exporter: ExporterStdout})

	ctx := context.Background()
	evalCtx, evalSpan := StartEvalSpan(ctx, p, EvalInfo{
		Name: "code-explainer-eval", Skill: "code-explainer",
		Engine: "copilot", Model: "gpt-4o", RunID: "run-42",
	})
	taskCtx, taskSpan := StartTaskSpan(evalCtx, p, TaskInfo{
		TestID: "tc1", DisplayName: "Explain recursion",
	})
	turnCtx, turnSpan := StartTurnSpan(taskCtx, p, TurnInfo{
		Number: 1, Kind: "initial", Model: "gpt-4o",
		SessionID: "sess-xyz", WorkspaceDir: "/tmp/ws",
		Prompt: "please explain this code",
	})
	RecordToolCall(turnCtx, p, ToolCallInfo{
		Name: "view", CallID: "tc-1",
		Arguments: `{"path":"main.go"}`, Result: "ok", Success: true,
	})
	RecordModelCall(turnCtx, p, ModelCallInfo{
		System: GenAISystemCopilot, RequestModel: "gpt-4o",
		ResponseModel: "gpt-4o", InputTokens: 120, OutputTokens: 60,
		CacheReads: 10, PremiumReqs: 1.5,
	})
	RecordCompletion(turnSpan, p, "the agent's reply")
	turnSpan.End()
	taskSpan.End()
	evalSpan.End()

	spans := exp.GetSpans().Snapshots()
	require.Len(t, spans, 5, "expected eval+task+turn+tool+model spans")

	eval := findSpan(t, spans, "waza.eval")
	task := findSpan(t, spans, "waza.task")
	turn := findSpan(t, spans, "waza.turn")
	tool := findSpan(t, spans, "waza.tool_call")
	model := findSpan(t, spans, "waza.model_call")

	// Parent links form the documented hierarchy.
	require.False(t, eval.Parent().HasSpanID(), "eval span should be a root")
	require.Equal(t, eval.SpanContext().SpanID(), task.Parent().SpanID(), "task parent should be eval")
	require.Equal(t, task.SpanContext().SpanID(), turn.Parent().SpanID(), "turn parent should be task")
	require.Equal(t, turn.SpanContext().SpanID(), tool.Parent().SpanID(), "tool parent should be turn")
	require.Equal(t, turn.SpanContext().SpanID(), model.Parent().SpanID(), "model parent should be turn")

	// GenAI conformance: required keys present with expected types.
	turnAttrs := attrMap(turn)
	require.Equal(t, GenAISystemCopilot, turnAttrs[AttrGenAISystem].AsString())
	require.Equal(t, "gpt-4o", turnAttrs[AttrGenAIRequestModel].AsString())
	require.Equal(t, GenAIOperationChat, turnAttrs[AttrGenAIOperationName].AsString())

	modelAttrs := attrMap(model)
	require.Equal(t, GenAISystemCopilot, modelAttrs[AttrGenAISystem].AsString())
	require.EqualValues(t, 120, modelAttrs[AttrGenAIUsageInputTokens].AsInt64())
	require.EqualValues(t, 60, modelAttrs[AttrGenAIUsageOutputTokens].AsInt64())
	require.EqualValues(t, 10, modelAttrs[AttrGenAIUsageCacheReadTokens].AsInt64())

	toolAttrs := attrMap(tool)
	require.Equal(t, "view", toolAttrs[AttrGenAIToolName].AsString())
	require.Equal(t, "tc-1", toolAttrs[AttrGenAIToolCallID].AsString())
	require.True(t, toolAttrs[AttrWazaToolSuccess].AsBool())
}

func TestPayloadRedactionDefault(t *testing.T) {
	p, exp := newRecordingProvider(t, Config{Exporter: ExporterStdout}) // IncludePayloads false

	ctx := context.Background()
	_, turn := StartTurnSpan(ctx, p, TurnInfo{Number: 1, Prompt: "secret prompt"})
	RecordToolCall(ctx, p, ToolCallInfo{
		Name: "bash", Arguments: `{"command":"rm -rf /"}`,
		Result: "destroyed", Success: false,
	})
	RecordCompletion(turn, p, "the model's secret answer")
	turn.End()

	spans := exp.GetSpans().Snapshots()
	for _, s := range spans {
		attrs := attrMap(s)
		// Raw payload keys must not be present.
		_, hasPrompt := attrs[AttrGenAIPromptText]
		_, hasCompletion := attrs[AttrGenAICompletionText]
		_, hasArgs := attrs[AttrGenAIToolArguments]
		_, hasResult := attrs[AttrGenAIToolResult]
		require.False(t, hasPrompt, "prompt text must be redacted by default in span %q", s.Name())
		require.False(t, hasCompletion, "completion text must be redacted by default in span %q", s.Name())
		require.False(t, hasArgs, "tool arguments must be redacted by default in span %q", s.Name())
		require.False(t, hasResult, "tool result must be redacted by default in span %q", s.Name())
	}

	// Hash + length surrogates are present on the turn span under the
	// prompt-namespaced keys (so other redacted payloads do not collide).
	turnSpan := findSpan(t, spans, "waza.turn")
	turnAttrs := attrMap(turnSpan)
	require.NotEmpty(t, turnAttrs[AttrWazaPromptHash].AsString())
	require.Greater(t, turnAttrs[AttrWazaPromptLength].AsInt64(), int64(0))

	// Completion surrogate keys must not collide with prompt keys when
	// both end up on the same span.
	require.NotEmpty(t, turnAttrs[AttrWazaCompletionHash].AsString())
	require.Greater(t, turnAttrs[AttrWazaCompletionLength].AsInt64(), int64(0))
	require.NotEqual(t,
		turnAttrs[AttrWazaPromptHash].AsString(),
		turnAttrs[AttrWazaCompletionHash].AsString(),
		"prompt and completion hashes must be distinct attributes")

	// Tool args + result on the tool span are also distinct.
	toolSpan := findSpan(t, spans, "waza.tool_call")
	toolAttrs := attrMap(toolSpan)
	require.NotEmpty(t, toolAttrs[AttrWazaToolArgsHash].AsString())
	require.Greater(t, toolAttrs[AttrWazaToolArgsLength].AsInt64(), int64(0))
	require.NotEmpty(t, toolAttrs[AttrWazaToolResultHash].AsString())
	require.Greater(t, toolAttrs[AttrWazaToolResultLength].AsInt64(), int64(0))
	require.NotEqual(t,
		toolAttrs[AttrWazaToolArgsHash].AsString(),
		toolAttrs[AttrWazaToolResultHash].AsString(),
		"tool args and result hashes must be distinct attributes")
}

func TestPayloadIncludeOptIn(t *testing.T) {
	p, exp := newRecordingProvider(t, Config{Exporter: ExporterStdout, IncludePayloads: true})

	ctx := context.Background()
	_, turn := StartTurnSpan(ctx, p, TurnInfo{Number: 1, Prompt: "hello"})
	RecordToolCall(ctx, p, ToolCallInfo{Name: "view", Arguments: `{"path":"x"}`, Result: "ok", Success: true})
	turn.End()

	spans := exp.GetSpans().Snapshots()

	turnSpan := findSpan(t, spans, "waza.turn")
	require.Equal(t, "hello", attrMap(turnSpan)[AttrGenAIPromptText].AsString())

	toolSpan := findSpan(t, spans, "waza.tool_call")
	toolAttrs := attrMap(toolSpan)
	require.Equal(t, `{"path":"x"}`, toolAttrs[AttrGenAIToolArguments].AsString())
	require.Equal(t, "ok", toolAttrs[AttrGenAIToolResult].AsString())
}

func TestDisabledProviderEmitsNothing(t *testing.T) {
	p, err := New(context.Background(), Config{})
	require.NoError(t, err)
	require.False(t, p.Enabled())

	tr := p.Tracer()
	_, span := tr.Start(context.Background(), "noop")
	require.False(t, span.SpanContext().IsValid(), "disabled provider must yield non-recording spans")
	require.False(t, span.IsRecording())
	span.End()
	require.NoError(t, p.Shutdown(context.Background()))
}

func TestParseHeaders(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    map[string]string
		wantErr bool
	}{
		{"empty", "", nil, false},
		{"single", "x-api-key=abc123", map[string]string{"x-api-key": "abc123"}, false},
		{"multi with spaces", " a = 1 , b = two ", map[string]string{"a": "1", "b": "two"}, false},
		{"empty key", "=value", nil, true},
		{"missing equals", "novalue", nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseHeaders(tc.input)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestConfigValidate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"disabled is valid", Config{}, false},
		{"otlp ok", Config{Exporter: ExporterOTLP, Endpoint: "localhost:4318"}, false},
		{"stdout ok", Config{Exporter: ExporterStdout}, false},
		{"file requires path", Config{Exporter: ExporterFile}, true},
		{"file with path ok", Config{Exporter: ExporterFile, FilePath: "/tmp/spans.json"}, false},
		{"unknown rejected", Config{Exporter: ExporterKind("zipkin")}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
