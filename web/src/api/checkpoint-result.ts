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
    terminal?: { exit_code: number };
    reservation?: ExecutionAttempt;
    completion?: ExecutionCompletion;
  };
  source?: ExecutionCheckpoint;
  before_checkpoint_sha256?: string;
  finalized_checkpoint_sha256?: string;
}

interface ExecutionAttempt {
  kind: "waza.execution-attempt";
  schema_version: "1.0";
  logical: { run: string; work: string };
  execution_id: string;
  binding: {
    identity: {
      version: string;
      canonical_digest: string;
      run: string;
      shard: string;
      task: string;
      trial: string;
      attempt: string;
    };
    selection: { shard: string; task: string; trial: number; attempt: number };
    execution_mode: string;
    grader_ids: string[];
  };
  parent?: { execution_id: string; relationship: "retry" | "branch" };
}

interface ExecutionCompletion {
  kind: "waza.execution-completion";
  schema_version: "1.0";
  execution_id: string;
  reservation_digest: string;
  manifest_digest: string;
}

interface ExecutionCheckpoint {
  execution_id: string;
  boundary: "before-unit" | "finalized-unit";
  reservation_digest: string;
  compatibility: { work: string };
  completion_digest?: string;
  manifest_digest?: string;
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
const utf8Encoder = new TextEncoder();

async function jsonDigest(value: object) {
  const wire = JSON.stringify(value)
    .replace(/</g, "\\u003c")
    .replace(/>/g, "\\u003e")
    .replace(/&/g, "\\u0026")
    .replace(/\u2028/g, "\\u2028")
    .replace(/\u2029/g, "\\u2029");
  const bytes = await globalThis.crypto.subtle.digest("SHA-256", new TextEncoder().encode(wire));
  return Array.from(new Uint8Array(bytes), (byte) => byte.toString(16).padStart(2, "0")).join("");
}

function compareUTF8(left: string, right: string) {
  const leftBytes = utf8Encoder.encode(left);
  const rightBytes = utf8Encoder.encode(right);
  for (const [index, leftByte] of leftBytes.entries()) {
    const rightByte = rightBytes[index];
    if (rightByte === undefined) return leftBytes.length - rightBytes.length;
    if (leftByte !== rightByte) return leftByte - rightByte;
  }
  return leftBytes.length - rightBytes.length;
}

function canonicalAttempt(attempt: ExecutionAttempt) {
  return {
    kind: attempt.kind,
    schema_version: attempt.schema_version,
    logical: { run: attempt.logical.run, work: attempt.logical.work },
    execution_id: attempt.execution_id,
    binding: {
      identity: {
        version: attempt.binding.identity.version,
        canonical_digest: attempt.binding.identity.canonical_digest,
        run: attempt.binding.identity.run,
        shard: attempt.binding.identity.shard,
        task: attempt.binding.identity.task,
        trial: attempt.binding.identity.trial,
        attempt: attempt.binding.identity.attempt,
      },
      selection: {
        shard: attempt.binding.selection.shard,
        task: attempt.binding.selection.task,
        trial: attempt.binding.selection.trial,
        attempt: attempt.binding.selection.attempt,
      },
      execution_mode: attempt.binding.execution_mode,
      grader_ids: [...attempt.binding.grader_ids].sort(compareUTF8),
    },
    ...(attempt.parent ? {
      parent: {
        execution_id: attempt.parent.execution_id,
        relationship: attempt.parent.relationship,
      },
    } : {}),
  };
}

async function completionDigest(completion: ExecutionCompletion) {
  return jsonDigest({
    kind: completion.kind,
    schema_version: completion.schema_version,
    execution_id: completion.execution_id,
    reservation_digest: completion.reservation_digest,
    manifest_digest: completion.manifest_digest,
  });
}

function validateBeforeSource(source: ExecutionCheckpoint, reservation: ExecutionAttempt) {
  const parent = reservation.parent;
  if (source.boundary !== "before-unit" || !parent ||
    parent.execution_id !== source.execution_id ||
    parent.relationship !== "retry" ||
    reservation.execution_id === source.execution_id) {
    throw new Error("Checkpoint source declaration does not match the retained submission.");
  }
}

async function validateFinalizedSource(source: ExecutionCheckpoint, reservation: ExecutionAttempt, completion?: ExecutionCompletion) {
  if (!completion ||
    reservation.execution_id !== source.execution_id ||
    completion.execution_id !== source.execution_id ||
    await jsonDigest(canonicalAttempt(reservation)) !== source.reservation_digest ||
    completion.reservation_digest !== source.reservation_digest ||
    completion.manifest_digest !== source.manifest_digest ||
    await completionDigest(completion) !== source.completion_digest) {
    throw new Error("Checkpoint source declaration does not match the retained submission.");
  }
}

async function validateCheckpointResult(result: CheckpointOperationResult) {
  if (result.before_checkpoint_sha256 &&
    result.before_checkpoint_sha256 === result.finalized_checkpoint_sha256) {
    throw new Error("Checkpoint boundary references must identify different blobs.");
  }

  const { source, submission } = result;
  if (submission?.terminal &&
    ["completed", "reused", "rejected"].includes(submission.status) &&
    submission.exit_code !== submission.terminal.exit_code) {
    throw new Error("Retained submission exit code contradicts its terminal.");
  }
  if (result.status === "operation_error") {
    if (!source) return;
    const reservation = submission?.reservation;
    if (!reservation || source.compatibility.work !== reservation.logical.work) {
      throw new Error("Operation-error source declaration does not match the retained submission.");
    }
    if (source.boundary === "before-unit") {
      validateBeforeSource(source, reservation);
      if (source.completion_digest !== undefined || source.manifest_digest !== undefined) {
        throw new Error("Operation-error source declaration does not match the retained submission.");
      }
      return;
    }
    await validateFinalizedSource(source, reservation, submission?.completion);
    return;
  }

  if (!submission) {
    if (result.status !== "rejected") {
      throw new Error("Checkpoint status requires a retained submission.");
    }
    return;
  }
  const reservation = submission.reservation;
  if (source && reservation && source.compatibility.work !== reservation.logical.work) {
    throw new Error("Checkpoint source declaration does not match the retained submission.");
  }
  if (result.status === "checkpoint_error") {
    if (submission.status !== "completed") {
      throw new Error("Checkpoint publication failure must retain a completed submission.");
    }
  } else if (result.status !== submission.status || result.exit_code !== submission.exit_code) {
    throw new Error("Checkpoint result status or exit code contradicts the retained submission.");
  }
  if (source && reservation && source.boundary === "before-unit") {
    validateBeforeSource(source, reservation);
  }
  if (result.status === "completed" && source?.boundary === "finalized-unit") {
    throw new Error("A finalized checkpoint source must be reused, not rerun.");
  }
  if (result.status === "reused") {
    if (!source || source.boundary !== "finalized-unit" || !reservation || !submission.completion) {
      throw new Error("Reused result requires its exact finalized source and retained sidecars.");
    }
    await validateFinalizedSource(source, reservation, submission.completion);
  }
}

export async function parseCheckpointOperationResult(text: string): Promise<CheckpointOperationResult> {
  const value = parseStrictJSON(text);
  if (validate10(value)) {
    await validateCheckpointResult(value);
    return value;
  }
  if (validate11(value)) {
    await validateCheckpointResult(value);
    return value;
  }
  throw new Error("Checkpoint result must match the supported 1.0 or 1.1 schema.");
}
