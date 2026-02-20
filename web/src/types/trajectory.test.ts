import { describe, it, expect } from "vitest";
import type { TranscriptEvent } from "../api/client";
import { extractToolSpans, groupSpansByTool, type ToolSpan } from "./trajectory";

// ── helpers ──────────────────────────────────────────────────────────

function startEvent(
  toolCallId: string,
  toolName: string,
  args?: unknown,
): TranscriptEvent {
  return { type: "ToolExecutionStart", toolCallId, toolName, arguments: args };
}

function completeEvent(
  toolCallId: string,
  opts: { success?: boolean; toolName?: string; toolResult?: unknown } = {},
): TranscriptEvent {
  return {
    type: "ToolExecutionComplete",
    toolCallId,
    success: opts.success ?? true,
    toolName: opts.toolName,
    toolResult: opts.toolResult,
  };
}

// ── extractToolSpans ─────────────────────────────────────────────────

describe("extractToolSpans", () => {
  it("returns [] for empty events array", () => {
    expect(extractToolSpans([])).toEqual([]);
  });

  it("returns a pending span for a Start without Complete", () => {
    const events: TranscriptEvent[] = [
      startEvent("tc-1", "read_file", { path: "foo.ts" }),
    ];
    const spans = extractToolSpans(events);
    expect(spans).toHaveLength(1);
    expect(spans[0]).toMatchObject({
      toolName: "read_file",
      toolCallId: "tc-1",
      startIndex: 0,
      status: "pending",
      arguments: { path: "foo.ts" },
    });
  });

  it("produces correct span for a matched Start/Complete pair", () => {
    const events: TranscriptEvent[] = [
      startEvent("tc-1", "edit_file"),
      { type: "Turn", content: "thinking..." },
      completeEvent("tc-1", { success: true, toolResult: "ok" }),
    ];
    const spans = extractToolSpans(events);
    expect(spans).toHaveLength(1);
    expect(spans[0]).toMatchObject({
      toolName: "edit_file",
      toolCallId: "tc-1",
      startIndex: 0,
      endIndex: 2,
      duration: 2,
      status: "pass",
      toolResult: "ok",
    });
  });

  it("correlates interleaved tools by toolCallId", () => {
    const events: TranscriptEvent[] = [
      startEvent("a", "read_file"),
      startEvent("b", "grep"),
      completeEvent("a"),
      completeEvent("b", { success: false }),
    ];
    const spans = extractToolSpans(events);
    expect(spans).toHaveLength(2);

    const spanA = spans.find((s) => s.toolCallId === "a")!;
    const spanB = spans.find((s) => s.toolCallId === "b")!;

    expect(spanA.toolName).toBe("read_file");
    expect(spanA.startIndex).toBe(0);
    expect(spanA.endIndex).toBe(2);
    expect(spanA.status).toBe("pass");

    expect(spanB.toolName).toBe("grep");
    expect(spanB.startIndex).toBe(1);
    expect(spanB.endIndex).toBe(3);
    expect(spanB.status).toBe("fail");
  });

  it("handles Complete without Start gracefully", () => {
    const events: TranscriptEvent[] = [
      completeEvent("orphan", { toolName: "bash", success: true }),
    ];
    const spans = extractToolSpans(events);
    expect(spans).toHaveLength(1);
    expect(spans[0]).toMatchObject({
      toolName: "bash",
      toolCallId: "orphan",
      startIndex: 0,
      endIndex: 0,
      duration: 0,
      status: "pass",
    });
  });

  it("ignores Turn and Error events (no tool spans created)", () => {
    const events: TranscriptEvent[] = [
      { type: "Turn", content: "hello" },
      { type: "Error", message: "boom" },
      { type: "Turn", content: "bye" },
    ];
    expect(extractToolSpans(events)).toEqual([]);
  });
});

// ── groupSpansByTool ─────────────────────────────────────────────────

describe("groupSpansByTool", () => {
  const makeSpan = (id: string, toolName: string): ToolSpan => ({
    id,
    toolName,
    toolCallId: id,
    startIndex: 0,
    endIndex: 1,
    duration: 1,
    status: "pass",
  });

  it("groups spans by toolName with insertion order preserved", () => {
    const spans = [
      makeSpan("s1", "read_file"),
      makeSpan("s2", "grep"),
      makeSpan("s3", "read_file"),
    ];
    const groups = groupSpansByTool(spans);
    const keys = [...groups.keys()];

    expect(keys).toEqual(["read_file", "grep"]);
    expect(groups.get("read_file")).toHaveLength(2);
    expect(groups.get("grep")).toHaveLength(1);
  });

  it("returns empty map for empty spans", () => {
    const groups = groupSpansByTool([]);
    expect(groups.size).toBe(0);
  });

  it("puts multiple spans of the same tool in one group", () => {
    const spans = [
      makeSpan("s1", "bash"),
      makeSpan("s2", "bash"),
      makeSpan("s3", "bash"),
    ];
    const groups = groupSpansByTool(spans);
    expect(groups.size).toBe(1);
    expect(groups.get("bash")).toHaveLength(3);
  });
});
