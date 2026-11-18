import Ajv2020 from "ajv/dist/2020";
import lineageSchema from "../../../schemas/execution-lineage-1.0.schema.json";
import attemptSchema from "../../../schemas/execution-attempt-1.0.schema.json";
import terminalSchema from "../../../schemas/runner-terminal-1.0.schema.json";
import manifestSchema from "../../../schemas/artifact-manifest-1.0.schema.json";
import lifecycleSchema from "../../../schemas/execution-lifecycle-1.0.schema.json";
import { lazyValidator } from "../lib/lazyValidator";
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
  lifecycle: AttemptLifecycle;
}

export interface AttemptLifecycle {
  state: string;
  observation: "recorded" | "unobserved" | "invalid";
  events?: number;
  started_at?: string;
  last_event_at?: string;
  deadline_at?: string;
  heartbeat_interval_ms?: number;
  stale_after_ms?: number;
  cancellation: "none" | "requested" | "acknowledged" | "forced";
  cancellation_reason?: "caller" | "deadline";
  deadline_exceeded: boolean;
  partial: boolean;
  retry: { eligible: boolean; reason: string };
  allowed_actions: string[];
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

const validateLineage = lazyValidator(() => new Ajv2020({ strict: false })
  .addSchema(terminalSchema)
  .addSchema(lifecycleSchema)
  .addSchema(manifestSchema)
  .addSchema(attemptSchema)
  .compile<ExecutionLineage>(lineageSchema));

export function parseLogical(run: string, work: string): { run: string; work: string } {
  const logical = { run: run.trim(), work: work.trim() };
  if (!/^run-[0-9a-f]{64}$/.test(logical.run) || !/^trial-[0-9a-f]{64}$/.test(logical.work)) {
    throw new LineageError("Enter a full run-<sha256> run ID and trial-<sha256> work ID.");
  }
  return logical;
}

// Derived lifecycle states each attempt status may report.
const lifecycleStates: Record<LineageAttempt["status"], string[]> = {
  reserved: ["reserved"],
  dispatched: ["unobserved", "running", "cancelling", "orphaned", "publication_incomplete"],
  completed: ["succeeded", "evaluation_failed", "failed", "cancelled", "timed_out", "rejected"],
  completion_unverified: ["unverified"],
};

// The lifecycle state a verified terminal outcome derives.
function terminalState(outcome: string): string {
  switch (outcome) {
    case "success": return "succeeded";
    case "evaluation_failure": return "evaluation_failed";
    case "invalid_input":
    case "unsupported_capability": return "rejected";
    case "cancelled": return "cancelled";
    case "timeout": return "timed_out";
    default: return "failed";
  }
}

// Retry guidance and actions each lifecycle state derives; a response that
// offers another action for a state is inconsistent.
type Policy = { eligible: boolean; reason: string; actions: string[] };
const fixInput: Policy = { eligible: false, reason: "input_rejected", actions: ["inspect", "fix_input"] };
const retryPolicy = (reason: string): Policy => ({ eligible: true, reason, actions: ["inspect", "retry_with_parent"] });
const statePolicies: Record<string, Policy> = {
  succeeded: { eligible: false, reason: "succeeded", actions: ["inspect", "reuse"] },
  evaluation_failed: { eligible: false, reason: "graded_result", actions: ["inspect", "reuse"] },
  rejected: fixInput,
  cancelled: retryPolicy("cancelled"),
  timed_out: retryPolicy("timed_out"),
  unverified: { eligible: false, reason: "evidence_unverified", actions: ["inspect"] },
  reserved: retryPolicy("not_dispatched"),
  unobserved: { eligible: false, reason: "liveness_unobserved", actions: ["inspect", "wait"] },
  publication_incomplete: retryPolicy("publication_incomplete"),
  orphaned: retryPolicy("orphaned"),
  cancelling: { eligible: false, reason: "in_progress", actions: ["inspect", "wait"] },
  running: { eligible: false, reason: "in_progress", actions: ["inspect", "wait", "cancel_owner"] },
};
const inputBoundaries = new Set(["input", "validation", "negotiation", "identity"]);

function policyFor(attempt: LineageAttempt): Policy | undefined {
  if (attempt.lifecycle.state !== "failed") return statePolicies[attempt.lifecycle.state];
  return inputBoundaries.has(attempt.terminal?.failure_boundary ?? "") ? fixInput : retryPolicy("infrastructure_failure");
}

// Liveness states of a dispatched attempt are claims only a recorded journal
// supports; without one the attempt is unobserved.
const observedLiveness = new Set(["running", "cancelling", "orphaned", "publication_incomplete"]);

// The browser re-checks producer invariants so a malformed response cannot
// present an attempt before its parent or misattribute terminals and reuse.
function consistent(lineage: ExecutionLineage, logical: { run: string; work: string }): boolean {
  if (lineage.logical.run !== logical.run || lineage.logical.work !== logical.work) return false;
  const ids = new Set(lineage.attempts.map((attempt) => attempt.execution_id));
  if (ids.size !== lineage.attempts.length) return false;
  const decisions = new Map(lineage.decisions.map((decision) => [decision.decision_id, decision]));
  if (decisions.size !== lineage.decisions.length) return false;
  const diagnosed = (code: string, id: string) => lineage.diagnostics.some((diagnostic) =>
    diagnostic.code === code && diagnostic.execution_ids.includes(id));
  const nativeAttempts = new Map(lineage.attempts.map((attempt) => [attempt.execution_id, attempt.native_attempt]));
  const distinctFor = new Map<string, string[]>();
  for (const decision of lineage.decisions) {
    if (decision.policy === "distinct" && decision.execution_id) {
      const native = nativeAttempts.get(decision.execution_id);
      if (native === undefined) {
        if (!diagnosed("decision_reservation_unavailable", decision.execution_id)) return false;
        continue;
      }
      // Reuse ordinals describe the caller, so only distinct targets must match.
      if (decision.native_attempt !== native) return false;
      distinctFor.set(decision.execution_id, [...(distinctFor.get(decision.execution_id) ?? []), decision.decision_id]);
    }
    if (decision.policy === "reuse" && decision.outcome === "reused" && decision.source
      && !ids.has(decision.source.execution_id) && !diagnosed("reuse_source_unavailable", decision.source.execution_id)) return false;
  }
  const seen = new Map<string, LineageAttempt>();
  for (const [index, attempt] of lineage.attempts.entries()) {
    if (attempt.order !== index + 1) return false;
    const parentID = attempt.parent?.execution_id;
    if (parentID && ids.has(parentID) && !seen.has(parentID)) return false;
    if (parentID && !ids.has(parentID) && !diagnosed("parent_unavailable", attempt.execution_id)) return false;
    const parent = parentID ? seen.get(parentID) : undefined;
    if (attempt.depth !== (parent ? parent.depth + 1 : 0)) return false;
    const finished = attempt.status === "completed" || attempt.status === "completion_unverified";
    if ((attempt.status === "completed") !== !!attempt.terminal) return false;
    if (finished !== !!attempt.manifest_digest) return false;
    if ((attempt.status === "dispatched") && !attempt.dispatch_decision) return false;
    if ((attempt.status === "reserved") && attempt.dispatch_decision) return false;
    // The producer always explains unfinished and unverified attempts.
    if (attempt.status === "dispatched" && !diagnosed("dispatched_without_completion", attempt.execution_id)) return false;
    if (attempt.status === "completion_unverified" && !diagnosed("completion_unverified", attempt.execution_id)
      && !diagnosed("completion_verification_interrupted", attempt.execution_id)) return false;
    if (!lifecycleStates[attempt.status].includes(attempt.lifecycle.state)) return false;
    if (attempt.terminal && attempt.lifecycle.state !== terminalState(attempt.terminal.outcome)) return false;
    if (attempt.lifecycle.state === "orphaned" && !diagnosed("orphaned_attempt", attempt.execution_id)) return false;
    if (attempt.lifecycle.observation === "invalid" && !diagnosed("lifecycle_journal_invalid", attempt.execution_id)) return false;
    const policy = policyFor(attempt);
    const { retry, allowed_actions: actions } = attempt.lifecycle;
    if (!policy || retry.eligible !== policy.eligible || retry.reason !== policy.reason
      || actions.length !== policy.actions.length || actions.some((action, index) => action !== policy.actions[index])) return false;
    if (attempt.status === "dispatched"
      && observedLiveness.has(attempt.lifecycle.state) !== (attempt.lifecycle.observation === "recorded")) return false;
    const dispatches = distinctFor.get(attempt.execution_id) ?? [];
    if ((dispatches.length > 0) !== !!attempt.dispatch_decision) return false;
    if (attempt.dispatch_decision && !dispatches.includes(attempt.dispatch_decision)) return false;
    // With competing dispatches no single journal owns liveness.
    if (dispatches.length > 1 && attempt.lifecycle.observation === "recorded") return false;
    // Unfinished or conflicting reuse decisions are listed but never linked.
    const reuses = lineage.decisions.filter((decision) => decision.policy === "reuse"
      && decision.outcome === "reused" && decision.source?.execution_id === attempt.execution_id);
    const expected = reuses.map((decision) => decision.decision_id);
    if (expected.length > 0 && !finished) return false;
    if (reuses.some((decision) => decision.source?.manifest_digest !== attempt.manifest_digest)) return false;
    if (attempt.reused_by.length !== expected.length
      || new Set(attempt.reused_by).size !== expected.length
      || attempt.reused_by.some((id) => !expected.includes(id))) return false;
    seen.set(attempt.execution_id, attempt);
  }
  // Concurrent-writer diagnostics must name the complete affected execution set.
  const diagnosedSet = (code: string, expected: string[]) => {
    const want = [...expected].sort();
    return lineage.diagnostics.some((diagnostic) => diagnostic.code === code
      && diagnostic.execution_ids.length === want.length
      && [...diagnostic.execution_ids].sort().every((id, index) => id === want[index]));
  };
  const roots = lineage.attempts.filter((attempt) => !attempt.parent).map((attempt) => attempt.execution_id);
  if (roots.length > 1 && !diagnosedSet("concurrent_distinct_attempts", roots)) return false;
  const children = new Map<string, string[]>();
  for (const attempt of lineage.attempts) {
    const parentID = attempt.parent?.execution_id;
    if (parentID && ids.has(parentID)) children.set(parentID, [...(children.get(parentID) ?? []), attempt.execution_id]);
  }
  for (const [parentID, siblings] of children) {
    if (siblings.length > 1 && !diagnosedSet("sibling_attempts", [parentID, ...siblings])) return false;
  }
  for (const [id, dispatches] of distinctFor) {
    if (dispatches.length > 1 && !diagnosedSet("duplicate_dispatch_decision", [id])) return false;
  }
  return true;
}

// Validates a lineage document against its published schema and the same
// consistency rules the lineage view applies.
export function isPublishedLineage(value: unknown): value is ExecutionLineage {
  if (!validateLineage(value)) return false;
  const lineage = value as ExecutionLineage;
  return consistent(lineage, lineage.logical);
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
