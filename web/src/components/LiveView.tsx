import { useState, useEffect } from "react";
import {
  Radio,
  CheckCircle2,
  XCircle,
  Loader2,
  Zap,
  Clock,
  Coins,
  Hash,
} from "lucide-react";
import { useSSE, type SSEEvent } from "../hooks/useSSE";
import { formatDuration, formatCost, formatNumber } from "../lib/format";

function ConnectionBadge({ connected }: { connected: boolean }) {
  return (
    <span
      className={`inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-medium ${
        connected
          ? "bg-green-500/10 text-green-500"
          : "bg-red-500/10 text-red-500"
      }`}
    >
      <span
        className={`h-2 w-2 rounded-full ${
          connected ? "animate-pulse bg-green-500" : "bg-red-500"
        }`}
      />
      {connected ? "Connected" : "Disconnected"}
    </span>
  );
}

function ProgressBar({ completed, total }: { completed: number; total: number }) {
  const pct = total > 0 ? Math.round((completed / total) * 100) : 0;
  return (
    <div className="space-y-1">
      <div className="flex justify-between text-xs text-zinc-400">
        <span>
          {completed}/{total} tasks
        </span>
        <span>{pct}%</span>
      </div>
      <div className="h-2 w-full overflow-hidden rounded-full bg-zinc-700">
        <div
          className="h-full rounded-full bg-blue-500 transition-all duration-300"
          style={{ width: `${pct}%` }}
        />
      </div>
    </div>
  );
}

function EventBadge({ type }: { type: SSEEvent["type"] }) {
  const styles: Record<string, string> = {
    task_start: "bg-blue-500/10 text-blue-400",
    task_complete: "bg-green-500/10 text-green-400",
    grader_result: "bg-purple-500/10 text-purple-400",
    run_complete: "bg-yellow-500/10 text-yellow-400",
  };
  return (
    <span
      className={`rounded px-1.5 py-0.5 text-xs font-medium ${styles[type] ?? "bg-zinc-700 text-zinc-300"}`}
    >
      {type}
    </span>
  );
}

function EventCard({ event }: { event: SSEEvent }) {
  const time = new Date(event.timestamp).toLocaleTimeString();
  let description = "";
  const d = event.data;

  switch (event.type) {
    case "task_start":
      description = `Started task: ${d.taskName ?? "unknown"}`;
      break;
    case "task_complete":
      description = `${d.taskName ?? "task"} → ${d.outcome ?? "done"}${d.score != null ? ` (${Math.round(d.score * 100)}%)` : ""}`;
      break;
    case "grader_result":
      description = `${d.graderName ?? "grader"} [${d.graderType ?? ""}]: ${d.passed ? "✓" : "✗"} ${d.message ?? ""}`;
      break;
    case "run_complete":
      description = `Run complete — ${d.passCount ?? 0}/${d.totalTasks ?? 0} passed`;
      break;
  }

  return (
    <div className="flex items-start gap-3 rounded-lg border border-zinc-700/50 bg-zinc-800 p-3">
      <span className="mt-0.5 text-xs text-zinc-500">{time}</span>
      <EventBadge type={event.type} />
      <span className="text-sm text-zinc-300">{description}</span>
    </div>
  );
}

function StatMini({
  icon: Icon,
  label,
  value,
}: {
  icon: React.ComponentType<{ className?: string }>;
  label: string;
  value: string;
}) {
  return (
    <div className="flex items-center gap-2 rounded-lg border border-zinc-700 bg-zinc-800 px-3 py-2">
      <Icon className="h-4 w-4 text-zinc-400" />
      <div>
        <p className="text-xs text-zinc-500">{label}</p>
        <p className="text-sm font-medium text-zinc-100">{value}</p>
      </div>
    </div>
  );
}

export default function LiveView() {
  const { isConnected, currentRun, events } = useSSE();
  const [elapsed, setElapsed] = useState(0);

  useEffect(() => {
    if (!currentRun || currentRun.done) return;
    const id = setInterval(() => {
      setElapsed(Math.floor((Date.now() - currentRun.startTime) / 1000));
    }, 1000);
    return () => clearInterval(id);
  }, [currentRun]);

  const hasActiveRun = currentRun && !currentRun.done;

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <Radio className="h-5 w-5 text-blue-500" />
          <h1 className="text-2xl font-semibold text-zinc-100">Live</h1>
        </div>
        <ConnectionBadge connected={isConnected} />
      </div>

      {!hasActiveRun && events.length === 0 && (
        <div className="rounded-lg border border-zinc-700 bg-zinc-800 p-12 text-center">
          <Radio className="mx-auto h-10 w-10 text-zinc-600" />
          <p className="mt-4 text-zinc-400">No active run.</p>
          <p className="mt-1 text-sm text-zinc-500">
            Start one with{" "}
            <code className="rounded bg-zinc-700 px-1.5 py-0.5 text-zinc-300">
              waza run
            </code>
          </p>
        </div>
      )}

      {hasActiveRun && (
        <>
          <ProgressBar
            completed={currentRun.completedTasks}
            total={currentRun.totalTasks}
          />

          {currentRun.currentTask && (
            <div className="flex items-center gap-2 rounded-lg border border-blue-500/30 bg-blue-500/5 px-4 py-3">
              <Loader2 className="h-4 w-4 animate-spin text-blue-400" />
              <span className="text-sm text-blue-300">
                Running: {currentRun.currentTask}
              </span>
            </div>
          )}

          <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
            <StatMini
              icon={Clock}
              label="Elapsed"
              value={formatDuration(elapsed)}
            />
            <StatMini
              icon={Hash}
              label="Tokens"
              value={formatNumber(currentRun.tokens)}
            />
            <StatMini
              icon={CheckCircle2}
              label="Passed"
              value={String(currentRun.passCount)}
            />
            <StatMini
              icon={XCircle}
              label="Failed"
              value={String(currentRun.failCount)}
            />
          </div>
        </>
      )}

      {currentRun?.done && (
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
          <StatMini
            icon={Zap}
            label="Tasks"
            value={`${currentRun.passCount}/${currentRun.totalTasks}`}
          />
          <StatMini
            icon={Hash}
            label="Tokens"
            value={formatNumber(currentRun.tokens)}
          />
          <StatMini
            icon={Coins}
            label="Cost"
            value={formatCost(currentRun.cost)}
          />
          <StatMini
            icon={CheckCircle2}
            label="Passed"
            value={String(currentRun.passCount)}
          />
        </div>
      )}

      {events.length > 0 && (
        <div className="space-y-2">
          <h2 className="text-sm font-medium text-zinc-400">Event Feed</h2>
          <div className="max-h-[480px] space-y-2 overflow-y-auto">
            {events.map((ev, i) => (
              <EventCard key={`${ev.timestamp}-${i}`} event={ev} />
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
