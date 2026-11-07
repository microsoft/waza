import Ajv2020 from "ajv/dist/2020";
import snapshotSchema from "../../../schemas/execution-lifecycle-1.0.schema.json";
import inspectionSchema from "../../../schemas/execution-lifecycle-inspection-1.0.schema.json";
import observationSchema from "../../../schemas/execution-lifecycle-observation-1.0.schema.json";
import manifestSchema from "../../../schemas/artifact-manifest-1.0.schema.json";
import terminalSchema from "../../../schemas/runner-terminal-1.0.schema.json";
import type { RunnerIdentity, RunnerTerminal, WorkSelection } from "./runner";
import { parseStrictJSON } from "../lib/strictJson";

interface Inspection {
  kind: "waza.execution-lifecycle-inspection";
  schema_version: "1.0";
  original_admission: {
    identity: RunnerIdentity;
    selection: WorkSelection;
    execution_mode: "mock" | "copilot";
    grader_ids: string[];
  };
  reservation_digest: string;
  observation_digests: string[];
  deadline?: string;
  as_of?: string;
  stale_after_ms: number;
}

export interface LifecycleSnapshot {
  kind: "waza.execution-lifecycle";
  schema_version: "1.0";
  execution_id: string;
  reservation_digest: string;
  evidence: "caller-declared-unverified";
  state: "admitted" | "running" | "stopped-without-terminal" | "terminal-recorded";
  cancellation: "none" | "requested" | "handling_acknowledged" | "force_requested" | "stop_observed";
  deadline?: string;
  deadline_expired: boolean;
  completed_units?: number;
  terminal?: {
    digest: string;
    outcome: RunnerTerminal["outcome"];
    exit_code: number;
    failure_boundary: string;
    execution_verdict: "unavailable" | "passed" | "failed" | "error";
  };
  retry: { advice: "unknown" | "none" | "conditional-fresh-attempt"; reason: string };
  heartbeat?: string;
  as_of?: string;
  heartbeat_age_ms?: number;
  suspected_orphan?: boolean;
  actions: string[];
  diagnostics: string[];
  observations: {
    sequence: number;
    observed_at: string;
    event: string;
    handling?: string;
    stop?: string;
  }[];
}

const ajv = new Ajv2020({ strict: false })
  .addSchema(terminalSchema)
  .addSchema(manifestSchema)
  .addSchema(observationSchema);
const validateInspection = ajv.compile<Inspection>(inspectionSchema);
const validateSnapshot = ajv.compile<LifecycleSnapshot>(snapshotSchema);

export async function inspectLifecycle(request: string, signal: AbortSignal): Promise<LifecycleSnapshot> {
  const context = parseStrictJSON(request);
  if (!validateInspection(context)) {
    throw new Error("Provide valid retained lifecycle inspection context, including original admission and ordered observation digests.");
  }
  const response = await fetch("/api/v1/lifecycle/inspect", {
    method: "POST", headers: { "Content-Type": "application/json" }, body: request,
    signal: AbortSignal.any([signal, AbortSignal.timeout(40_000)]),
  });
  if (!response.ok) {
    throw new Error(`Lifecycle inspection returned HTTP ${response.status}. No verified worker state or previous projection is available.`);
  }
  const result = parseStrictJSON(await response.text());
  if (!validateSnapshot(result) || result.reservation_digest !== context.reservation_digest ||
      result.deadline !== context.deadline || result.as_of !== context.as_of ||
      result.observations.length !== context.observation_digests.length ||
      result.observations.some((event, index) => event.sequence !== index + 1)) {
    throw new Error("Lifecycle API returned an invalid or mismatched declared projection.");
  }
  return result;
}
