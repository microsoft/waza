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
  submission?: { status: string; exit_code: number };
  source?: { execution_id: string; boundary: string };
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

export function parseCheckpointOperationResult(text: string): CheckpointOperationResult {
  const value = parseStrictJSON(text);
  if (validate10(value) || validate11(value)) return value;
  throw new Error("Checkpoint result must match the supported 1.0 or 1.1 schema.");
}
