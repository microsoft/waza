package telemetry

import "go.opentelemetry.io/otel/attribute"

// GenAI and waza-specific attribute keys. These follow the OpenTelemetry
// GenAI semantic conventions where possible
// (https://opentelemetry.io/docs/specs/semconv/gen-ai/) so spans land in
// off-the-shelf backends (Aspire, Jaeger, Tempo, App Insights, Honeycomb,
// Datadog) with familiar names. Waza-specific keys live under the `waza.*`
// namespace.
//
// We define these as constants instead of importing the upstream
// `semconv/genai` module so that:
//  1. The set of keys we emit is reviewable in one place; and
//  2. We avoid pinning to a specific semconv version.
const (
	SemConvServiceName    = attribute.Key("service.name")
	SemConvServiceVersion = attribute.Key("service.version")

	// GenAI standard keys.
	AttrGenAISystem               = attribute.Key("gen_ai.system")
	AttrGenAIOperationName        = attribute.Key("gen_ai.operation.name")
	AttrGenAIRequestModel         = attribute.Key("gen_ai.request.model")
	AttrGenAIResponseModel        = attribute.Key("gen_ai.response.model")
	AttrGenAIUsageInputTokens     = attribute.Key("gen_ai.usage.input_tokens")
	AttrGenAIUsageOutputTokens    = attribute.Key("gen_ai.usage.output_tokens")
	AttrGenAIUsageCacheReadTokens = attribute.Key("gen_ai.usage.cache_read_tokens")
	AttrGenAIToolName             = attribute.Key("gen_ai.tool.name")
	AttrGenAIToolCallID           = attribute.Key("gen_ai.tool.call.id")
	AttrGenAIPromptText           = attribute.Key("gen_ai.prompt")
	AttrGenAICompletionText       = attribute.Key("gen_ai.completion")
	AttrGenAIToolArguments        = attribute.Key("gen_ai.tool.arguments")
	AttrGenAIToolResult           = attribute.Key("gen_ai.tool.result")

	// Waza-specific keys for shape that GenAI semconv does not yet cover.
	AttrWazaEvalName     = attribute.Key("waza.eval.name")
	AttrWazaEvalSkill    = attribute.Key("waza.eval.skill")
	AttrWazaEvalEngine   = attribute.Key("waza.eval.engine")
	AttrWazaEvalRunID    = attribute.Key("waza.eval.run_id")
	AttrWazaTaskID       = attribute.Key("waza.task.id")
	AttrWazaTaskName     = attribute.Key("waza.task.name")
	AttrWazaTurnNumber   = attribute.Key("waza.turn.number")
	AttrWazaTurnTrial    = attribute.Key("waza.turn.trial")
	AttrWazaTurnKind     = attribute.Key("waza.turn.kind")
	AttrWazaSessionID    = attribute.Key("waza.session.id")
	AttrWazaWorkspaceDir = attribute.Key("waza.workspace.dir")
	AttrWazaToolSuccess  = attribute.Key("waza.tool.success")
	AttrWazaPremiumReqs  = attribute.Key("waza.usage.premium_requests")

	// Payload-redaction surrogate keys. When --otel-include-payloads is
	// off, payloadAttr emits a sha256 + length pair instead of the raw
	// payload. Keys are namespaced per payload slot so multiple redacted
	// payloads on the same span (prompt + completion, or tool args +
	// result) do not collide.
	AttrWazaPromptHash       = attribute.Key("waza.prompt.sha256")
	AttrWazaPromptLength     = attribute.Key("waza.prompt.length")
	AttrWazaCompletionHash   = attribute.Key("waza.completion.sha256")
	AttrWazaCompletionLength = attribute.Key("waza.completion.length")
	AttrWazaToolArgsHash     = attribute.Key("waza.tool.arguments.sha256")
	AttrWazaToolArgsLength   = attribute.Key("waza.tool.arguments.length")
	AttrWazaToolResultHash   = attribute.Key("waza.tool.result.sha256")
	AttrWazaToolResultLength = attribute.Key("waza.tool.result.length")
)

// GenAI system value identifying waza-driven runs through the GitHub Copilot
// engine. We expose a constant so other engines (mock, custom) can override
// it by passing a different system into StartTurnSpan.
const GenAISystemCopilot = "github_copilot"

// Operation names per GenAI semconv.
const (
	GenAIOperationChat = "chat"
	GenAIOperationTool = "execute_tool"
)
