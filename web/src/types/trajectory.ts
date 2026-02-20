import type { TranscriptEvent } from "../api/client";

export interface ToolSpan {
  id: string;
  toolName: string;
  toolCallId: string;
  startIndex: number;
  endIndex: number;
  duration: number;
  status: "pass" | "fail" | "pending";
  arguments?: unknown;
  toolResult?: unknown;
  success?: boolean;
}

/**
 * Correlate ToolExecutionStart ↔ ToolExecutionComplete by toolCallId,
 * producing a ToolSpan for each matched pair (or orphaned start).
 */
export function extractToolSpans(events: TranscriptEvent[]): ToolSpan[] {
  const starts = new Map<
    string,
    { index: number; toolName: string; args?: unknown }
  >();
  const spans: ToolSpan[] = [];
  let spanCounter = 0;

  for (let i = 0; i < events.length; i++) {
    const ev = events[i];
    if (!ev || !ev.toolCallId) continue;

    if (ev.type === "ToolExecutionStart") {
      starts.set(ev.toolCallId, {
        index: i,
        toolName: ev.toolName ?? "unknown",
        args: ev.arguments,
      });
    } else if (ev.type === "ToolExecutionComplete") {
      const start = starts.get(ev.toolCallId);
      const startIndex = start?.index ?? i;
      const toolName = start?.toolName ?? ev.toolName ?? "unknown";

      spans.push({
        id: `span-${spanCounter++}`,
        toolName,
        toolCallId: ev.toolCallId,
        startIndex,
        endIndex: i,
        duration: i - startIndex,
        status: ev.success === false ? "fail" : "pass",
        arguments: start?.args,
        toolResult: ev.toolResult,
        success: ev.success,
      });

      starts.delete(ev.toolCallId);
    }
  }

  // Orphaned starts (no matching Complete) → pending
  for (const [callId, start] of starts) {
    spans.push({
      id: `span-${spanCounter++}`,
      toolName: start.toolName,
      toolCallId: callId,
      startIndex: start.index,
      endIndex: events.length - 1,
      duration: events.length - 1 - start.index,
      status: "pending",
      arguments: start.args,
    });
  }

  return spans.sort((a, b) => a.startIndex - b.startIndex);
}

/** Group spans by toolName, preserving insertion order of first occurrence. */
export function groupSpansByTool(spans: ToolSpan[]): Map<string, ToolSpan[]> {
  const map = new Map<string, ToolSpan[]>();
  for (const span of spans) {
    const existing = map.get(span.toolName);
    if (existing) {
      existing.push(span);
    } else {
      map.set(span.toolName, [span]);
    }
  }
  return map;
}
