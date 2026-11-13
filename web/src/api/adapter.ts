import { parseStrictJSON } from "../lib/strictJson";

export interface AdapterCall {
  command: string[];
  http: { method: "GET" | "POST"; path: string };
  input?: string;
  input_sha256?: string;
}

export interface AdapterTranslation {
  kind: "waza.adapter-translation";
  schema_version: "1.0";
  adapter: { name: string; version: string; runner_versions: string[] };
  status: "translated" | "rejected";
  descriptor_sha256: string;
  work_id?: string;
  operation?: "submit" | "checkpoint" | "resume" | "status";
  summary?: {
    run_key?: string;
    selection?: { shard: string; task: string; trial: number; attempt: number };
    request_sha256?: string;
    policy?: string;
    parent?: { execution_id: string; relationship: string };
    source?: { execution_id: string; manifest_digest: string };
    checkpoint_sha256?: string;
    logical?: { run: string; work: string };
  };
  call?: AdapterCall;
  diagnostics: { code: string; message: string }[];
}

export type AdapterStatus = "terminal" | "rejected" | "operation_error" | "observed" | "contract_error";

export interface AdapterOutcome {
  status: AdapterStatus;
  exitCode: number;
  publicKind?: string;
  publicStatus?: string;
  outcome?: string;
  engineCalls?: number;
  executionId?: string;
  logical?: { run: string; work: string };
  manifestDigest?: string;
  checkpointSHA256?: string;
  attempts?: number;
  diagnostics: { code: string; message: string }[];
  json: string;
}

export class AdapterError extends Error {}

const expectedKinds: Record<string, string> = {
  submit: "waza.execution-submission-result",
  checkpoint: "waza.runner-checkpoint-result",
  resume: "waza.runner-checkpoint-result",
  status: "waza.execution-lineage",
};

// Mirrors internal/runneradapter statusTable: one public status, one adapter status.
const statusTable: Record<string, AdapterStatus> = {
  completed: "terminal",
  reused: "terminal",
  rejected: "rejected",
  submission_error: "operation_error",
  checkpoint_error: "operation_error",
  operation_error: "operation_error",
};

export async function translateDescriptor(text: string, signal?: AbortSignal): Promise<AdapterTranslation> {
  const response = await fetch("/api/v1/adapter/translate", {
    method: "POST", headers: { "Content-Type": "application/json" }, body: text, signal,
  });
  const body = await response.text();
  let value: AdapterTranslation;
  try {
    value = parseStrictJSON(body) as AdapterTranslation;
  } catch {
    throw new AdapterError(`Translation route returned HTTP ${response.status} without a translation document.`);
  }
  if (value?.kind !== "waza.adapter-translation") throw new AdapterError("Translation route returned an unexpected document.");
  return value;
}

interface PublicSubmission {
  status?: string;
  exit_code?: number;
  engine_calls?: number;
  reservation?: { execution_id: string; logical: { run: string; work: string } };
  completion?: { execution_id: string; manifest_digest: string };
  terminal?: { outcome: string };
  diagnostics?: { code: string; message: string }[];
}

function contractError(message: string, json: string): AdapterOutcome {
  return { status: "contract_error", exitCode: 2, diagnostics: [{ code: "contract_violation", message }], json };
}

export function mapReply(operation: string, exitCode: number, json: string): AdapterOutcome {
  let value: Record<string, unknown>;
  try {
    value = parseStrictJSON(json) as Record<string, unknown>;
  } catch {
    return contractError("Public reply is not strict JSON.", json);
  }
  if (value?.kind !== expectedKinds[operation]) {
    return contractError(`Operation ${operation} expects ${expectedKinds[operation]}, got ${String(value?.kind)}.`, json);
  }
  if (operation === "status") {
    const lineage = value as { logical: { run: string; work: string }; attempts: unknown[]; diagnostics: { code: string; message: string }[] };
    return { status: "observed", exitCode: 0, publicKind: String(value.kind), logical: lineage.logical,
      attempts: lineage.attempts.length, diagnostics: [], json };
  }
  const submission = (operation === "submit" ? value : value.submission) as PublicSubmission | undefined;
  const publicStatus = String(value.status);
  if (value.exit_code !== exitCode) return contractError(`Public exit code ${exitCode} contradicts the emitted result.`, json);
  const status = statusTable[publicStatus];
  if (!status) return contractError(`Public status ${publicStatus} is not part of the contract.`, json);
  const outcome: AdapterOutcome = {
    status, exitCode, publicKind: String(value.kind), publicStatus, json,
    diagnostics: (value.diagnostics as { code: string; message: string }[] | undefined) ?? [],
    checkpointSHA256: typeof value.finalized_checkpoint_sha256 === "string" ? value.finalized_checkpoint_sha256 : undefined,
  };
  if (submission) {
    outcome.engineCalls = submission.engine_calls;
    outcome.executionId = submission.completion?.execution_id ?? submission.reservation?.execution_id;
    outcome.logical = submission.reservation?.logical;
    outcome.manifestDigest = submission.completion?.manifest_digest;
    outcome.outcome = submission.terminal?.outcome;
  }
  if (status === "terminal" && (!outcome.manifestDigest || !outcome.logical || !outcome.outcome)) {
    return contractError("Terminal public result is missing its completion, reservation or terminal outcome.", json);
  }
  return outcome;
}

// Sends the translated call to its public route exactly once; never retries.
export async function dispatchCall(operation: string, call: AdapterCall, signal?: AbortSignal): Promise<AdapterOutcome> {
  const init: RequestInit = { method: call.http.method, signal };
  if (call.http.method === "POST") {
    init.headers = { "Content-Type": "application/json" };
    init.body = call.input ?? "";
  }
  const response = await fetch(call.http.path, init);
  const json = await response.text();
  const header = response.headers.get("X-Waza-Exit-Code");
  if (header === null && !response.ok) {
    return contractError(`Public route returned HTTP ${response.status} without a contract result.`, json);
  }
  const exitCode = header === null ? 0 : Number(header);
  if (!Number.isInteger(exitCode) || exitCode < 0 || exitCode > 6) return contractError(`Invalid exit code header ${header}.`, json);
  return mapReply(operation, exitCode, json);
}
