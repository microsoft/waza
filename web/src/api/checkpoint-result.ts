import Ajv2020 from "ajv/dist/2020";
import artifactSchema from "../../../schemas/artifact-manifest-1.0.schema.json";
import attemptSchema from "../../../schemas/execution-attempt-1.0.schema.json";
import checkpointSchema from "../../../schemas/execution-checkpoint-1.0.schema.json";
import completionSchema from "../../../schemas/execution-completion-1.0.schema.json";
import result10Schema from "../../../schemas/runner-checkpoint-result-1.0.schema.json";
import result11Schema from "../../../schemas/runner-checkpoint-result-1.1.schema.json";
import negotiationSchema from "../../../schemas/runner-negotiation-response-1.0.schema.json";
import runnerTerminalSchema from "../../../schemas/runner-terminal-1.0.schema.json";
import runnerUnitSchema from "../../../schemas/runner-unit-1.0.schema.json";
import submissionResultSchema from "../../../schemas/execution-submission-result-1.0.schema.json";
import { parseStrictJSON } from "../lib/strictJson";

export interface CheckpointOperationResult {
  kind: "waza.runner-checkpoint-result";
  schema_version: "1.0" | "1.1";
  status: "rejected" | "submission_error" | "checkpoint_error" | "completed" | "reused" | "operation_error";
  exit_code: number;
  error_code?: string;
  submission?: {
    status: string;
    exit_code: number;
    reservation?: {
      execution_id: string;
      logical: { work: string };
      parent?: { execution_id: string; relationship: string };
    };
    completion?: {
      kind: "waza.execution-completion";
      schema_version: "1.0";
      execution_id: string;
      reservation_digest: string;
      manifest_digest: string;
    };
  };
  source?: {
    execution_id: string;
    boundary: "before-unit" | "finalized-unit";
    reservation_digest: string;
    compatibility: { work: string };
    completion_digest?: string;
    manifest_digest?: string;
  };
  before_checkpoint_sha256?: string;
  finalized_checkpoint_sha256?: string;
}

function validator(schema: object) {
  return new Ajv2020({ strict: false })
    .addSchema(artifactSchema)
    .addSchema(attemptSchema)
    .addSchema(checkpointSchema)
    .addSchema(completionSchema)
    .addSchema(negotiationSchema)
    .addSchema(runnerTerminalSchema)
    .addSchema(runnerUnitSchema)
    .addSchema(submissionResultSchema)
    .compile<CheckpointOperationResult>(schema);
}

const validate10 = validator(result10Schema);
const validate11 = validator(result11Schema);

async function completionDigest(completion: NonNullable<CheckpointOperationResult["submission"]>["completion"]) {
  if (!completion) return "";
  const wire = JSON.stringify({
    kind: completion.kind,
    schema_version: completion.schema_version,
    execution_id: completion.execution_id,
    reservation_digest: completion.reservation_digest,
    manifest_digest: completion.manifest_digest,
  });
  const bytes = await globalThis.crypto.subtle.digest("SHA-256", new TextEncoder().encode(wire));
  return Array.from(new Uint8Array(bytes), (byte) => byte.toString(16).padStart(2, "0")).join("");
}

async function validateOperationErrorSource(result: CheckpointOperationResult) {
  if (result.status !== "operation_error" || !result.source) return;
  const { source, submission } = result;
  const reservation = submission?.reservation;
  const completion = submission?.completion;
  if (!reservation || !completion || source.compatibility.work !== reservation.logical.work) {
    throw new Error("Operation-error source declaration does not match the retained submission.");
  }
  if (source.boundary === "before-unit") {
    if (reservation.parent?.execution_id !== source.execution_id ||
      reservation.parent?.relationship !== "retry" ||
      reservation.execution_id === source.execution_id ||
      source.completion_digest !== undefined || source.manifest_digest !== undefined) {
      throw new Error("Operation-error source declaration does not match the retained submission.");
    }
    return;
  }
  if (reservation.execution_id !== source.execution_id ||
    completion.execution_id !== source.execution_id ||
    completion.reservation_digest !== source.reservation_digest ||
    completion.manifest_digest !== source.manifest_digest ||
    await completionDigest(completion) !== source.completion_digest) {
    throw new Error("Operation-error source declaration does not match the retained submission.");
  }
}

export async function parseCheckpointOperationResult(text: string): Promise<CheckpointOperationResult> {
  const value = parseStrictJSON(text);
  if (validate10(value)) return value;
  if (validate11(value)) {
    await validateOperationErrorSource(value);
    return value;
  }
  throw new Error("Checkpoint result must match the supported 1.0 or 1.1 schema.");
}
