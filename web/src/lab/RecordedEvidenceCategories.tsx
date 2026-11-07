import { useEffect, useRef, useState } from "react";
import { inspectRecordedCategories, RecordedCategoryError, type RecordedCategories } from "../api/executionEvidenceCategories";

export default function RecordedEvidenceCategories({ digest, onSourceFailure, onAdmissionChange }: {
  digest: string; onSourceFailure: () => void; onAdmissionChange: (complete: boolean) => void;
}) {
  const [receipt, setReceipt] = useState<RecordedCategories | null>(null);
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const request = useRef<AbortController | null>(null);
  useEffect(() => () => request.current?.abort(), []);
  async function inspect() {
    request.current?.abort();
    setReceipt(null);
    onAdmissionChange(false);
    setError("");
    const controller = new AbortController();
    request.current = controller;
    setPending(true);
    try {
      const result = await inspectRecordedCategories(digest, controller.signal);
      if (!controller.signal.aborted) {
        if (!result.sourceVerified) onSourceFailure();
        else {
          setReceipt(result);
          onAdmissionChange(result.admissionComplete);
        }
      }
    } catch (error) {
      if (!controller.signal.aborted) setError(error instanceof RecordedCategoryError ? error.message : "Recorded category inspection failed.");
    } finally {
      if (!controller.signal.aborted) setPending(false);
    }
  }
  return <section aria-label="Recorded category admission">
    <h3>Admit recorded category shapes</h3>
    <p>Separate from producer declarations and recorded verdicts. A complete read is not assurance acceptance or new execution billing.</p>
    <button className="lab-button" disabled={pending} onClick={() => void inspect()}>
      {pending ? "Admitting categories..." : "Admit recorded categories"}
    </button>
    {error && <p role="alert">{error} Previous admitted observations have been cleared.</p>}
    {receipt && <>
      {!receipt.sourceVerified && <p role="alert">Current source integrity failed. All recorded observations are withheld.</p>}
      {receipt.sourceVerified && !receipt.admissionComplete && <p role="alert">
        {receipt.sourceContext ? "Current source or immediate parent context did not qualify." : "Category admission reported a conflict or invalid shape."}
        {" "}Independently verified facts remain visible, not acceptance.
      </p>}
      {receipt.sourceContext && <p>Current source context: {receipt.sourceContext.state}; {receipt.sourceContext.verification}.
        This is content and declared context, not runtime or billing attestation.</p>}
      {receipt.sourceContext?.parent_context && <p aria-label="Immediate parent context">
        Immediate declared parent: {receipt.sourceContext.parent_context.relationship}; {receipt.sourceContext.parent_context.state};
        {" "}{receipt.sourceContext.parent_context.verification}.
        {receipt.admissionComplete && receipt.sourceContext.parent_context.source && <>
          {" "}Execution: <code>{receipt.sourceContext.parent_context.source.execution_id}</code>;
          {" "}<a href={`/api/v1/manifests/${receipt.sourceContext.parent_context.source.manifest_digest}`}>Verified immediate parent manifest</a>.
        </>}
        {" "}One hop only. Unavailable ancestry is not a fully verified graph.
      </p>}
      <div className="lab-table-scroll"><table>
        <caption>Admitted recorded categories, not declaration completeness</caption>
        <thead><tr><th scope="col">Category</th><th scope="col">Declared state</th><th scope="col">Admitted state</th><th scope="col">Observed count</th><th scope="col">Verification</th><th scope="col">Reason</th></tr></thead>
        <tbody>{receipt.categories.map((category) => <tr key={category.category}>
          <th scope="row">{category.category}</th><td>{category.declared_state ?? "Unavailable"}</td>
          <td>{category.state}{category.verification === "declared-state-only" ? " (unqualified declaration)" :
            category.state === "present" && category.observed_count === "0" ? " (observed empty)" : ""}</td>
          <td>{category.observed_count ?? "Unavailable"}</td><td>{category.verification}</td><td>{category.reason ?? "None"}</td>
        </tr>)}</tbody>
      </table></div>
      {receipt.categories.map((category) => category.usage ? <p key={category.category}>
        Recorded usage ({category.state}): input tokens {category.usage.input_tokens}; output tokens {category.usage.output_tokens};
        {" "}AI credits {category.usage.ai_credits ?? "Unavailable"}. Incomplete inputs are not an authoritative billing total.
      </p> : null)}
      {receipt.sourceVerified && receipt.admissionComplete && <p>
        <a href={`/api/v1/evidence/${digest}/categories/document`} download>Download qualified category JSON</a>{" "}
        <a href={`/api/v1/evidence/${digest}/categories/csv`} download>Download qualified category CSV</a>
      </p>}
      <p>Exports recheck source bytes and category admission; unavailable counts and credits remain absent.</p>
    </>}
  </section>;
}
