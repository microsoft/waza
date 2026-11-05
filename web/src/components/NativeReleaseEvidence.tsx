import type { NativeDecision } from "../api/release";

export default function NativeReleaseEvidence({ native }: { native: NativeDecision }) {
  return (
    <section aria-label="Native collection evidence" className="space-y-4 border-t border-zinc-700 pt-4">
      <h3 className="text-lg text-zinc-100">Native collection evidence</h3>
      <dl className="grid gap-3 text-sm sm:grid-cols-2">
        <div><dt className="text-zinc-300">Profile</dt><dd className="break-all text-zinc-400">{native.profile} / {native.version}</dd></div>
        <div><dt className="text-zinc-300">Runtime version</dt><dd className="text-zinc-400">{native.runtime_version}</dd></div>
        <div><dt className="text-zinc-300">Provider currency</dt><dd className="text-zinc-400">{native.provider_currency}</dd></div>
        <div><dt className="text-zinc-300">Assurance capability</dt><dd className="text-zinc-400">{native.assurance}</dd></div>
      </dl>
      <p className="text-sm text-zinc-400">
        {native.client_tool_policy === "deny_all_client_policy_not_server_isolation"
          ? "Declared client tool policy: deny all. This is not server isolation."
          : `Client tool policy: ${native.client_tool_policy}`}
      </p>
      <p className="text-sm text-zinc-400">
        {native.budget_contract === "per_arm_final_observed_limits_best_effort_global_start_checks_async_overshoot_possible"
          ? "Budgets use per-arm final observed usage. Global next-start checks are best effort; unknown required usage blocks new starts. Asynchronous overshoot is possible, not an atomic spending cap."
          : `Budget contract: ${native.budget_contract}`}
      </p>
      <p className="text-xs text-zinc-500">Native evidence establishes local observations, not authenticity, independent review, or assurance.</p>
      {!native.attempts?.length ? <p className="text-sm text-zinc-400">No native attempt observations available.</p> : (
        <div role="region" aria-label="Native attempt observations" tabIndex={0} className="overflow-x-auto focus-visible:outline-2 focus-visible:outline-blue-400">
          <table className="w-full text-left text-sm text-zinc-400">
            <caption className="sr-only">Native attempts, model attribution, usage completeness and lifecycle observations</caption>
            <thead className="text-zinc-300">
              <tr>
                <th scope="col" className="p-2">Attempt identity</th>
                <th scope="col" className="p-2">Outcome</th>
                <th scope="col" className="p-2">Models / usage</th>
                <th scope="col" className="p-2">Evidence details</th>
              </tr>
            </thead>
            <tbody>
              {native.attempts.map((attempt) => {
                const key = attempt.key;
                const identity = `${key.arm} / ${key.cluster_id} / ${key.task_id} / trial ${key.trial_ordinal} / attempt ${key.attempt_ordinal}`;
                return (
                  <tr key={JSON.stringify(key)} className="border-t border-zinc-800 align-top">
                    <th scope="row" className="max-w-64 break-words p-2 font-normal text-zinc-300">
                      {identity}<p className="text-xs text-zinc-500">Eval: {key.eval_id}</p>
                    </th>
                    <td className="p-2">
                      <p>Status: {attempt.status || "unavailable"}</p>
                      <p>Category: {attempt.category || "not recorded"}</p>
                      {attempt.error && <p className="max-w-80 whitespace-pre-wrap break-words text-amber-400">Error: {attempt.error}</p>}
                    </td>
                    <td className="p-2">
                      <p>Received event models: {attempt.event_models?.length ? attempt.event_models.join(", ") : "unavailable"}</p>
                      <p>Accounting models: {attempt.accounting_models?.length ? attempt.accounting_models.join(", ") : "unavailable"}</p>
                      <p>Usage: {attempt.usage_complete ? "complete" : "incomplete / unavailable"}</p>
                    </td>
                    <td className="min-w-56 p-2">
                      <p>Output: {attempt.output_availability || "unavailable"}</p>
                      {attempt.output !== undefined && (
                        <details className="mt-2">
                          <summary className="cursor-pointer text-zinc-300">Actual output - {identity}</summary>
                          <pre className="mt-2 max-h-64 max-w-xl overflow-auto whitespace-pre-wrap break-words">{attempt.output === "" ? "(empty output)" : attempt.output}</pre>
                        </details>
                      )}
                      <details className="mt-2">
                        <summary className="cursor-pointer text-zinc-300">Lifecycle - {identity}</summary>
                        {Object.keys(attempt.lifecycle).length === 0
                          ? <p>No lifecycle observations available.</p>
                          : <dl className="mt-2 space-y-2">{Object.entries(attempt.lifecycle).map(([name, phase]) => (
                            <div key={name}><dt className="text-zinc-300">{name}</dt><dd>{phase.state}{phase.reason && `: ${phase.reason}`}</dd></div>
                          ))}</dl>}
                      </details>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}
