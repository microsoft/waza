import Ajv2020 from "ajv/dist/2020";
import responseSchema from "../../../schemas/runner-negotiation-response-1.0.schema.json";
import unitSchema from "../../../schemas/runner-unit-1.0.schema.json";
import { parseStrictJSON } from "../lib/strictJson";

export interface RunnerNegotiation {
  kind: "waza.runner-negotiation";
  schema_version: "1.0";
  status: "compatible" | "unsupported_version" | "unsupported_capability" | "invalid_request";
  negotiation: {
    version?: "1.0";
    requested_versions: string[];
    required_capabilities: string[];
    optional_capabilities: string[];
    available_capabilities: string[];
    selected_capabilities: string[];
    unavailable_optional_capabilities: string[];
  };
  diagnostics: { code: string; message: string }[];
}

const validate = new Ajv2020({ strict: false }).compile<RunnerNegotiation>(responseSchema);

export interface WorkSelection {
  shard: string;
  task: string;
  trial: number;
  attempt: number;
}

export interface RunnerSelectionReport {
  kind: "waza.runner-unit";
  schema_version: "1.0";
  status: "ready" | "rejected";
  compatibility: RunnerNegotiation;
  requested?: WorkSelection;
  resolved?: WorkSelection;
  identity?: RunnerIdentity;
  validation: "not_checked" | "invalid" | "valid";
  adjacent_work_selected: false;
  engine_calls: 0;
  diagnostics: { code: string; message: string }[];
}

export interface RunnerIdentity {
  version: "sha256-waza-c14n-v1";
  canonical_digest: string;
  run: string;
  shard: string;
  task: string;
  trial: string;
  attempt: string;
}

const validateUnit = new Ajv2020({ strict: false })
  .addSchema(responseSchema)
  .compile<RunnerSelectionReport>(unitSchema);

export async function validateRunnerSelection(request: string): Promise<RunnerSelectionReport> {
  const response = await fetch("/api/v1/runner/validate", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: request,
    signal: AbortSignal.timeout(10_000),
  });
  if (!response.ok) throw new Error(`Runner API returned HTTP ${response.status}.`);
  const value = parseStrictJSON(await response.text());
  if (!validateUnit(value) || (value.status !== "ready" && value.status !== "rejected")) {
    throw new Error("Runner API returned an invalid selection report.");
  }
  if (value.status === "ready") {
    const a = value.requested;
    const b = value.resolved;
    if (!a || !b || a.shard !== b.shard || a.task !== b.task || a.trial !== b.trial || a.attempt !== b.attempt) {
      throw new Error("Runner API returned conflicting requested and resolved work.");
    }
  }
  return value;
}

export async function negotiateRunner(request: string): Promise<RunnerNegotiation> {
  const response = await fetch("/api/v1/runner/negotiate", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: request,
    signal: AbortSignal.timeout(10_000),
  });
  if (!response.ok) throw new Error(`Runner API returned HTTP ${response.status}.`);
  const value = parseStrictJSON(await response.text());
  if (!validate(value)) throw new Error("Runner API returned an invalid negotiation response.");
  return value;
}
