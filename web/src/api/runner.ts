import Ajv2020 from "ajv/dist/2020.js";
import schema from "../../../schemas/runner-result-1.0.schema.json" with { type: "json" };
import { parseStrictJSON } from "../lib/strictJson";

export interface RunnerSelection {
  shard: string;
  task: string;
  trial: number;
  attempt: number;
}

export interface RunnerIdentity {
  version: "sha256-json-v1";
  canonical_digest: string;
  run: string;
  shard: string;
  task: string;
  trial: string;
  attempt: string;
}

export type RunnerOutcome =
  | "success" | "evaluation_failure" | "infrastructure_failure"
  | "canceled" | "timeout" | "unsupported_capability" | "invalid_request";

export interface RunnerResult {
  kind: "waza.runner-result";
  schema_version: "1.0";
  outcome: RunnerOutcome;
  exit_code: number;
  failure_boundary: "none" | "admission" | "negotiation" | "selection"
    | "execution" | "grading" | "shutdown" | "terminalization";
  negotiation: {
    version?: "1.0";
    requested_versions: string[];
    required_capabilities: string[];
    optional_capabilities: string[];
    available_capabilities: string[];
    selected_capabilities: string[];
    unavailable_optional_capabilities: string[];
  };
  requested?: RunnerSelection;
  resolved?: RunnerSelection;
  selection_valid: boolean;
  adjacent_work_selected: false;
  identity?: RunnerIdentity;
  observation?: Record<string, unknown>;
  diagnostics: { code: string; message: string }[];
  envelope_valid: true;
}

export type RunnerEntry =
  | { state: "recorded"; result: RunnerResult }
  | { state: "malformed" | "unsupported"; reason: string };

export interface RunnerResults {
  schema_version: "1.0";
  results: RunnerEntry[];
}

// Use the backend's published schema without coercion, defaults, or field removal.
const ajv = new Ajv2020({ strict: false, allErrors: true });
const validate = ajv.compile<RunnerResult>(schema);
const object = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === "object" && !Array.isArray(value);

export function inspectRunnerResult(value: unknown): RunnerEntry {
  if (object(value) && typeof value.schema_version === "string" && value.schema_version !== "1.0") {
    return { state: "unsupported", reason: `Unsupported runner result version: ${value.schema_version}` };
  }
  if (!validate(value)) {
    return { state: "malformed", reason: ajv.errorsText(validate.errors, { separator: "; " }) };
  }
  const result = value;
  const { requested, resolved, observation } = result;
  // Match ValidateResult's cross-field invariants; observation status never selects an outcome.
  if (result.selection_valid && (!requested || !resolved ||
    (["shard", "task", "trial", "attempt"] as const).some((key) => requested[key] !== resolved[key]))) {
    return { state: "malformed", reason: "Resolved selection must match the exact requested tuple." };
  }
  if (result.outcome === "success" || result.outcome === "evaluation_failure") {
    if (!observation || observation.run_number !== resolved?.trial || observation.attempts !== resolved?.attempt) {
      return { state: "malformed", reason: "Graded observation does not bind the resolved trial and attempt." };
    }
    if (result.outcome === "success" && (observation.status !== "passed" ||
      (observation.error_msg !== undefined && observation.error_msg !== "") ||
      !object(observation.validations) || Object.keys(observation.validations).length === 0 ||
      !Object.values(observation.validations).every((check) => object(check) && check.passed === true))) {
      return { state: "malformed", reason: "Success requires complete passing checks without execution errors." };
    }
  }
  return { state: "recorded", result };
}

export function parseRunnerResults(value: unknown): RunnerResults {
  if (!object(value)) throw new Error("Malformed runner-results response: expected an object.");
  if (typeof value.schema_version === "string" && value.schema_version !== "1.0") {
    throw new Error(`Unsupported runner-results API version: ${value.schema_version}`);
  }
  if (value.schema_version !== "1.0" || !Array.isArray(value.results) ||
    Object.keys(value).some((key) => key !== "schema_version" && key !== "results")) {
    throw new Error("Malformed runner-results response: expected schema_version 1.0 and a results array.");
  }
  return { schema_version: "1.0", results: value.results.map(inspectRunnerResult) };
}

export async function fetchRunnerResults(signal?: AbortSignal): Promise<RunnerResults> {
  const response = await fetch("/api/v1/lab/runner-results", { signal });
  if (!response.ok) throw new Error(`Runner results API error: ${response.status} ${response.statusText}`);
  let value: unknown;
  try {
    value = parseStrictJSON(await response.text());
  } catch {
    throw new Error("Malformed runner-results response: invalid or ambiguous JSON.");
  }
  return parseRunnerResults(value);
}
