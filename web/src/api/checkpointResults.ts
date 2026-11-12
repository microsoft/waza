import Ajv2020 from "ajv/dist/2020";
import resultSchema from "../../../schemas/runner-checkpoint-result-1.0.schema.json";
import operationErrorSchema from "../../../schemas/runner-checkpoint-result-1.1.schema.json";
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
    kind: "waza.execution-attempt";
    schema_version: "1.0";
    execution_id: string;
    logical: { run: string; work: string };
    parent?: { execution_id: string; relationship: "retry" | "branch" };
    binding: {
      identity: RunnerIdentity;
      selection: WorkSelection;
      execution_mode: "mock" | "copilot";
      grader_ids: string[];
    };
  };
  completion?: {
    kind: "waza.execution-completion";
    schema_version: "1.0";
    execution_id: string;
    manifest_digest: string;
    reservation_digest: string;
  };
  decision?: {
    policy: "distinct" | "reuse";
    outcome: string;
    execution_id?: string;
    logical: { run: string; work: string };
    binding: NonNullable<SubmissionResult["reservation"]>["binding"];
    source?: SourceReference;
  };
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
  compatibility: {
    runner_revision: string;
    request_version: string;
    identity_version: string;
    terminal_version: string;
    manifest_version: string;
    attempt_version: string;
    completion_version: string;
    work: string;
    request_policy_digest: string;
  };
  completion_digest?: string;
  manifest_digest?: string;
}

export interface CheckpointOperationResult {
  kind: "waza.runner-checkpoint-result";
  schema_version: "1.0" | "1.1";
  status: "rejected" | "submission_error" | "checkpoint_error" | "completed" | "reused" | "operation_error";
  exit_code: number;
  error_code?: string;
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
const validateOperationError = ajv.compile<CheckpointOperationResult>(operationErrorSchema);

async function goJSONDigest(value: object): Promise<string> {
  const canonical = JSON.stringify(value).replace(
    /[<>&\u2028\u2029]/g, (character) => `\\u${character.charCodeAt(0).toString(16).padStart(4, "0")}`,
  );
  if (!globalThis.crypto?.subtle) throw new Error("Checkpoint declaration hashing requires browser Web Crypto.");
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(canonical));
  return Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, "0")).join("");
}

function sortedStrings(ids: string[]): string[] {
  const encoder = new TextEncoder();
  return ids.map((id) => ({ id, bytes: encoder.encode(id) })).sort((a, b) => {
    for (let index = 0; index < Math.min(a.bytes.length, b.bytes.length); index++) {
      const difference = a.bytes[index]! - b.bytes[index]!;
      if (difference) return difference;
    }
    return a.bytes.length - b.bytes.length;
  }).map(({ id }) => id);
}

function sameGraders(left: string[], right: string[]): boolean {
  return left.length === right.length && left.every((id, index) => id === right[index]);
}

async function requestPolicyDigest(request: object): Promise<string> {
  const strings = (value: unknown): string[] => {
    if (!Array.isArray(value) || !value.every((item: unknown) => typeof item === "string")) {
      throw new Error("Captured checkpoint request has an invalid negotiation policy.");
    }
    return sortedStrings(value);
  };
  if (!("versions" in request) || !("required_capabilities" in request) ||
      !("optional_capabilities" in request) || !("allow_paid_execution" in request) ||
      typeof request.allow_paid_execution !== "boolean") {
    throw new Error("Captured checkpoint request has an invalid negotiation policy.");
  }
  // Match executioncheckpoint.ForRequest's typed policy, without defaulting
  // absent/null arrays or consent into an apparently compatible declaration.
  return goJSONDigest({
    versions: strings(request.versions), required: strings(request.required_capabilities),
    optional: strings(request.optional_capabilities), paid: request.allow_paid_execution,
  });
}

async function reservationDigest(reservation: NonNullable<SubmissionResult["reservation"]>): Promise<string> {
  const { identity, selection, execution_mode, grader_ids } = reservation.binding;
  // Match executionattempt.EncodeAttempt: typed field order, UTF-8 grader
  // ordering and compact Go JSON escaping, independent of response key order.
  return goJSONDigest({
    kind: reservation.kind, schema_version: reservation.schema_version,
    logical: { run: reservation.logical.run, work: reservation.logical.work },
    execution_id: reservation.execution_id,
    binding: {
      identity: {
        version: identity.version, canonical_digest: identity.canonical_digest,
        run: identity.run, shard: identity.shard, task: identity.task,
        trial: identity.trial, attempt: identity.attempt,
      },
      selection: { shard: selection.shard, task: selection.task, trial: selection.trial, attempt: selection.attempt },
      execution_mode, grader_ids: sortedStrings(grader_ids),
    },
    ...(reservation.parent ? { parent: {
      execution_id: reservation.parent.execution_id, relationship: reservation.parent.relationship,
    } } : {}),
  });
}

async function checkpointDigest(source: CheckpointDeclaration): Promise<string> {
  const compatibility = source.compatibility;
  // Match executioncheckpoint.Encode's typed field order and optional fields.
  return goJSONDigest({
    kind: source.kind, schema_version: source.schema_version, boundary: source.boundary,
    created_at: source.created_at, execution_id: source.execution_id,
    reservation_digest: source.reservation_digest,
    compatibility: {
      runner_revision: compatibility.runner_revision, request_version: compatibility.request_version,
      identity_version: compatibility.identity_version, terminal_version: compatibility.terminal_version,
      manifest_version: compatibility.manifest_version, attempt_version: compatibility.attempt_version,
      completion_version: compatibility.completion_version, work: compatibility.work,
      request_policy_digest: compatibility.request_policy_digest,
    },
    ...(source.completion_digest ? { completion_digest: source.completion_digest } : {}),
    ...(source.manifest_digest ? { manifest_digest: source.manifest_digest } : {}),
  });
}

function isCanonicalCheckpointTime(value: string): boolean {
  const match = value.match(/^([0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2})(?:\.[0-9]{0,8}[1-9])?Z$/);
  const seconds = match?.[1];
  if (!seconds || seconds.startsWith("0000")) return false;
  const time = Date.parse(`${seconds}Z`);
  return Number.isFinite(time) && new Date(time).toISOString() === `${seconds}.000Z`;
}

async function validateOperationErrorSource(result: CheckpointOperationResult): Promise<void> {
  if (result.schema_version !== "1.1" || result.status !== "operation_error" || !result.source) return;
  const { source, submission } = result;
  const reservation = submission?.reservation;
  const completion = submission?.completion;
  if (!reservation || source.compatibility.work !== reservation.logical.work) {
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
  if (!completion || reservation.execution_id !== source.execution_id ||
      completion.execution_id !== source.execution_id ||
      await reservationDigest(reservation) !== source.reservation_digest ||
      completion.reservation_digest !== source.reservation_digest ||
      completion.manifest_digest !== source.manifest_digest ||
      await goJSONDigest({
        kind: completion.kind, schema_version: completion.schema_version,
        execution_id: completion.execution_id, reservation_digest: completion.reservation_digest,
        manifest_digest: completion.manifest_digest,
      }) !== source.completion_digest) {
    throw new Error("Operation-error source declaration does not match the retained submission.");
  }
}

// These checks consume protected responses; they do not authorize disclosure
// or turn declaration-only hashes into runtime or recovery evidence.
export async function parseCheckpointOperationResult(text: string): Promise<CheckpointOperationResult> {
  const value = parseStrictJSON(text);
  const isOperationError = validateOperationError(value);
  if (!isOperationError && !validate(value)) throw new Error("Checkpoint API returned an invalid operation result.");
  if (isOperationError) await validateOperationErrorSource(value);
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
  if (submission && value.status !== "operation_error" &&
      (value.status === "checkpoint_error"
        ? submission.status !== "completed"
        : value.status !== submission.status || value.exit_code !== submission.exit_code)) {
    throw new Error("Checkpoint status or exit code contradicts its actual submission.");
  }
  const binding = submission?.reservation?.binding;
  const terminal = submission?.terminal;
  const reservation = submission?.reservation;
  const decision = submission?.decision;
  if (reservation && binding &&
      (reservation.logical.run !== binding.identity.run || reservation.logical.work !== binding.identity.trial ||
       submission?.caller?.run !== reservation.logical.run || submission.caller.trial !== reservation.logical.work ||
       (!decision && !sameRunnerIdentity(submission.caller, binding.identity)))) {
    throw new Error("Checkpoint reservation contradicts its original logical work.");
  }
  if (decision && reservation && binding) {
    const caller = decision.binding;
    if (!sameRunnerIdentity(caller.identity, submission?.caller) ||
        decision.logical.run !== reservation.logical.run || decision.logical.work !== reservation.logical.work) {
      throw new Error("Checkpoint decision contradicts its caller or original logical work.");
    }
    if (decision.policy === "distinct"
      ? decision.execution_id !== reservation.execution_id ||
        !sameRunnerIdentity(caller.identity, binding.identity) || !sameSelection(caller.selection, binding.selection) ||
        caller.execution_mode !== binding.execution_mode || !sameGraders(caller.grader_ids, binding.grader_ids)
      : decision.outcome !== "reused" || decision.source?.execution_id !== reservation.execution_id ||
        (["version", "run", "shard", "task", "trial"] as const).some((key) => caller.identity[key] !== binding.identity[key]) ||
        caller.selection.shard !== binding.selection.shard || caller.selection.task !== binding.selection.task ||
        caller.selection.trial !== binding.selection.trial || caller.execution_mode !== binding.execution_mode ||
        !sameGraders(sortedStrings(caller.grader_ids), binding.grader_ids)) {
      throw new Error("Checkpoint decision contradicts its original execution binding.");
    }
  }
  if (submission?.completion &&
      (!submission.reservation || submission.completion.execution_id !== submission.reservation.execution_id ||
       (!(isOperationError && value.source?.boundary === "finalized-unit") &&
        submission.completion.reservation_digest !== await reservationDigest(submission.reservation)))) {
    throw new Error("Checkpoint completion contradicts its canonical original reservation.");
  }
  if (terminal && submission && submission.status !== "submission_error" &&
      submission.exit_code !== terminal.exit_code) {
    throw new Error("Retained submission exit code contradicts its terminal.");
  }
  if (terminal && submission && submission.status !== "reused" &&
      submission.engine_calls !== terminal.engine_calls) {
    throw new Error("Checkpoint submission contradicts its original terminal call count.");
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
        ids.some((id) => !binding.grader_ids.includes(id)) ||
        (observed.status !== "error" &&
         (observed.status === "failed") !== observed.validations.some((verdict) => !verdict.passed))) {
      throw new Error("Checkpoint observation contradicts its original binding.");
    }
  }
  if (value.source && submission?.reservation &&
      value.source.compatibility.work !== submission.reservation.logical.work) {
    throw new Error("Checkpoint source contradicts the actual submission work.");
  }
  if (submission?.status === "completed" && value.source?.boundary === "finalized-unit") {
    throw new Error("Finalized checkpoint work must be reused, not executed again.");
  }
  if (submission?.status === "reused") {
    const source = value.source;
    const sourceWithheld = value.status === "operation_error" && value.source_withheld === true;
    if (submission.engine_calls !== 0 || !submission.reservation || !submission.completion ||
        !submission.decision?.source || (!source && !sourceWithheld) ||
        (source && (submission.reservation.execution_id !== source.execution_id ||
          submission.completion.execution_id !== source.execution_id ||
          submission.completion.manifest_digest !== source.manifest_digest ||
          submission.completion.reservation_digest !== source.reservation_digest ||
          submission.decision.source.execution_id !== source.execution_id ||
          submission.decision.source.manifest_digest !== source.manifest_digest))) {
      throw new Error("Checkpoint reuse does not retain its exact original source.");
    }
    const completion = submission.completion;
    if (source && !isOperationError && source.completion_digest !== await goJSONDigest({
      kind: completion.kind, schema_version: completion.schema_version, execution_id: completion.execution_id,
      reservation_digest: completion.reservation_digest, manifest_digest: completion.manifest_digest,
    })) {
      throw new Error("Checkpoint reuse contradicts its canonical original completion.");
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
  const result = await parseCheckpointOperationResult(json);
  if (action === "create" && (result.source || result.source_withheld || result.submission?.status === "reused")) {
    throw new Error("Checkpoint create response contains resume-only state.");
  }
  if (action === "resume" && !result.source &&
      (result.status === "completed" || (result.submission?.status === "completed" && !result.source_withheld))) {
    throw new Error("Checkpoint API returned completed resume without its checkpoint source.");
  }
  if (action === "resume" && result.source && await checkpointDigest(result.source) !== digest) {
    throw new Error("Checkpoint source contradicts the requested checkpoint digest.");
  }
  if (action === "resume" && result.source && result.compatibility_result === "compatible" &&
      result.source.compatibility.request_policy_digest !== await requestPolicyDigest(wrapper.request)) {
    throw new Error("Checkpoint source policy contradicts the admitted captured request.");
  }
  if (response.headers.get("X-Waza-Exit-Code") !== String(result.exit_code) ||
      (!response.ok && (result.status === "completed" || result.status === "reused"))) {
    throw new Error("Checkpoint transport status contradicts its operation result.");
  }
  const caller = result.submission?.caller;
  if (caller && !sameRunnerIdentity(caller, admitted.identity)) {
    throw new Error("Checkpoint caller contradicts the admitted captured request.");
  }
  const decision = result.submission?.decision;
  if (decision && !sameSelection(decision.binding.selection, admitted.resolved)) {
    throw new Error("Checkpoint decision selection contradicts the admitted captured request.");
  }
  const binding = result.submission?.reservation?.binding;
  const reused = result.status === "reused" || result.submission?.status === "reused";
  if (binding) {
    const original = binding.selection;
    const current = admitted.resolved;
    if (!current || original.shard !== current.shard || original.task !== current.task || original.trial !== current.trial ||
        (!reused && !sameSelection(original, current)) ||
        !admitted.identity || (reused
          ? (["version", "run", "shard", "task", "trial"] as const)
            .some((key) => admitted.identity?.[key] !== binding.identity[key])
          : !sameRunnerIdentity(binding.identity, admitted.identity))) {
      throw new Error("Checkpoint original work contradicts the admitted captured request.");
    }
  }
  return { result, json };
}
