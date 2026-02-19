export interface SummaryResponse {
  totalRuns: number;
  totalTasks: number;
  passRate: number;
  avgTokens: number;
  avgCost: number;
  avgDuration: number;
}

export interface RunSummary {
  id: string;
  spec: string;
  model: string;
  outcome: string;
  passCount: number;
  taskCount: number;
  tokens: number;
  cost: number;
  duration: number;
  timestamp: string;
}

export interface GraderResult {
  name: string;
  type: string;
  passed: boolean;
  score: number;
  message: string;
}

export interface TaskResult {
  name: string;
  outcome: string;
  score: number;
  duration: number;
  graderResults: GraderResult[];
}

export interface RunDetail extends RunSummary {
  tasks: TaskResult[];
}

async function fetchJSON<T>(url: string): Promise<T> {
  const res = await fetch(url);
  if (!res.ok) {
    throw new Error(`API error: ${res.status} ${res.statusText}`);
  }
  return res.json() as Promise<T>;
}

export function fetchSummary(): Promise<SummaryResponse> {
  return fetchJSON<SummaryResponse>("/api/summary");
}

export function fetchRuns(
  sort = "timestamp",
  order = "desc",
): Promise<RunSummary[]> {
  return fetchJSON<RunSummary[]>(
    `/api/runs?sort=${encodeURIComponent(sort)}&order=${encodeURIComponent(order)}`,
  );
}

export function fetchRunDetail(id: string): Promise<RunDetail> {
  return fetchJSON<RunDetail>(`/api/runs/${encodeURIComponent(id)}`);
}
