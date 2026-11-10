import Ajv2020 from "ajv/dist/2020";
import resultSchema from "../../../schemas/runner-checkpoint-result-1.0.schema.json";
import submissionSchema from "../../../schemas/execution-submission-result-1.0.schema.json";
import checkpointSchema from "../../../schemas/execution-checkpoint-1.0.schema.json";
import attemptSchema from "../../../schemas/execution-attempt-1.0.schema.json";
import completionSchema from "../../../schemas/execution-completion-1.0.schema.json";
import manifestSchema from "../../../schemas/artifact-manifest-1.0.schema.json";
import terminalSchema from "../../../schemas/runner-terminal-1.0.schema.json";
import unitSchema from "../../../schemas/runner-unit-1.0.schema.json";
import negotiationSchema from "../../../schemas/runner-negotiation-response-1.0.schema.json";
import {
  sameRunnerIdentity, sameSelection, validateRunnerSelection,
  type RunnerIdentity, type RunnerTerminal, type WorkSelection,
} from "./runner";
import { parseStrictJSON } from "../lib/strictJson";

interface SourceReference {
  execution_id: string;
  manifest_digest: string;
}

interface SubmissionResult {
  kind: "waza.execution-submission-result";
  schema_version: "1.0";
  status: "rejected" | "submission_error" | "completed" | "reused";
  exit_code: number;
  engine_calls: number;
  caller?: RunnerIdentity;
  reservation?: {
    execution_id: string;
    logical: { work: string };
    parent?: { execution_id: string; relationship: "retry" | "branch" };
    binding: {
      identity: RunnerIdentity;
      selection: WorkSelection;
      execution_mode: "mock" | "copilot";
      grader_ids: string[];
    };
  };
  completion?: { execution_id: string; manifest_digest: string; reservation_digest: string };
  decision?: { policy: "distinct" | "reuse"; source?: SourceReference };
  terminal?: RunnerTerminal;
  diagnostics: { code: string; message: string }[];
}

interface CheckpointDeclaration {
  kind: "waza.execution-checkpoint";
  schema_version: "1.0";
  boundary: "before-unit" | "finalized-unit";
  created_at: string;
  execution_id: string;
  reservation_digest: string;
  compatibility: { runner_revision: string; work: string };
  completion_digest?: string;
  manifest_digest?: string;
}

export interface CheckpointOperationResult {
  kind: "waza.runner-checkpoint-result";
  schema_version: "1.0";
  status: "rejected" | "submission_error" | "checkpoint_error" | "completed" | "reused";
  exit_code: number;
  submission?: SubmissionResult;
  source?: CheckpointDeclaration;
  source_withheld?: true;
  before_checkpoint_sha256?: string;
  finalized_checkpoint_sha256?: string;
  compatibility_result: "unavailable" | "incompatible" | "compatible";
  compatibility_differences: string[];
  restored_units: 0 | 1 | null;
  remaining_work: 0 | 1 | null;
  diagnostics: { code: string; message: string }[];
}

const ajv = new Ajv2020({ strict: false });
for (const schema of [
  negotiationSchema, unitSchema, terminalSchema, manifestSchema,
  attemptSchema, completionSchema, submissionSchema, checkpointSchema,
]) ajv.addSchema(schema);
const validate = ajv.compile<CheckpointOperationResult>(resultSchema);

function isCanonicalCheckpointTime(value: string): boolean {
  const match = value.match(/^([0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2})(?:\.[0-9]{0,8}[1-9])?Z$/);
  const seconds = match?.[1];
  if (!seconds || seconds.startsWith("0000")) return false;
  const time = Date.parse(`${seconds}Z`);
  return Number.isFinite(time) && new Date(time).toISOString() === `${seconds}.000Z`;
}

// These checks consume protected responses; they do not authorize disclosure
// or turn declaration-only hashes into runtime or recovery evidence.
export function parseCheckpointOperationResult(text: string): CheckpointOperationResult {
  const value = parseStrictJSON(text);
  if (!validate(value)) throw new Error("Checkpoint API returned an invalid operation result.");
  if (value.source && !isCanonicalCheckpointTime(value.source.created_at)) {
    throw new Error("Checkpoint API returned a noncanonical source creation time.");
  }
  if (value.before_checkpoint_sha256 &&
      value.before_checkpoint_sha256 === value.finalized_checkpoint_sha256) {
    throw new Error("Checkpoint API returned one reference for different safe boundaries.");
  }
  const submission = value.submission;
  if (submission && "report" in submission) {
    throw new Error("Checkpoint API returned an unapproved raw report.");
  }
  if (submission &&
      (value.status === "checkpoint_error"
        ? submission.status !== "completed"
        : value.status !== submission.status || value.exit_code !== submission.exit_code)) {
    throw new Error("Checkpoint operation contradicts its actual submission.");
  }
  const binding = submission?.reservation?.binding;
  const terminal = submission?.terminal;
  if (terminal && submission &&
      ((submission.status !== "submission_error" && submission.exit_code !== terminal.exit_code) ||
       (submission.status !== "reused" && submission.engine_calls !== terminal.engine_calls))) {
    throw new Error("Checkpoint submission contradicts its original terminal outcome or call count.");
  }
  if (terminal && (!binding || !sameSelection(terminal.selection, binding.selection) ||
      !sameRunnerIdentity(terminal.identity, binding.identity))) {
    throw new Error("Checkpoint terminal contradicts its original reservation.");
  }
  if (terminal?.observation && binding) {
    const observed = terminal.observation;
    const ids = observed.validations.map((verdict) => verdict.identifier);
    if (!sameSelection(observed.selection, terminal.selection) ||
        observed.execution_mode !== binding.execution_mode ||
        observed.run_number !== terminal.selection?.trial || observed.attempts !== terminal.selection?.attempt ||
        observed.engine_calls !== terminal.engine_calls || new Set(ids).size !== ids.length ||
        (observed.status !== "error" && ids.length !== binding.grader_ids.length) ||
        ids.some((id) => !binding.grader_ids.includes(id))) {
      throw new Error("Checkpoint observation contradicts its original binding.");
    }
  }
  if (value.source && submission?.reservation &&
      value.source.compatibility.work !== submission.reservation.logical.work) {
    throw new Error("Checkpoint source contradicts the actual submission work.");
  }
  if (value.status === "completed" && value.source?.boundary === "finalized-unit") {
    throw new Error("Finalized checkpoint work must be reused, not executed again.");
  }
  if (value.status === "reused") {
    const source = value.source;
    if (!source || submission?.engine_calls !== 0 ||
        submission.reservation?.execution_id !== source.execution_id ||
        submission.completion?.execution_id !== source.execution_id ||
        submission.completion.manifest_digest !== source.manifest_digest ||
        submission.completion.reservation_digest !== source.reservation_digest ||
        submission.decision?.source?.execution_id !== source.execution_id ||
        submission.decision.source.manifest_digest !== source.manifest_digest) {
      throw new Error("Checkpoint reuse does not retain its exact original source.");
    }
  }
  if (value.source?.boundary === "before-unit" && submission?.reservation) {
    const parent = submission.reservation.parent;
    if (!parent || parent.relationship !== "retry" ||
        parent.execution_id !== value.source.execution_id ||
        submission.reservation.execution_id === value.source.execution_id) {
      throw new Error("Before-unit recovery does not retain a fresh original-source retry child.");
    }
  }
  return value;
}

export interface CheckpointOperationResponse {
  result: CheckpointOperationResult;
  json: string;
}

export async function runCheckpointOperation(
  action: "create" | "resume", request: string, digest: string, signal: AbortSignal,
): Promise<CheckpointOperationResponse> {
  if (action === "resume" && !/^[a-f0-9]{64}$/.test(digest)) {
    throw new Error("Provide a full lowercase SHA-256 checkpoint digest before resume.");
  }
  const wrapper = parseStrictJSON(request);
  if (typeof wrapper !== "object" || wrapper === null || !("request" in wrapper) ||
      typeof wrapper.request !== "object" || wrapper.request === null ||
      !("timeout_ms" in wrapper.request) || typeof wrapper.request.timeout_ms !== "number" ||
      !Number.isInteger(wrapper.request.timeout_ms) ||
      wrapper.request.timeout_ms < 1 || wrapper.request.timeout_ms > 86_400_000) {
    throw new Error("Provide one captured submission with an explicit native request timeout_ms.");
  }
  const admitted = await validateRunnerSelection(JSON.stringify(wrapper.request), signal);
  if (admitted.status !== "ready") {
    throw new Error("Captured runner selection was not admitted; no checkpoint operation was sent.");
  }
  const response = await fetch(action === "create"
    ? "/api/v1/checkpoints" : `/api/v1/checkpoints/${digest}/resume`, {
    method: "POST", headers: { "Content-Type": "application/json" }, body: request,
    signal: AbortSignal.any([signal, AbortSignal.timeout(wrapper.request.timeout_ms + 30_000)]),
  });
  const json = await response.text();
  const result = parseCheckpointOperationResult(json);
  if (response.headers.get("X-Waza-Exit-Code") !== String(result.exit_code) ||
      (!response.ok && (result.status === "completed" || result.status === "reused"))) {
    throw new Error("Checkpoint transport status contradicts its operation result.");
  }
  const caller = result.submission?.caller;
  if (caller && !sameRunnerIdentity(caller, admitted.identity)) {
    throw new Error("Checkpoint caller contradicts the admitted captured request.");
  }
  const binding = result.submission?.reservation?.binding;
  if (binding) {
    const original = binding.selection;
    const current = admitted.resolved;
    if (!current || original.shard !== current.shard || original.task !== current.task || original.trial !== current.trial ||
        (result.status !== "reused" && !sameSelection(original, current)) ||
        !admitted.identity || (["version", "run", "shard", "task", "trial"] as const)
          .some((key) => admitted.identity?.[key] !== binding.identity[key])) {
      throw new Error("Checkpoint original work contradicts the admitted captured request.");
    }
  }
  return { result, json };
}
