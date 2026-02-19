import { useState } from "react";
import { useRuns, useRunDetail } from "../hooks/useApi";
import type { RunDetail, TaskResult } from "../api/client";
import {
  formatDuration,
  formatCost,
  formatNumber,
  formatPercent,
  formatRelativeTime,
} from "../lib/format";

function Delta({
  a,
  b,
  format,
  higherIsBetter = true,
}: {
  a: number;
  b: number;
  format: (v: number) => string;
  higherIsBetter?: boolean;
}) {
  const diff = b - a;
  if (Math.abs(diff) < 0.001)
    return <span className="text-zinc-400">—</span>;

  const improved = higherIsBetter ? diff > 0 : diff < 0;
  const arrow = diff > 0 ? "↑" : "↓";
  const color = improved ? "text-green-500" : "text-red-500";

  return (
    <span className={`text-xs font-medium ${color}`}>
      {arrow} {format(Math.abs(diff))}
    </span>
  );
}

function PassRateBar({ rate, label }: { rate: number; label: string }) {
  const pct = Math.round(rate * 100);
  return (
    <div className="space-y-1">
      <div className="flex justify-between text-xs">
        <span className="text-zinc-400">{label}</span>
        <span className="text-zinc-100">{pct}%</span>
      </div>
      <div className="h-2 rounded-full bg-zinc-700">
        <div
          className="h-2 rounded-full bg-blue-500"
          style={{ width: `${pct}%` }}
        />
      </div>
    </div>
  );
}

function RunHeader({ run }: { run: RunDetail }) {
  return (
    <div className="space-y-1">
      <h3 className="font-medium text-zinc-100">{run.spec}</h3>
      <p className="text-sm text-zinc-400">{run.model}</p>
      <p className="text-xs text-zinc-500">{formatRelativeTime(run.timestamp)}</p>
    </div>
  );
}

function RunSelector({
  label,
  value,
  onChange,
  runs,
  excludeId,
}: {
  label: string;
  value: string;
  onChange: (id: string) => void;
  runs: { id: string; spec: string; model: string; timestamp: string }[];
  excludeId?: string;
}) {
  return (
    <div>
      <label className="mb-1 block text-xs font-medium text-zinc-400">
        {label}
      </label>
      <select
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="w-full rounded border border-zinc-700 bg-zinc-800 px-3 py-2 text-sm text-zinc-100 focus:border-blue-500 focus:outline-none"
      >
        <option value="">Select a run…</option>
        {runs
          .filter((r) => r.id !== excludeId)
          .map((r) => (
            <option key={r.id} value={r.id}>
              {r.spec} — {r.model} ({formatRelativeTime(r.timestamp)})
            </option>
          ))}
      </select>
    </div>
  );
}

function TaskComparisonTable({
  runA,
  runB,
}: {
  runA: RunDetail;
  runB: RunDetail;
}) {
  const taskMap = new Map<string, { a?: TaskResult; b?: TaskResult }>();
  for (const t of runA.tasks) {
    taskMap.set(t.name, { a: t });
  }
  for (const t of runB.tasks) {
    const existing = taskMap.get(t.name) ?? {};
    taskMap.set(t.name, { ...existing, b: t });
  }

  const tasks = Array.from(taskMap.entries());

  return (
    <div className="overflow-x-auto rounded-lg border border-zinc-700 bg-zinc-800">
      <table className="w-full text-left text-sm">
        <thead>
          <tr className="border-b border-zinc-700">
            <th className="px-4 py-3 text-xs font-medium uppercase text-zinc-400">
              Task
            </th>
            <th className="px-4 py-3 text-xs font-medium uppercase text-zinc-400">
              Outcome A
            </th>
            <th className="px-4 py-3 text-xs font-medium uppercase text-zinc-400">
              Outcome B
            </th>
            <th className="px-4 py-3 text-xs font-medium uppercase text-zinc-400">
              Score A
            </th>
            <th className="px-4 py-3 text-xs font-medium uppercase text-zinc-400">
              Score B
            </th>
            <th className="px-4 py-3 text-xs font-medium uppercase text-zinc-400">
              Δ Score
            </th>
            <th className="px-4 py-3 text-xs font-medium uppercase text-zinc-400">
              Duration A
            </th>
            <th className="px-4 py-3 text-xs font-medium uppercase text-zinc-400">
              Duration B
            </th>
            <th className="px-4 py-3 text-xs font-medium uppercase text-zinc-400">
              Δ Duration
            </th>
          </tr>
        </thead>
        <tbody>
          {tasks.map(([name, { a, b }]) => (
            <tr
              key={name}
              className="border-b border-zinc-700/50 hover:bg-zinc-700/30"
            >
              <td className="px-4 py-3 font-medium text-zinc-100">{name}</td>
              <td className="px-4 py-3">
                <OutcomeCell outcome={a?.outcome} />
              </td>
              <td className="px-4 py-3">
                <OutcomeCell outcome={b?.outcome} />
              </td>
              <td className="px-4 py-3 text-zinc-300">
                {a ? formatPercent(a.score) : "—"}
              </td>
              <td className="px-4 py-3 text-zinc-300">
                {b ? formatPercent(b.score) : "—"}
              </td>
              <td className="px-4 py-3">
                {a && b ? (
                  <Delta
                    a={a.score}
                    b={b.score}
                    format={(v) => formatPercent(v)}
                    higherIsBetter
                  />
                ) : (
                  <span className="text-zinc-500">—</span>
                )}
              </td>
              <td className="px-4 py-3 text-zinc-300">
                {a ? formatDuration(a.duration) : "—"}
              </td>
              <td className="px-4 py-3 text-zinc-300">
                {b ? formatDuration(b.duration) : "—"}
              </td>
              <td className="px-4 py-3">
                {a && b ? (
                  <Delta
                    a={a.duration}
                    b={b.duration}
                    format={formatDuration}
                    higherIsBetter={false}
                  />
                ) : (
                  <span className="text-zinc-500">—</span>
                )}
              </td>
            </tr>
          ))}
          {tasks.length === 0 && (
            <tr>
              <td colSpan={9} className="p-8 text-center text-zinc-500">
                No tasks to compare.
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}

function OutcomeCell({ outcome }: { outcome?: string }) {
  if (!outcome) return <span className="text-zinc-500">—</span>;
  if (outcome.startsWith("pass"))
    return (
      <span className="rounded-full bg-green-500/10 px-2 py-0.5 text-xs font-medium text-green-500">
        pass
      </span>
    );
  if (outcome.startsWith("fail"))
    return (
      <span className="rounded-full bg-red-500/10 px-2 py-0.5 text-xs font-medium text-red-500">
        fail
      </span>
    );
  return (
    <span className="rounded-full bg-yellow-500/10 px-2 py-0.5 text-xs font-medium text-yellow-500">
      {outcome}
    </span>
  );
}

export default function CompareView() {
  const { data: runs, isLoading: runsLoading } = useRuns();
  const [idA, setIdA] = useState("");
  const [idB, setIdB] = useState("");

  const detailA = useRunDetail(idA);
  const detailB = useRunDetail(idB);

  const loading = detailA.isLoading || detailB.isLoading;
  const runA = detailA.data;
  const runB = detailB.data;

  return (
    <div className="space-y-6">
      <h1 className="text-2xl font-semibold text-zinc-100">Compare Runs</h1>

      {/* Run selectors */}
      <div className="grid gap-4 sm:grid-cols-2">
        <RunSelector
          label="Run A"
          value={idA}
          onChange={setIdA}
          runs={runs ?? []}
          excludeId={idB}
        />
        <RunSelector
          label="Run B"
          value={idB}
          onChange={setIdB}
          runs={runs ?? []}
          excludeId={idA}
        />
      </div>

      {runsLoading && (
        <div className="text-sm text-zinc-500">Loading runs…</div>
      )}

      {loading && idA && idB && (
        <div className="text-sm text-zinc-500">Loading run details…</div>
      )}

      {/* Comparison */}
      {runA && runB && (
        <div className="space-y-6">
          {/* Headers side by side */}
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="rounded-lg border border-zinc-700 bg-zinc-800 p-4">
              <RunHeader run={runA} />
            </div>
            <div className="rounded-lg border border-zinc-700 bg-zinc-800 p-4">
              <RunHeader run={runB} />
            </div>
          </div>

          {/* Metrics comparison */}
          <div className="rounded-lg border border-zinc-700 bg-zinc-800 p-4">
            <h3 className="mb-4 text-sm font-medium text-zinc-300">
              Metrics Comparison
            </h3>
            <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
              <MetricCard
                label="Pass Rate"
                valueA={formatPercent(
                  runA.taskCount > 0
                    ? runA.passCount / runA.taskCount
                    : 0,
                )}
                valueB={formatPercent(
                  runB.taskCount > 0
                    ? runB.passCount / runB.taskCount
                    : 0,
                )}
                delta={
                  <Delta
                    a={
                      runA.taskCount > 0
                        ? runA.passCount / runA.taskCount
                        : 0
                    }
                    b={
                      runB.taskCount > 0
                        ? runB.passCount / runB.taskCount
                        : 0
                    }
                    format={(v) => formatPercent(v)}
                    higherIsBetter
                  />
                }
              />
              <MetricCard
                label="Tokens"
                valueA={formatNumber(runA.tokens)}
                valueB={formatNumber(runB.tokens)}
                delta={
                  <Delta
                    a={runA.tokens}
                    b={runB.tokens}
                    format={formatNumber}
                    higherIsBetter={false}
                  />
                }
              />
              <MetricCard
                label="Cost"
                valueA={formatCost(runA.cost)}
                valueB={formatCost(runB.cost)}
                delta={
                  <Delta
                    a={runA.cost}
                    b={runB.cost}
                    format={formatCost}
                    higherIsBetter={false}
                  />
                }
              />
              <MetricCard
                label="Duration"
                valueA={formatDuration(runA.duration)}
                valueB={formatDuration(runB.duration)}
                delta={
                  <Delta
                    a={runA.duration}
                    b={runB.duration}
                    format={formatDuration}
                    higherIsBetter={false}
                  />
                }
              />
            </div>
          </div>

          {/* Pass rate bars */}
          <div className="rounded-lg border border-zinc-700 bg-zinc-800 p-4">
            <h3 className="mb-3 text-sm font-medium text-zinc-300">
              Pass Rate Comparison
            </h3>
            <div className="space-y-3">
              <PassRateBar
                rate={
                  runA.taskCount > 0
                    ? runA.passCount / runA.taskCount
                    : 0
                }
                label={`A: ${runA.spec} (${runA.model})`}
              />
              <PassRateBar
                rate={
                  runB.taskCount > 0
                    ? runB.passCount / runB.taskCount
                    : 0
                }
                label={`B: ${runB.spec} (${runB.model})`}
              />
            </div>
          </div>

          {/* Per-task comparison */}
          <div>
            <h3 className="mb-3 text-sm font-medium text-zinc-300">
              Per-Task Comparison
            </h3>
            <TaskComparisonTable runA={runA} runB={runB} />
          </div>
        </div>
      )}

      {!idA && !idB && !runsLoading && (
        <div className="rounded-lg border border-zinc-700 bg-zinc-800 p-12 text-center text-zinc-500">
          Select two runs above to compare them side-by-side.
        </div>
      )}
    </div>
  );
}

function MetricCard({
  label,
  valueA,
  valueB,
  delta,
}: {
  label: string;
  valueA: string;
  valueB: string;
  delta: React.ReactNode;
}) {
  return (
    <div className="space-y-2">
      <p className="text-xs font-medium uppercase text-zinc-400">{label}</p>
      <div className="flex items-baseline gap-3">
        <span className="text-lg font-semibold text-zinc-100">{valueA}</span>
        <span className="text-zinc-500">→</span>
        <span className="text-lg font-semibold text-zinc-100">{valueB}</span>
      </div>
      <div>{delta}</div>
    </div>
  );
}
