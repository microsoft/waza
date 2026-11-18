import { useState } from "react";
import { parseIsolationPreflight, type IsolationCapability, type IsolationPreflight as Report } from "../api/isolation";

const labels: Record<IsolationCapability["dimension"], string> = {
  filesystem: "Filesystem",
  environment: "Environment and secrets",
  network: "Network",
  subprocess: "Subprocesses",
  remote_tools: "Remote tools",
};

function effectiveLabel(entry: IsolationCapability) {
  if (entry.effective === "restricted") return "Restricted (enforced)";
  if (entry.effective === "unavailable") return "UNAVAILABLE (rejected)";
  return "Unrestricted";
}

export default function IsolationPreflight() {
  const [wire, setWire] = useState("");
  const [report, setReport] = useState<Report | null>(null);
  const [error, setError] = useState("");

  function parse() {
    setReport(null);
    setError("");
    try {
      setReport(parseIsolationPreflight(wire));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Isolation report could not be parsed.");
    }
  }

  const unavailable = report?.dimensions.filter((entry) => entry.diagnostic) ?? [];

  return (
    <section className="lab-panel" aria-label="Execution isolation">
      <div className="lab-panel-header">
        <div>
          <h2>Inspect execution isolation</h2>
          <p>
            Paste the output of <code>waza isolation preflight</code> or <code>waza run --isolation-report</code>.
            Waza enforces no boundary itself; a boundary is shown as enforced only when a provider's conformance probe blocked the attempted violation.
          </p>
        </div>
      </div>
      <label htmlFor="isolation-report-json">Isolation preflight report (JSON)</label>
      <textarea id="isolation-report-json" rows={8} spellCheck={false} value={wire}
        onChange={(event) => {
                setWire(event.target.value);
          setReport(null);
          setError("");
        }}
      />
      <button className="lab-button" onClick={parse}>Inspect isolation report</button>
      {error && <p role="alert">{error} No isolation capability is shown.</p>}
      {report && (
        <div role="status" aria-label="Isolation preflight result">
          <h3>{report.decision === "admitted" ? "Preflight admitted: execution may start" : "Preflight rejected: execution must not start"}</h3>
          <p>Checked at {report.checked_at}. Probe evidence is point-in-time for this host.</p>
          <table aria-label="Isolation capabilities">
            <thead>
              <tr><th>Boundary</th><th>Requested</th><th>Available</th><th>Effective</th><th>Enforcement source</th><th>Probe evidence</th></tr>
            </thead>
            <tbody>
              {report.dimensions.map((entry) => (
                <tr key={entry.dimension}>
                  <td>{labels[entry.dimension]}</td>
                  <td>{entry.requested}</td>
                  <td>{entry.available ? "Yes (probe enforced)" : "No"}</td>
                  <td>{effectiveLabel(entry)}</td>
                  <td>{entry.enforcement_source ?? "None"}</td>
                  <td>{entry.probes.length === 0 ? "No provider probed this boundary"
                    : entry.probes.map((probe) => `${probe.provider}: ${probe.outcome} (${probe.detail})`).join("; ")}</td>
                </tr>
              ))}
            </tbody>
          </table>
          <h4>Providers</h4>
          <ul aria-label="Isolation providers">
            {report.providers.map((provider) => (
              <li key={provider.name}>
                {provider.name}: {provider.status}{provider.enforced_by ? ` (declared enforcement: ${provider.enforced_by})` : ""}{provider.reason ? ` - ${provider.reason}` : ""}
              </li>
            ))}
          </ul>
          <h4>Diagnostics for unproven or unavailable boundaries</h4>
          {unavailable.length === 0 ? <p>None. No requested boundary is unproven, unavailable, or violated.</p> : (
            <ul aria-label="Isolation diagnostics">
              {unavailable.map((entry) => (
                <li key={entry.dimension}>{labels[entry.dimension]}: {entry.diagnostic!.code} - {entry.diagnostic!.message}</li>
              ))}
            </ul>
          )}
        </div>
      )}
    </section>
  );
}
