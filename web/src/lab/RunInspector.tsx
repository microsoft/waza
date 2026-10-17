import { useEffect, useRef, useState } from "react";
import { Check, ChevronRight, X } from "lucide-react";
import { duration, percent, shortDate, runScore, skillLabel, type LabRun } from "./data";
import { useLabDetails } from "./source";
import { formatAICredits } from "../lib/format";
import TrajectoryViewer from "../components/TrajectoryViewer";

const evidenceTabs = ["graders", "trajectory", "configuration"] as const;

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
  const [tab, setTab] = useState<"graders" | "trajectory" | "configuration">(
    "graders",
  );
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
                    : value === "trajectory"
                      ? "Trajectory"
                      : "Configuration"}
                </button>
              ),
            )}
          </div>
          <div id="lab-evidence-panel" role="tabpanel" aria-labelledby={`lab-evidence-tab-${tab}`}>
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
                                {invocation.command} attempt {index + 1} · exit {invocation.exit_code} · response index {invocation.response_index}
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
