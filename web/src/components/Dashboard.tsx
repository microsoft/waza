import { Download } from "lucide-react";
import KPICards, { KPICardsSkeleton } from "./KPICards";
import RunsTable, { RunsTableSkeleton } from "./RunsTable";
import { useSummary, useRuns } from "../hooks/useApi";
import { exportRunsToCSV } from "../lib/export";

function ErrorBox({
  message,
  onRetry,
}: {
  message: string;
  onRetry: () => void;
}) {
  return (
    <div className="rounded-lg border border-red-500/30 bg-red-500/10 p-6 text-center">
      <p className="text-red-400">{message}</p>
      <button
        onClick={onRetry}
        className="mt-3 rounded bg-zinc-700 px-4 py-2 text-sm text-zinc-100"
      >
        Retry
      </button>
    </div>
  );
}

export default function Dashboard() {
  const summary = useSummary();
  const runs = useRuns();

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-semibold text-zinc-100">Eval Runs</h1>
        {runs.data && runs.data.length > 0 && (
          <button
            onClick={() => exportRunsToCSV(runs.data!)}
            className="inline-flex items-center gap-1.5 rounded bg-zinc-700 px-3 py-1.5 text-sm text-zinc-100 hover:bg-zinc-600 transition-colors"
          >
            <Download className="h-3.5 w-3.5" />
            Export CSV
          </button>
        )}
      </div>

      {summary.isLoading && <KPICardsSkeleton />}
      {summary.isError && (
        <ErrorBox
          message={
            summary.error instanceof Error
              ? summary.error.message
              : "Failed to load summary"
          }
          onRetry={() => void summary.refetch()}
        />
      )}
      {summary.data && <KPICards data={summary.data} />}

      {runs.isLoading && <RunsTableSkeleton />}
      {runs.isError && (
        <ErrorBox
          message={
            runs.error instanceof Error
              ? runs.error.message
              : "Failed to load runs"
          }
          onRetry={() => void runs.refetch()}
        />
      )}
      {runs.data && <RunsTable data={runs.data} />}
    </div>
  );
}
