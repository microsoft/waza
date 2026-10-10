import type { ReleaseCollection } from "../../src/api/release";

export const RELEASE_COLLECTIONS: ReleaseCollection[] = [{
  path: "controlled-example",
  decision: {
    kind: "waza.release-decision-view",
    version: "1.0",
    accepted: false,
    compatibility: { state: "compatible", reasons: [] },
    completeness: { state: "partial", reasons: ["A durable attempt started without final publication."] },
    assurance: { state: "not_assessed", reasons: ["Required attributable assurance is missing."] },
    golden: { state: "missing", reasons: ["Required first-attempt golden evidence is missing."] },
    billing: { state: "unavailable", reasons: ["Final attributable credits are unavailable."] },
    statistics: { state: "inconclusive", reasons: ["Independent complete clusters are not established."] },
    operations: { state: "incomplete", reasons: [] },
    planned_clusters: 1,
    limitations: ["Mock observations are not agent quality. Retries do not add independent samples."],
    accounting: {
      baseline: {
        planned_trials: 2, started_trials: 1, complete_trials: 0,
        started_attempts: 1, complete_attempts: 0,
        first_attempt_passes: 0, retry_policy_passes: 0, recovered_trials: 0,
        first_attempt_rate: null, retry_policy_rate: null,
      },
    },
    usage: {
      baseline: [
        { axis: "input_tokens", availability: "available", value: "9007199254740993", observation: "partial" },
        { axis: "ai_credits", availability: "unavailable", observation: "unknown", reason: "No final credit observation." },
      ],
    },
  },
}];

export function acceptedReleaseCollection(): ReleaseCollection {
  const collection = structuredClone(RELEASE_COLLECTIONS[0]);
  Object.assign(collection.decision, {
    accepted: true, planned_clusters: 8, estimate: 0, lower: -0.97, upper: 0.97, half_width: 0.97,
    completeness: { state: "complete", reasons: [] }, assurance: { state: "not_required", reasons: [] },
    golden: { state: "not_required", reasons: [] }, billing: { state: "not_required", reasons: [] },
    statistics: { state: "noninferiority", reasons: [] }, operations: { state: "observed", reasons: [] },
  });
  const counts = {
    planned_trials: 8, started_trials: 8, complete_trials: 8, started_attempts: 8, complete_attempts: 8,
    first_attempt_passes: 8, retry_policy_passes: 8, recovered_trials: 0, first_attempt_rate: 1, retry_policy_rate: 1,
  };
  collection.decision.accounting = { baseline: counts, candidate: counts };
  const usage = [
    ...collection.decision.usage.baseline ?? [],
    { axis: "output_tokens", availability: "unavailable", observation: "unknown", reason: "No token observation." },
  ];
  collection.decision.usage = { baseline: usage, candidate: usage };
  return collection;
}
