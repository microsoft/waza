import Ajv2020 from "ajv/dist/2020";
import checkpointSchema from "../../../schemas/execution-checkpoint-1.0.schema.json";
import attemptSchema from "../../../schemas/execution-attempt-1.0.schema.json";
import manifestSchema from "../../../schemas/artifact-manifest-1.0.schema.json";
import terminalSchema from "../../../schemas/runner-terminal-1.0.schema.json";
import { parseStrictJSON } from "../lib/strictJson";

export interface CheckpointDeclaration {
  kind: "waza.execution-checkpoint";
  schema_version: "1.0";
  boundary: "before-unit" | "finalized-unit";
  created_at: string;
  execution_id: string;
  reservation_digest: string;
  compatibility: {
    runner_revision: string;
    request_version: "1.0";
    identity_version: "sha256-waza-c14n-v1";
    terminal_version: "1.0";
    manifest_version: "1.0";
    attempt_version: "1.0";
    completion_version: "1.0";
    work: string;
    request_policy_digest: string;
  };
  completion_digest?: string;
  manifest_digest?: string;
}

export interface CheckpointInspection {
  checkpoint_sha256: string;
  availability: "blob-verified" | "unavailable";
  verification: "canonical-declaration-only";
  checkpoint?: CheckpointDeclaration;
  compatibility_result: "unavailable";
  restored_units: null;
  remaining_work: null;
  diagnostics: { code: string; message: string }[];
}

export class CheckpointInspectionError extends Error {}

const ajv = new Ajv2020({ strict: false });
for (const schema of [terminalSchema, manifestSchema, attemptSchema, checkpointSchema]) {
  ajv.addSchema(schema);
}
const validateInspection = ajv.compile<CheckpointInspection>({
  type: "object",
  additionalProperties: false,
  required: ["checkpoint_sha256", "availability", "verification", "compatibility_result", "restored_units", "remaining_work", "diagnostics"],
  properties: {
    checkpoint_sha256: { type: "string", pattern: "^[0-9a-f]{64}$" },
    availability: { enum: ["blob-verified", "unavailable"] },
    verification: { const: "canonical-declaration-only" },
    checkpoint: { $ref: checkpointSchema.$id },
    compatibility_result: { const: "unavailable" },
    restored_units: { type: "null" },
    remaining_work: { type: "null" },
    diagnostics: { type: "array", minItems: 1, items: {
      type: "object", additionalProperties: false, required: ["code", "message"],
      properties: { code: { type: "string", minLength: 1 }, message: { type: "string", minLength: 1 } },
    } },
  },
  if: { properties: { availability: { const: "blob-verified" } } },
  then: { required: ["checkpoint"] },
  else: { not: { required: ["checkpoint"] } },
});

function canonicalTime(value: string): boolean {
  const match = value.match(/^([0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2})(?:\.[0-9]{0,8}[1-9])?Z$/);
  const seconds = match?.[1];
  if (!seconds || seconds.startsWith("0000")) return false;
  const time = Date.parse(`${seconds}Z`);
  return Number.isFinite(time) && new Date(time).toISOString() === `${seconds}.000Z`;
}

export async function inspectCheckpoint(digest: string, signal: AbortSignal): Promise<CheckpointInspection> {
  if (!/^[0-9a-f]{64}$/.test(digest)) {
    throw new CheckpointInspectionError("Enter a full lowercase SHA-256 checkpoint blob digest (64 hexadecimal characters).");
  }
  let response: Response;
  let text: string;
  try {
    response = await fetch(`/api/v1/checkpoints/${encodeURIComponent(digest)}`, {
      method: "GET",
      signal: AbortSignal.any([signal, AbortSignal.timeout(10_000)]),
    });
    text = await response.text();
  } catch {
    throw new CheckpointInspectionError("Checkpoint inspection could not be loaded. Check the server connection and retry.");
  }
  if (![200, 400, 404, 409, 503].includes(response.status)) {
    throw new CheckpointInspectionError(`Checkpoint API returned HTTP ${response.status}.`);
  }
  let value: unknown;
  try {
    value = parseStrictJSON(text);
  } catch {
    throw new CheckpointInspectionError("Checkpoint API returned malformed data.");
  }
  if (!validateInspection(value) || value.checkpoint_sha256 !== digest ||
      (response.status === 200) !== (value.availability === "blob-verified") ||
      (value.checkpoint !== undefined && !canonicalTime(value.checkpoint.created_at))) {
    throw new CheckpointInspectionError("Checkpoint API returned invalid or inconsistent source metadata.");
  }
  return value;
}
