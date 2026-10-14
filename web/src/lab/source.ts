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
      await queryClient.cancelQueries({ queryKey: ["lab", "comparison"], type: "active" });
      const pairs = queryClient.getQueryCache().findAll({
        queryKey: ["lab", "comparison"], type: "active",
      });
      const pairedIds = new Set(pairs.flatMap((pair) => pair.queryKey.slice(2)));
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["lab", "comparison"], refetchType: "active" }),
        queryClient.invalidateQueries({
          queryKey: ["lab", "detail"],
          predicate: (detail) => !pairedIds.has(detail.queryKey[2]),
          refetchType: "active",
        }),
      ]);
      return summaries;
    },
    enabled: !demo,
    refetchInterval: (query) => isLocalArchive(query.state.data) ? 15_000 : false,
    refetchOnWindowFocus: (query) => isLocalArchive(query.state.data),
    refetchOnReconnect: (query) => isLocalArchive(query.state.data) ? "always" : false,
    retry: false,
  });
  return {
    ...query,
    automaticRefresh: isLocalArchive(query.data),
    runs: demo ? runs : (query.data ?? []),
  };
}

export function useLabComparison(ids: [string, string] | undefined, demo: boolean) {
  const queryClient = useQueryClient();
  return useQuery({
    queryKey: ["lab", "comparison", ids?.[0], ids?.[1]],
    enabled: !demo && ids !== undefined,
    queryFn: async ({ signal }): Promise<[LabRun, LabRun]> => {
      if (!ids) throw new Error("Choose two runs to load comparison evidence");
      const before = ids.map((id) => queryClient.getQueryState<LabRun>(["lab", "detail", id]));
      const abort = new AbortController();
      try {
        const requestSignal = AbortSignal.any([signal, abort.signal]);
        const pair = await Promise.all(ids.map(async (id) =>
          fromDetail(await fetchRunDetail(id, requestSignal)),
        ));
        signal.throwIfAborted();
        // Publish shared inspector data only after both responses succeeded, and
        // never overwrite an independent request or a newer cache update.
        pair.forEach((run, index) => {
          const key = ["lab", "detail", run.id];
          const current = queryClient.getQueryState<LabRun>(key);
          if (current?.fetchStatus !== "fetching" &&
              current?.data === before[index]?.data &&
              current?.dataUpdatedAt === before[index]?.dataUpdatedAt) {
            queryClient.setQueryData(key, run);
          }
        });
        return [pair[0]!, pair[1]!];
      } catch (error) {
        abort.abort(error);
        throw error;
      }
    },
    staleTime: 0,
    // The archive observer drives local reconnects through the same pair refresh.
    refetchOnReconnect: false,
    retry: false,
  });
}

export function useLabDetails(ids: string[], demo: boolean, snapshot = false) {
  const queryClient = useQueryClient();
  return useQueries({
    queries: ids.map((id) => ({
      queryKey: ["lab", "detail", id],
      queryFn: async ({ signal }: { signal: AbortSignal }) =>
        fromDetail(await fetchRunDetail(id, signal)),
      enabled: !demo,
      staleTime: 0,
      refetchOnReconnect: (query: { state: { data?: LabRun } }) =>
        !snapshot && isLocalArchive(query.state.data
          ? [query.state.data]
          : queryClient.getQueryData<LabRun[]>(["lab", "runs"])),
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
