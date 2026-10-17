import type { EvidenceOrigin, PromptJudgeUsage, TaskResult } from "../api/client";
import { canDisplayEvidence, canDisplayPromptJudging } from "../api/client";
import { downloadText } from "../lib/export";

function originLabel(origin: EvidenceOrigin) {
  return `eval ${origin.eval_id || "unavailable"} · task ${origin.task_id || "unavailable"} · run ${origin.run_number || "unavailable"} · attempt ${origin.attempt_count || "unavailable"} · prior attempts ${origin.prior_attempts}`;
}

function versionLabel(value: string | null) {
  return !value || value === "unknown" ? "unavailable" : value;
}

export default function EvidenceView({ tasks, regradeUsage }: { tasks: TaskResult[]; regradeUsage?: PromptJudgeUsage }) {
  return (
    <div className="space-y-4" data-testid="evidence-view">
      <p className="text-sm text-zinc-400">
        Recorded evidence is not verified enforcement or current external state.
        A digest is not file contents. Live replay is unsupported.
        {" "}Requirement checks describe existing grader observations, not requirement enforcement.
        This view does not fetch local paths, download artifacts, or upload evidence.
      </p>
      {regradeUsage && <p className="text-sm text-zinc-400">Current regrade AI credits: {regradeUsage.aiCredits !== undefined ? String(regradeUsage.aiCredits) : "unavailable"}. Original execution billing is unchanged; regrade usage is separate.</p>}
      {tasks.map((task, taskIndex) => {
        const judgingRuns = Array.isArray(task.promptJudgingRuns) ? task.promptJudgingRuns : [];
        const malformedJudging = task.promptJudgingRuns !== undefined && !Array.isArray(task.promptJudgingRuns);
        return (
        <section key={`${task.name}-${taskIndex}`} className="rounded-lg border border-zinc-700 bg-zinc-800 p-4">
          <h2 className="font-medium text-zinc-100">{task.name}</h2>
          <div className="mt-3 space-y-3" data-testid="prompt-judging">
            <h3 className="text-zinc-100">Prompt judging</h3>
            <p className="text-sm text-zinc-400">
              Frozen inline evidence; no actor-session continuation or live workspace access.
              {" "}Submission is observed only from recorded grade callbacks. Inspection is not observable.
              {" "}Digests check supplied bytes, not authenticity, unbiased judgment, or evidence truth.
            </p>
            {malformedJudging && <p className="text-sm text-yellow-400">Prompt judging metadata withheld: invalid or unsupported metadata.</p>}
            {!malformedJudging && !judgingRuns.length && <p className="text-sm text-zinc-400">Prompt judging unassessed: no frozen-judging metadata recorded.</p>}
            {judgingRuns.map((run, index) => {
              if (!canDisplayPromptJudging(run)) return <p key={index} className="text-sm text-yellow-400">Prompt judging metadata withheld: invalid or unsupported metadata.</p>;
              return <div key={`${run.runNumber}-${index}`} className="space-y-2 rounded border border-zinc-700 p-3 text-sm">
                <p>Run {run.runNumber} · attempt {run.attempts} · {run.assessment}{run.cached ? " · cached source, not new judging" : ""}</p>
                <p className="text-zinc-400">{run.message}</p>
                {run.bundleSHA256 && <p className="break-all">Frozen bundle SHA-256: {run.bundleSHA256} · execution {run.executionMode}</p>}
                {run.limits && <p>Limits: {run.limits.max_files} files · {run.limits.max_file_bytes} bytes/file · {run.limits.max_bundle_bytes} bytes/bundle</p>}
                {run.coverage?.map((section) => <p key={section.section}>{section.section}: {section.availability} / {section.completeness}{section.count !== undefined ? ` · ${section.count} items` : ""}</p>)}
                {!run.observations.length && <p>Judge submission unavailable; no bound judge observation.</p>}
                {run.observations.map((observation, observationIndex) => <div key={observationIndex} className="space-y-1 border-t border-zinc-700 pt-2">
                  <p>{observation.grader} · {observation.purpose} · {observation.state} · submission {observation.submissionObserved ? "observed" : "not observed"} · inspection not observable</p>
                  <p>Requested judge model: {observation.model ?? "unavailable"} · session: {observation.sessionID ?? "unavailable"} · {observation.durationMs} ms · {observation.fileCount} selected files</p>
                  <p>Judge AI credits: {observation.usage?.aiCredits !== undefined ? String(observation.usage.aiCredits) : "unavailable"}{observation.usage ? ` · ${observation.usage.inputTokens} input / ${observation.usage.outputTokens} output tokens` : " · usage unavailable"}</p>
                  {observation.deliverySHA256 && <p className="break-all">Delivery SHA-256: {observation.deliverySHA256}</p>}
                  {observation.diagnostics?.map((code, codeIndex) => <p key={codeIndex} className="text-yellow-400">{code}</p>)}
                </div>)}
              </div>;
            })}
            {!!judgingRuns.length && <button type="button" className="rounded border border-zinc-600 px-3 py-1 text-sm" onClick={() => {
              const runs = judgingRuns.filter(canDisplayPromptJudging);
              downloadText(JSON.stringify({ version: "1.0", taskID: task.id, promptJudgingRuns: runs, withheldRunCount: judgingRuns.length - runs.length }, null, 2), "waza-prompt-judging.json", "application/json");
            }}>Export prompt judging metadata</button>}
          </div>
          {!task.evidenceRuns?.length && (
            <p className="mt-2 text-sm text-zinc-400">
              Evidence unassessed: historical metadata is unavailable or capture was not requested.
            </p>
          )}
          {task.evidenceRuns?.map((run, index) => {
            const manifest = canDisplayEvidence(run) ? run.manifest : undefined;
            const withheld = (run.assessment === "recorded" || !!run.manifest) && !manifest;
            return (
            <div key={`${run.runNumber}-${index}`} className="mt-4 space-y-3 text-sm">
              <p className="text-zinc-300">
                Run {run.runNumber} · {run.attempts} {run.attempts === 1 ? "attempt" : "attempts"} · {withheld ? "invalid" : run.assessment}
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
                    Redaction: {manifest.redaction.policy} · {manifest.redaction.match_count} rule {manifest.redaction.match_count === 1 ? "match" : "matches"}.
                    {" "}Rules: {manifest.redaction.applied_rules?.join(", ") || "none recorded"}.
                    {" "}{manifest.redaction.limitations?.join(" ") || "No limitations recorded; this is not proof of secret absence."}
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
                  {requirement.checks?.length ? requirement.checks.map((check, i) => (
                    <div key={i} className="mt-2 text-zinc-400">
                      <p>{check.check.scope || "uncovered"} / {check.check.grader || "no check"}{check.check.after_turn ? ` / turn ${check.check.after_turn}` : ""}: {check.observation} · {check.category}</p>
                      <p>{check.message}</p>
                      {check.references?.map((reference, j) => (
                        <p key={j} className="break-all text-xs">Evidence: {reference.artifact_id} · artifact metadata only; value pointer unverified · {originLabel(reference.origin)}</p>
                      ))}
                    </div>
                  )) : (
                    <p className="mt-2 text-zinc-400">
                      Check observations unavailable; requirement satisfaction is unassessed.
                    </p>
                  )}
                </div>
              ))}
            </div>
            );
          })}
        </section>
        );
      })}
    </div>
  );
}
