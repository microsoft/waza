import type { EvidenceOrigin, TaskResult } from "../api/client";
import { canDisplayEvidence } from "../api/client";

function originLabel(origin: EvidenceOrigin) {
  return `eval ${origin.eval_id} · task ${origin.task_id} · run ${origin.run_number} · attempt ${origin.attempt_count} · prior attempts ${origin.prior_attempts}`;
}

function versionLabel(value: string | null) {
  return !value || value === "unknown" ? "unavailable" : value;
}

export default function EvidenceView({ tasks }: { tasks: TaskResult[] }) {
  return (
    <div className="space-y-4" data-testid="evidence-view">
      <p className="text-sm text-zinc-400">
        Recorded evidence is not verified enforcement or current external state.
        A digest is not file contents. Live replay is unsupported.
        {" "}Requirement checks describe existing grader observations, not requirement enforcement.
        This view does not fetch local paths, download artifacts, or upload evidence.
      </p>
      {tasks.map((task, taskIndex) => (
        <section key={`${task.name}-${taskIndex}`} className="rounded-lg border border-zinc-700 bg-zinc-800 p-4">
          <h2 className="font-medium text-zinc-100">{task.name}</h2>
          {!task.evidenceRuns?.length && (
            <p className="mt-2 text-sm text-zinc-400">
              Evidence unassessed: historical metadata is unavailable or capture was not requested.
            </p>
          )}
          {task.evidenceRuns?.map((run, index) => {
            const manifest = canDisplayEvidence(run) ? run.manifest : undefined;
            const withheld = !!run.manifest && !manifest;
            return (
            <div key={`${run.runNumber}-${index}`} className="mt-4 space-y-3 text-sm">
              <p className="text-zinc-300">
                Run {run.runNumber} · {run.attempts} attempts · {withheld ? "invalid" : run.assessment}
                {run.cached ? " · cached source evidence, not a new execution" : ""}
              </p>
              <p className="text-zinc-400">{withheld ? "Evidence metadata withheld: invalid metadata, assessment, or unsupported manifest version." : run.message}</p>
              {manifest && (
                <>
                  <p className="break-all text-zinc-400">
                    Manifest {manifest.version} SHA-256: {manifest.sha256}
                  </p>
                  {manifest.source_manifest_sha256 && <p className="break-all text-zinc-400">Original capture SHA-256: {manifest.source_manifest_sha256}</p>}
                  <p className="break-all text-zinc-300">Source origin: {originLabel(manifest.origin)}</p>
                  <p className="text-zinc-300">
                    Requested engine: {manifest.runtime.requested_engine} · Requested model: {manifest.runtime.requested_model}
                    {" · "}Actual execution mode: {manifest.runtime.execution_mode}
                    {" · "}Dependencies: {manifest.runtime.dependency_mode}
                    {" · "}Requested tool policy: {manifest.runtime.requested_policy}
                    {" · "}Verified enforcement: {manifest.runtime.verified_enforcement}
                  </p>
                  <p className="text-zinc-400">
                    Versions — waza: {versionLabel(manifest.runtime.waza_version)}
                    {" · "}SDK: {versionLabel(manifest.runtime.sdk_version)}
                    {" · "}Effective model: {versionLabel(manifest.runtime.effective_model_version)}
                    {" · "}Go: {versionLabel(manifest.runtime.go_version)}
                    {" · "}Platform: {versionLabel(manifest.runtime.platform)}
                  </p>
                  <p className="text-zinc-400">
                    Requested no-skills: {manifest.runtime.no_skills == null ? "unknown" : String(manifest.runtime.no_skills)}
                    {" · "}Native skill control: {manifest.runtime.native_skill_control || "unknown"}.
                    {" "}Requested control is not proof of SDK enforcement.
                  </p>
                  <p className="text-zinc-400">
                    Redaction: {manifest.redaction.policy} · {manifest.redaction.match_count} rule matches.
                    {" "}Rules: {manifest.redaction.applied_rules?.join(", ") || "none recorded"}.
                    {" "}{manifest.redaction.limitations.join(" ")}
                  </p>
                  <div className="overflow-x-auto">
                    <table className="w-full text-left">
                      <thead className="text-zinc-400"><tr><th className="p-2">Artifact</th><th className="p-2">Availability</th><th className="p-2">Completeness</th><th className="p-2">Digests / locator / limit</th></tr></thead>
                      <tbody>
                        {manifest.artifacts.map((artifact) => (
                          <tr key={artifact.id} className="border-t border-zinc-700 text-zinc-300">
                            <td className="p-2">{artifact.id}</td><td className="p-2">{artifact.availability}</td>
                            <td className="p-2">{artifact.completeness}</td>
                            <td className="break-all p-2">
                              {artifact.content_digest && <p>Content SHA-256 ({artifact.content_digest.encoding}): {artifact.content_digest.sha256}</p>}
                              {artifact.source_digest && <p>Source SHA-256 ({artifact.source_digest.encoding}): {artifact.source_digest.sha256} (no source bytes)</p>}
                              <p>Locator: {artifact.document ? `${artifact.document}${artifact.pointer ?? ""}` : "unavailable"}</p>
                              <p>{artifact.reason}{artifact.redacted ? " Changed by redaction or normalization; this flag is not a secret-match count." : ""}</p>
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                  {manifest.diagnostics?.map((diagnostic, i) => (
                    <p key={i} className="text-yellow-400">{diagnostic.category} / {diagnostic.code}: {diagnostic.message}</p>
                  ))}
                </>
              )}
              {!withheld && run.assessment !== "invalid" && run.explanations?.map((requirement) => (
                <div key={`${requirement.task_id}-${requirement.requirement_id}`} className="rounded border border-zinc-700 p-3">
                  <h3 className="text-zinc-100">Requirement {requirement.requirement_id}</h3>
                  {requirement.checks.map((check, i) => (
                    <div key={i} className="mt-2 text-zinc-400">
                      <p>{check.check.scope || "uncovered"} / {check.check.grader || "no check"}{check.check.after_turn ? ` / turn ${check.check.after_turn}` : ""}: {check.observation} · {check.category}</p>
                      <p>{check.message}</p>
                      {check.references?.map((reference, j) => (
                        <p key={j} className="break-all text-xs">Evidence: {reference.artifact_id}{reference.pointer ?? ""} · {originLabel(reference.origin)}</p>
                      ))}
                    </div>
                  ))}
                </div>
              ))}
            </div>
            );
          })}
        </section>
      ))}
    </div>
  );
}
