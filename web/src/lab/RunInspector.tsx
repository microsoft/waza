import { useEffect, useRef, useState } from "react";
import { Check, ChevronRight, X } from "lucide-react";
import { duration, percent, shortDate, runScore, skillLabel, type LabRun } from "./data";
import { useLabDetails } from "./source";
import { formatAICredits } from "../lib/format";
import { canDisplayEvidence, canDisplayPromptJudging } from "../api/client";
import TrajectoryViewer from "../components/TrajectoryViewer";
import ConversationView from "./ConversationView";

const evidenceTabs = ["graders", "conversation", "trajectory", "configuration", "evidence"] as const;

function isProbability(value: unknown): value is number {
  return typeof value === "number" && Number.isFinite(value) && value >= 0 && value <= 1;
}

function intervalLabel(value: unknown) {
  if (value === undefined) return "Not assessed";
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return "Withheld: invalid recorded interval";
  }
  const interval = value as Record<string, unknown>;
  if (
    !isProbability(interval.lower) || !isProbability(interval.upper) ||
    !isProbability(interval.mean) || !isProbability(interval.confidenceLevel) ||
    interval.confidenceLevel === 0 || interval.confidenceLevel === 1 ||
    interval.lower > interval.upper
  ) {
    return "Withheld: invalid recorded interval";
  }
  return `${percent(interval.mean * 100)} · ${(interval.confidenceLevel * 100).toFixed(0)}% CI ${percent(interval.lower * 100)}–${percent(interval.upper * 100)}`;
}

function passCurveLabel(value: unknown) {
  if (value === undefined || (Array.isArray(value) && value.length === 0)) {
    return "Not assessed";
  }
  if (!Array.isArray(value) || !value.every(isProbability)) {
    return "Withheld: invalid recorded pass^k estimates";
  }
  return value.map((probability, index) => `pass^${index + 1} ${percent(probability * 100)}`).join(" · ");
}

function trialCountsLabel(value: unknown) {
  if (value === undefined) return "Not assessed";
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return "Withheld: invalid recorded trial counts";
  }
  const counts = value as Record<string, unknown>;
  if (
    typeof counts.total !== "number" || !Number.isSafeInteger(counts.total) || counts.total <= 0 ||
    typeof counts.passed !== "number" || !Number.isSafeInteger(counts.passed) ||
    counts.passed < 0 || counts.passed > counts.total
  ) {
    return "Withheld: invalid recorded trial counts";
  }
  return `${counts.passed}/${counts.total} passed`;
}

function validRequirementExplanations(value: unknown): boolean {
  if (!Array.isArray(value)) return false;
  return value.every((requirement) => {
    if (!requirement || typeof requirement !== "object" || Array.isArray(requirement)) {
      return false;
    }
    const item = requirement as Record<string, unknown>;
    if (typeof item.task_id !== "string" || typeof item.requirement_id !== "string") {
      return false;
    }
    if (item.checks === undefined || item.checks === null) return true;
    if (!Array.isArray(item.checks)) return false;
    return item.checks.every((entry) => {
      if (!entry || typeof entry !== "object" || Array.isArray(entry)) return false;
      const check = entry as Record<string, unknown>;
      if (!check.check || typeof check.check !== "object" || Array.isArray(check.check)) {
        return false;
      }
      const declaration = check.check as Record<string, unknown>;
      const references = check.references;
      return typeof declaration.scope === "string" &&
        typeof declaration.grader === "string" &&
        (declaration.after_turn === undefined ||
          (typeof declaration.after_turn === "number" &&
            Number.isSafeInteger(declaration.after_turn))) &&
        typeof check.observation === "string" &&
        typeof check.category === "string" &&
        typeof check.message === "string" &&
        (references === undefined ||
          (Array.isArray(references) &&
            references.every((reference) =>
              !!reference &&
              typeof reference === "object" &&
              !Array.isArray(reference) &&
              typeof (reference as Record<string, unknown>).artifact_id === "string",
            )));
    });
  });
}

function RecordedEvidence({ run, task }: { run: LabRun; task?: LabRun["tasks"][number] }) {
  const taskResult = task?.api;
  const rawEvidenceRuns = taskResult?.evidenceRuns;
  const malformedEvidenceRuns =
    rawEvidenceRuns !== undefined && !Array.isArray(rawEvidenceRuns);
  const evidenceRuns = Array.isArray(rawEvidenceRuns) ? rawEvidenceRuns : [];
  const rawJudgingRuns = taskResult?.promptJudgingRuns;
  const malformedJudgingRuns =
    rawJudgingRuns !== undefined && !Array.isArray(rawJudgingRuns);
  const judgingRuns = Array.isArray(rawJudgingRuns) ? rawJudgingRuns : [];
  const successRateCI = run.api?.statistics?.successRateCI;
  const passHatK = run.api?.statistics?.passHatK;

  return (
    <div className="lab-recorded-evidence">
      <p className="lab-note">
        These are saved metadata and grader observations, not verified external
        state or requirement enforcement. Evidence contents are not fetched.
        Missing historical fields remain unassessed.
      </p>
      <article className="lab-grader">
        <h3>Recorded reliability</h3>
        <p>
          Task trials:{" "}
          {trialCountsLabel(taskResult?.trials)}
        </p>
        <p>Task pass-rate Wilson interval: {intervalLabel(taskResult?.passRateCI)}</p>
        <p>Weighted-score bootstrap interval: {intervalLabel(taskResult?.bootstrapCI)}</p>
        <p>Suite success-rate Wilson interval: {intervalLabel(successRateCI)}</p>
        <p>
          pass^k (all k trials pass):{" "}
          {passCurveLabel(passHatK)}
        </p>
        <small>
          Stored estimates are not reconstructed from task scores, are not pass@k
          guarantees, and are not significance tests between runs.
        </small>
      </article>

      <section className="lab-grader">
        <h3>Portable evidence</h3>
        {malformedEvidenceRuns && (
          <p>Portable evidence metadata withheld: invalid or unsupported run list.</p>
        )}
        {!malformedEvidenceRuns && !evidenceRuns.length && (
          <p>Evidence unassessed: no portable evidence metadata was recorded.</p>
        )}
        {evidenceRuns.map((evidence, index) => {
          if (
            !evidence || typeof evidence !== "object" || Array.isArray(evidence) ||
            !Number.isSafeInteger(evidence.runNumber) || evidence.runNumber < 0 ||
            !Number.isSafeInteger(evidence.attempts) || evidence.attempts < 0 ||
            typeof evidence.cached !== "boolean" ||
            !["recorded", "unavailable", "invalid", "unassessed"].includes(evidence.assessment) ||
            (evidence.message !== undefined && typeof evidence.message !== "string")
          ) {
            return (
              <div key={`invalid-${index}`} className="lab-evidence-record">
                <strong>Invalid · withheld</strong>
                <p>Evidence metadata withheld: invalid or unsupported run record.</p>
              </div>
            );
          }
          const manifest = canDisplayEvidence(evidence) ? evidence.manifest : undefined;
          const withheld =
            (evidence.assessment === "recorded" || evidence.manifest != null) &&
            !manifest;
          const malformedExplanations =
            evidence.explanations !== undefined &&
            !validRequirementExplanations(evidence.explanations);
          return (
            <div key={`${evidence.runNumber}-${index}`} className="lab-evidence-record">
              <strong>
                Run {evidence.runNumber} · {evidence.attempts}{" "}
                {evidence.attempts === 1 ? "attempt" : "attempts"} ·{" "}
                {withheld ? "invalid · withheld" : evidence.assessment}
                {evidence.cached ? " · cached source evidence" : ""}
              </strong>
              <p>
                {withheld
                  ? "Evidence metadata withheld: invalid identity, digest, or unsupported manifest."
                  : evidence.message}
              </p>
              {manifest && (
                <>
                  <p className="break-all">
                    Source origin: eval {manifest.origin.eval_id || "unavailable"} ·
                    task {manifest.origin.task_id || "unavailable"} · run{" "}
                    {manifest.origin.run_number || "unavailable"} · attempt{" "}
                    {manifest.origin.attempt_count || "unavailable"} · prior attempts{" "}
                    {manifest.origin.prior_attempts}
                  </p>
                  <p>
                    Requested engine/model: {manifest.runtime.requested_engine} /{" "}
                    {manifest.runtime.requested_model} · execution{" "}
                    {manifest.runtime.execution_mode} · requested policy{" "}
                    {manifest.runtime.requested_policy} · verified enforcement{" "}
                    {manifest.runtime.verified_enforcement}
                  </p>
                  <p>
                    Manifest SHA-256: <code>{manifest.sha256}</code>
                    {manifest.source_manifest_sha256 && (
                      <> · original capture SHA-256: <code>{manifest.source_manifest_sha256}</code></>
                    )}
                  </p>
                  {manifest.artifacts.map((artifact) => (
                    <p key={artifact.id}>
                      {artifact.id}: {artifact.availability} / {artifact.completeness}
                      {artifact.reason ? ` · ${artifact.reason}` : ""}
                      {artifact.source_digest &&
                        ` · source digest only (no source bytes): ${artifact.source_digest.sha256}`}
                      {artifact.content_digest &&
                        ` · content digest (${artifact.content_digest.encoding}): ${artifact.content_digest.sha256}`}
                    </p>
                  ))}
                </>
              )}
              {malformedExplanations && (
                <p>Requirement observations withheld: invalid or unsupported metadata.</p>
              )}
              {!withheld &&
                !malformedExplanations &&
                evidence.assessment !== "invalid" &&
                evidence.explanations?.map((requirement) => (
                  <div key={`${requirement.task_id}-${requirement.requirement_id}`}>
                    <strong>Requirement {requirement.requirement_id}</strong>
                    {requirement.checks?.length ? (
                      requirement.checks.map((check, checkIndex) => (
                        <p key={checkIndex}>
                          {check.check.scope || "uncovered"} /{" "}
                          {check.check.grader || "no check"}
                          {check.check.after_turn
                            ? ` / turn ${check.check.after_turn}`
                            : ""}
                          : {check.observation} · {check.category} — {check.message}
                          {check.references?.length
                            ? ` · artifact metadata: ${check.references.map((reference) => reference.artifact_id).join(", ")}`
                            : ""}
                        </p>
                      ))
                    ) : (
                      <p>Check observations unavailable; requirement satisfaction is unassessed.</p>
                    )}
                  </div>
                ))}
            </div>
          );
        })}
      </section>

      <section className="lab-grader">
        <h3>Independent prompt judging</h3>
        <p>
          Submission callbacks are observable; whether a model inspected every
          evidence item is not.
          {run.api?.regradeUsage &&
            ` Current regrade usage is separate from original execution billing; AI Credits ${
              run.api.regradeUsage.aiCredits === undefined
                ? "unavailable"
                : formatAICredits(run.api.regradeUsage.aiCredits)
            }.`}
        </p>
        {malformedJudgingRuns && (
          <p>Prompt judging metadata withheld: invalid or unsupported run list.</p>
        )}
        {!malformedJudgingRuns && !judgingRuns.length && (
          <p>Prompt judging unassessed: no frozen-judging metadata was recorded.</p>
        )}
        {judgingRuns.map((judging, index) => {
          const valid = canDisplayPromptJudging(judging);
          if (!valid) {
            return (
              <div key={`invalid-${index}`} className="lab-evidence-record">
                <strong>Invalid · withheld</strong>
                <p>Prompt judging metadata withheld: invalid or unsupported metadata.</p>
              </div>
            );
          }
          return (
            <div key={`${judging.runNumber}-${index}`} className="lab-evidence-record">
              <strong>
                Run {judging.runNumber} · attempt {judging.attempts} ·{" "}
                {valid ? judging.assessment : "invalid · withheld"}
                {judging.cached ? " · cached source, not new judging" : ""}
              </strong>
              <p>
                {valid
                  ? judging.message
                  : "Prompt judging metadata withheld: invalid or unsupported metadata."}
              </p>
              {valid && judging.bundleSHA256 && (
                <>
                  <p className="break-all">
                    Frozen bundle SHA-256: <code>{judging.bundleSHA256}</code> · execution{" "}
                    {judging.executionMode}
                  </p>
                  {judging.coverage?.map((section) => (
                    <p key={section.section}>
                      {section.section}: {section.availability} / {section.completeness}
                      {section.count !== undefined ? ` · ${section.count} items` : ""}
                    </p>
                  ))}
                </>
              )}
              {valid &&
                judging.observations.map((observation, observationIndex) => (
                  <p key={observationIndex}>
                    {observation.grader} · {observation.purpose} · {observation.state} ·
                    submission {observation.submissionObserved ? "observed" : "not observed"} ·
                    inspection not observable · judge AI Credits{" "}
                    {observation.usage?.aiCredits === undefined
                      ? "unavailable"
                      : formatAICredits(observation.usage.aiCredits)}
                  </p>
                ))}
            </div>
          );
        })}
      </section>
    </div>
  );
}

export default function RunInspector({
  run: summary,
  demo,
  initialTask,
  snapshot = false,
  onClose,
}: {
  run: LabRun;
  demo: boolean;
  initialTask?: string;
  snapshot?: boolean;
  onClose: () => void;
}) {
  const query = useLabDetails([summary.id], demo, snapshot)[0];
  const run = snapshot ? summary : query?.data ?? summary;
  const dialog = useRef<HTMLDialogElement>(null);
  const [taskId, setTaskId] = useState(
    initialTask ??
      run.tasks.find((task) => task.outcome === "failed")?.id ??
      run.tasks[0]?.id,
  );
  const [resolvedTasks, setResolvedTasks] = useState<LabRun["tasks"]>();
  const [tab, setTab] = useState<(typeof evidenceTabs)[number]>("graders");
  const task =
    run.tasks.find((task) => task.id === taskId) ??
    run.tasks.find((task) => task.outcome === "failed") ??
    run.tasks[0];
  const activeTaskId = task?.id;
  // Persist a fallback only once full evidence, including an empty task set, exists.
  if ((demo || run.api !== undefined) && resolvedTasks !== run.tasks) {
    setResolvedTasks(run.tasks);
    if (taskId !== activeTaskId) setTaskId(activeTaskId);
  }

  useEffect(() => {
    const element = dialog.current;
    const previous = document.activeElement;
    element?.showModal();
    return () => {
      element?.close();
      if (previous instanceof HTMLElement) previous.focus();
    };
  }, []);

  return (
    <dialog
      ref={dialog}
      className="lab-inspector"
      aria-labelledby="lab-inspector-title"
      onCancel={onClose}
      onKeyDown={(event) => {
        if (event.key !== "Tab") return;
        const controls = event.currentTarget.querySelectorAll<HTMLElement>(
          "button:not([disabled]):not([tabindex='-1']), select:not([disabled])",
        );
        const first = controls[0];
        const last = controls[controls.length - 1];
        if (event.shiftKey && document.activeElement === first && last) {
          event.preventDefault();
          last.focus();
        } else if (
          !event.shiftKey &&
          document.activeElement === last &&
          first
        ) {
          event.preventDefault();
          first.focus();
        }
      }}
    >
      <div className="lab-inspector-heading">
        <div>
          <p className="lab-eyebrow">
            RUN INSPECTOR ·{" "}
            {demo ? "SYNTHETIC EVIDENCE" : "SAVED RESULT EVIDENCE"}
          </p>
          <h2 id="lab-inspector-title">{skillLabel(run.skill)}</h2>
          <p>
            {run.model} · {shortDate(run.date)} · <code>{run.revision}</code>
          </p>
        </div>
        <button
          className="lab-icon-button"
          aria-label="Close run inspector"
          onClick={onClose}
        >
          <X size={20} />
        </button>
      </div>
      <div className="lab-inspector-stats">
        <span>
          Weighted score<strong>{percent(runScore(run))}</strong>
        </span>
        <span>
          Judge<strong>{run.judge}</strong>
        </span>
        <span>
          AI Credits
          <strong>
            {run.aiCredits === undefined
              ? "Unavailable"
              : formatAICredits(run.aiCredits)}
          </strong>
        </span>
      </div>
      {!demo && (
        <p className="lab-note">
          Task scores aggregate repetitions. Grader evidence, prompt, session
          digest, dependency modes, CLI attempts, and trajectory describe the
          first repetition; they are not a complete repetition-level export.
        </p>
      )}
      {!demo && !snapshot && query?.isPending ? (
        <p role="status" className="lab-note">
          Loading task evidence...
        </p>
      ) : !demo && !snapshot && query?.isError && !query.data ? (
        <div role="alert" className="lab-note">
          <p>Run evidence could not be loaded: {query.error.message}</p>
          <button className="lab-button" onClick={() => void query.refetch()}>
            Retry evidence
          </button>
        </div>
      ) : (
        <>
          {!demo && query?.isError && (snapshot || query.data) && (
            <p role="alert" className="lab-note">
              Evidence refresh failed: {query.error.message}. Previously loaded
              evidence remains visible.
            </p>
          )}
          <label className="lab-field">
            Task
            <select
              value={activeTaskId ?? ""}
              disabled={run.tasks.length === 0}
              onChange={(event) => setTaskId(event.target.value)}
            >
              {run.tasks.map((item) => (
                <option key={item.id} value={item.id}>
                  {item.outcome.toUpperCase()} · {item.name}
                </option>
              ))}
            </select>
          </label>
          {run.tasks.length === 0 && (
            <p className="lab-note">No recorded tasks in this result.</p>
          )}
          <div className="lab-tabs" role="tablist" aria-label="Task evidence">
            {evidenceTabs.map(
              (value, index) => (
                <button
                  key={value}
                  id={`lab-evidence-tab-${value}`}
                  role="tab"
                  tabIndex={tab === value ? 0 : -1}
                  aria-selected={tab === value}
                  aria-controls="lab-evidence-panel"
                  onClick={() => setTab(value)}
                  onKeyDown={(event) => {
                    let next: number;
                    switch (event.key) {
                      case "ArrowRight":
                        next = (index + 1) % evidenceTabs.length;
                        break;
                      case "ArrowLeft":
                        next = (index + evidenceTabs.length - 1) % evidenceTabs.length;
                        break;
                      case "Home":
                        next = 0;
                        break;
                      case "End":
                        next = evidenceTabs.length - 1;
                        break;
                      default:
                        return;
                    }
                    event.preventDefault();
                    const selected = evidenceTabs[next]!;
                    setTab(selected);
                    dialog.current
                      ?.querySelector<HTMLButtonElement>(`#lab-evidence-tab-${selected}`)
                      ?.focus();
                  }}
                >
                  {value === "graders"
                    ? "Grader evidence"
                    : value === "conversation"
                      ? "Conversation"
                      : value === "trajectory"
                        ? "Trajectory"
                        : value === "configuration"
                          ? "Configuration"
                          : "Recorded evidence"}
                </button>
              ),
            )}
          </div>
          <div id="lab-evidence-panel" role="tabpanel" aria-labelledby={`lab-evidence-tab-${tab}`}>
            {tab === "conversation" &&
              (demo ? (
                <p className="lab-note">
                  The synthetic demo has no recorded conversation. Open a saved
                  result in the real-results Lab to review user and agent messages.
                </p>
              ) : (
                <ConversationView task={task?.api} />
              ))}
            {tab === "evidence" &&
              (demo ? (
                <p className="lab-note">
                  The synthetic demo has no recorded reliability, portable
                  evidence, or prompt-judging metadata.
                </p>
              ) : (
                <RecordedEvidence run={run} task={task} />
              ))}
            {tab === "graders" && task && (
              <>
                <div className="lab-task-summary">
                  <h3>{task.name}</h3>
                  <span className={`lab-status ${task.outcome}`}>
                    {task.outcome}
                  </span>
                  <p>
                    Weighted score{" "}
                    {percent(task.score === null ? null : task.score * 100)} ·{" "}
                    {duration(task.duration)} ·{" "}
                    {task.tokens?.toLocaleString() ?? "Unavailable"} tokens
                  </p>
                </div>
                {task.graders.map((grader) => (
                  <article key={grader.name} className="lab-grader">
                    <div>
                      <strong>{grader.name}</strong>
                      <span
                        className={`lab-status ${grader.passed ? "passed" : "failed"}`}
                      >
                        {grader.passed ? <Check size={13} /> : <X size={13} />}
                        {percent(grader.score * 100)}
                      </span>
                    </div>
                    <small>
                      {grader.type} · weight {grader.weight ?? "Unavailable"}
                    </small>
                    <p>{grader.evidence}</p>
                  </article>
                ))}
              </>
            )}
            {tab === "trajectory" &&
              task &&
              (demo ? (
                <>
                  <p className="lab-note">
                    Illustrative task trace, not a recorded Copilot session.
                    Timing is distributed across the synthetic task duration.
                  </p>
                  <div className="lab-waterfall">
                    {[
                      "Read skill context",
                      "Inspect workspace",
                      "Execute tool",
                      "Compose response",
                      "Run graders",
                    ].map((label, index) => (
                      <div key={label}>
                        <span>{label}</span>
                        <div>
                          <i
                            className={
                              index === 2 && task.outcome === "failed"
                                ? "failed"
                                : ""
                            }
                            style={{
                              marginLeft: `${index * 17}%`,
                              width: "17%",
                            }}
                          />
                        </div>
                        <small>{(task.duration / 5).toFixed(1)}s</small>
                      </div>
                    ))}
                  </div>
                  <ol className="lab-events">
                    <li>
                      <ChevronRight size={15} />
                      <div>
                        <strong>User prompt</strong>
                        <p>
                          Follow the {skillLabel(run.skill)} skill to complete:{" "}
                          {task.name.toLowerCase()}. Verify the result and
                          explain the outcome.
                        </p>
                      </div>
                    </li>
                    <li>
                      <ChevronRight size={15} />
                      <div>
                        <strong>Tool call</strong>
                        <pre>
                          {JSON.stringify(
                            {
                              tool: "read_file",
                              arguments: {
                                path: run.skill
                                  ? `skills/${run.skill}/SKILL.md`
                                  : undefined,
                              },
                              success: true,
                            },
                            null,
                            2,
                          )}
                        </pre>
                      </div>
                    </li>
                    <li>
                      <ChevronRight size={15} />
                      <div>
                        <strong>
                          {task.outcome === "failed"
                            ? "Validation / policy failure"
                            : "Validation complete"}
                        </strong>
                        <p>
                          {task.graders.find((grader) => !grader.passed)
                            ?.evidence ??
                            "The expected artifacts and tool calls were verified."}
                        </p>
                      </div>
                    </li>
                  </ol>
                </>
              ) : (
                task.api && (
                  <>
                    {task.api.commandInvocations?.length ? (
                      <ol className="lab-events" aria-label="Mocked CLI attempts">
                        {task.api.commandInvocations.map((invocation, index) => (
                          <li key={`${invocation.command}-${index}`}>
                            <ChevronRight size={15} />
                            <div>
                              <strong>
                                {invocation.command} attempt {index + 1} · exit {invocation.exit_code ?? 'unavailable'} · response index {invocation.response_index}
                              </strong>
                              <pre>{JSON.stringify(invocation.args ?? [], null, 2)}</pre>
                            </div>
                          </li>
                        ))}
                      </ol>
                    ) : null}
                    <div className="lab-recorded-trajectory">
                      <TrajectoryViewer task={task.api} />
                    </div>
                  </>
                )
              ))}
            {tab === "configuration" && (
              <>
                <p className="lab-note">
                  {demo
                    ? "Illustrative configuration. Prototype identities and aggregates are not a drop-in mapping to the current results API."
                    : "Dependency modes reflect declared configuration, not proof of use. Mocked interactions test agent behavior against fixtures, not a real CLI or MCP server. Git revision, skill-routing flags, fixture isolation, and complete eval configuration are not supplied by this API."}
                </p>
                <pre>
                  {JSON.stringify(
                    {
                      eval: run.evalId,
                      skill: run.skill,
                      revision: run.revision,
                      model: run.model,
                      judge_model: run.judge,
                      repetitions: run.repetitions,
                      ...(demo
                        ? {
                            inject_skill_body: false,
                            trigger_skill_routing: true,
                            fixture_isolation: true,
                          }
                        : {}),
                      graders: task?.graders.map((grader) => ({
                        name: grader.name,
                        type: grader.type,
                        weight: grader.weight,
                      })),
                      source: run.source,
                      ...(!demo
                        ? {
                            dependency_modes: task?.api?.dependencyModes,
                            prompt: task?.api?.prompt,
                            responder: task?.api?.responder,
                            model_usage: run.api?.modelUsage,
                          }
                        : {}),
                    },
                    null,
                    2,
                  )}
                </pre>
              </>
            )}
          </div>
        </>
      )}
    </dialog>
  );
}
