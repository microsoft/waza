import { useState } from "react";
import { negotiateRunner, type RunnerNegotiation as Negotiation } from "../api/runner";
import RunnerSelection from "./RunnerSelection";

const example = JSON.stringify({
  schema_version: "1.0",
  versions: ["2.0", "1.0"],
  required_capabilities: ["negotiation"],
  optional_capabilities: ["terminal-result"],
}, null, 2);

export default function RunnerNegotiation({ demo }: { demo: boolean }) {
  const [request, setRequest] = useState(example);
  const [result, setResult] = useState<Negotiation | null>(null);
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);

  async function check() {
    setResult(null);
    setError("");
    setPending(true);
    try {
      setResult(await negotiateRunner(request));
    } catch (error) {
      setError(error instanceof Error ? error.message : "Runner negotiation could not be loaded.");
    } finally {
      setPending(false);
    }
  }

  return (
    <>
    <section className="lab-panel lab-runner-negotiation" aria-label="Runner compatibility">
      <div className="lab-panel-header">
        <div>
          <h2>Check runner compatibility</h2>
          <p>Live offline negotiation only. This check never runs an evaluation or selects work.</p>
        </div>
      </div>
      {demo ? (
        <p>Runner compatibility is unavailable in the synthetic demo. Open the saved-results Lab with a running Waza server.</p>
      ) : (
        <>
          <label htmlFor="runner-request">Runner negotiation request (JSON)</label>
          <textarea
            id="runner-request"
            rows={11}
            spellCheck={false}
            value={request}
            disabled={pending}
            onChange={(event) => {
              setRequest(event.target.value);
              setResult(null);
              setError("");
            }}
          />
          <button className="lab-button" onClick={() => void check()} disabled={pending}>
            {pending ? "Checking compatibility..." : "Check compatibility"}
          </button>
          {error && <p role="alert">{error} No previous negotiation is shown.</p>}
          {result && (
            <div role="status" aria-label="Negotiation result">
              <h3>{result.status === "compatible" ? "Compatible (no execution)" : "Incompatible request"}</h3>
              <dl>
                <dt>Runner/API version</dt><dd>{result.negotiation.version ?? "Not negotiated"}</dd>
                <dt>Offered versions</dt><dd>{result.negotiation.requested_versions.join(", ") || "None"}</dd>
                <dt>Required capabilities</dt><dd>{result.negotiation.required_capabilities.join(", ") || "None"}</dd>
                <dt>Optional capabilities</dt><dd>{result.negotiation.optional_capabilities.join(", ") || "None"}</dd>
                <dt>Available capabilities</dt><dd>{result.negotiation.available_capabilities.join(", ") || "None"}</dd>
                <dt>Selected capabilities</dt><dd>{result.negotiation.selected_capabilities.join(", ") || "None"}</dd>
                <dt>Unavailable optional capabilities</dt><dd>{result.negotiation.unavailable_optional_capabilities.join(", ") || "None"}</dd>
              </dl>
              {result.diagnostics.map((diagnostic, index) => (
                <p key={`${diagnostic.code}-${index}`}>{diagnostic.code}: {diagnostic.message}</p>
              ))}
            </div>
          )}
        </>
      )}
    </section>
    <RunnerSelection demo={demo} />
    </>
  );
}
