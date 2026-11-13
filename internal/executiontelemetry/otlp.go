package executiontelemetry

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"unicode/utf8"
)

// The types below are the OTLP/JSON encoding (opentelemetry-proto, JSON
// mapping): lowerCamelCase fields, hex trace/span IDs, 64-bit integers and
// nanosecond timestamps as decimal strings, and enums as integers.

type AnyValue struct {
	StringValue *string  `json:"stringValue,omitempty"`
	BoolValue   *bool    `json:"boolValue,omitempty"`
	IntValue    *string  `json:"intValue,omitempty"`
	DoubleValue *float64 `json:"doubleValue,omitempty"`
}

type KeyValue struct {
	Key   string   `json:"key"`
	Value AnyValue `json:"value"`
}

type Resource struct {
	Attributes             []KeyValue `json:"attributes"`
	DroppedAttributesCount int        `json:"droppedAttributesCount,omitempty"`
}

type Scope struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type Status struct {
	Code    int    `json:"code"`
	Message string `json:"message,omitempty"`
}

// OTLP status and kind enum values.
const (
	statusUnset  = 0
	statusOK     = 1
	statusError  = 2
	kindInternal = 1
)

type Link struct {
	TraceID                string     `json:"traceId"`
	SpanID                 string     `json:"spanId"`
	Attributes             []KeyValue `json:"attributes,omitempty"`
	DroppedAttributesCount int        `json:"droppedAttributesCount,omitempty"`
}

type Span struct {
	TraceID                string     `json:"traceId"`
	SpanID                 string     `json:"spanId"`
	ParentSpanID           string     `json:"parentSpanId,omitempty"`
	Name                   string     `json:"name"`
	Kind                   int        `json:"kind"`
	StartTimeUnixNano      string     `json:"startTimeUnixNano"`
	EndTimeUnixNano        string     `json:"endTimeUnixNano"`
	Attributes             []KeyValue `json:"attributes"`
	DroppedAttributesCount int        `json:"droppedAttributesCount,omitempty"`
	Links                  []Link     `json:"links,omitempty"`
	DroppedLinksCount      int        `json:"droppedLinksCount,omitempty"`
	Status                 Status     `json:"status"`
}

type ScopeSpans struct {
	Scope Scope  `json:"scope"`
	Spans []Span `json:"spans"`
}

type ResourceSpans struct {
	Resource   Resource     `json:"resource"`
	ScopeSpans []ScopeSpans `json:"scopeSpans"`
}

// TracesData is an OTLP ExportTraceServiceRequest.
type TracesData struct {
	ResourceSpans []ResourceSpans `json:"resourceSpans"`
}

type NumberDataPoint struct {
	Attributes        []KeyValue `json:"attributes"`
	StartTimeUnixNano string     `json:"startTimeUnixNano"`
	TimeUnixNano      string     `json:"timeUnixNano"`
	AsInt             string     `json:"asInt"`
}

type Sum struct {
	DataPoints             []NumberDataPoint `json:"dataPoints"`
	AggregationTemporality int               `json:"aggregationTemporality"`
	IsMonotonic            bool              `json:"isMonotonic"`
}

type Metric struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Unit        string `json:"unit"`
	Sum         Sum    `json:"sum"`
}

type ScopeMetrics struct {
	Scope   Scope    `json:"scope"`
	Metrics []Metric `json:"metrics"`
}

type ResourceMetrics struct {
	Resource     Resource       `json:"resource"`
	ScopeMetrics []ScopeMetrics `json:"scopeMetrics"`
}

// MetricsData is an OTLP ExportMetricsServiceRequest.
type MetricsData struct {
	ResourceMetrics []ResourceMetrics `json:"resourceMetrics"`
}

type LogRecord struct {
	TimeUnixNano           string     `json:"timeUnixNano"`
	ObservedTimeUnixNano   string     `json:"observedTimeUnixNano"`
	SeverityNumber         int        `json:"severityNumber"`
	SeverityText           string     `json:"severityText"`
	Body                   AnyValue   `json:"body"`
	Attributes             []KeyValue `json:"attributes"`
	DroppedAttributesCount int        `json:"droppedAttributesCount,omitempty"`
	TraceID                string     `json:"traceId,omitempty"`
	SpanID                 string     `json:"spanId,omitempty"`
}

type ScopeLogs struct {
	Scope      Scope       `json:"scope"`
	LogRecords []LogRecord `json:"logRecords"`
}

type ResourceLogs struct {
	Resource  Resource    `json:"resource"`
	ScopeLogs []ScopeLogs `json:"scopeLogs"`
}

// LogsData is an OTLP ExportLogsServiceRequest.
type LogsData struct {
	ResourceLogs []ResourceLogs `json:"resourceLogs"`
}

// OTLP severity numbers.
const (
	severityInfo  = 9
	severityWarn  = 13
	severityError = 17
)

func str(key, value string) KeyValue { return KeyValue{Key: key, Value: AnyValue{StringValue: &value}} }
func boolean(key string, value bool) KeyValue {
	return KeyValue{Key: key, Value: AnyValue{BoolValue: &value}}
}
func integer(key string, value int64) KeyValue {
	text := strconv.FormatInt(value, 10)
	return KeyValue{Key: key, Value: AnyValue{IntValue: &text}}
}
func double(key string, value float64) KeyValue {
	return KeyValue{Key: key, Value: AnyValue{DoubleValue: &value}}
}

// payloadDigest returns digest surrogates for a payload slot under
// PayloadDigest and nothing otherwise. Raw payloads are never returned.
func payloadDigest(policy PayloadPolicy, hashKey, lengthKey, value string) []KeyValue {
	if policy != PayloadDigest || value == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(value))
	return []KeyValue{str(hashKey, hex.EncodeToString(sum[:])), integer(lengthKey, int64(len(value)))}
}

// sanitizer enforces the attribute catalog, payload policy and limits, and
// counts every dropped or truncated attribute by reason.
type sanitizer struct {
	policy  PayloadPolicy
	limits  Limits
	dropped map[string]map[string]int
}

func newSanitizer(policy PayloadPolicy, limits Limits) *sanitizer {
	return &sanitizer{policy: policy, limits: limits, dropped: map[string]map[string]int{}}
}

func (s *sanitizer) drop(signal, reason string, count int) {
	if count <= 0 {
		return
	}
	if s.dropped[signal] == nil {
		s.dropped[signal] = map[string]int{}
	}
	s.dropped[signal][reason] += count
}

// clean returns the admitted attributes and the number dropped.
func (s *sanitizer) clean(signal string, placement Placement, attrs []KeyValue) ([]KeyValue, int) {
	out := make([]KeyValue, 0, len(attrs))
	dropped := 0
	for _, kv := range attrs {
		entry, known := catalogIndex[kv.Key]
		switch {
		case !known:
			s.drop(signal, DropUnregisteredAttribute, 1)
			dropped++
			continue
		case entry.Class == ClassPayload || (entry.Class == ClassPayloadDigest && s.policy != PayloadDigest):
			s.drop(signal, DropPayloadExcluded, 1)
			dropped++
			continue
		case !hasPlacement(entry, placement):
			s.drop(signal, DropPlacement, 1)
			dropped++
			continue
		case len(out) >= s.limits.MaxAttributes:
			s.drop(signal, DropAttributeLimit, 1)
			dropped++
			continue
		}
		if kv.Value.StringValue != nil && len(*kv.Value.StringValue) > s.limits.MaxStringLength {
			truncated := truncateUTF8(*kv.Value.StringValue, s.limits.MaxStringLength)
			kv.Value.StringValue = &truncated
			s.drop(signal, DropAttributeTruncated, 1)
		}
		out = append(out, kv)
	}
	return out, dropped
}

func hasPlacement(entry Attribute, placement Placement) bool {
	for _, p := range entry.Placements {
		if p == placement {
			return true
		}
	}
	return false
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}
