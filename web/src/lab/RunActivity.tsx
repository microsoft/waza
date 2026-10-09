import { useEffect, useState } from "react";
import type { SSEEvent } from "../hooks/useSSE";
import type { LabRun } from "./data";

const eventTypes = [
  "run_started",
  "task_started",
  "step_executed",
  "task_completed",
  "run_completed",
  "run_failed",
];

function isRunEvent(
  value: unknown,
  runId: string,
): value is SSEEvent & { sequence: number } {
  if (!value || typeof value !== "object") return false;
  if (
    !("runId" in value) ||
    value.runId !== runId ||
    !("sequence" in value) ||
    typeof value.sequence !== "number" ||
    !Number.isSafeInteger(value.sequence) ||
    value.sequence <= 0
  )
    return false;
  if (
    !("type" in value) ||
    typeof value.type !== "string" ||
    !eventTypes.includes(value.type)
  )
    return false;
  if (
    !("timestamp" in value) ||
    typeof value.timestamp !== "string" ||
    !Number.isFinite(Date.parse(value.timestamp))
  )
    return false;
  if (
    !("data" in value) ||
    !value.data ||
    typeof value.data !== "object" ||
    Array.isArray(value.data)
  )
    return false;
  for (const [key, item] of Object.entries(value.data)) {
    if (
      [
        "totalTasks",
        "completedTasks",
        "passCount",
        "failCount",
        "tokens",
        "score",
        "cost",
        "duration",
      ].includes(key) &&
      (typeof item !== "number" || !Number.isFinite(item) || item < 0)
    )
      return false;
    if (
      [
        "spec",
        "model",
        "taskName",
        "outcome",
        "graderName",
        "graderType",
        "message",
      ].includes(key) &&
      typeof item !== "string"
    )
      return false;
  }
  return true;
}

export default function RunActivity({ runs }: { runs: LabRun[] }) {
  const [selected, setSelected] = useState("");
  const [attempt, setAttempt] = useState(0);
  const runId = runs.find((run) => run.id === selected)?.id ?? runs[0]?.id;
  const [stream, setStream] = useState<{
    runId?: string;
    status: string;
    events: SSEEvent[];
    error?: string;
  }>({ status: "Connecting", events: [] });
  useEffect(() => {
    if (!runId) return;
    let sequence = 0;
    const eventSource = new EventSource(
      `/api/v1/runs/${encodeURIComponent(runId)}/events`,
    );
    eventSource.onopen = () =>
      setStream((previous) => ({
        runId,
        status: "Connected",
        events: previous.runId === runId ? previous.events : [],
      }));
    eventSource.onmessage = (message) => {
      let parsed: unknown;
      try {
        parsed = JSON.parse(message.data);
      } catch {
        eventSource.close();
        setStream((previous) => ({
          ...previous,
          runId,
          status: "Error",
          error: "The server sent invalid event JSON. Reconnect to retry.",
        }));
        return;
      }
      if (!isRunEvent(parsed, runId)) {
        eventSource.close();
        setStream((previous) => ({
          ...previous,
          runId,
          status: "Error",
          error: "The server sent an invalid run event. Reconnect to retry.",
        }));
        return;
      }
      if (parsed.sequence <= sequence) return;
      sequence = parsed.sequence;
      const terminal =
        parsed.type === "run_completed" || parsed.type === "run_failed";
      if (terminal) eventSource.close();
      setStream((previous) => ({
        runId,
        status: terminal
          ? parsed.type === "run_failed"
            ? "Run failed (replayed)"
            : "Run completed (replayed)"
          : "Connected",
        events: [
          parsed,
          ...(previous.runId === runId ? previous.events : []),
        ].slice(0, 200),
      }));
    };
    eventSource.onerror = () =>
      setStream((previous) => ({
        ...previous,
        runId,
        status: "Reconnecting",
        error:
          "Event connection lost. The browser will reconnect using Last-Event-ID; saved progress remains visible.",
      }));
    return () => eventSource.close();
  }, [runId, attempt]);
  const state =
    stream.runId === runId ? stream : { status: "Connecting", events: [] };
  const completed =
    state.events.find((event) => event.data.completedTasks !== undefined)?.data
      .completedTasks ??
    state.events.filter((event) => event.type === "task_completed").length;
  const total = state.events.find(
    (event) => event.data.totalTasks !== undefined,
  )?.data.totalTasks;
  return (
    <>
      <p className="lab-note">
        Saved-result activity, not an active worker monitor. The current API
        reconstructs events and timestamps from result artifacts. Live execution
        discovery, start, cancel, and retry are not available in the lab.
      </p>
      {runId ? (
        <section className="lab-panel">
          <div className="lab-panel-header">
            <div>
              <h2>Run activity</h2>
              <p>Replayable event stream · up to 200 recent events</p>
            </div>
            <span
              className={`lab-status ${state.status === "Error" || state.status.includes("failed") ? "failed" : "neutral"}`}
            >
              {state.status}
            </span>
          </div>
          <div className="lab-history-toolbar">
            <label className="lab-field">
              Run
              <select
                aria-label="Activity run"
                value={runId}
                onChange={(event) => {
                  setSelected(event.target.value);
                  setStream({ status: "Connecting", events: [] });
                }}
              >
                {runs.map((run) => (
                  <option key={run.id} value={run.id}>
                    {run.skill} · {run.model} · {run.id}
                  </option>
                ))}
              </select>
            </label>
            <button
              className="lab-button"
              onClick={() => {
                setStream({ status: "Connecting", events: [] });
                setAttempt((value) => value + 1);
              }}
            >
              Reconnect stream
            </button>
          </div>
          {"error" in state && state.error && (
            <p role="alert" className="lab-note lab-negative">
              {state.error}
            </p>
          )}
          <div className="lab-live-body">
            <p>
              {completed} / {total ?? "Unavailable"} stored tasks replayed
            </p>
            {total !== undefined && (
              <progress
                aria-label="Stored task progress"
                max={Math.max(1, total)}
                value={completed}
              />
            )}
          </div>
          <div className="lab-stream">
            {state.events.map((event) => (
              <div key={event.sequence}>
                <code>#{event.sequence}</code>
                <strong>{event.type}</strong>
                <span>
                  {event.data.taskName ??
                    event.data.outcome ??
                    event.data.spec ??
                    ""}
                  {event.data.message ? ` · ${event.data.message}` : ""}
                </span>
              </div>
            ))}
          </div>
        </section>
      ) : (
        <div className="lab-empty">
          <h3>No saved runs in this scope</h3>
          <p>
            Run an evaluation with an output file, then refresh the result list.
          </p>
        </div>
      )}
    </>
  );
}
