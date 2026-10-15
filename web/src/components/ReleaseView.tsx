import { useQuery } from "@tanstack/react-query";
import { fetchReleaseCollections } from "../api/release";
import type { ReleaseDecision } from "../api/release";

const dimensions = [
  "compatibility", "completeness", "assurance", "golden", "billing",
  "statistics", "operations",
] as const;

function Decision({ decision }: { decision: ReleaseDecision }) {
  if (decision.kind !== "waza.release-decision-view" || decision.version !== "1.0") {
    return <p role="alert">Unsupported release decision; not passed.</p>;
  }
  return (
    <div className="space-y-4">
      <p className={decision.accepted ? "text-green-400" : "text-amber-400"}>
        Selected policy: {decision.accepted ? "PASSED" : "NOT PASSED"}
      </p>
      <dl className="grid gap-3 sm:grid-cols-2">
        {dimensions.map((name) => (
          <div key={name}>
            <dt className="capitalize text-zinc-300">{name}</dt>
            <dd className="text-sm text-zinc-400">
              <span>{decision[name].state}</span>
              {(decision[name].reasons ?? []).map((reason, index) => (
                <p key={index}>{reason}</p>
              ))}
            </dd>
          </div>
        ))}
      </dl>
      {(["baseline", "candidate"] as const).map((arm) => {
        const counts = decision.accounting[arm];
        return (
          <section key={arm} className="space-y-1 text-sm text-zinc-400">
            <h3 className="capitalize text-zinc-200">{arm}</h3>
            {counts ? (
              <>
                <p>Trials: {counts.started_trials}/{counts.planned_trials} started, {counts.complete_trials} complete.
                  {" "}Attempts: {counts.started_attempts} started, {counts.complete_attempts} complete.</p>
                <p>First-attempt passes: {counts.first_attempt_passes};
                  {" "}retry-policy passes: {counts.retry_policy_passes}; recovered: {counts.recovered_trials}.</p>
                <p>First-attempt rate: {counts.first_attempt_rate == null ? "unavailable" : `${(counts.first_attempt_rate * 100).toFixed(1)}%`};
                  {" "}retry-policy rate: {counts.retry_policy_rate == null ? "unavailable" : `${(counts.retry_policy_rate * 100).toFixed(1)}%`}.</p>
              </>
            ) : <p>Trial/attempt accounting unavailable.</p>}
            {(decision.usage[arm] ?? []).map((usage) => (
              <p key={`${usage.axis}:${usage.currency ?? ""}`}>
                {usage.axis} {usage.currency}: {usage.availability === "available" && usage.value != null ? usage.value : "unavailable"}
                {" "}({usage.observation}) {usage.reason}
              </p>
            ))}
          </section>
        );
      })}
      {decision.estimate != null && decision.lower != null && decision.upper != null && (
        <p className="text-sm text-zinc-400">
          Fixed-suite estimate {decision.estimate.toPrecision(4)}, interval [{decision.lower.toPrecision(4)}, {decision.upper.toPrecision(4)}].
          {" "}{decision.planned_clusters} planned clusters; retries are not iid samples.
        </p>
      )}
      {decision.limitations.map((limitation, index) => (
        <p key={index} className="text-xs text-zinc-500">{limitation}</p>
      ))}
    </div>
  );
}

export default function ReleaseView() {
  const { data, error, isPending } = useQuery({
    queryKey: ["release-collections"],
    queryFn: fetchReleaseCollections,
    retry: false,
  });
  return (
    <div className="space-y-6">
      <h1 className="text-2xl font-semibold text-zinc-100">Release policies</h1>
      <p className="text-sm text-zinc-400">
        Explicit controlled collections are separate from historical descriptive comparisons.
        Local consistency is not assurance or agent quality.
      </p>
      {isPending && <p className="text-zinc-400">Loading controlled collections...</p>}
      {error && <p role="alert" className="text-red-400">{error.message}</p>}
      {data?.length === 0 && <p className="text-zinc-400">No controlled collections in the configured local results directory.</p>}
      {data?.map((collection) => (
        <article key={collection.path} className="space-y-4 rounded border border-zinc-700 p-5">
          <h2 className="text-lg text-zinc-100">{collection.path}</h2>
          {collection.error && <p role="alert" className="text-amber-400">{collection.error}</p>}
          <Decision decision={collection.decision} />
        </article>
      ))}
    </div>
  );
}
