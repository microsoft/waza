// Package executiontelemetry derives vendor-neutral, OpenTelemetry-shaped
// traces, metrics and logs from Waza's public runner contract documents. It
// correlates external scheduler work, the logical run and work, immutable
// execution attempts, the exact task/trial attempt, model and tool activity,
// artifacts and aggregation through deterministic identifiers. Payloads are
// never emitted; an explicit digest policy may add SHA-256/length surrogates.
package executiontelemetry

import (
	"slices"
)

const (
	Kind          = "waza.execution-telemetry"
	SchemaVersion = "1.0"
	// ScopeName is the OTLP instrumentation scope for every signal.
	ScopeName = "github.com/microsoft/waza/executiontelemetry"
)

// PayloadPolicy controls payload-derived attributes. There is intentionally
// no raw-payload mode in this contract.
type PayloadPolicy string

const (
	// PayloadExclude emits no payload-derived attributes (default).
	PayloadExclude PayloadPolicy = "exclude"
	// PayloadDigest emits SHA-256 and byte length surrogates only.
	PayloadDigest PayloadPolicy = "digest"
)

// Placement says where an attribute key may appear.
type Placement string

const (
	OnResource Placement = "resource"
	OnSpan     Placement = "span"
	OnLink     Placement = "link"
	OnMetric   Placement = "metric"
	OnLog      Placement = "log"
)

// Class describes an attribute's privacy and cardinality class.
type Class string

const (
	// ClassIdentifier is a correlation identifier: high cardinality, never on
	// metric data points.
	ClassIdentifier Class = "identifier"
	// ClassBounded is a closed or naturally bounded value set.
	ClassBounded Class = "bounded"
	// ClassMeasure is a numeric measurement.
	ClassMeasure Class = "measure"
	// ClassPayloadDigest is emitted only under PayloadDigest.
	ClassPayloadDigest Class = "payload_digest"
	// ClassPayload is never emitted by this contract.
	ClassPayload Class = "payload"
)

// Attribute is one stable entry in the attribute catalog.
type Attribute struct {
	Key        string      `json:"key"`
	Type       string      `json:"type"`
	Class      Class       `json:"class"`
	Placements []Placement `json:"placements"`
}

func attr(key, typ string, class Class, placements ...Placement) Attribute {
	return Attribute{Key: key, Type: typ, Class: class, Placements: placements}
}

// Stable attribute keys. Renaming or retyping any key is a breaking change to
// schema version 1.0.
const (
	KeyServiceName           = "service.name"
	KeyServiceVersion        = "service.version"
	KeyTelemetrySchema       = "waza.telemetry.schema_version"
	KeyRunID                 = "waza.run.id"
	KeyWorkID                = "waza.work.id"
	KeySchedulerWorkID       = "waza.scheduler.work_id"
	KeyExecutionID           = "waza.execution.id"
	KeyAttemptOrder          = "waza.attempt.order"
	KeyAttemptNative         = "waza.attempt.native"
	KeyAttemptStatus         = "waza.attempt.status"
	KeyAttemptParentRelation = "waza.attempt.parent_relationship"
	KeyLifecycleState        = "waza.lifecycle.state"
	KeyOutcome               = "waza.outcome"
	KeyExitCode              = "waza.exit_code"
	KeyFailureBoundary       = "waza.failure_boundary"
	KeyManifestSHA256        = "waza.manifest.sha256"
	KeyIdentityDigest        = "waza.identity.digest"
	KeyShardID               = "waza.shard.id"
	KeyTaskID                = "waza.task.id"
	KeyTrialID               = "waza.trial.id"
	KeyAttemptID             = "waza.attempt.id"
	KeySelectionTask         = "waza.selection.task"
	KeySelectionTrial        = "waza.selection.trial"
	KeySelectionAttempt      = "waza.selection.attempt"
	KeyExecutionMode         = "waza.execution.mode"
	KeyGenAIOperation        = "gen_ai.operation.name"
	KeyGenAIModel            = "gen_ai.request.model"
	KeyGenAIInputTokens      = "gen_ai.usage.input_tokens"
	KeyGenAIOutputTokens     = "gen_ai.usage.output_tokens"
	KeyGenAITokenType        = "gen_ai.token.type"
	KeyCacheReadTokens       = "waza.usage.cache_read_tokens"
	KeyCacheWriteTokens      = "waza.usage.cache_write_tokens"
	KeyRequestCount          = "waza.usage.request_count"
	KeyToolName              = "gen_ai.tool.name"
	KeyToolCallID            = "gen_ai.tool.call.id"
	KeyToolSuccess           = "waza.tool.success"
	KeyToolIndex             = "waza.tool.index"
	KeyArtifactName          = "waza.artifact.name"
	KeyArtifactSHA256        = "waza.artifact.sha256"
	KeyArtifactSize          = "waza.artifact.size_bytes"
	KeyArtifactMediaType     = "waza.artifact.media_type"
	KeyArtifactTerminal      = "waza.artifact.terminal"
	KeyAggregationSHA256     = "waza.aggregation.sha256"
	KeyAggregationState      = "waza.aggregation.state"
	KeyAggregationMembers    = "waza.aggregation.members"
	KeyAggregationVerified   = "waza.aggregation.verified_manifests"
	KeyMemberState           = "waza.aggregation.member_state"
	KeyLinkRelationship      = "waza.link.relationship"
	KeyEventName             = "event.name"
	KeyDiagnosticCode        = "waza.diagnostic.code"
	KeyDiagnosticSource      = "waza.diagnostic.source"
	KeySignal                = "waza.signal"
	KeyDropReason            = "waza.drop.reason"
	KeyMetricOverflow        = "otel.metric.overflow"
	KeyPromptSHA256          = "waza.prompt.sha256"
	KeyPromptLength          = "waza.prompt.length"
	KeyCompletionSHA256      = "waza.completion.sha256"
	KeyCompletionLength      = "waza.completion.length"
	KeyToolArgsSHA256        = "waza.tool.arguments.sha256"
	KeyToolArgsLength        = "waza.tool.arguments.length"
	KeyToolResultSHA256      = "waza.tool.result.sha256"
	KeyToolResultLength      = "waza.tool.result.length"
	KeyGenAIPrompt           = "gen_ai.prompt"
	KeyGenAICompletion       = "gen_ai.completion"
	KeyGenAIToolArguments    = "gen_ai.tool.arguments"
	KeyGenAIToolResult       = "gen_ai.tool.result"
)

var catalog = []Attribute{
	attr(KeyServiceName, "string", ClassBounded, OnResource),
	attr(KeyServiceVersion, "string", ClassBounded, OnResource),
	attr(KeyTelemetrySchema, "string", ClassBounded, OnResource),
	attr(KeyRunID, "string", ClassIdentifier, OnResource, OnSpan, OnLink),
	attr(KeyWorkID, "string", ClassIdentifier, OnResource, OnSpan, OnLink),
	attr(KeySchedulerWorkID, "string", ClassIdentifier, OnResource),
	attr(KeyExecutionID, "string", ClassIdentifier, OnSpan, OnLink, OnLog),
	attr(KeyAttemptOrder, "int", ClassMeasure, OnSpan),
	attr(KeyAttemptNative, "int", ClassMeasure, OnSpan),
	attr(KeyAttemptStatus, "string", ClassBounded, OnSpan, OnMetric),
	attr(KeyAttemptParentRelation, "string", ClassBounded, OnSpan, OnLink),
	attr(KeyLifecycleState, "string", ClassBounded, OnSpan),
	attr(KeyOutcome, "string", ClassBounded, OnSpan, OnMetric, OnLog),
	attr(KeyExitCode, "int", ClassBounded, OnSpan, OnLog),
	attr(KeyFailureBoundary, "string", ClassBounded, OnSpan, OnLog),
	attr(KeyManifestSHA256, "string", ClassIdentifier, OnSpan, OnLink, OnLog),
	attr(KeyIdentityDigest, "string", ClassIdentifier, OnSpan),
	attr(KeyShardID, "string", ClassIdentifier, OnSpan),
	attr(KeyTaskID, "string", ClassIdentifier, OnSpan),
	attr(KeyTrialID, "string", ClassIdentifier, OnSpan),
	attr(KeyAttemptID, "string", ClassIdentifier, OnSpan, OnLog),
	attr(KeySelectionTask, "string", ClassIdentifier, OnSpan),
	attr(KeySelectionTrial, "int", ClassMeasure, OnSpan),
	attr(KeySelectionAttempt, "int", ClassMeasure, OnSpan),
	attr(KeyExecutionMode, "string", ClassBounded, OnSpan),
	attr(KeyGenAIOperation, "string", ClassBounded, OnSpan),
	attr(KeyGenAIModel, "string", ClassBounded, OnSpan, OnMetric),
	attr(KeyGenAIInputTokens, "int", ClassMeasure, OnSpan),
	attr(KeyGenAIOutputTokens, "int", ClassMeasure, OnSpan),
	attr(KeyGenAITokenType, "string", ClassBounded, OnMetric),
	attr(KeyCacheReadTokens, "int", ClassMeasure, OnSpan),
	attr(KeyCacheWriteTokens, "int", ClassMeasure, OnSpan),
	attr(KeyRequestCount, "double", ClassMeasure, OnSpan),
	attr(KeyToolName, "string", ClassBounded, OnSpan, OnMetric),
	attr(KeyToolCallID, "string", ClassIdentifier, OnSpan),
	attr(KeyToolSuccess, "bool", ClassBounded, OnSpan, OnMetric),
	attr(KeyToolIndex, "int", ClassMeasure, OnSpan),
	attr(KeyArtifactName, "string", ClassBounded, OnSpan),
	attr(KeyArtifactSHA256, "string", ClassIdentifier, OnSpan),
	attr(KeyArtifactSize, "int", ClassMeasure, OnSpan),
	attr(KeyArtifactMediaType, "string", ClassBounded, OnSpan),
	attr(KeyArtifactTerminal, "bool", ClassBounded, OnSpan),
	attr(KeyAggregationSHA256, "string", ClassIdentifier, OnSpan, OnLog),
	attr(KeyAggregationState, "string", ClassBounded, OnSpan),
	attr(KeyAggregationMembers, "int", ClassMeasure, OnSpan),
	attr(KeyAggregationVerified, "int", ClassMeasure, OnSpan),
	attr(KeyMemberState, "string", ClassBounded, OnLink, OnMetric),
	attr(KeyLinkRelationship, "string", ClassBounded, OnLink),
	attr(KeyEventName, "string", ClassBounded, OnLog),
	attr(KeyDiagnosticCode, "string", ClassBounded, OnLog),
	attr(KeyDiagnosticSource, "string", ClassBounded, OnLog),
	attr(KeySignal, "string", ClassBounded, OnMetric, OnLog),
	attr(KeyDropReason, "string", ClassBounded, OnMetric, OnLog),
	attr(KeyMetricOverflow, "bool", ClassBounded, OnMetric),
	attr(KeyPromptSHA256, "string", ClassPayloadDigest, OnSpan),
	attr(KeyPromptLength, "int", ClassPayloadDigest, OnSpan),
	attr(KeyCompletionSHA256, "string", ClassPayloadDigest, OnSpan),
	attr(KeyCompletionLength, "int", ClassPayloadDigest, OnSpan),
	attr(KeyToolArgsSHA256, "string", ClassPayloadDigest, OnSpan),
	attr(KeyToolArgsLength, "int", ClassPayloadDigest, OnSpan),
	attr(KeyToolResultSHA256, "string", ClassPayloadDigest, OnSpan),
	attr(KeyToolResultLength, "int", ClassPayloadDigest, OnSpan),
	attr(KeyGenAIPrompt, "string", ClassPayload),
	attr(KeyGenAICompletion, "string", ClassPayload),
	attr(KeyGenAIToolArguments, "string", ClassPayload),
	attr(KeyGenAIToolResult, "string", ClassPayload),
}

var catalogIndex = func() map[string]Attribute {
	index := make(map[string]Attribute, len(catalog))
	for _, entry := range catalog {
		index[entry.Key] = entry
	}
	return index
}()

// Catalog returns a copy of the stable attribute catalog.
func Catalog() []Attribute {
	out := make([]Attribute, len(catalog))
	for i, entry := range catalog {
		entry.Placements = slices.Clone(entry.Placements)
		out[i] = entry
	}
	return out
}

// Signal names. Span, log event and metric names are stable in schema 1.0.
const (
	SpanWork        = "waza.work"
	SpanAttempt     = "waza.attempt"
	SpanTask        = "waza.task_attempt"
	SpanModel       = "waza.model"
	SpanTool        = "waza.tool"
	SpanArtifact    = "waza.artifact"
	SpanAggregation = "waza.aggregation"

	EventAttemptTerminal = "waza.attempt.terminal"
	EventDiagnostic      = "waza.diagnostic"
	EventSignalsDropped  = "waza.telemetry.dropped"

	MetricAttempts     = "waza.execution.attempts"
	MetricArtifacts    = "waza.execution.artifacts"
	MetricArtifactSize = "waza.execution.artifact.size"
	MetricTokens       = "gen_ai.client.token.usage"
	MetricToolCalls    = "waza.tool.calls"
	MetricMembers      = "waza.aggregation.members"
	MetricDropped      = "waza.telemetry.dropped"
)

// SpanNames, EventNames and MetricNames list every stable signal name.
var (
	SpanNames   = []string{SpanWork, SpanAttempt, SpanTask, SpanModel, SpanTool, SpanArtifact, SpanAggregation}
	EventNames  = []string{EventAttemptTerminal, EventDiagnostic, EventSignalsDropped}
	MetricNames = []string{MetricAttempts, MetricArtifacts, MetricArtifactSize, MetricTokens, MetricToolCalls, MetricMembers, MetricDropped}
)

// Drop reasons are a closed set reported in diagnostics and metrics.
const (
	DropPayloadExcluded       = "payload_excluded"
	DropUnregisteredAttribute = "unregistered_attribute"
	DropPlacement             = "attribute_placement"
	DropAttributeLimit        = "attribute_limit"
	DropAttributeTruncated    = "attribute_truncated"
	DropSpanLimit             = "span_limit"
	DropToolSpanLimit         = "tool_span_limit"
	DropLogLimit              = "log_limit"
	DropMetricCardinality     = "metric_cardinality"
	DropLinkLimit             = "link_limit"
)

// DropReasons lists the closed set of drop reasons.
var DropReasons = []string{
	DropPayloadExcluded, DropUnregisteredAttribute, DropPlacement, DropAttributeLimit,
	DropAttributeTruncated, DropSpanLimit, DropToolSpanLimit, DropLogLimit,
	DropMetricCardinality, DropLinkLimit,
}

// Limits bounds signal volume and attribute cardinality.
type Limits struct {
	MaxSpans            int
	MaxToolSpansPerTask int
	MaxLogs             int
	MaxAttributes       int
	MaxStringLength     int
	MaxSeriesPerMetric  int
	MaxLinks            int
}

// DefaultLimits returns the documented defaults.
func DefaultLimits() Limits {
	return Limits{
		MaxSpans:            2048,
		MaxToolSpansPerTask: 256,
		MaxLogs:             1024,
		MaxAttributes:       32,
		MaxStringLength:     256,
		MaxSeriesPerMetric:  32,
		MaxLinks:            128,
	}
}
