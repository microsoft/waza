package executiontelemetry

import (
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnerterminal"
)

// Options configure one derivation. Zero values select documented defaults.
type Options struct {
	Policy         PayloadPolicy
	Limits         Limits
	ServiceVersion string
	Now            time.Time
}

// Bundle holds the three OTLP/JSON signal documents and their report.
type Bundle struct {
	Traces  TracesData
	Metrics MetricsData
	Logs    LogsData
	Report  Report
}

// AttemptStatusObserved marks an attempt known only from its terminal and
// optional attempt sidecar, without a ledger lineage record.
const AttemptStatusObserved = "observed"

// Build derives correlated traces, metrics and logs from an admitted source.
// It is deterministic for the same source and options.
func Build(src *Source, opts Options) *Bundle {
	if opts.Policy == "" {
		opts.Policy = PayloadExclude
	}
	if opts.Limits == (Limits{}) {
		opts.Limits = DefaultLimits()
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now().UTC()
	}
	if opts.ServiceVersion == "" {
		opts.ServiceVersion = "dev"
	}
	b := &builder{
		src: src, opts: opts, san: newSanitizer(opts.Policy, opts.Limits),
		spanNames: map[string]int{}, logNames: map[string]int{}, metrics: map[string]*metricBuilder{},
	}
	return b.build()
}

type builder struct {
	src       *Source
	opts      Options
	san       *sanitizer
	traceID   string
	spans     []Span
	spanNames map[string]int
	logs      []LogRecord
	logNames  map[string]int
	metrics   map[string]*metricBuilder
	order     []string
}

func nanos(t time.Time) string { return strconv.FormatInt(t.UnixNano(), 10) }

func statusFor(outcome runnerterminal.Outcome) Status {
	switch outcome {
	case "":
		return Status{Code: statusUnset}
	case runnerterminal.Success:
		return Status{Code: statusOK}
	case runnerterminal.EvaluationFailure:
		return Status{Code: statusUnset}
	default:
		return Status{Code: statusError, Message: string(outcome)}
	}
}

func severityFor(outcome runnerterminal.Outcome) (int, string) {
	switch outcome {
	case runnerterminal.Success:
		return severityInfo, "INFO"
	case runnerterminal.EvaluationFailure:
		return severityWarn, "WARN"
	default:
		return severityError, "ERROR"
	}
}

func (b *builder) addSpan(name, spanID, parent string, start, end time.Time, attrs []KeyValue, status Status, links []Link) bool {
	if len(b.spans) >= b.opts.Limits.MaxSpans {
		b.san.drop(SignalTraces, DropSpanLimit, 1)
		return false
	}
	clean, dropped := b.san.clean(SignalTraces, OnSpan, attrs)
	span := Span{
		TraceID: b.traceID, SpanID: spanID, ParentSpanID: parent, Name: name, Kind: kindInternal,
		StartTimeUnixNano: nanos(start), EndTimeUnixNano: nanos(end),
		Attributes: clean, DroppedAttributesCount: dropped, Status: status,
	}
	for i, link := range links {
		if i >= b.opts.Limits.MaxLinks {
			span.DroppedLinksCount = len(links) - i
			b.san.drop(SignalTraces, DropLinkLimit, span.DroppedLinksCount)
			break
		}
		link.Attributes, link.DroppedAttributesCount = b.san.clean(SignalTraces, OnLink, link.Attributes)
		span.Links = append(span.Links, link)
	}
	b.spans = append(b.spans, span)
	b.spanNames[name]++
	return true
}

func (b *builder) addLog(event, spanID string, outcome runnerterminal.Outcome, attrs []KeyValue) {
	if len(b.logs) >= b.opts.Limits.MaxLogs {
		b.san.drop(SignalLogs, DropLogLimit, 1)
		return
	}
	severity, text := severityFor(outcome)
	if outcome == "" {
		severity, text = severityWarn, "WARN"
	}
	clean, dropped := b.san.clean(SignalLogs, OnLog, append([]KeyValue{str(KeyEventName, event)}, attrs...))
	body := event
	b.logs = append(b.logs, LogRecord{
		TimeUnixNano: nanos(b.opts.Now), ObservedTimeUnixNano: nanos(b.opts.Now),
		SeverityNumber: severity, SeverityText: text, Body: AnyValue{StringValue: &body},
		Attributes: clean, DroppedAttributesCount: dropped, TraceID: b.traceID, SpanID: spanID,
	})
	b.logNames[event]++
}

// payload adds digest surrogates under PayloadDigest and otherwise records
// that a payload slot was excluded.
func (b *builder) payload(hashKey, lengthKey, value string) []KeyValue {
	if value == "" {
		return nil
	}
	if b.opts.Policy != PayloadDigest {
		b.san.drop(SignalTraces, DropPayloadExcluded, 1)
		return nil
	}
	return payloadDigest(b.opts.Policy, hashKey, lengthKey, value)
}

func (b *builder) build() *Bundle {
	src := b.src
	identity := *src.Terminal.Identity
	selection := *src.Terminal.Selection
	b.traceID = TraceID(identity.Run, identity.Trial)
	rootID := WorkSpanID(identity.Run, identity.Trial)
	now := b.opts.Now

	resourceAttrs := []KeyValue{
		str(KeyServiceName, "waza"), str(KeyServiceVersion, b.opts.ServiceVersion),
		str(KeyTelemetrySchema, SchemaVersion), str(KeyRunID, identity.Run), str(KeyWorkID, identity.Trial),
	}
	if src.SchedulerWorkID != "" {
		resourceAttrs = append(resourceAttrs, str(KeySchedulerWorkID, src.SchedulerWorkID))
	}
	cleanResource, droppedResource := b.san.clean(SignalTraces, OnResource, resourceAttrs)
	resource := Resource{Attributes: cleanResource, DroppedAttributesCount: droppedResource}

	report := Report{
		Kind: Kind, SchemaVersion: SchemaVersion, PayloadPolicy: b.opts.Policy,
		Resource: map[string]string{},
		Correlation: Correlation{
			TraceID: b.traceID, RootSpanID: rootID, Run: identity.Run, Work: identity.Trial,
			SchedulerWorkID: src.SchedulerWorkID, IdentityDigest: identity.CanonicalDigest,
			Shard: identity.Shard, Task: identity.Task, Trial: identity.Trial, Attempt: identity.Attempt,
			Attempts: []AttemptCorrelation{}, Artifacts: []ArtifactCorrelation{},
		},
		Dropped: []Drop{}, Exports: []Export{},
	}
	for _, kv := range cleanResource {
		if kv.Value.StringValue != nil {
			report.Resource[kv.Key] = *kv.Value.StringValue
		}
	}
	report.Sources = []SourceState{
		{"terminal", SourcePresent}, {"manifest", presence(src.Manifest != nil)},
		{"attempt", presence(src.Attempt != nil)}, {"lineage", presence(src.Lineage != nil)},
		{"report", src.ReportState}, {"aggregate", presence(src.Aggregate != nil)},
	}

	attempts, times := b.attempts()
	start, end := now, now
	for i, t := range times {
		if i == 0 || t[0].Before(start) {
			start = t[0]
		}
		if i == 0 || t[1].After(end) {
			end = t[1]
		}
	}
	rootAttrs := []KeyValue{str(KeyRunID, identity.Run), str(KeyWorkID, identity.Trial)}
	b.addSpan(SpanWork, rootID, "", start, end, rootAttrs, Status{Code: statusUnset}, nil)

	known := map[string]bool{}
	for _, a := range attempts {
		if a.ExecutionID != "" {
			known[a.ExecutionID] = true
		}
	}
	taskParent, taskTimes := rootID, [2]time.Time{now, now}
	for i, a := range attempts {
		parent := rootID
		if a.ParentExecutionID != "" && known[a.ParentExecutionID] {
			parent = SpanID(SpanAttempt, a.ParentExecutionID)
		}
		attrs := []KeyValue{integer(KeyAttemptOrder, int64(i+1)), integer(KeyAttemptNative, int64(a.NativeAttempt)), str(KeyAttemptStatus, a.Status)}
		if a.ExecutionID != "" {
			attrs = append(attrs, str(KeyExecutionID, a.ExecutionID))
		}
		if a.Relationship != "" {
			attrs = append(attrs, str(KeyAttemptParentRelation, a.Relationship))
		}
		if a.lifecycle != "" {
			attrs = append(attrs, str(KeyLifecycleState, a.lifecycle))
		}
		if a.Outcome != "" {
			attrs = append(attrs, str(KeyOutcome, a.Outcome), integer(KeyExitCode, int64(a.exitCode)), str(KeyFailureBoundary, a.boundary))
		}
		if a.ManifestSHA256 != "" {
			attrs = append(attrs, str(KeyManifestSHA256, a.ManifestSHA256))
		}
		b.addSpan(SpanAttempt, a.SpanID, parent, times[i][0], times[i][1], attrs, statusFor(runnerterminal.Outcome(a.Outcome)), nil)
		if a.Selected {
			taskParent, taskTimes = a.SpanID, times[i]
		}
		report.Correlation.Attempts = append(report.Correlation.Attempts, a.AttemptCorrelation)
	}

	selectedExecution := ""
	for _, a := range attempts {
		if a.Selected {
			selectedExecution = a.ExecutionID
		}
	}
	taskKey := selectedExecution
	if taskKey == "" && src.ManifestSHA256 != "" {
		taskKey = manifestKey(src.ManifestSHA256)
	}
	taskID := SpanID(SpanTask, taskKey, identity.Attempt)
	report.Correlation.TaskSpanID = taskID
	mode := ""
	if src.Manifest != nil {
		mode = src.Manifest.Binding.ExecutionMode
	} else if src.Terminal.Observation != nil {
		mode = src.Terminal.Observation.ExecutionMode
	}
	taskAttrs := []KeyValue{
		str(KeyIdentityDigest, identity.CanonicalDigest), str(KeyShardID, identity.Shard), str(KeyTaskID, identity.Task),
		str(KeyTrialID, identity.Trial), str(KeyAttemptID, identity.Attempt), str(KeySelectionTask, selection.Task),
		integer(KeySelectionTrial, int64(selection.Trial)), integer(KeySelectionAttempt, int64(selection.Attempt)),
		str(KeyOutcome, string(src.Terminal.Outcome)), integer(KeyExitCode, int64(src.Terminal.ExitCode)),
		str(KeyFailureBoundary, src.Terminal.FailureBoundary),
	}
	if selectedExecution != "" {
		taskAttrs = append(taskAttrs, str(KeyExecutionID, selectedExecution))
	}
	if mode != "" {
		taskAttrs = append(taskAttrs, str(KeyExecutionMode, mode))
	}
	if src.Run != nil {
		taskAttrs = append(taskAttrs, b.payload(KeyPromptSHA256, KeyPromptLength, src.Run.Prompt)...)
		taskAttrs = append(taskAttrs, b.payload(KeyCompletionSHA256, KeyCompletionLength, src.Run.FinalOutput)...)
	}
	b.addSpan(SpanTask, taskID, taskParent, taskTimes[0], taskTimes[1], taskAttrs, statusFor(src.Terminal.Outcome), nil)

	b.aggregation(rootID, &report)
	b.artifacts(taskID, taskTimes, &report)
	b.models(taskID, taskTimes)
	b.tools(taskID, taskTimes)
	b.terminalLogs(attempts, taskID)

	for _, a := range attempts {
		outcome := a.Outcome
		if outcome == "" {
			outcome = "unavailable"
		}
		b.metric(MetricAttempts, "{attempt}", "Execution attempts by terminal outcome and ledger status").
			add(b, []KeyValue{str(KeyOutcome, outcome), str(KeyAttemptStatus, a.Status)}, 1)
	}
	b.finishDrops(&report)

	bundle := &Bundle{Report: report}
	scope := Scope{Name: ScopeName, Version: SchemaVersion}
	bundle.Traces = TracesData{ResourceSpans: []ResourceSpans{{Resource: resource, ScopeSpans: []ScopeSpans{{Scope: scope, Spans: b.spans}}}}}
	bundle.Logs = LogsData{ResourceLogs: []ResourceLogs{{Resource: resource, ScopeLogs: []ScopeLogs{{Scope: scope, LogRecords: b.logs}}}}}
	metrics := make([]Metric, 0, len(b.order))
	series := map[string]int{}
	total := 0
	for _, name := range b.order {
		m := b.metrics[name].metric(now)
		metrics = append(metrics, m)
		series[name] = len(m.Sum.DataPoints)
		total += len(m.Sum.DataPoints)
	}
	bundle.Metrics = MetricsData{ResourceMetrics: []ResourceMetrics{{Resource: resource, ScopeMetrics: []ScopeMetrics{{Scope: scope, Metrics: metrics}}}}}
	bundle.Report.Signals = Signals{
		Traces:  SignalCount{Emitted: len(b.spans), Dropped: b.droppedTotal(SignalTraces), Names: b.spanNames},
		Metrics: SignalCount{Emitted: total, Dropped: b.droppedTotal(SignalMetrics), Names: series},
		Logs:    SignalCount{Emitted: len(b.logs), Dropped: b.droppedTotal(SignalLogs), Names: b.logNames},
	}
	return bundle
}

func presence(ok bool) string {
	if ok {
		return SourcePresent
	}
	return SourceUnavailable
}

type attemptView struct {
	AttemptCorrelation
	lifecycle string
	exitCode  int
	boundary  string
}

// attempts projects lineage attempts (or one observed attempt) and marks the
// attempt that produced the terminal as selected.
func (b *builder) attempts() ([]attemptView, [][2]time.Time) {
	src := b.src
	execution := ""
	if src.Attempt != nil {
		execution = src.Attempt.ExecutionID
	}
	var views []attemptView
	var times [][2]time.Time
	selected := false
	if src.Lineage != nil {
		for i, a := range src.Lineage.Attempts {
			v := attemptView{AttemptCorrelation: AttemptCorrelation{
				SpanID: SpanID(SpanAttempt, a.ExecutionID), ExecutionID: a.ExecutionID,
				NativeAttempt: a.NativeAttempt, Status: a.Status, ManifestSHA256: a.ManifestDigest,
			}, lifecycle: a.Lifecycle.State}
			if a.Parent != nil {
				v.ParentExecutionID, v.Relationship = a.Parent.ExecutionID, a.Parent.Relationship
			}
			if a.Terminal != nil {
				v.Outcome, v.exitCode, v.boundary = string(a.Terminal.Outcome), a.Terminal.ExitCode, a.Terminal.FailureBoundary
			}
			if i == src.LineageIndex {
				v.Selected, selected = true, true
				b.applyTerminal(&v)
			}
			views = append(views, v)
			times = append(times, b.lifecycleTimes(a))
		}
	}
	if !selected {
		v := attemptView{AttemptCorrelation: AttemptCorrelation{
			ExecutionID: execution, NativeAttempt: src.Terminal.Selection.Attempt,
			Status: AttemptStatusObserved, ManifestSHA256: src.ManifestSHA256, Selected: true,
		}}
		switch {
		case execution != "":
			v.SpanID = SpanID(SpanAttempt, execution)
		case src.ManifestSHA256 != "":
			v.SpanID = SpanID(SpanAttempt, manifestKey(src.ManifestSHA256))
		default:
			v.SpanID = SpanID(SpanAttempt, src.Terminal.Identity.Attempt)
		}
		if src.Attempt != nil && src.Attempt.Parent != nil {
			v.ParentExecutionID, v.Relationship = src.Attempt.Parent.ExecutionID, src.Attempt.Parent.Relationship
		}
		b.applyTerminal(&v)
		views = append(views, v)
		times = append(times, [2]time.Time{b.opts.Now, b.opts.Now})
	}
	return views, times
}

// manifestKey distinguishes publications of the same native selection when no
// execution ID is known: each publication has its own immutable manifest.
func manifestKey(digest string) string { return "manifest:" + digest }

func (b *builder) applyTerminal(v *attemptView) {
	t := b.src.Terminal
	v.Outcome, v.exitCode, v.boundary = string(t.Outcome), t.ExitCode, t.FailureBoundary
}

func (b *builder) lifecycleTimes(a executionledger.LineageAttempt) [2]time.Time {
	start, end := b.opts.Now, b.opts.Now
	if a.Lifecycle.StartedAt != nil {
		start = *a.Lifecycle.StartedAt
		end = start
	}
	if a.Lifecycle.LastEventAt != nil && !a.Lifecycle.LastEventAt.Before(start) {
		end = *a.Lifecycle.LastEventAt
	}
	return [2]time.Time{start, end}
}

func (b *builder) aggregation(rootID string, report *Report) {
	agg := b.src.Aggregate
	if agg == nil {
		return
	}
	spanID := SpanID(SpanAggregation, b.src.AggregateSHA256)
	corr := &AggregationCorrelation{SpanID: spanID, SHA256: b.src.AggregateSHA256, State: agg.State, Members: []MemberLink{}}
	var links []Link
	for _, m := range agg.Members {
		link := MemberLink{ManifestSHA256: m.Manifest, State: m.State, ExecutionID: m.Execution}
		if m.Binding != nil {
			link.TraceID = TraceID(m.Binding.Identity.Run, m.Binding.Identity.Trial)
			link.SpanID = WorkSpanID(m.Binding.Identity.Run, m.Binding.Identity.Trial)
			attrs := []KeyValue{str(KeyLinkRelationship, "aggregation_member"), str(KeyMemberState, m.State),
				str(KeyManifestSHA256, m.Manifest), str(KeyRunID, m.Binding.Identity.Run), str(KeyWorkID, m.Binding.Identity.Trial)}
			if m.Execution != "" {
				attrs = append(attrs, str(KeyExecutionID, m.Execution))
			}
			links = append(links, Link{TraceID: link.TraceID, SpanID: link.SpanID, Attributes: attrs})
		}
		corr.Members = append(corr.Members, link)
		b.metric(MetricMembers, "{member}", "Aggregation members by admission state").add(b, []KeyValue{str(KeyMemberState, m.State)}, 1)
		for _, code := range m.Diagnostics {
			b.addLog(EventDiagnostic, spanID, "", []KeyValue{str(KeyDiagnosticSource, "aggregation"), str(KeyDiagnosticCode, code), str(KeyManifestSHA256, m.Manifest)})
		}
	}
	for _, code := range agg.Usage.Diagnostics {
		b.addLog(EventDiagnostic, spanID, "", []KeyValue{str(KeyDiagnosticSource, "aggregation"), str(KeyDiagnosticCode, code), str(KeyAggregationSHA256, b.src.AggregateSHA256)})
	}
	attrs := []KeyValue{str(KeyAggregationSHA256, b.src.AggregateSHA256), str(KeyAggregationState, agg.State),
		integer(KeyAggregationMembers, int64(agg.Counts.Members)), integer(KeyAggregationVerified, int64(agg.Counts.VerifiedManifests))}
	status := Status{Code: statusOK}
	if agg.State != "complete" {
		status = Status{Code: statusError, Message: agg.State}
	}
	if b.addSpan(SpanAggregation, spanID, rootID, b.opts.Now, b.opts.Now, attrs, status, links) {
		report.Correlation.Aggregation = corr
	}
}

func (b *builder) artifacts(taskID string, times [2]time.Time, report *Report) {
	m := b.src.Manifest
	if m == nil {
		return
	}
	for _, a := range m.Artifacts {
		spanID := SpanID(SpanArtifact, b.src.ManifestSHA256, a.Name)
		terminal := a.Name == m.TerminalArtifact
		attrs := []KeyValue{str(KeyArtifactName, a.Name), str(KeyArtifactSHA256, a.SHA256), integer(KeyArtifactSize, a.SizeBytes),
			str(KeyArtifactMediaType, a.MediaType), boolean(KeyArtifactTerminal, terminal), str(KeyManifestSHA256, b.src.ManifestSHA256)}
		if b.addSpan(SpanArtifact, spanID, taskID, times[1], times[1], attrs, Status{Code: statusUnset}, nil) {
			report.Correlation.Artifacts = append(report.Correlation.Artifacts, ArtifactCorrelation{
				SpanID: spanID, Name: a.Name, SHA256: a.SHA256, SizeBytes: a.SizeBytes, Terminal: terminal,
			})
		}
		b.metric(MetricArtifacts, "{artifact}", "Published immutable artifacts").add(b, nil, 1)
		b.metric(MetricArtifactSize, "By", "Published immutable artifact bytes").add(b, nil, a.SizeBytes)
	}
}

func (b *builder) models(taskID string, times [2]time.Time) {
	run := b.src.Run
	if run == nil {
		return
	}
	usage := run.Usage
	if usage == nil {
		usage = run.SessionDigest.Usage
	}
	if usage == nil {
		return
	}
	for _, model := range slices.Sorted(maps.Keys(usage.ModelMetrics)) {
		u := usage.ModelMetrics[model]
		attrs := []KeyValue{str(KeyGenAIOperation, "chat"), str(KeyGenAIModel, model),
			integer(KeyGenAIInputTokens, int64(u.InputTokens)), integer(KeyGenAIOutputTokens, int64(u.OutputTokens)),
			integer(KeyCacheReadTokens, int64(u.CacheReadTokens)), integer(KeyCacheWriteTokens, int64(u.CacheWriteTokens)),
			double(KeyRequestCount, u.RequestCount)}
		b.addSpan(SpanModel, SpanID(SpanModel, taskID, model), taskID, times[0], times[1], attrs, Status{Code: statusUnset}, nil)
		tokens := b.metric(MetricTokens, "{token}", "Recorded model token usage by token type")
		for _, kind := range []struct {
			name  string
			value int
		}{{"input", u.InputTokens}, {"output", u.OutputTokens}, {"cache_read", u.CacheReadTokens}, {"cache_write", u.CacheWriteTokens}} {
			tokens.add(b, []KeyValue{str(KeyGenAIModel, model), str(KeyGenAITokenType, kind.name)}, int64(kind.value))
		}
	}
}

func (b *builder) tools(taskID string, times [2]time.Time) {
	run := b.src.Run
	if run == nil {
		return
	}
	for i, call := range toolActivity(run) {
		b.metric(MetricToolCalls, "{call}", "Recorded tool calls by tool name and success").
			add(b, []KeyValue{str(KeyToolName, call.Name), boolean(KeyToolSuccess, call.Success)}, 1)
		if i >= b.opts.Limits.MaxToolSpansPerTask {
			b.san.drop(SignalTraces, DropToolSpanLimit, 1)
			continue
		}
		attrs := []KeyValue{str(KeyGenAIOperation, "execute_tool"), str(KeyToolName, call.Name),
			boolean(KeyToolSuccess, call.Success), integer(KeyToolIndex, int64(i))}
		if call.ID != "" {
			attrs = append(attrs, str(KeyToolCallID, call.ID))
		}
		attrs = append(attrs, b.payload(KeyToolArgsSHA256, KeyToolArgsLength, marshalPayload(call.Arguments))...)
		if call.Result != nil {
			attrs = append(attrs, b.payload(KeyToolResultSHA256, KeyToolResultLength, marshalPayload(call.Result))...)
		}
		status := Status{Code: statusUnset}
		if !call.Success {
			status = Status{Code: statusError, Message: "tool call reported failure"}
		}
		b.addSpan(SpanTool, SpanID(SpanTool, taskID, strconv.Itoa(i)), taskID, times[0], times[1], attrs, status, nil)
	}
}

type toolCall struct {
	ID, Name          string
	Success           bool
	Arguments, Result any
}

// toolActivity prefers the normalized ToolEvents record and falls back to the
// legacy SessionDigest.ToolCalls for results written before schema 1.1.
func toolActivity(run *models.RunResult) []toolCall {
	var calls []toolCall
	if len(run.ToolEvents) > 0 {
		for _, event := range run.ToolEvents {
			calls = append(calls, toolCall{ID: event.ToolCallID, Name: event.ToolName, Success: event.Success,
				Arguments: event.Args, Result: event.Result})
		}
		return calls
	}
	for _, call := range run.SessionDigest.ToolCalls {
		tc := toolCall{ID: call.ID, Name: call.Name, Success: call.Success, Arguments: call.Arguments}
		if call.Result != nil {
			tc.Result = call.Result
		}
		calls = append(calls, tc)
	}
	return calls
}

func marshalPayload(value any) string {
	data, err := json.Marshal(value)
	if err != nil || string(data) == "null" {
		return ""
	}
	var fields map[string]any
	if json.Unmarshal(data, &fields) == nil {
		empty := true
		for _, v := range fields {
			if v != nil && v != "" {
				empty = false
				break
			}
		}
		if empty {
			return ""
		}
	}
	return string(data)
}

func (b *builder) terminalLogs(attempts []attemptView, taskID string) {
	for _, a := range attempts {
		if a.Outcome == "" {
			continue
		}
		attrs := []KeyValue{str(KeyOutcome, a.Outcome), integer(KeyExitCode, int64(a.exitCode)), str(KeyFailureBoundary, a.boundary)}
		if a.ExecutionID != "" {
			attrs = append(attrs, str(KeyExecutionID, a.ExecutionID))
		}
		if a.ManifestSHA256 != "" {
			attrs = append(attrs, str(KeyManifestSHA256, a.ManifestSHA256))
		}
		spanID := a.SpanID
		if a.Selected {
			spanID = taskID
			attrs = append(attrs, str(KeyAttemptID, b.src.Terminal.Identity.Attempt))
		}
		b.addLog(EventAttemptTerminal, spanID, runnerterminal.Outcome(a.Outcome), attrs)
	}
	for _, d := range b.src.Terminal.Diagnostics {
		b.addLog(EventDiagnostic, taskID, b.src.Terminal.Outcome, []KeyValue{str(KeyDiagnosticSource, "terminal"), str(KeyDiagnosticCode, d.Code)})
	}
	if b.src.Lineage != nil {
		for _, d := range b.src.Lineage.Diagnostics {
			attrs := []KeyValue{str(KeyDiagnosticSource, "lineage"), str(KeyDiagnosticCode, d.Code)}
			spanID := WorkSpanID(b.src.Lineage.Logical.Run, b.src.Lineage.Logical.Work)
			if len(d.ExecutionIDs) > 0 {
				attrs = append(attrs, str(KeyExecutionID, d.ExecutionIDs[0]))
				spanID = SpanID(SpanAttempt, d.ExecutionIDs[0])
			}
			b.addLog(EventDiagnostic, spanID, "", attrs)
		}
	}
}

func (b *builder) drops() []Drop {
	out := []Drop{}
	for _, signal := range []string{SignalTraces, SignalMetrics, SignalLogs} {
		for _, reason := range slices.Sorted(maps.Keys(b.san.dropped[signal])) {
			out = append(out, Drop{Signal: signal, Reason: reason, Count: b.san.dropped[signal][reason]})
		}
	}
	return out
}

// finishDrops emits drop summaries as a metric and logs. Drops caused while
// emitting these summaries (for example the log limit) appear in the report
// but cannot appear in the summaries themselves.
func (b *builder) finishDrops(report *Report) {
	summary := b.drops()
	for _, d := range summary {
		b.metric(MetricDropped, "{signal}", "Signals or attributes dropped or truncated by safeguards").
			add(b, []KeyValue{str(KeySignal, d.Signal), str(KeyDropReason, d.Reason)}, int64(d.Count))
	}
	for _, d := range summary {
		b.addLog(EventSignalsDropped, WorkSpanID(report.Correlation.Run, report.Correlation.Work), "",
			[]KeyValue{str(KeySignal, d.Signal), str(KeyDropReason, d.Reason)})
	}
	report.Dropped = b.drops()
}

func (b *builder) droppedTotal(signal string) int {
	total := 0
	for reason, count := range b.san.dropped[signal] {
		if reason != DropAttributeTruncated {
			total += count
		}
	}
	return total
}

type metricBuilder struct {
	name, unit, description string
	points                  map[string]*NumberDataPoint
	values                  map[string]int64
	keys                    []string
}

func (b *builder) metric(name, unit, description string) *metricBuilder {
	if m, ok := b.metrics[name]; ok {
		return m
	}
	m := &metricBuilder{name: name, unit: unit, description: description, points: map[string]*NumberDataPoint{}, values: map[string]int64{}}
	b.metrics[name] = m
	b.order = append(b.order, name)
	return m
}

const overflowKey = "\x00overflow"

func (m *metricBuilder) add(b *builder, attrs []KeyValue, value int64) {
	clean, _ := b.san.clean(SignalMetrics, OnMetric, attrs)
	key := seriesKey(clean)
	if _, ok := m.points[key]; !ok {
		// Reserve one series for the overflow bucket so the total never
		// exceeds MaxSeriesPerMetric.
		if len(m.points) >= b.opts.Limits.MaxSeriesPerMetric-1 {
			b.san.drop(SignalMetrics, DropMetricCardinality, 1)
			key, clean = overflowKey, []KeyValue{boolean(KeyMetricOverflow, true)}
		}
		if _, ok := m.points[key]; !ok {
			m.points[key] = &NumberDataPoint{Attributes: clean}
			m.keys = append(m.keys, key)
		}
	}
	m.values[key] += value
}

func seriesKey(attrs []KeyValue) string {
	parts := make([]string, 0, len(attrs))
	for _, kv := range attrs {
		data, _ := json.Marshal(kv.Value)
		parts = append(parts, kv.Key+"="+string(data))
	}
	slices.Sort(parts)
	return strings.Join(parts, "\x00")
}

func (m *metricBuilder) metric(now time.Time) Metric {
	points := make([]NumberDataPoint, 0, len(m.keys))
	for _, key := range m.keys {
		p := *m.points[key]
		p.StartTimeUnixNano, p.TimeUnixNano = nanos(now), nanos(now)
		p.AsInt = strconv.FormatInt(m.values[key], 10)
		if p.Attributes == nil {
			p.Attributes = []KeyValue{}
		}
		points = append(points, p)
	}
	return Metric{Name: m.name, Description: m.description, Unit: m.unit,
		Sum: Sum{DataPoints: points, AggregationTemporality: 2, IsMonotonic: true}}
}
