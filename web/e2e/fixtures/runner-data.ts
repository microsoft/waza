import producerResult from "./runner-result.json" with { type: "json" };
import type { RunnerOutcome, RunnerResult } from "../../src/api/runner";

// runner-result.json is captured from the mock-only public CLI using
// internal/runnercontract/testdata/request.json; only volatile observation fields are removed.
export function runnerResult(outcome: RunnerOutcome = "success"): RunnerResult {
  const result = structuredClone(producerResult) as RunnerResult;
  const codes: Record<RunnerOutcome, number> = {
    success: 0, evaluation_failure: 1, infrastructure_failure: 2,
    canceled: 3, timeout: 4, unsupported_capability: 5, invalid_request: 6,
  };
  result.outcome = outcome;
  result.exit_code = codes[outcome];
  if (outcome !== "success") {
    result.failure_boundary = "execution";
    result.diagnostics = [{ code: "recorded_test_diagnostic", message: `Recorded ${outcome} diagnostic.` }];
    result.observation!.status = "error";
    result.observation!.error_msg = `Recorded ${outcome} execution error.`;
    result.observation!.validations = null;
  }
  if (outcome === "evaluation_failure") {
    result.failure_boundary = "grading";
    result.observation!.status = "failed";
    delete result.observation!.error_msg;
    result.observation!.validations = {
      output: {
        identifier: "output", type: "contains", score: 0, weight: 1,
        passed: false, feedback: "required output absent", duration_ms: 0,
      },
    };
  }
  if (outcome === "unsupported_capability" || outcome === "invalid_request") {
    result.failure_boundary = outcome === "unsupported_capability" ? "negotiation" : "selection";
    result.selection_valid = false;
    delete result.resolved;
    delete result.identity;
    delete result.observation;
    if (outcome === "unsupported_capability") {
      result.negotiation.required_capabilities.push("future-required");
      result.diagnostics = [{
        code: "required_capability",
        message: "Required capability future-required is unavailable. Use an advertised capability.",
      }];
    }
  }
  return result;
}

export function runnerCollection(results: unknown[] = [runnerResult()]) {
  return { schema_version: "1.0", results };
}
