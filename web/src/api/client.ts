export type CostSource = 'sdk' | 'table' | 'estimate' | 'mixed';

export interface SummaryResponse {
  totalRuns: number;
  totalTasks: number;
  passRate: number;
  avgTokens: number;
  avgCost: number;
  avgDuration: number;
  costSource?: CostSource;
}

export interface RunSummary {
  id: string;
  spec: string;
  model: string;
  judgeModel?: string;
  outcome: string;
  passCount: number;
  taskCount: number;
  tokens: number;
  cost: number;
  costSource?: CostSource;
  duration: number;
  timestamp: string;
  weightedScore?: number;
}

export interface GraderResult {
  name: string;
  type: string;
  passed: boolean;
  score: number;
  weight?: number;
  message: string;
}

export interface TranscriptEvent {
  type: string;
  content?: string;
  message?: string;
  toolCallId?: string;
  toolName?: string;
  arguments?: unknown;
  toolResult?: unknown;
  success?: boolean;
}

export interface BootstrapCI {
  lower: number;
  upper: number;
  mean: number;
  confidenceLevel: number;
}

export interface SessionDigest {
  totalTurns: number;
  toolCallCount: number;
  tokensIn: number;
  tokensOut: number;
  tokensTotal: number;
  toolsUsed: string[];
  errors: string[];
}

export interface TaskResult {
  name: string;
  outcome: string;
  score: number;
  weightedScore?: number;
  duration: number;
  graderResults: GraderResult[];
  transcript?: TranscriptEvent[];
  sessionDigest?: SessionDigest;
  bootstrapCI?: BootstrapCI;
  isSignificant?: boolean;
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
