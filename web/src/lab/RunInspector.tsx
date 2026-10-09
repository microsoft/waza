import { useEffect, useRef, useState } from "react";
import { Check, ChevronRight, X } from "lucide-react";
import { duration, percent, shortDate, runScore, type LabRun } from "./data";
import { useLabDetails } from "./source";
import { formatAICredits } from "../lib/format";
import TrajectoryViewer from "../components/TrajectoryViewer";

export default function RunInspector({
  run: summary,
  demo,
  initialTask,
  onClose,
}: {
  run: LabRun;
  demo: boolean;
  initialTask?: string;
  onClose: () => void;
}) {
  const query = useLabDetails([summary.id], demo)[0];
  const run = query?.data ?? summary;
  const dialog = useRef<HTMLDialogElement>(null);
  const [taskId, setTaskId] = useState(
    initialTask ??
      run.tasks.find((task) => task.outcome === "failed")?.id ??
      run.tasks[0]?.id,
  );
  const [tab, setTab] = useState<"graders" | "trajectory" | "configuration">(
    "graders",
  );
  const activeTaskId =
    taskId ??
    initialTask ??
    run.tasks.find((task) => task.outcome === "failed")?.id ??
    run.tasks[0]?.id;
  const task = run.tasks.find((task) => task.id === activeTaskId);

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
          "button:not([disabled]), select:not([disabled])",
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
          <h2 id="lab-inspector-title">{run.skill}</h2>
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
          digest, and trajectory describe the first repetition; they are not a
          complete repetition-level export.
        </p>
      )}
      {!demo && query?.isPending ? (
        <p role="status" className="lab-note">
          Loading task evidence...
        </p>
      ) : !demo && query?.isError ? (
        <div role="alert" className="lab-note">
          <p>Run evidence could not be loaded: {query.error.message}</p>
          <button className="lab-button" onClick={() => void query.refetch()}>
            Retry evidence
          </button>
        </div>
      ) : (
        <>
          <label className="lab-field">
            Task
            <select
              value={activeTaskId}
              onChange={(event) => setTaskId(event.target.value)}
            >
              {run.tasks.map((item) => (
                <option key={item.id} value={item.id}>
                  {item.outcome.toUpperCase()} · {item.name}
                </option>
              ))}
            </select>
          </label>
          <div className="lab-tabs" role="tablist" aria-label="Task evidence">
            {(["graders", "trajectory", "configuration"] as const).map(
              (value) => (
                <button
                  key={value}
                  role="tab"
                  aria-selected={tab === value}
                  aria-controls="lab-evidence-panel"
                  onClick={() => setTab(value)}
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
          <div id="lab-evidence-panel" role="tabpanel" aria-label={tab}>
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
                          Follow the {run.skill} skill to complete:{" "}
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
                                path: `skills/${run.skill}/SKILL.md`,
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
                  <div className="lab-recorded-trajectory">
                    <TrajectoryViewer task={task.api} />
                  </div>
                )
              ))}
            {tab === "configuration" && (
              <>
                <p className="lab-note">
                  {demo
                    ? "Illustrative configuration. Prototype identities and aggregates are not a drop-in mapping to the current results API."
                    : "Only recorded metadata is shown. Git revision, skill-routing flags, fixture isolation, and complete eval configuration are not supplied by this API."}
                </p>
                <pre>
                  {JSON.stringify(
                    {
                      eval: run.evalId,
                      skill: demo
                        ? run.skill
                        : (run.summary?.skill ?? "Unavailable"),
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
