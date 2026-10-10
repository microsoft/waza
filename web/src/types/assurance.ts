import Ajv2020 from "ajv/dist/2020";
import addFormats from "ajv-formats";
import reportSchema from "../../../schemas/grader-assurance-1.0.schema.json";
import calibratedSchema from "../../../schemas/grader-assurance-1.1.schema.json";
import referenceSchema from "../../../schemas/grader-reference-1.0.schema.json";
import evidenceSchema from "../../../schemas/evidence-manifest-1.0.schema.json";
import { parseStrictJSON } from "../lib/strictJson";

export const assuranceStates = [
  "passed", "failed", "not_assessed", "insufficient_evidence", "operational_error", "invalid",
] as const;
export type AssuranceState = typeof assuranceStates[number];

export interface AssuranceBinding {
  domain: string;
  applicable: boolean;
  state: "verified" | "mismatch" | "missing" | "not_applicable";
  expected: string | null;
  actual: string | null;
}

export interface AssuranceObservation {
  case_id: string;
  scenario_id: string;
  domain: string;
  classification: string;
  source_scope: "preserved_file_subset" | "authored_finite_output";
  expected_passed: boolean;
  state: string;
  reason: string;
  agreement: boolean | null;
  result: { passed: boolean; score: number; feedback: string } | null;
  evidence: { artifact_id: string; pointer?: string; origin: { eval_id: string; task_id: string; run_number: number } }[];
  bindings: AssuranceBinding[] | null;
}

export interface JudgeExecution {
  case_id: string;
  task_id: string;
  requirement_id: string;
  check: { scope: "eval" | "task" | "checkpoint"; grader: string; after_turn?: number };
  initialized: boolean;
  executed: boolean;
  callbacks: number;
  requested_model: string;
  event_models: string[] | null;
  accounting_models: string[] | null;
  event_model_attribution_complete: boolean;
  accounting_model_attribution_complete: boolean;
  usage_source: string;
  usage_complete: boolean;
  state: AssuranceState;
  reason: string;
  diagnostics: { stage: string; code: string }[];
  usage: object | null;
  credits: number | null;
}

interface AssuranceReportBase {
  kind: "waza.grader-assurance";
  state: AssuranceState;
  reason: string;
  labels_sha256: string;
  review: { source_id: string; current_source_accepted: boolean; eligible: boolean; declared_state: string };
  bindings: AssuranceBinding[];
  domains: {
    id: string; state: AssuranceState; minimum_cases: number; minimum_agreement: number;
    observed_cases: number; observed_checks: number; expected_checks: number; agreement: number | null;
  }[];
  requirements: {
    task_id: string;
    requirement_id: string;
    check: { scope: "eval" | "task" | "checkpoint"; grader: string; after_turn?: number };
    state: AssuranceState;
    reason: string;
    declared_coverage: { good: number; alternative_valid: number; critical_bad: number; intended_negative: number };
    observed_coverage: { good: number; alternative_valid: number; critical_bad: number; intended_negative: number };
    observations: AssuranceObservation[];
  }[];
}

interface CalibrationClaims {
    state: AssuranceState;
    reason: string;
    credits: number | null;
    usage: object | null;
    confidence_supported: false;
    max_judge_executions: number;
    executions: number;
    billable_calls_bounded: false;
    cost_bounded: false;
}

export type AssuranceReport = AssuranceReportBase & (
  { schema_version: "1.0"; assessment_mode?: never; calibration: CalibrationClaims } |
  { schema_version: "1.1"; assessment_mode: "independent_authored_rubric_calibration";
    calibration: CalibrationClaims & { execution_ledger: JudgeExecution[] } }
);

const compiler = new Ajv2020({ strict: false, strictNumbers: true, allErrors: false });
addFormats(compiler);
compiler.addSchema(evidenceSchema);
compiler.addSchema(referenceSchema);
const validateReport = compiler.compile<AssuranceReport & { schema_version: "1.0" }>(reportSchema);
const validateCalibrated = compiler.compile<AssuranceReport & { schema_version: "1.1" }>(calibratedSchema);

export function parseAssuranceReport(text: string, allowCalibrated = false): AssuranceReport {
  const value = parseStrictJSON(text);
  if (!validateReport(value) && !(allowCalibrated && validateCalibrated(value))) {
    throw new Error("Unsupported or incomplete assurance report.");
  }
  const observations = value.requirements.reduce((count, item) => count + item.observations.length, 0);
  if (value.requirements.length > 1000 || observations > 5000 ||
    (value.schema_version === "1.1" && value.calibration.execution_ledger.length > 5000)) {
    throw new Error("Assurance report exceeds the display limit.");
  }
  return value;
}
