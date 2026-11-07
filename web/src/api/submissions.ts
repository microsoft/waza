import { parseProtectedSubmissionResult, type SubmissionResult } from "./checkpointResults";
import { parseStrictJSON } from "../lib/strictJson";

export interface SubmissionResponse {
  result: SubmissionResult;
  json: string;
}

export async function submitExecution(request: string, signal: AbortSignal): Promise<SubmissionResponse> {
  const wrapper = parseStrictJSON(request);
  if (typeof wrapper !== "object" || wrapper === null || !("request" in wrapper) ||
      typeof wrapper.request !== "object" || wrapper.request === null ||
      !("timeout_ms" in wrapper.request) || typeof wrapper.request.timeout_ms !== "number" ||
      !Number.isInteger(wrapper.request.timeout_ms) || wrapper.request.timeout_ms < 1 ||
      wrapper.request.timeout_ms > 86_400_000) {
    throw new Error("Provide a captured submission with an explicit native request timeout_ms.");
  }
  const response = await fetch("/api/v1/runner/submit", {
    method: "POST", headers: { "Content-Type": "application/json" }, body: request,
    signal: AbortSignal.any([signal, AbortSignal.timeout(wrapper.request.timeout_ms + 30_000)]),
  });
  const json = await response.text();
  const result = parseProtectedSubmissionResult(json);
  if (response.headers.get("X-Waza-Exit-Code") !== String(result.exit_code) ||
      (!response.ok && (result.status === "completed" || result.status === "reused"))) {
    throw new Error("Submission transport status contradicts its operation result.");
  }
  return { result, json };
}
