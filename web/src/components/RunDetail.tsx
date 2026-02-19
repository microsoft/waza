import { useState } from "react";
import {
  ArrowLeft,
  CheckCircle2,
  XCircle,
  AlertCircle,
  ChevronRight,
  ChevronDown,
  Download,
} from "lucide-react";
import { useRunDetail } from "../hooks/useApi";
import type { TaskResult, GraderResult } from "../api/client";
import {
  formatDuration,
  formatCost,
  formatNumber,
  formatPercent,
  formatRelativeTime,
} from "../lib/format";
import { exportRunDetailToCSV } from "../lib/export";
import TrajectoryViewer from "./TrajectoryViewer";

function OutcomeBadge({ outcome }: { outcome: string }) {
  if (outcome.startsWith("pass"))
    return (
      <span className="inline-flex items-center gap-1 rounded-full bg-green-500/10 px-2 py-0.5 text-xs font-medium text-green-500">
        <CheckCircle2 className="h-3 w-3" /> pass
      </span>
    );
  if (outcome.startsWith("fail"))
    return (
      <span className="inline-flex items-center gap-1 rounded-full bg-red-500/10 px-2 py-0.5 text-xs font-medium text-red-500">
        <XCircle className="h-3 w-3" /> fail
      </span>
    );
  return (
    <span className="inline-flex items-center gap-1 rounded-full bg-yellow-500/10 px-2 py-0.5 text-xs font-medium text-yellow-500">
      <AlertCircle className="h-3 w-3" /> error
    </span>
  );
}

function TypeBadge({ type }: { type: string }) {
  return (
    <span className="rounded bg-zinc-700 px-1.5 py-0.5 text-xs text-zinc-300">
      {type}
    </span>
  );
}

function GraderRow({ grader }: { grader: GraderResult }) {
  return (
    <tr className="border-b border-zinc-700/30">
      <td className="py-2 pl-12 pr-4 text-zinc-300">{grader.name}</td>
      <td className="px-4 py-2">
        <TypeBadge type={grader.type} />
      </td>
      <td className="px-4 py-2">
        {grader.passed ? (
          <CheckCircle2 className="h-4 w-4 text-green-500" />
        ) : (
          <XCircle className="h-4 w-4 text-red-500" />
        )}
      </td>
      <td className="px-4 py-2 text-zinc-300">
        {formatPercent(grader.score)}
      </td>
      <td className="px-4 py-2 text-zinc-400">{grader.message}</td>
    </tr>
  );
}

function TaskRow({ task }: { task: TaskResult }) {
  const [expanded, setExpanded] = useState(false);

  return (
    <>
      <tr
        className="cursor-pointer border-b border-zinc-700/50 hover:bg-zinc-700/50"
        onClick={() => setExpanded(!expanded)}
      >
        <td className="px-4 py-3">
          <span className="flex items-center gap-2">
            {expanded ? (
              <ChevronDown className="h-4 w-4 text-zinc-500" />
            ) : (
              <ChevronRight className="h-4 w-4 text-zinc-500" />
            )}
            <span className="font-medium text-zinc-100">{task.name}</span>
          </span>
        </td>
        <td className="px-4 py-3">
          <OutcomeBadge outcome={task.outcome} />
        </td>
        <td className="px-4 py-3 text-zinc-300">
          {formatPercent(task.score)}
        </td>
        <td className="px-4 py-3 text-zinc-300">
          {formatDuration(task.duration)}
        </td>
      </tr>
      {expanded &&
        task.graderResults.map((g) => (
          <GraderRow key={g.name} grader={g} />
        ))}
    </>
  );
}

function DetailSkeleton() {
  return (
    <div className="space-y-6">
      <div className="h-5 w-24 rounded bg-zinc-700" />
      <div className="h-8 w-64 rounded bg-zinc-700" />
      <div className="flex gap-4">
        {Array.from({ length: 4 }).map((_, i) => (
          <div key={i} className="h-16 w-32 rounded-lg bg-zinc-800 border border-zinc-700" />
        ))}
      </div>
      <div className="h-48 rounded-lg bg-zinc-800 border border-zinc-700" />
    </div>
  );
}

export default function RunDetail({ id }: { id: string }) {
  const { data, isLoading, isError, error, refetch } = useRunDetail(id);
  const [activeTab, setActiveTab] = useState<"tasks" | "trajectory">("tasks");
  const [trajectoryTask, setTrajectoryTask] = useState<TaskResult | null>(null);

  if (isLoading) return <DetailSkeleton />;

  if (isError) {
    return (
      <div className="space-y-4">
        <a href="#/" className="text-sm text-blue-500">
          <ArrowLeft className="mr-1 inline h-4 w-4" />
          Back to runs
        </a>
        <div className="rounded-lg border border-red-500/30 bg-red-500/10 p-6 text-center">
          <p className="text-red-400">
            {error instanceof Error ? error.message : "Failed to load run"}
          </p>
          <button
            onClick={() => void refetch()}
            className="mt-3 rounded bg-zinc-700 px-4 py-2 text-sm text-zinc-100"
          >
            Retry
          </button>
        </div>
      </div>
    );
  }

  if (!data) return null;

  const passRate =
    data.taskCount > 0 ? data.passCount / data.taskCount : 0;

  return (
    <div className="space-y-6">
      <a href="#/" className="inline-flex items-center gap-1 text-sm text-blue-500">
        <ArrowLeft className="h-4 w-4" />
        Back to runs
      </a>

      <div className="flex flex-wrap items-center gap-3">
        <h1 className="text-2xl font-semibold text-zinc-100">{data.spec}</h1>
        <OutcomeBadge outcome={data.outcome} />
        <span className="text-sm text-zinc-400">{data.model}</span>
        <span className="text-sm text-zinc-500">
          {formatRelativeTime(data.timestamp)}
        </span>
        <button
          onClick={() => exportRunDetailToCSV(data)}
          className="ml-auto inline-flex items-center gap-1.5 rounded bg-zinc-700 px-3 py-1.5 text-sm text-zinc-100 hover:bg-zinc-600 transition-colors"
        >
          <Download className="h-3.5 w-3.5" />
          Export CSV
        </button>
      </div>

      <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
        <StatCard label="Pass Rate" value={formatPercent(passRate)} />
        <StatCard label="Tokens" value={formatNumber(data.tokens)} />
        <StatCard label="Cost" value={formatCost(data.cost)} />
        <StatCard label="Duration" value={formatDuration(data.duration)} />
      </div>

      <div className="flex gap-1 border-b border-zinc-700">
        <button
          onClick={() => { setActiveTab("tasks"); setTrajectoryTask(null); }}
          className={`px-4 py-2 text-sm font-medium transition-colors ${
            activeTab === "tasks"
              ? "border-b-2 border-blue-500 text-zinc-100"
              : "text-zinc-400 hover:text-zinc-200"
          }`}
        >
          Tasks
        </button>
        <button
          onClick={() => setActiveTab("trajectory")}
          className={`px-4 py-2 text-sm font-medium transition-colors ${
            activeTab === "trajectory"
              ? "border-b-2 border-blue-500 text-zinc-100"
              : "text-zinc-400 hover:text-zinc-200"
          }`}
        >
          Trajectory
        </button>
      </div>

      {activeTab === "tasks" && (
      <div className="overflow-x-auto rounded-lg border border-zinc-700 bg-zinc-800">
        <table className="w-full text-left text-sm">
          <thead>
            <tr className="border-b border-zinc-700">
              <th className="px-4 py-3 text-xs font-medium text-zinc-400 uppercase">
                Task
              </th>
              <th className="px-4 py-3 text-xs font-medium text-zinc-400 uppercase">
                Outcome
              </th>
              <th className="px-4 py-3 text-xs font-medium text-zinc-400 uppercase">
                Score
              </th>
              <th className="px-4 py-3 text-xs font-medium text-zinc-400 uppercase">
                Duration
              </th>
            </tr>
          </thead>
          <tbody>
            {data.tasks.map((task) => (
              <TaskRow key={task.name} task={task} />
            ))}
          </tbody>
        </table>
        {data.tasks.length === 0 && (
          <div className="p-8 text-center text-zinc-500">No tasks found.</div>
        )}
      </div>
      )}

      {activeTab === "trajectory" && (
        <div className="space-y-4">
          {!trajectoryTask ? (
            <div className="space-y-2">
              <p className="text-sm text-zinc-400">Select a task to view its trajectory:</p>
              {data.tasks.map((task) => (
                <button
                  key={task.name}
                  onClick={() => setTrajectoryTask(task)}
                  className="flex w-full items-center justify-between rounded-lg border border-zinc-700 bg-zinc-800 px-4 py-3 text-left hover:bg-zinc-700/50 transition-colors"
                >
                  <span className="font-medium text-zinc-100">{task.name}</span>
                  <OutcomeBadge outcome={task.outcome} />
                </button>
              ))}
              {data.tasks.length === 0 && (
                <p className="text-sm text-zinc-500">No tasks available.</p>
              )}
            </div>
          ) : (
            <div className="space-y-3">
              <button
                onClick={() => setTrajectoryTask(null)}
                className="inline-flex items-center gap-1 text-sm text-blue-500 hover:text-blue-400"
              >
                <ArrowLeft className="h-3.5 w-3.5" />
                Back to task list
              </button>
              <TrajectoryViewer task={trajectoryTask} />
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function StatCard({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-lg border border-zinc-700 bg-zinc-800 p-3">
      <p className="text-xs text-zinc-400">{label}</p>
      <p className="mt-1 text-lg font-semibold text-zinc-100">{value}</p>
    </div>
  );
}
