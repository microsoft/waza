import Ajv2020 from "ajv/dist/2020";
import lineageSchema from "../../../schemas/execution-lineage-1.0.schema.json";
import attemptSchema from "../../../schemas/execution-attempt-1.0.schema.json";
import terminalSchema from "../../../schemas/runner-terminal-1.0.schema.json";
import manifestSchema from "../../../schemas/artifact-manifest-1.0.schema.json";
import { parseStrictJSON } from "../lib/strictJson";

export interface LineageAttempt {
  order: number;
  depth: number;
  execution_id: string;
  reservation_digest: string;
  native_attempt: number;
  parent?: { execution_id: string; relationship: "retry" | "branch" };
  status: "reserved" | "dispatched" | "completed" | "completion_unverified";
  dispatch_decision?: string;
  manifest_digest?: string;
  terminal?: { outcome: string; exit_code: number; failure_boundary: string; engine_calls: number };
  reused_by: string[];
}

export interface ExecutionLineage {
  kind: "waza.execution-lineage";
  schema_version: "1.0";
  logical: { run: string; work: string };
  attempts: LineageAttempt[];
  decisions: {
    decision_id: string;
    policy: "distinct" | "reuse";
    outcome: string;
    native_attempt: number;
    execution_id?: string;
    source?: { execution_id: string; manifest_digest: string };
  }[];
  diagnostics: { code: string; message: string; execution_ids: string[] }[];
}

export interface LineageResult {
  lineage: ExecutionLineage;
  json: string;
}

export class LineageError extends Error {}

const validateLineage = new Ajv2020({ strict: false })
  .addSchema(terminalSchema)
  .addSchema(manifestSchema)
  .addSchema(attemptSchema)
  .compile<ExecutionLineage>(lineageSchema);

export function parseLogical(run: string, work: string): { run: string; work: string } {
  const logical = { run: run.trim(), work: work.trim() };
  if (!/^run-[0-9a-f]{64}$/.test(logical.run) || !/^trial-[0-9a-f]{64}$/.test(logical.work)) {
    throw new LineageError("Enter a full run-<sha256> run ID and trial-<sha256> work ID.");
  }
  return logical;
}

// The browser re-checks producer invariants so a malformed response cannot
// present an attempt before its parent or misattribute terminals and reuse.
function consistent(lineage: ExecutionLineage, logical: { run: string; work: string }): boolean {
  if (lineage.logical.run !== logical.run || lineage.logical.work !== logical.work) return false;
  const ids = new Set(lineage.attempts.map((attempt) => attempt.execution_id));
  if (ids.size !== lineage.attempts.length) return false;
  const decisions = new Map(lineage.decisions.map((decision) => [decision.decision_id, decision]));
  if (decisions.size !== lineage.decisions.length) return false;
  const seen = new Map<string, LineageAttempt>();
  for (const [index, attempt] of lineage.attempts.entries()) {
    if (attempt.order !== index + 1) return false;
    const parentID = attempt.parent?.execution_id;
    if (parentID && ids.has(parentID) && !seen.has(parentID)) return false;
    const parent = parentID ? seen.get(parentID) : undefined;
    if (attempt.depth !== (parent ? parent.depth + 1 : 0)) return false;
    const finished = attempt.status === "completed" || attempt.status === "completion_unverified";
    if ((attempt.status === "completed") !== !!attempt.terminal) return false;
    if (finished !== !!attempt.manifest_digest) return false;
    if ((attempt.status === "dispatched") && !attempt.dispatch_decision) return false;
    if ((attempt.status === "reserved") && attempt.dispatch_decision) return false;
    if (attempt.dispatch_decision) {
      const dispatch = decisions.get(attempt.dispatch_decision);
      if (dispatch?.policy !== "distinct" || dispatch.execution_id !== attempt.execution_id) return false;
    }
    const expected = lineage.decisions.filter((decision) => decision.policy === "reuse"
      && decision.source?.execution_id === attempt.execution_id).map((decision) => decision.decision_id);
    if (expected.length > 0 && !finished) return false;
    if (lineage.decisions.some((decision) => decision.source?.execution_id === attempt.execution_id
      && decision.source.manifest_digest !== attempt.manifest_digest)) return false;
    if (attempt.reused_by.length !== expected.length
      || new Set(attempt.reused_by).size !== expected.length
      || attempt.reused_by.some((id) => !expected.includes(id))) return false;
    seen.set(attempt.execution_id, attempt);
  }
  return true;
}

export async function loadLineage(logical: { run: string; work: string }, signal: AbortSignal): Promise<LineageResult> {
  let response: Response;
  let text: string;
  try {
    const query = new URLSearchParams(logical);
    response = await fetch(`/api/v1/lineage?${query.toString()}`, {
      signal: AbortSignal.any([signal, AbortSignal.timeout(40_000)]),
    });
    text = await response.text();
  } catch {
    throw new LineageError("Lineage could not be loaded. Check the server connection and retry.");
  }
  if (response.status !== 200) {
    throw new LineageError(`Lineage API returned HTTP ${response.status}; no lineage is shown. Preserve existing records and inspect operator logs.`);
  }
  let value: unknown;
  try {
    value = parseStrictJSON(text);
  } catch {
    throw new LineageError("Lineage API returned malformed JSON.");
  }
  if (!validateLineage(value) || !consistent(value, logical)) {
    throw new LineageError("Lineage API returned an invalid or inconsistent lineage.");
  }
  return { lineage: value, json: text };
}
