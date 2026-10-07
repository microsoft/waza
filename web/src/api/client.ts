export type CostSource = 'sdk' | 'table' | 'estimate' | 'mixed';

export interface SummaryResponse {
  totalRuns: number;
  totalTasks: number;
  passRate: number;
  avgTokens: number;
  avgPremiumRequests: number;
  /** Mean final AI-credit total across runs that reported one; omitted when none did. */
  avgAICredits?: number;
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
  premiumRequests: number;
  /** Final AI-credit total reported by the Copilot SDK; omitted for legacy runs. */
  aiCredits?: number;
  /** Per-model usage breakdown, sorted by model ID. */
  modelUsage?: ModelUsage[];
  cost: number;
  costSource?: CostSource;
  duration: number;
  timestamp: string;
  weightedScore?: number;
}

export interface ModelUsage {
  model: string;
  /** Final AI-credit total the Copilot SDK attributed to this model. */
  aiCredits?: number;
  inputTokens: number;
  cacheReadTokens: number;
  cacheWriteTokens: number;
  outputTokens: number;
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
  toolPolicyMode?: "unrestricted" | "deny_all" | "allow_list";
  toolPolicyDenials?: { tool: string; kind: string; reason: string }[];
  totalTurns: number;
  toolCallCount: number;
  tokensIn: number;
  tokensOut: number;
  tokensTotal: number;
  toolsUsed: string[];
  errors: string[];
}

export type ResponderOutcome = "stopped" | "abstained" | "cap_exhausted" | "error";

export interface ResponderInfo {
  followupsSent: number;
  outcome: ResponderOutcome;
  reason?: string;
}

export interface TaskResult {
  name: string;
  prompt?: string;
  outcome: string;
  score: number;
  weightedScore?: number;
  duration: number;
  graderResults: GraderResult[];
  transcript?: TranscriptEvent[];
  sessionDigest?: SessionDigest;
  responder?: ResponderInfo;
  bootstrapCI?: BootstrapCI;
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
