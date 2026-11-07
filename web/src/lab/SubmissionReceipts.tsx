import { useMemo } from "react";
import { parseProtectedSubmissionResult } from "../api/checkpointResults";
import { downloadText } from "../lib/export";
import { clearSubmissionReceipts, type SubmissionReceipt } from "./submissionReceiptStore";

export default function SubmissionReceipts({ receipts, pending }: {
  receipts: readonly SubmissionReceipt[]; pending: boolean;
}) {
  const rows = useMemo(() => receipts.map((receipt) => ({
    receipt, result: parseProtectedSubmissionResult(receipt.json),
  })), [receipts]);
  if (rows.length === 0) return null;
  const originals = new Set(rows.flatMap(({ result }) => result.reservation ? [result.reservation.execution_id] : []));
  return (
    <section aria-label="Historical submission receipts">
      <h3>This page's submission receipts</h3>
      <p>Historical protected responses, not reread/current source availability or fresh runtime assurance.
        Navigation within the Lab retains them; page reload ends this view. Receipt order is not global
        execution order. Independent roots and sibling branches remain incomparable.</p>
      <p>Each response is retained separately, including reuse of the same original and operation errors.
        Source engine calls and billing are not added to new-operation totals.</p>
      <div className="lab-table-scroll">
        <table className="lab-table" aria-label="Recorded attempts and reuse decisions">
          <thead><tr>
            <th>Receipt</th><th>Logical work</th><th>Original execution / native ordinal</th>
            <th>Caller attempt / decision</th><th>Parent lineage</th><th>Reuse source</th>
            <th>Operation / native outcome</th><th>New calls</th><th>Diagnostics</th>
          </tr></thead>
          <tbody>{rows.map(({ receipt, result }) => <tr key={receipt.sequence}>
            <td>{receipt.sequence}</td>
            <td><code>{result.reservation?.logical.run ?? "Unavailable"}</code><br />
              <code>{result.reservation?.logical.work ?? "Unavailable"}</code></td>
            <td><code>{result.reservation?.execution_id ?? "No admitted reservation"}</code><br />
              {result.reservation?.binding.selection.attempt ?? "Unavailable"}</td>
            <td><code>{result.caller?.attempt ?? "Unavailable"}</code><br />
              <code>{result.decision?.decision_id ?? "No admitted decision"}</code><br />
              {result.decision ? `${result.decision.policy} / ${result.decision.outcome}` : "Unavailable"}</td>
            <td>{result.reservation?.parent
              ? <>{result.reservation.parent.relationship}: <code>{result.reservation.parent.execution_id}</code><br />
                {originals.has(result.reservation.parent.execution_id) ? "Parent appears in these receipts" : "Parent not retained in this view"}</>
              : result.reservation ? "No parent declared" : "No admitted reservation"}</td>
            <td>{result.decision?.source
              ? <><code>{result.decision.source.execution_id}</code><br /><code>{result.decision.source.manifest_digest}</code></>
              : "No admitted reuse source"}</td>
            <td>{result.status} / {result.terminal?.outcome ?? "Unavailable"}<br />
              {result.completion ? "Completion recorded by this response" : "No operation completion claim"}</td>
            <td>{result.engine_calls}</td>
            <td>{result.diagnostics.length ? result.diagnostics.map((item) => item.code).join(", ") : "None recorded"}</td>
          </tr>)}</tbody>
        </table>
      </div>
      <p>The receipt bundle is not a server sidecar profile. Each <code>json</code> string contains the exact response bytes.</p>
      <button className="lab-button" onClick={() => downloadText(
        `${JSON.stringify(receipts, null, 2)}\n`, "waza-browser-submission-receipts.json", "application/json",
      )}>Download exact historical receipts</button>
      <button className="lab-button" disabled={pending} onClick={clearSubmissionReceipts}>
        Clear historical view (does not delete stored evidence)
      </button>
    </section>
  );
}
