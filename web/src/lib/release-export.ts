import type { ReleaseCollection } from "../api/release";
import { toCSV } from "./export";

export function nativeAttemptsCSV(collection: ReleaseCollection): string {
  const native = collection.decision.native;
  const headers = [
    "Collection", "Selected policy accepted", "Profile", "Version",
    "Arm", "Cluster", "Task", "Trial", "Eval", "Attempt", "Status", "Category", "Error",
    "Output availability", "Output present", "Output", "Event models JSON", "Accounting models JSON", "Usage complete",
    "Lifecycle JSON", "Runtime version", "Provider currency", "Assurance capability", "Client tool policy",
    "Budget contract", "Arm usage JSON",
  ];
  if (!native) return toCSV(headers, []);
  return toCSV(headers, (native.attempts ?? []).map((attempt) => [
    collection.path, String(collection.decision.accepted), native.profile, native.version,
    attempt.key.arm, attempt.key.cluster_id, attempt.key.task_id, String(attempt.key.trial_ordinal),
    attempt.key.eval_id, String(attempt.key.attempt_ordinal), attempt.status, attempt.category, attempt.error,
    attempt.output_availability, String(attempt.output !== undefined), attempt.output ?? "", JSON.stringify(attempt.event_models),
    JSON.stringify(attempt.accounting_models), String(attempt.usage_complete), JSON.stringify(attempt.lifecycle),
    native.runtime_version, native.provider_currency, native.assurance, native.client_tool_policy,
    native.budget_contract, JSON.stringify(collection.decision.usage[attempt.key.arm] ?? null),
  ]));
}
