import { useRef, useState } from "react";
import { parseAssuranceReport } from "../types/assurance";
import type { AssuranceBinding, AssuranceReport } from "../types/assurance";
import { formatAICredits } from "../lib/format";

function Bindings({ bindings }: { bindings: AssuranceBinding[] | null }) {
  if (!bindings?.length) return <p className="text-zinc-400">Bindings unavailable.</p>;
  return <dl className="space-y-2 text-sm">{bindings.map((binding) =>
    <div key={binding.domain} className="break-all">
      <dt className="text-zinc-200">Declared {binding.domain}: {binding.state} (not reverified)</dt>
      <dd className="text-zinc-400">Expected: {binding.expected ?? "unavailable"}; actual: {binding.actual ?? "unavailable"}</dd>
    </div>,
  )}</dl>;
}

export default function AssurancePage() {
  const [report, setReport] = useState<AssuranceReport | null>(null);
  const [error, setError] = useState<string | null>(null);
  const generation = useRef(0);

  async function load(file: File | undefined) {
    const current = ++generation.current;
    setError(null);
    setReport(null);
    if (!file) return;
    if (file.size > 16 * 1024 * 1024) {
      setError("Report exceeds the 16 MiB import limit.");
      return;
    }
    try {
      const bytes = await file.arrayBuffer();
      const parsed = parseAssuranceReport(new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(bytes));
      if (current === generation.current) setReport(parsed);
    } catch {
      if (current === generation.current) setError("Cannot inspect this report: invalid JSON, unsupported kind/version/shape, unreadable file, or display limit exceeded.");
    }
  }

  return <div className="space-y-6">
    <h1 className="text-2xl font-semibold text-zinc-100">Grader assurance</h1>
    <p className="text-zinc-400">
      Inspect a supplied standalone report locally. Nothing is uploaded or automatically associated with a historical run.
      Importing validates raw JSON and shape, not claimed assessment states, digest bindings, fresh evidence or reviewer identity.
      It does not rerun graders.
    </p>
    <label className="block text-sm text-zinc-200">
      Supplied assurance report
      <input className="mt-2 block rounded border border-zinc-700 p-2" type="file" accept=".json,application/json"
        onChange={(event) => void load(event.currentTarget.files?.[0])} />
    </label>
    {error && <p role="alert" className="rounded border border-red-500/40 p-4 text-red-400">{error}</p>}
    {!report && !error && <p className="text-zinc-400">Not assessed: no assurance report selected. Native grader passes are not assurance.</p>}
    {report && <>
      <section className="rounded border border-zinc-700 p-4" aria-label="Supplied assessment">
        <h2 className="text-lg text-zinc-100">Supplied assessment: {report.state}</h2>
        <p className="text-zinc-300">{report.reason}</p>
        <p className="break-all text-sm text-zinc-400">Exact label bytes SHA-256: {report.labels_sha256}</p>
      </section>
      <section className="rounded border border-zinc-700 p-4" aria-label="Supplied review and provenance">
        <h2 className="mb-3 text-lg text-zinc-100">Supplied review and provenance</h2>
        <p className="mb-3 text-zinc-300">
          Declared review: {report.review.declared_state || "unavailable"}; source: {report.review.source_id || "unavailable"};
          {" "}current source accepted: {String(report.review.current_source_accepted)}; eligible: {String(report.review.eligible)}.
          {" "}Eligibility is not authenticated human identity.
        </p>
        <Bindings bindings={report.bindings} />
      </section>
      <section className="rounded border border-zinc-700 p-4" aria-label="Model calibration">
        <h2 className="text-lg text-zinc-100">Model calibration: {report.calibration.state}</h2>
        <p className="text-zinc-300">{report.calibration.reason}</p>
        <p className="text-zinc-300">Judge executions: {report.calibration.executions}/{report.calibration.max_judge_executions}.
          {" "}SDK follow-up calls and spend are not bounded; billable-call count and cost may be unknown.</p>
        <p className="text-zinc-300">Supplied observed credits: {report.calibration.credits == null ? "unavailable" : formatAICredits(report.calibration.credits)}</p>
        <p className="text-sm text-zinc-400">Usage: {report.calibration.usage == null ? "unavailable" : "supplied"}; no confidence guarantee.</p>
      </section>
      <section aria-label="Domain criteria" className="space-y-2">
        <h2 className="text-lg text-zinc-100">Domain criteria</h2>
        {report.domains.map((domain, index) => <div key={index} className="rounded border border-zinc-700 p-3 text-zinc-300">
          {domain.id}: {domain.state}; {domain.observed_cases}/{domain.minimum_cases} required cases;
          {" "}{domain.observed_checks}/{domain.expected_checks} checks observed;
          {" "}agreement {domain.agreement == null ? "unavailable" : domain.agreement.toFixed(3)} / criterion {domain.minimum_agreement}.
          <p className="text-sm text-zinc-400">Finite-corpus agreement, not independence or confidence.</p>
        </div>)}
      </section>
      <nav aria-label="Scoped requirements" className="flex flex-wrap gap-3 text-sm text-blue-400">
        {report.requirements.map((requirement, index) => <a key={index} href={`#assurance-requirement-${index}`} onClick={(event) => {
          event.preventDefault();
          document.getElementById(`assurance-requirement-${index}`)?.scrollIntoView();
        }}>{requirement.task_id}/{requirement.requirement_id}/{requirement.check.scope}/{requirement.check.grader}</a>)}
      </nav>
      {report.requirements.map((requirement, index) => <section key={index} id={`assurance-requirement-${index}`}
        className="space-y-3 rounded border border-zinc-700 p-4" aria-label={`Requirement ${requirement.task_id}/${requirement.requirement_id}`}>
        <h2 className="text-lg text-zinc-100">{requirement.task_id}/{requirement.requirement_id}: {requirement.state}</h2>
        <p className="text-zinc-300">{requirement.check.scope}{requirement.check.after_turn == null ? "" : ` turn ${requirement.check.after_turn}`} / {requirement.check.grader}: {requirement.reason}</p>
        <p className="text-sm text-zinc-400">Observed good/alternative/bad/intended-negative:
          {" "}{requirement.observed_coverage.good}/{requirement.observed_coverage.alternative_valid}/{requirement.observed_coverage.critical_bad}/{requirement.observed_coverage.intended_negative}.
        </p>
        {requirement.observations.map((observation, caseIndex) => <details key={caseIndex} className="rounded bg-zinc-800 p-3">
          <summary className="cursor-pointer text-zinc-200">{observation.case_id} ({observation.classification}): {observation.state}; agreement {observation.agreement == null ? "unavailable" : String(observation.agreement)}</summary>
          <div className="mt-3 space-y-3 text-sm text-zinc-300">
            <p>Scope: {observation.source_scope}; scenario: {observation.scenario_id}; {observation.reason}</p>
            <p>Expected pass: {String(observation.expected_passed)}; actual native pass: {observation.result == null ? "unavailable" : String(observation.result.passed)};
              {" "}score: {observation.result?.score ?? "unavailable"}</p>
            <p>{observation.result?.feedback ?? "Native feedback unavailable."}</p>
            <Bindings bindings={observation.bindings} />
            <ul aria-label="Evidence references" className="space-y-1 break-all">{observation.evidence.map((reference, refIndex) =>
              <li key={refIndex}>{reference.origin.eval_id}/{reference.origin.task_id}/run {reference.origin.run_number}:
                {" "}{reference.artifact_id}{reference.pointer || ""}</li>,
            )}</ul>
          </div>
        </details>)}
      </section>)}
    </>}
  </div>;
}
