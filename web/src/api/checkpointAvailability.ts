import Ajv2020 from "ajv/dist/2020";
import availabilitySchema from "../../../schemas/runner-checkpoint-availability-1.0.schema.json";
import attemptSchema from "../../../schemas/execution-attempt-1.0.schema.json";
import manifestSchema from "../../../schemas/artifact-manifest-1.0.schema.json";
import terminalSchema from "../../../schemas/runner-terminal-1.0.schema.json";
import { parseStrictJSON } from "../lib/strictJson";

export interface CheckpointAvailability {
  kind: "waza.runner-checkpoint-availability";
  schema_version: "1.0";
  checkpoint_sha256?: string;
  status: "available" | "unavailable" | "rejected" | "operation_error";
  exit_code: 0 | 2 | 6;
  source?: { boundary: "before-unit" | "finalized-unit"; created_at: string; execution_id: string };
  compatibility_result: "compatible" | "incompatible" | "unavailable";
  compatibility_differences: string[];
  resume_action?: "fresh_retry_child" | "reuse_original_terminal";
  restored_units: 0 | 1 | null;
  remaining_work: 0 | 1 | null;
  diagnostics: { code: string; message: string }[];
}

const ajv = new Ajv2020({ strict: false });
for (const schema of [terminalSchema, manifestSchema, attemptSchema]) ajv.addSchema(schema);
const validate = ajv.compile<CheckpointAvailability>(availabilitySchema);

const expectedAction = {
  "before-unit": "fresh_retry_child", "finalized-unit": "reuse_original_terminal",
} as const;

export function parseCheckpointAvailability(text: string, digest?: string): CheckpointAvailability {
  const value = parseStrictJSON(text);
  if (!validate(value)) throw new Error("Checkpoint availability response does not match its schema.");
  if (digest && value.checkpoint_sha256 !== undefined && value.checkpoint_sha256 !== digest) {
    throw new Error("Checkpoint availability contradicts the requested checkpoint digest.");
  }
  if (value.status === "available" && value.source && value.resume_action !== expectedAction[value.source.boundary]) {
    throw new Error("Checkpoint availability resume action contradicts its boundary.");
  }
  return value;
}

export async function checkCheckpointAvailability(
  request: string, digest: string, signal: AbortSignal,
): Promise<{ result: CheckpointAvailability; json: string }> {
  if (!/^[a-f0-9]{64}$/.test(digest)) {
    throw new Error("Provide a full lowercase SHA-256 checkpoint digest before checking availability.");
  }
  parseStrictJSON(request);
  const response = await fetch(`/api/v1/checkpoints/${digest}/availability`, {
    method: "POST", headers: { "Content-Type": "application/json" }, body: request,
    signal: AbortSignal.any([signal, AbortSignal.timeout(60_000)]),
  });
  const json = await response.text();
  const result = parseCheckpointAvailability(json, digest);
  if (response.headers.get("X-Waza-Exit-Code") !== String(result.exit_code) ||
      response.ok !== (result.status === "available")) {
    throw new Error("Checkpoint availability transport status contradicts its result.");
  }
  return { result, json };
}
