import Ajv2020 from "ajv/dist/2020";
import responseSchema from "../../../schemas/runner-negotiation-response-1.0.schema.json";
import unitSchema from "../../../schemas/runner-unit-1.0.schema.json";
import terminalSchema from "../../../schemas/runner-terminal-1.0.schema.json";
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

export interface RunnerTerminal {
  kind: "waza.runner-terminal";
  schema_version: "1.0";
  outcome: "success" | "evaluation_failure" | "infrastructure_failure" | "cancelled" | "timeout" | "unsupported_capability" | "invalid_input";
  exit_code: number;
  failure_boundary: string;
  validation: "not_checked" | "invalid" | "valid";
  engine_calls: number;
  selection?: WorkSelection;
  identity?: RunnerIdentity;
  observation?: {
    execution_mode: "mock";
    engine_calls: 1;
    selection: WorkSelection;
    run_number: number;
    attempts: number;
    status: "passed" | "failed" | "error";
    validations: { identifier: string; passed: boolean }[];
    error_msg?: string;
  };
  diagnostics: { code: string; message: string }[];
}

const validateTerminal = new Ajv2020({ strict: false }).compile<RunnerTerminal>(terminalSchema);

function sameSelection(a: WorkSelection | undefined, b: WorkSelection | undefined): boolean {
  return !!a && !!b && a.shard === b.shard && a.task === b.task && a.trial === b.trial && a.attempt === b.attempt;
}

// Server admission is authoritative for grader bindings. The browser checks the
// exact original JSON, schema, selection and identity against offline admission;
// it neither derives identities nor treats them as proof of live execution.
export async function runRunnerUnit(request: string, signal: AbortSignal): Promise<RunnerTerminal> {
  const admitted = await validateRunnerSelection(request, signal);
  const response = await fetch("/api/v1/runner/run", {
    method: "POST", headers: { "Content-Type": "application/json" }, body: request, signal,
  });
  const value = parseStrictJSON(await response.text());
  if (!validateTerminal(value)) throw new Error("Runner API returned an invalid terminal envelope.");
  if (response.headers.get("X-Waza-Exit-Code") !== String(value.exit_code)) {
    throw new Error("Runner API terminal exit header contradicts its body.");
  }
  if (value.identity) {
    if (admitted.status !== "ready" || !sameSelection(value.selection, admitted.resolved) || !admitted.identity ||
        (Object.keys(admitted.identity) as (keyof RunnerIdentity)[]).some((key) => admitted.identity?.[key] !== value.identity?.[key])) {
      throw new Error("Terminal identity or selection does not match admitted input.");
    }
  } else if (value.selection || value.observation || value.engine_calls !== 0) {
    throw new Error("Unadmitted terminal returned execution evidence.");
  }
  if (value.observation) {
    const observed = value.observation;
    const ids = observed.validations.map((verdict) => verdict.identifier);
    if (!sameSelection(observed.selection, value.selection) ||
        observed.run_number !== value.selection?.trial || observed.attempts !== value.selection?.attempt ||
        observed.engine_calls !== value.engine_calls || new Set(ids).size !== ids.length) {
      throw new Error("Terminal observation contradicts its admitted binding.");
    }
  }
  return value;
}

const validateUnit = new Ajv2020({ strict: false })
  .addSchema(responseSchema)
  .compile<RunnerSelectionReport>(unitSchema);

export async function validateRunnerSelection(request: string, signal?: AbortSignal): Promise<RunnerSelectionReport> {
  const response = await fetch("/api/v1/runner/validate", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: request,
    signal: signal ? AbortSignal.any([signal, AbortSignal.timeout(10_000)]) : AbortSignal.timeout(10_000),
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
