import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  fetchLabRuns,
  fetchRunDetail,
  normalizeRunDetail,
  type RunDetailWire,
  type RunSummary,
} from "../api/client";
import { runs, type LabRun } from "./data";

export function fromSummary(summary: RunSummary): LabRun {
  return {
    id: summary.id,
    ...(summary.skill === undefined ? {} : { skill: summary.skill }),
    evalId: summary.spec,
    revision: "Unavailable",
    model: summary.model,
    judge: summary.judgeModel ?? "Unavailable",
    date: new Date(summary.timestamp).toISOString(),
    source: summary.source ?? "Unavailable",
    repetitions: summary.repetitions,
    tasks: [],
    aiCredits: summary.aiCredits,
    summary,
  };
}

export function fromDetail(wire: RunDetailWire): LabRun {
  const detail = normalizeRunDetail(wire);
  const names = new Map<string, number>();
  const ids = new Map<string, number>();
  for (const task of detail.tasks) {
    names.set(task.name, (names.get(task.name) ?? 0) + 1);
    if (task.id) ids.set(task.id, (ids.get(task.id) ?? 0) + 1);
  }
  return {
    ...fromSummary(detail),
    api: detail,
    tasks: detail.tasks.map((task, index) => {
      const comparable = task.id
        ? ids.get(task.id) === 1
        : names.get(task.name) === 1;
      return {
        id: comparable
          ? task.id
            ? `id:${task.id}`
            : `name:${task.name}`
          : `unmatched:${detail.id}:${index}`,
        comparable,
        name: task.name,
        outcome: /^pass/i.test(task.outcome)
          ? "passed"
          : /^(fail|error)/i.test(task.outcome)
            ? "failed"
            : task.outcome,
        score: task.weightedScore ?? null,
        duration: task.duration,
        tokens: task.sessionDigest?.tokensTotal,
        graders: task.graderResults.map((grader) => ({
          name: grader.name,
          type: grader.type,
          score: grader.score,
          passed: grader.passed,
          weight: grader.weight,
          evidence: grader.message,
        })),
        api: task,
      };
    }),
  };
}

function isLocalArchive(items: LabRun[] | undefined) {
  return !(items?.some((run) => run.source !== "local") ?? false);
}

export function useLabSource(demo: boolean) {
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: ["lab", "runs"],
    queryFn: async ({ signal }) => {
      const summaries = (await fetchLabRuns(signal)).map(fromSummary);
      // Regrading can change evidence without changing the run ID or timestamp.
      // Refresh mounted details only after the archive refresh succeeds.
      await queryClient.invalidateQueries({
        queryKey: ["lab", "detail"],
        refetchType: "active",
      });
      return summaries;
    },
    enabled: !demo,
    refetchInterval: (query) => isLocalArchive(query.state.data) ? 15_000 : false,
    refetchOnWindowFocus: (query) => isLocalArchive(query.state.data),
    refetchOnReconnect: (query) => isLocalArchive(query.state.data),
    retry: false,
  });
  return {
    ...query,
    automaticRefresh: isLocalArchive(query.data),
    runs: demo ? runs : (query.data ?? []),
  };
}

export function useLabDetails(ids: string[], demo: boolean) {
  return useQueries({
    queries: ids.map((id) => ({
      queryKey: ["lab", "detail", id],
      queryFn: async ({ signal }: { signal: AbortSignal }) =>
        fromDetail(await fetchRunDetail(id, signal)),
      enabled: !demo,
      staleTime: 0,
      refetchOnReconnect: (query: { state: { data?: LabRun } }) =>
        isLocalArchive(query.state.data ? [query.state.data] : undefined),
      retry: false,
    })),
  });
}

export async function reportDetails(items: LabRun[]): Promise<LabRun[]> {
  const abort = new AbortController();
  let failure: unknown;
  let index = 0;
  const result = new Array<LabRun>(items.length);
  const worker = async () => {
    while (!abort.signal.aborted) {
      const next = index++;
      const item = items[next];
      if (!item) return;
      try {
        result[next] = fromDetail(await fetchRunDetail(item.id, abort.signal));
      } catch (error) {
        if (!abort.signal.aborted) failure = error;
        abort.abort();
        throw error;
      }
    }
  };
  // Settle every worker before returning so a failed export leaves no requests running.
  const settled = await Promise.allSettled(
    Array.from({ length: Math.min(4, items.length) }, worker),
  );
  const rejected = settled.find((result) => result.status === "rejected");
  if (rejected?.status === "rejected") throw failure ?? rejected.reason;
  return result;
}
