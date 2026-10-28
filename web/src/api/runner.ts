import Ajv2020 from "ajv/dist/2020";
import responseSchema from "../../../schemas/runner-negotiation-response-1.0.schema.json";
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
