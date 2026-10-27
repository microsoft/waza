import { useQuery } from "@tanstack/react-query";
import { fetchRunnerResults, type RunnerResult, type RunnerSelection } from "../api/runner";

function Values({ values }: { values: string[] }) {
  return values.length ? <ul>{values.map((value) => <li key={value}><code>{value || "(empty string)"}</code></li>)}</ul> : <>None recorded</>;
}

function Tuple({ value }: { value?: RunnerSelection }) {
  return value ? <dl className="lab-runner-tuple">
    <dt>Shard</dt><dd>{value.shard}</dd>
    <dt>Task</dt><dd>{value.task}</dd>
    <dt>Trial</dt><dd>{value.trial}</dd>
    <dt>Attempt</dt><dd>{value.attempt}</dd>
  </dl> : <p>Unavailable — no tuple recorded.</p>;
}

function Result({ result, index }: { result: RunnerResult; index: number }) {
  const id = `runner-result-${index}`;
  const negotiation = result.negotiation;
  return <article className="lab-panel lab-runner-result" aria-labelledby={`${id}-title`}>
    <div className="lab-panel-header">
      <div>
        <h2 id={`${id}-title`}>Result {index + 1}: <code>{result.outcome}</code></h2>
        <p>Recorded terminal outcome — not inferred from HTTP or observation status.</p>
      </div>
      <a href={`#${id}-diagnostics`} onClick={(event) => {
        event.preventDefault();
        document.getElementById(`${id}-diagnostics`)?.focus();
      }}>Diagnostics ({result.diagnostics.length})</a>
    </div>
    <div className="lab-runner-body">
      <section aria-labelledby={`${id}-terminal`}>
        <h3 id={`${id}-terminal`}>Terminal envelope</h3>
        <dl className="lab-runner-fields">
          <dt>Result schema version</dt><dd>{result.schema_version}</dd>
          <dt>Terminal outcome</dt><dd><code>{result.outcome}</code></dd>
          <dt>Stable exit category (exit_code)</dt><dd>{result.exit_code}</dd>
          <dt>Failure boundary</dt><dd><code>{result.failure_boundary}</code></dd>
          <dt>Envelope validation (recorded)</dt><dd>{String(result.envelope_valid)}</dd>
          <dt>Client contract validation</dt><dd>Passed schema and cross-field checks</dd>
        </dl>
      </section>
      <section aria-labelledby={`${id}-negotiation`}>
        <h3 id={`${id}-negotiation`}>Runner / API negotiation</h3>
        <dl className="lab-runner-fields">
          <dt>Negotiated version</dt><dd>{negotiation.version ?? "Unavailable — no version selected"}</dd>
          <dt>Requested versions</dt><dd><Values values={negotiation.requested_versions} /></dd>
          <dt>Required capabilities</dt><dd><Values values={negotiation.required_capabilities} /></dd>
          <dt>Optional capabilities</dt><dd><Values values={negotiation.optional_capabilities} /></dd>
          <dt>Available capabilities</dt><dd><Values values={negotiation.available_capabilities} /></dd>
          <dt>Selected capabilities</dt><dd><Values values={negotiation.selected_capabilities} /></dd>
          <dt>Unavailable optional capabilities</dt><dd><Values values={negotiation.unavailable_optional_capabilities} /></dd>
        </dl>
      </section>
      <section aria-labelledby={`${id}-selection`}>
        <h3 id={`${id}-selection`}>Exact work selection</h3>
        <dl className="lab-runner-fields">
          <dt>Pre-execution validation (selection_valid)</dt><dd>{result.selection_valid ? "true — validated before execution" : "false — not validated"}</dd>
          <dt>Adjacent work selected</dt><dd>{String(result.adjacent_work_selected)} — no adjacent work selected</dd>
        </dl>
        <div className="lab-runner-tuples">
          <div><h4>Requested tuple</h4><Tuple value={result.requested} /></div>
          <div><h4>Resolved tuple</h4><Tuple value={result.resolved} /></div>
        </div>
        <p className="lab-muted">Selection is recorded validation evidence, not proof of exactly-once execution.</p>
      </section>
      <section aria-labelledby={`${id}-identity`}>
        <h3 id={`${id}-identity`}>Logical identity hierarchy</h3>
        {result.identity ? <>
          <dl className="lab-runner-fields">
            <dt>Derivation version</dt><dd><code>{result.identity.version}</code></dd>
            <dt>Canonical input digest</dt><dd><code>{result.identity.canonical_digest}</code></dd>
          </dl>
          <ol aria-label="Logical identity lineage">
            {(["run", "shard", "task", "trial", "attempt"] as const).map((level) =>
              <li key={level}><strong>{level}</strong><code>{result.identity![level]}</code></li>)}
          </ol>
        </> : <p>Unavailable — no identity derived.</p>}
      </section>
      <section id={`${id}-diagnostics`} tabIndex={-1} aria-labelledby={`${id}-diagnostics-title`}>
        <h3 id={`${id}-diagnostics-title`}>Linked diagnostics for result {index + 1}</h3>
        {result.diagnostics.length ? <ul className="lab-runner-diagnostics">
          {result.diagnostics.map((diagnostic, diagnosticIndex) =>
            <li key={diagnosticIndex}><code>{diagnostic.code}</code><p>{diagnostic.message}</p></li>)}
        </ul> : <p>No diagnostics recorded. This does not imply success.</p>}
      </section>
    </div>
  </article>;
}

export default function RunnerResults({ demo }: { demo: boolean }) {
  const query = useQuery({
    queryKey: ["lab", "runner-results"],
    queryFn: ({ signal }) => fetchRunnerResults(signal),
    enabled: !demo,
    retry: false,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  });
  if (demo) return <div className="lab-panel lab-runner-message" role="status">
    Runner results are unavailable in the synthetic demo. No runner API is requested and no runner fixtures are substituted.{" "}
    <a href="#/lab/runner">Open saved runner results</a>
  </div>;
  return <div className="lab-runner">
    <div className="lab-history-toolbar">
      <p>Saved <code>*.runner.json</code> artifacts, separate from evaluation history. Read-only; no execution controls.</p>
      <button className="lab-button" disabled={query.isFetching} onClick={() => void query.refetch()}>
        {query.isFetching ? "Loading runner results..." : "Refresh runner results"}
      </button>
    </div>
    {query.isPending ? <p role="status">Loading saved runner envelopes...</p>
      : query.isError ? <div role="alert" className="lab-panel lab-runner-message">
        <h2>Runner results unavailable</h2>
        <p>{query.error.message}</p>
        <p>No cached success or synthetic fallback is displayed. Check the API and saved artifacts, then refresh.</p>
      </div> : query.data && <>
        <p role="status">API collection version {query.data.schema_version} · {query.data.results.length} saved runner envelopes</p>
        {query.data.results.length === 0 && <div className="lab-panel lab-runner-message">
          <h2>No saved runner results</h2>
          <p>Save runner stdout as a <code>*.runner.json</code> artifact in the dashboard results directory, then refresh. Missing evidence is unassessed, not success.</p>
        </div>}
        {query.data.results.map((entry, index) => entry.state === "recorded"
          ? <Result key={index} result={entry.result} index={index} />
          : <article key={index} className="lab-panel lab-runner-message">
            <div role="alert">
            <h2>Result {index + 1}: {entry.state === "unsupported" ? "Unsupported envelope" : "Malformed envelope"}</h2>
            <p>{entry.reason}</p>
            <p>Outcome, selection and identity withheld; unavailable / unassessed. No success is inferred.</p>
            </div>
          </article>)}
      </>}
  </div>;
}
