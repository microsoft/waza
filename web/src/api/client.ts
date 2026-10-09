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
  isSignificant?: boolean;
  evidenceRuns?: EvidenceRun[];
}

export interface EvidenceOrigin {
  eval_id: string;
  task_id: string;
  run_number: number;
  attempt_count: number;
  prior_attempts: string;
}

export interface EvidenceReference {
  origin: EvidenceOrigin;
  artifact_id: string;
  pointer?: string;
}

export interface EvidenceManifest {
  version: string;
  sha256: string;
  source_manifest_sha256?: string;
  origin: EvidenceOrigin;
  runtime: {
    waza_version: string | null;
    requested_engine: string;
    requested_model: string;
    execution_mode: string;
    dependency_mode: string;
    requested_policy: string;
    verified_enforcement: string;
    go_version: string;
    platform: string;
    sdk_version: string | null;
    effective_model_version: string | null;
    no_skills: boolean | null;
    native_skill_control: "unknown" | "sdk_default" | "requested_sdk_disable" | "mock_not_applicable";
  };
  redaction: { policy: string; applied_rules?: string[]; match_count: number; limitations: string[] };
  artifacts: {
    id: string;
    kind: string;
    availability: string;
    completeness: string;
    document?: string;
    pointer?: string;
    content_digest?: { sha256: string; encoding: string };
    source_digest?: { sha256: string; encoding: string };
    reason?: string;
    redacted?: boolean;
  }[];
  diagnostics?: { category: string; code: string; message: string }[];
}

export interface EvidenceRun {
  runNumber: number;
  attempts: number;
  cached: boolean;
  assessment: string;
  message?: string;
  manifest?: EvidenceManifest;
  explanations?: {
    task_id: string;
    requirement_id: string;
    checks: {
      check: { scope: string; grader: string; after_turn?: number };
      observation: string;
      category: string;
      message: string;
      references?: EvidenceReference[];
    }[];
  }[];
}

// A rendering guard, not a replacement for the server's digest/origin verification.
export function canDisplayEvidence(run: EvidenceRun): boolean {
  const manifest = run.manifest;
  if (run.assessment !== "recorded" || !manifest || manifest.version !== "1.0") return false;
  const knownKeys = (value: object, keys: string[]) =>
    Object.keys(value).every((key) => keys.includes(key));
  const digest = (value: EvidenceManifest["artifacts"][number]["content_digest"]) =>
    value != null && knownKeys(value, ["sha256", "encoding"]) &&
    /^[a-f0-9]{64}$/.test(value.sha256) && ["json-v1", "utf8", "source-bytes"].includes(value.encoding);
  if (!knownKeys(manifest, ["version", "sha256", "source_manifest_sha256", "origin", "runtime", "redaction", "artifacts", "diagnostics"]) ||
      !/^[a-f0-9]{64}$/.test(manifest.sha256) ||
      (manifest.source_manifest_sha256 !== undefined && !/^[a-f0-9]{64}$/.test(manifest.source_manifest_sha256))) return false;
  const { origin, runtime, redaction, artifacts } = manifest;
  if (!origin || !runtime || !redaction || !Array.isArray(artifacts)) return false;
  if (!knownKeys(origin, ["eval_id", "task_id", "run_number", "attempt_count", "prior_attempts"]) ||
      ![origin.eval_id, origin.task_id, origin.prior_attempts].every((value) => typeof value === "string") ||
      !Number.isSafeInteger(origin.run_number) || origin.run_number < 0 ||
      !Number.isSafeInteger(origin.attempt_count) || origin.attempt_count < 0 ||
      origin.run_number !== run.runNumber || origin.attempt_count !== run.attempts) return false;
  if (!knownKeys(runtime, ["waza_version", "requested_engine", "requested_model", "execution_mode", "dependency_mode", "requested_policy", "verified_enforcement", "go_version", "platform", "sdk_version", "effective_model_version", "no_skills", "native_skill_control"]) ||
      ![runtime.requested_engine, runtime.requested_model, runtime.execution_mode, runtime.dependency_mode, runtime.requested_policy, runtime.verified_enforcement, runtime.go_version, runtime.platform].every((value) => typeof value === "string") ||
      !["mock", "live", "unknown"].includes(runtime.execution_mode) ||
      ![runtime.waza_version, runtime.sdk_version, runtime.effective_model_version].every((value) => value === null || typeof value === "string") ||
      !(runtime.no_skills === null || typeof runtime.no_skills === "boolean") ||
      !["unknown", "sdk_default", "requested_sdk_disable", "mock_not_applicable"].includes(runtime.native_skill_control)) return false;
  if (!knownKeys(redaction, ["policy", "applied_rules", "match_count", "limitations"]) ||
      typeof redaction.policy !== "string" || !Number.isSafeInteger(redaction.match_count) || redaction.match_count < 0 ||
      !Array.isArray(redaction.limitations) || !redaction.limitations.every((value) => typeof value === "string") ||
      (redaction.applied_rules !== undefined && (!Array.isArray(redaction.applied_rules) || !redaction.applied_rules.every((value) => typeof value === "string")))) return false;
  const ids = new Set<string>();
  return artifacts.every((artifact) => {
    if (!artifact || !knownKeys(artifact, ["id", "kind", "availability", "completeness", "document", "pointer", "content_digest", "source_digest", "reason", "redacted"]) ||
        ![artifact.id, artifact.kind, artifact.completeness].every((value) => typeof value === "string") ||
        !["complete", "partial", "unknown"].includes(artifact.completeness) ||
        ![artifact.document, artifact.pointer, artifact.reason].every((value) => value === undefined || typeof value === "string") ||
        (artifact.redacted !== undefined && typeof artifact.redacted !== "boolean")) return false;
    if (!artifact.id || ids.has(artifact.id)) return false;
    ids.add(artifact.id);
    switch (artifact.availability) {
      case "captured":
        return digest(artifact.content_digest) && artifact.content_digest!.encoding !== "source-bytes" &&
          (artifact.source_digest === undefined || digest(artifact.source_digest)) &&
          artifact.document === "snapshot" && typeof artifact.pointer === "string" && artifact.pointer.startsWith("/") &&
          (artifact.completeness === "complete" || !!artifact.reason);
      case "digest_only":
        return digest(artifact.source_digest) && artifact.content_digest === undefined &&
          artifact.document === undefined && artifact.pointer === undefined && !!artifact.reason;
      case "unavailable":
      case "not_requested":
        return !!artifact.reason && artifact.completeness !== "complete" &&
          artifact.content_digest === undefined && artifact.source_digest === undefined &&
          artifact.document === undefined && artifact.pointer === undefined;
      default:
        return false;
    }
  }) && (manifest.diagnostics === undefined || (Array.isArray(manifest.diagnostics) &&
    manifest.diagnostics.every((value) => value && knownKeys(value, ["category", "code", "message"]) &&
      [value.category, value.code, value.message].every((field) => typeof field === "string"))));
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
