import { useMemo, useState } from "react";
import { useRuns } from "../hooks/useApi";
import TrendChart from "./TrendChart";
import type { DataPoint } from "./TrendChart";
import type { RunSummary } from "../api/client";
import {
  formatPercent,
  formatNumber,
  formatCost,
  formatDuration,
} from "../lib/format";

function formatShortDate(iso: string): string {
  const d = new Date(iso);
  return `${d.getMonth() + 1}/${d.getDate()}`;
}

export default function TrendsPage() {
  const { data: runs, isLoading } = useRuns("timestamp", "asc");
  const [modelFilter, setModelFilter] = useState("all");

  const models = useMemo(() => {
    if (!runs) return [];
    const unique = Array.from(new Set(runs.map((r) => r.model)));
    unique.sort();
    return unique;
  }, [runs]);

  const filtered = useMemo(() => {
    if (!runs) return [];
    if (modelFilter === "all") return runs;
    return runs.filter((r) => r.model === modelFilter);
  }, [runs, modelFilter]);

  const passRateData: DataPoint[] = useMemo(
    () =>
      filtered.map((r: RunSummary) => ({
        label: formatShortDate(r.timestamp),
        value: r.taskCount > 0 ? r.passCount / r.taskCount : 0,
      })),
    [filtered],
  );

  const tokensData: DataPoint[] = useMemo(
    () =>
      filtered.map((r: RunSummary) => ({
        label: formatShortDate(r.timestamp),
        value: r.tokens,
      })),
    [filtered],
  );

  const costData: DataPoint[] = useMemo(
    () =>
      filtered.map((r: RunSummary) => ({
        label: formatShortDate(r.timestamp),
        value: r.cost,
      })),
    [filtered],
  );

  const durationData: DataPoint[] = useMemo(
    () =>
      filtered.map((r: RunSummary) => ({
        label: formatShortDate(r.timestamp),
        value: r.duration,
      })),
    [filtered],
  );

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-4">
        <h1 className="text-2xl font-semibold text-zinc-100">Trends</h1>
        <div>
          <label className="mr-2 text-xs font-medium text-zinc-400">
            Model:
          </label>
          <select
            value={modelFilter}
            onChange={(e) => setModelFilter(e.target.value)}
            className="rounded border border-zinc-700 bg-zinc-800 px-3 py-1.5 text-sm text-zinc-100 focus:border-blue-500 focus:outline-none"
          >
            <option value="all">All Models</option>
            {models.map((m) => (
              <option key={m} value={m}>
                {m}
              </option>
            ))}
          </select>
        </div>
      </div>

      {isLoading && (
        <div className="grid gap-4 sm:grid-cols-2">
          {Array.from({ length: 4 }).map((_, i) => (
            <div
              key={i}
              className="h-[260px] animate-pulse rounded-lg border border-zinc-700 bg-zinc-800"
            />
          ))}
        </div>
      )}

      {!isLoading && filtered.length === 0 && (
        <div className="rounded-lg border border-zinc-700 bg-zinc-800 p-12 text-center text-zinc-500">
          No runs found{modelFilter !== "all" ? ` for model "${modelFilter}"` : ""}.
        </div>
      )}

      {filtered.length > 0 && (
        <div className="grid gap-4 sm:grid-cols-2">
          <TrendChart
            title="Pass Rate"
            data={passRateData}
            formatValue={(v) => formatPercent(v)}
          />
          <TrendChart
            title="Tokens per Run"
            data={tokensData}
            formatValue={formatNumber}
          />
          <TrendChart
            title="Cost per Run"
            data={costData}
            formatValue={formatCost}
          />
          <TrendChart
            title="Duration per Run"
            data={durationData}
            formatValue={formatDuration}
          />
        </div>
      )}
    </div>
  );
}
