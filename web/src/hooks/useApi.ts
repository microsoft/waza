import { useQuery } from "@tanstack/react-query";
import { fetchSummary, fetchRuns, fetchRunDetail } from "../api/client";

export function useSummary() {
  return useQuery({
    queryKey: ["summary"],
    queryFn: fetchSummary,
  });
}

export function useRuns(sort = "timestamp", order = "desc") {
  return useQuery({
    queryKey: ["runs", sort, order],
    queryFn: () => fetchRuns(sort, order),
  });
}

export function useRunDetail(id: string) {
  return useQuery({
    queryKey: ["run", id],
    queryFn: () => fetchRunDetail(id),
    enabled: !!id,
  });
}
