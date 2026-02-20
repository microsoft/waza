import { useState, useMemo } from "react";
import {
  Wrench,
  Play,
  CheckCircle2,
  XCircle,
  MessageSquare,
  AlertTriangle,
  Terminal,
  ChevronDown,
  ChevronRight,
  Brain,
  FileText,
  BarChart3,
  List,
} from "lucide-react";
import type { TaskResult, TranscriptEvent } from "../api/client";
import SessionDigestCard from "./SessionDigestCard";
import ToolCallDetail from "./ToolCallDetail";
import WaterfallTimeline from "./WaterfallTimeline";

// ---------------------------------------------------------------------------
// Event type → visual mapping
// ---------------------------------------------------------------------------

type EventKind =
  | "tool_start"
  | "tool_complete_pass"
  | "tool_complete_fail"
  | "turn"
  | "error"
  | "partial"
  | "fallback";

function classifyEvent(ev: TranscriptEvent): EventKind {
  switch (ev.type) {
    case "ToolExecutionStart":
      return "tool_start";
    case "ToolExecutionComplete":
      return ev.success === false ? "tool_complete_fail" : "tool_complete_pass";
    case "ToolExecutionPartialResult":
      return "partial";
    case "Turn":
      return "turn";
    case "Error":
      return "error";
    default:
      return "fallback";
  }
}

const dotColor: Record<EventKind, string> = {
  tool_start: "bg-blue-500",
  tool_complete_pass: "bg-green-500",
  tool_complete_fail: "bg-red-500",
  turn: "bg-emerald-500",
  error: "bg-red-500",
  partial: "bg-zinc-400",
  fallback: "bg-zinc-500",
};

const badgeStyle: Record<EventKind, string> = {
  tool_start: "bg-blue-500/10 text-blue-400",
  tool_complete_pass: "bg-green-500/10 text-green-400",
  tool_complete_fail: "bg-red-500/10 text-red-400",
  turn: "bg-emerald-500/10 text-emerald-400",
  error: "bg-red-500/10 text-red-400",
  partial: "bg-zinc-500/10 text-zinc-400",
  fallback: "bg-zinc-500/10 text-zinc-400",
};

const iconMap: Record<EventKind, React.ComponentType<{ className?: string }>> =
  {
    tool_start: Play,
    tool_complete_pass: CheckCircle2,
    tool_complete_fail: XCircle,
    turn: MessageSquare,
    error: AlertTriangle,
    partial: Wrench,
    fallback: Terminal,
  };

const kindLabel: Record<EventKind, string> = {
  tool_start: "tool start",
  tool_complete_pass: "tool complete",
  tool_complete_fail: "tool failed",
  turn: "assistant turn",
  error: "error",
  partial: "partial result",
  fallback: "event",
};

// ---------------------------------------------------------------------------
// Correlate tool calls: match Start ↔ Complete by toolCallId
// ---------------------------------------------------------------------------

interface CorrelatedToolCall {
  toolName: string;
  toolCallId?: string;
  args?: unknown;
  result?: unknown;
  success?: boolean;
}

function correlateToolCalls(
  events: TranscriptEvent[],
): Map<string, CorrelatedToolCall> {
  const map = new Map<string, CorrelatedToolCall>();

  for (const ev of events) {
    if (!ev.toolCallId) continue;
    const existing = map.get(ev.toolCallId) ?? {
      toolName: ev.toolName ?? "unknown",
      toolCallId: ev.toolCallId,
    };

    if (ev.type === "ToolExecutionStart") {
      existing.toolName = ev.toolName ?? existing.toolName;
      existing.args = ev.arguments;
    }
    if (ev.type === "ToolExecutionComplete") {
      existing.result = ev.toolResult;
      existing.success = ev.success;
    }
    map.set(ev.toolCallId, existing);
  }

  return map;
}

// ---------------------------------------------------------------------------
// Single timeline row
// ---------------------------------------------------------------------------

function TimelineRow({
  event,
  correlated,
}: {
  event: TranscriptEvent;
  correlated: Map<string, CorrelatedToolCall>;
}) {
  const [expanded, setExpanded] = useState(false);
  const kind = classifyEvent(event);
  const Icon = iconMap[kind];

  // Build description
  let description = "";
  if (event.toolName) description = event.toolName;
  else if (event.content)
    description =
      event.content.length > 120
        ? event.content.slice(0, 120) + "…"
        : event.content;
  else if (event.message)
    description =
      event.message.length > 120
        ? event.message.slice(0, 120) + "…"
        : event.message;
  else description = event.type;

  // Determine if there is expandable content
  const hasContent =
    event.content ||
    event.message ||
    event.arguments !== undefined ||
    event.toolResult !== undefined;

  // For ToolExecutionStart, pull correlated data
  const tool =
    event.toolCallId && event.type === "ToolExecutionStart"
      ? correlated.get(event.toolCallId)
      : undefined;

  return (
    <div className="relative flex gap-3 pb-5 last:pb-0">
      {/* vertical line + dot */}
      <div className="flex flex-col items-center">
        <div className={`h-3 w-3 shrink-0 rounded-full ${dotColor[kind]}`} />
        <div className="w-px flex-1 bg-zinc-700" />
      </div>

      {/* content */}
      <div className="-mt-0.5 flex-1 min-w-0 space-y-1.5">
        <div className="flex items-center gap-2 flex-wrap">
          <span
            className={`inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-xs font-medium ${badgeStyle[kind]}`}
          >
            <Icon className="h-3 w-3" />
            {kindLabel[kind]}
          </span>
          {event.toolCallId && (
            <span className="text-xs font-mono text-zinc-600 truncate max-w-48">
              {event.toolCallId}
            </span>
          )}
        </div>

        <p className="text-sm text-zinc-300 break-words">{description}</p>

        {hasContent && (
          <button
            onClick={() => setExpanded(!expanded)}
            className="flex items-center gap-1 text-xs text-zinc-500 hover:text-zinc-300"
          >
            {expanded ? (
              <ChevronDown className="h-3 w-3" />
            ) : (
              <ChevronRight className="h-3 w-3" />
            )}
            {expanded ? "Hide" : "Show"} details
          </button>
        )}

        {expanded && (
          <div className="space-y-2">
            {/* Full message / content */}
            {(event.content || event.message) && (
              <pre className="max-h-64 overflow-auto rounded bg-zinc-900 p-2.5 text-xs text-zinc-300">
                <code>{event.content ?? event.message}</code>
              </pre>
            )}

            {/* Correlated tool call detail */}
            {tool && (
              <ToolCallDetail
                toolName={tool.toolName}
                toolCallId={tool.toolCallId}
                args={tool.args}
                result={tool.result}
                success={tool.success}
              />
            )}

            {/* Inline args/result when no correlation */}
            {!tool && event.arguments !== undefined && (
              <pre className="max-h-48 overflow-auto rounded bg-zinc-900 p-2.5 text-xs text-zinc-300">
                <code>{JSON.stringify(event.arguments, null, 2)}</code>
              </pre>
            )}
            {!tool && event.toolResult !== undefined && (
              <pre className="max-h-48 overflow-auto rounded bg-zinc-900 p-2.5 text-xs text-zinc-300">
                <code>{JSON.stringify(event.toolResult, null, 2)}</code>
              </pre>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Grader-based fallback (preserved from old implementation)
// ---------------------------------------------------------------------------

interface FallbackEvent {
  type: "tool_call" | "file_edit" | "reasoning" | "output";
  description: string;
  content?: string;
}

const fallbackDot: Record<FallbackEvent["type"], string> = {
  tool_call: "bg-blue-500",
  file_edit: "bg-green-500",
  reasoning: "bg-purple-500",
  output: "bg-yellow-500",
};

const fallbackBadge: Record<FallbackEvent["type"], string> = {
  tool_call: "bg-blue-500/10 text-blue-400",
  file_edit: "bg-green-500/10 text-green-400",
  reasoning: "bg-purple-500/10 text-purple-400",
  output: "bg-yellow-500/10 text-yellow-400",
};

const fallbackIcon: Record<
  FallbackEvent["type"],
  React.ComponentType<{ className?: string }>
> = {
  tool_call: Wrench,
  file_edit: FileText,
  reasoning: Brain,
  output: Terminal,
};

function buildFallbackEvents(task: TaskResult): FallbackEvent[] {
  const events: FallbackEvent[] = [];

  for (const g of task.graderResults) {
    if (!g.message) continue;
    if (
      /\b(edit|creat|modif|writ|updat)\w*\b.*\.(ts|js|go|py|md|json|yaml|yml)\b/i.test(
        g.message,
      )
    ) {
      events.push({ type: "file_edit", description: g.message });
    } else if (/\b(tool|command|run|exec|invoke|call)\b/i.test(g.message)) {
      events.push({ type: "tool_call", description: g.message });
    } else {
      events.push({ type: "reasoning", description: g.message });
    }
  }

  // Add grader summaries
  for (const g of task.graderResults) {
    events.push({
      type: "tool_call",
      description: `Grader: ${g.name} (${g.type})`,
      content: `Passed: ${g.passed ? "yes" : "no"}\nScore: ${Math.round(g.score * 100)}%\nMessage: ${g.message}`,
    });
  }

  if (events.length === 0) {
    events.push({
      type: "output",
      description: `Task "${task.name}" completed with outcome: ${task.outcome}`,
      content: `Score: ${Math.round(task.score * 100)}%\nDuration: ${Math.round(task.duration)}s\nGraders: ${task.graderResults.length}`,
    });
  }

  // Dedupe
  const seen = new Set<string>();
  return events.filter((e) => {
    if (seen.has(e.description)) return false;
    seen.add(e.description);
    return true;
  });
}

function FallbackRow({ event }: { event: FallbackEvent }) {
  const [expanded, setExpanded] = useState(false);
  const Icon = fallbackIcon[event.type];

  return (
    <div className="relative flex gap-3 pb-5 last:pb-0">
      <div className="flex flex-col items-center">
        <div
          className={`h-3 w-3 shrink-0 rounded-full ${fallbackDot[event.type]}`}
        />
        <div className="w-px flex-1 bg-zinc-700" />
      </div>
      <div className="-mt-0.5 flex-1 min-w-0 space-y-1">
        <span
          className={`inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-xs font-medium ${fallbackBadge[event.type]}`}
        >
          <Icon className="h-3 w-3" />
          {event.type.replace("_", " ")}
        </span>
        <p className="text-sm text-zinc-300">{event.description}</p>
        {event.content && (
          <>
            <button
              onClick={() => setExpanded(!expanded)}
              className="flex items-center gap-1 text-xs text-zinc-500 hover:text-zinc-300"
            >
              {expanded ? (
                <ChevronDown className="h-3 w-3" />
              ) : (
                <ChevronRight className="h-3 w-3" />
              )}
              {expanded ? "Hide" : "Show"} details
            </button>
            {expanded && (
              <pre className="mt-1 overflow-x-auto rounded-lg bg-zinc-900 p-3 text-xs text-zinc-300">
                <code>{event.content}</code>
              </pre>
            )}
          </>
        )}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Main component
// ---------------------------------------------------------------------------

type ViewMode = "timeline" | "events";

export default function TrajectoryViewer({ task }: { task: TaskResult }) {
  const hasTranscript = task.transcript && task.transcript.length > 0;
  const hasToolEvents = useMemo(
    () =>
      hasTranscript &&
      task.transcript!.some(
        (e) =>
          e.type === "ToolExecutionStart" ||
          e.type === "ToolExecutionComplete",
      ),
    [hasTranscript, task.transcript],
  );

  const [view, setView] = useState<ViewMode>(
    hasToolEvents ? "timeline" : "events",
  );

  const correlated = hasTranscript
    ? correlateToolCalls(task.transcript!)
    : new Map<string, CorrelatedToolCall>();

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h3 className="text-sm font-medium text-zinc-400">
          Trajectory — {task.name}
        </h3>

        {/* View toggle (only when transcript has tool events) */}
        {hasToolEvents && (
          <div className="flex rounded-lg border border-zinc-700 overflow-hidden">
            <button
              onClick={() => setView("timeline")}
              className={`flex items-center gap-1.5 px-3 py-1 text-xs font-medium transition-colors ${
                view === "timeline"
                  ? "bg-zinc-700 text-zinc-200"
                  : "bg-zinc-800 text-zinc-500 hover:text-zinc-300"
              }`}
            >
              <BarChart3 className="h-3 w-3" />
              Timeline
            </button>
            <button
              onClick={() => setView("events")}
              className={`flex items-center gap-1.5 px-3 py-1 text-xs font-medium transition-colors ${
                view === "events"
                  ? "bg-zinc-700 text-zinc-200"
                  : "bg-zinc-800 text-zinc-500 hover:text-zinc-300"
              }`}
            >
              <List className="h-3 w-3" />
              Events
            </button>
          </div>
        )}
      </div>

      {/* Session digest card */}
      {task.sessionDigest && (
        <SessionDigestCard digest={task.sessionDigest} />
      )}

      {/* Waterfall timeline view */}
      {hasTranscript && view === "timeline" ? (
        <WaterfallTimeline events={task.transcript!} />
      ) : hasTranscript ? (
        /* Vertical event list (original) */
        <div className="rounded-lg border border-zinc-700 bg-zinc-800 p-4">
          {task.transcript!.map((event, i) => (
            <TimelineRow
              key={`${event.type}-${event.toolCallId ?? "noid"}-${i}`}
              event={event}
              correlated={correlated}
            />
          ))}
        </div>
      ) : (
        /* Graceful fallback to grader-based summary */
        <div className="space-y-2">
          <p className="text-xs text-zinc-500 italic">
            No transcript data — showing grader-based summary
          </p>
          <div className="rounded-lg border border-zinc-700 bg-zinc-800 p-4">
            {buildFallbackEvents(task).map((event, i) => (
              <FallbackRow key={`fb-${event.type}-${i}`} event={event} />
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
