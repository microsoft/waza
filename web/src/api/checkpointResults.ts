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
import { isCanonicalCheckpointTime, type CheckpointDeclaration } from "./checkpoints";
import type { RunnerIdentity, RunnerTerminal, WorkSelection } from "./runner";
import { parseStrictJSON } from "../lib/strictJson";

interface SourceReference {
  execution_id: string;
  manifest_digest: string;
}

export interface SubmissionResult {
  kind: "waza.execution-submission-result";
  schema_version: "1.0";
  status: "rejected" | "submission_error" | "completed" | "reused";
  exit_code: number;
  engine_calls: number;
  caller?: RunnerIdentity;
  reservation?: {
    execution_id: string;
    logical: { run: string; work: string };
    parent?: { execution_id: string; relationship: "retry" | "branch" };
    binding: { identity: RunnerIdentity; selection: WorkSelection; execution_mode: "mock" | "copilot"; grader_ids: string[] };
  };
  completion?: { execution_id: string; manifest_digest: string; reservation_digest: string };
  decision?: {
    policy: "distinct" | "reuse"; outcome: "distinct" | "reused";
    execution_id?: string; source?: SourceReference;
    logical: { run: string; work: string };
    binding: { identity: RunnerIdentity; selection: WorkSelection; execution_mode: "mock" | "copilot"; grader_ids: string[] };
  };
  terminal?: RunnerTerminal;
  diagnostics: { code: string; message: string }[];
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
const validateSubmission = ajv.compile<SubmissionResult>(submissionSchema);

function sameIdentity(a: RunnerIdentity | undefined, b: RunnerIdentity | undefined): boolean {
  return !!a && !!b && ([
    "version", "canonical_digest", "run", "shard", "task", "trial", "attempt",
  ] as const).every((key) => a[key] === b[key]);
}

function sameSelection(a: WorkSelection | undefined, b: WorkSelection | undefined): boolean {
  return !!a && !!b && (["shard", "task", "trial", "attempt"] as const)
    .every((key) => a[key] === b[key]);
}

function validateSubmissionContext(value: SubmissionResult): void {
  if ("report" in value) throw new Error("Submission API returned an unapproved raw report.");
  const reservation = value.reservation;
  const decision = value.decision;
  const terminal = value.terminal;
  if (terminal && (value.status === "rejected" || !value.caller || !reservation || !decision)) {
    throw new Error("Submission terminal has no admitted original ownership context.");
  }
  if (reservation && (!value.caller || value.caller.run !== reservation.logical.run ||
      value.caller.trial !== reservation.logical.work)) {
    throw new Error("Submission caller does not retain original logical work.");
  }
  if (decision && (!reservation || !sameIdentity(decision.binding.identity, value.caller) ||
      decision.logical.run !== reservation.logical.run ||
      decision.logical.work !== reservation.logical.work)) {
    throw new Error("Submission decision contradicts its caller or original work.");
  }
  if (decision?.policy === "distinct" && (
      !reservation || decision.execution_id !== reservation.execution_id ||
      !sameIdentity(decision.binding.identity, reservation.binding.identity) ||
      !sameSelection(decision.binding.selection, reservation.binding.selection) ||
      decision.binding.execution_mode !== reservation.binding.execution_mode ||
      decision.binding.grader_ids.length !== reservation.binding.grader_ids.length ||
      decision.binding.grader_ids.some((id, index) => id !== reservation.binding.grader_ids[index]))) {
    throw new Error("Distinct submission changed its original reservation.");
  }
  if (value.completion && (!reservation || value.completion.execution_id !== reservation.execution_id ||
      (decision?.policy === "reuse" &&
        (!decision.source || decision.source.execution_id !== reservation.execution_id ||
          decision.source.manifest_digest !== value.completion.manifest_digest)))) {
    throw new Error("Submission completion or reuse source contradicts its original.");
  }
  if (terminal && ((reservation && (!sameIdentity(terminal.identity, reservation.binding.identity) ||
      !sameSelection(terminal.selection, reservation.binding.selection))) ||
      (value.status !== "submission_error" && value.exit_code !== terminal.exit_code) ||
      (value.status !== "reused" && value.engine_calls !== terminal.engine_calls))) {
    throw new Error("Submission changed its original native terminal or call count.");
  }
  if (terminal?.observation && (!sameSelection(terminal.selection, terminal.observation.selection) ||
      terminal.observation.attempts !== terminal.selection?.attempt ||
      terminal.observation.engine_calls !== terminal.engine_calls ||
      (reservation && terminal.observation.execution_mode !== reservation.binding.execution_mode))) {
    throw new Error("Submission observation changed the original native ordinal.");
  }
}

export function parseProtectedSubmissionResult(text: string): SubmissionResult {
  const value = parseStrictJSON(text);
  if (!validateSubmission(value)) throw new Error("Submission API returned an invalid result.");
  validateSubmissionContext(value);
  return value;
}

// This consumes protected operation responses, never authorizes source
// disclosure or infers recovery from a declaration-only inspection receipt.
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
  if (submission) validateSubmissionContext(submission);
  if (submission && "report" in submission) {
    throw new Error("Checkpoint API returned an unapproved raw report.");
  }
  if (submission &&
      (value.status === "checkpoint_error"
        ? submission.status !== "completed"
        : value.status !== submission.status || value.exit_code !== submission.exit_code)) {
    throw new Error("Checkpoint operation contradicts its actual submission.");
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
  return { result, json };
}
