import Ajv2020 from "ajv/dist/2020";
import sourceSchema from "../../../schemas/execution-lifecycle-source-1.0.schema.json";
import querySchema from "../../../schemas/execution-lifecycle-source-inspection-1.0.schema.json";
import snapshotSchema from "../../../schemas/execution-lifecycle-1.0.schema.json";
import observationSchema from "../../../schemas/execution-lifecycle-observation-1.0.schema.json";
import attemptSchema from "../../../schemas/execution-attempt-1.0.schema.json";
import manifestSchema from "../../../schemas/artifact-manifest-1.0.schema.json";
import terminalSchema from "../../../schemas/runner-terminal-1.0.schema.json";
import { parseStrictJSON } from "../lib/strictJson";
import { isCanonicalCheckpointTime } from "./checkpoints";
import type { LifecycleSnapshot } from "./lifecycle";

export interface LifecycleSourceRequest {
  kind: "waza.execution-lifecycle-source-inspection";
  schema_version: "1.0";
  source: { execution_id: string; manifest_digest: string };
  as_of?: string;
  stale_after_ms: number;
}

export interface LifecycleSource {
  kind: "waza.execution-lifecycle-source";
  schema_version: "1.0";
  source: LifecycleSourceRequest["source"];
  verification: "verified-nonsnapshot-content";
  context: "original-source-declarations";
  snapshot: LifecycleSnapshot;
  runtime_observations: {
    owned_engine_bound: boolean;
    observations: { sequence: number; observed_at: string; operation: string; scope: string; result: string }[];
    dropped: 0;
    pending_at_seal: 0;
    process_exit: "unknown";
    late_observation_policy: "excluded-after-seal; retained-operator-counter";
  };
}

const ajv = new Ajv2020({ strict: false })
  .addSchema(terminalSchema).addSchema(manifestSchema).addSchema(attemptSchema)
  .addSchema(observationSchema).addSchema(snapshotSchema);
const validateQuery = ajv.compile<LifecycleSourceRequest>(querySchema);
const validateSource = ajv.compile<LifecycleSource>(sourceSchema);

export function parseLifecycleSourceRequest(text: string): LifecycleSourceRequest {
  const query = parseStrictJSON(text);
  if (!validateQuery(query) || (query.as_of !== undefined && !isCanonicalCheckpointTime(query.as_of))) {
    throw new Error("Provide an original execution/manifest source and explicit positive stale_after_ms; as_of must be canonical UTC when present.");
  }
  return query;
}

export function parseLifecycleSource(text: string, query: LifecycleSourceRequest): LifecycleSource {
  const value = parseStrictJSON(text);
  if (!validateSource(value) || value.source.execution_id !== query.source.execution_id ||
      value.source.manifest_digest !== query.source.manifest_digest ||
      value.snapshot.execution_id !== query.source.execution_id || value.snapshot.as_of !== query.as_of ||
      value.snapshot.observations.some((event, index) =>
        event.sequence !== index + 1 || !isCanonicalCheckpointTime(event.observed_at)) ||
      value.runtime_observations.observations.some((event, index) =>
        event.sequence !== index + 1 || !isCanonicalCheckpointTime(event.observed_at)) ||
      [value.snapshot.deadline, value.snapshot.heartbeat].some((time) =>
        time !== undefined && !isCanonicalCheckpointTime(time))) {
    throw new Error("Lifecycle source API returned invalid or mismatched original metadata.");
  }
  return value;
}

export async function inspectLifecycleSource(text: string, signal: AbortSignal): Promise<{ value: LifecycleSource; json: string }> {
  const query = parseLifecycleSourceRequest(text);
  const response = await fetch("/api/v1/lifecycle/source", {
    method: "POST", headers: { "Content-Type": "application/json" }, body: text,
    signal: AbortSignal.any([signal, AbortSignal.timeout(40_000)]),
  });
  if (!response.ok) {
    throw new Error(`Lifecycle source inspection returned HTTP ${response.status}. No current source projection is available.`);
  }
  const json = await response.text();
  if (new TextEncoder().encode(json).byteLength > 1_048_576) {
    throw new Error("Lifecycle source API exceeded its response bound.");
  }
  return { value: parseLifecycleSource(json, query), json };
}
