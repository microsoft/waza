import { useEffect, useState, type ReactNode } from "react";
import {
  Activity,
  ArrowDownRight,
  ArrowLeft,
  ArrowRight,
  BookOpen,
  Check,
  ChevronRight,
  CircleHelp,
  Clock3,
  Download,
  FileBarChart2,
  FlaskConical,
  GitCompareArrows,
  History,
  Layers3,
  LayoutDashboard,
  Pause,
  Play,
  Radio,
  RotateCcw,
  Search,
  ShieldCheck,
  SlidersHorizontal,
  Sparkles,
  X,
} from "lucide-react";
import { downloadText, toCSV } from "../lib/export";
import { formatAICredits } from "../lib/format";
import { reportDetails, useLabSource, useLabDetails } from "./source";
import RunActivity from "./RunActivity";
import { ModelChart, QualityChart } from "./Charts";
import RunInspector from "./RunInspector";
import {
  alignTasks,
  duration,
  filterRuns,
  isFailedRun,
  models,
  passRate,
  percent,
  referenceDate,
  runScore,
  shortDate,
  skills,
  summarize,
  type Filters,
  type LabRun,
  type Model,
  type SkillId,
} from "./data";
import "./lab.css";

const sections = [
  { id: "overview", label: "Overview", icon: LayoutDashboard },
  { id: "current", label: "Current runs", icon: Radio },
  { id: "history", label: "Run history", icon: History },
  { id: "compare", label: "Compare", icon: GitCompareArrows },
  { id: "reports", label: "Reports", icon: FileBarChart2 },
  { id: "catalog", label: "Skills & evals", icon: Layers3 },
] as const;
type Section = (typeof sections)[number]["id"];

function currentSection(): Section {
  const parts = window.location.hash.split("/");
  const value = parts[2] === "demo" ? parts[3] : parts[2];
  return sections.find((section) => section.id === value)?.id ?? "overview";
}

function Panel({
  title,
  subtitle,
  action,
  children,
  className = "",
}: {
  title: string;
  subtitle?: string;
  action?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section className={`lab-panel ${className}`}>
      <div className="lab-panel-header">
        <div>
          <h2>{title}</h2>
          {subtitle && <p>{subtitle}</p>}
        </div>
        {action}
      </div>
      {children}
    </section>
  );
}

function Metrics({ items }: { items: LabRun[] }) {
  const stats = summarize(items);
  return (
    <div className="lab-metrics">
      <div>
        <span>Evaluation runs</span>
        <strong>{stats.runs}</strong>
        <small>{stats.tasks.toLocaleString()} task results</small>
      </div>
      <div>
        <span>Task pass rate</span>
        <strong>{percent(stats.passRate)}</strong>
        <small>Passed tasks / all task results</small>
      </div>
      <div>
        <span>Weighted quality</span>
        <strong>{percent(stats.score)}</strong>
        <small>
          Mean weighted task score · {stats.scoreCoverage}/{stats.runs} runs
          reported
        </small>
      </div>
      <div>
        <span>
          AI Credits{" "}
          <CircleHelp
            size={13}
            aria-label="Reported credits, not token-derived estimates"
          />
        </span>
        <strong>
          {stats.credits === null
            ? "Unavailable"
            : formatAICredits(stats.credits)}
        </strong>
        <small>
          Known subtotal · {stats.creditCoverage}/{stats.runs} runs reported
        </small>
      </div>
    </div>
  );
}

function RunsTable({
  items,
  onInspect,
  selected,
  onSelect,
  compact = false,
}: {
  items: LabRun[];
  onInspect: (run: LabRun) => void;
  selected?: string[];
  onSelect?: (id: string) => void;
  compact?: boolean;
}) {
  return (
    <div className="lab-table-scroll">
      <table className="lab-table">
        <thead>
          <tr>
            {onSelect && <th aria-label="Select runs" />}
            <th>Evaluation / revision</th>
            <th>Model</th>
            <th>Status</th>
            <th>Pass rate</th>
            {!compact && (
              <>
                <th>Quality</th>
                <th>AI Credits</th>
                <th>Duration</th>
              </>
            )}
            <th>Started (UTC)</th>
          </tr>
        </thead>
        <tbody>
          {items.map((run) => {
            const failed = isFailedRun(run);
            return (
              <tr key={run.id}>
                {onSelect && (
                  <td>
                    <input
                      type="checkbox"
                      aria-label={`Select ${run.id}`}
                      checked={selected?.includes(run.id) ?? false}
                      disabled={
                        selected?.length === 2 && !selected.includes(run.id)
                      }
                      onChange={() => onSelect(run.id)}
                    />
                  </td>
                )}
                <td>
                  <button
                    className="lab-run-link"
                    onClick={() => onInspect(run)}
                  >
                    {run.skill}
                    <ChevronRight size={13} />
                  </button>
                  <small>
                    {run.summary && `${run.evalId} · `}
                    {run.revision} · {run.source}
                  </small>
                </td>
                <td>
                  <span
                    className={`lab-model-dot ${run.model === models[0] ? "blue" : "teal"}`}
                  />
                  {run.model}
                </td>
                <td>
                  <span
                    className={`lab-status ${failed ? "failed" : !run.summary || /^pass/i.test(run.summary.outcome) ? "passed" : "neutral"}`}
                  >
                    {run.summary?.outcome ?? (failed ? "Failed" : "Passed")}
                  </span>
                </td>
                <td className="lab-numeric">{percent(passRate(run))}</td>
                {!compact && (
                  <>
                    <td className="lab-numeric">{percent(runScore(run))}</td>
                    <td className="lab-numeric">
                      {run.aiCredits !== undefined ? (
                        formatAICredits(run.aiCredits)
                      ) : (
                        <span className="lab-muted">Unavailable</span>
                      )}
                    </td>
                    <td className="lab-numeric">
                      {duration(
                        run.summary?.duration ??
                          run.tasks.reduce(
                            (sum, task) => sum + task.duration,
                            0,
                          ),
                      )}
                    </td>
                  </>
                )}
                <td>
                  {shortDate(run.date)}
                  <small>
                    {new Date(run.date).toISOString().slice(11, 16)}
                  </small>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
      {items.length === 0 && (
        <div className="lab-empty">
          <Search size={24} />
          <h3>No runs match this view</h3>
          <p>Change the filters or clear the search to see evaluation runs.</p>
        </div>
      )}
    </div>
  );
}

function Comparison({
  baseline,
  candidate,
  onInspect,
}: {
  baseline: LabRun;
  candidate: LabRun;
  onInspect: (run: LabRun, task?: string) => void;
}) {
  const rows = alignTasks(baseline, candidate);
  const comparable = rows.filter((row) => row.delta !== null);
  const regressions = comparable.filter((row) => (row.delta ?? 0) < -0.1);
  const meanDelta = comparable.length
    ? comparable.reduce((sum, row) => sum + (row.delta ?? 0), 0) /
      comparable.length
    : null;
  return (
    <>
      <div className="lab-comparison-summary">
        <div>
          <span>Aligned quality delta</span>
          <strong
            className={
              meanDelta !== null && meanDelta < 0
                ? "lab-negative"
                : "lab-positive"
            }
          >
            {meanDelta === null
              ? "Unavailable"
              : `${meanDelta >= 0 ? "+" : ""}${meanDelta.toFixed(1)} pp`}
          </strong>
          <small>
            Stable task IDs, or unique legacy names when IDs are absent
          </small>
        </div>
        <div>
          <span>Regressions</span>
          <strong>{regressions.length}</strong>
          <small>{comparable.length} comparable tasks</small>
        </div>
        <div>
          <span>Unaligned tasks</span>
          <strong>{rows.length - comparable.length}</strong>
          <small>Missing tasks are not scored as zero</small>
        </div>
      </div>
      <Panel
        title="Task-level score matrix"
        subtitle="Reported weighted scores · click a task to inspect evidence"
      >
        <div className="lab-table-scroll">
          <table className="lab-table lab-matrix">
            <thead>
              <tr>
                <th>Task</th>
                <th>Baseline score</th>
                <th>Candidate score</th>
                <th>Delta</th>
                <th>Assessment</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <tr key={row.id}>
                  <td>
                    {row.after ? (
                      <button
                        className="lab-run-link"
                        onClick={() => onInspect(candidate, row.id)}
                      >
                        {row.after.name}
                        <ChevronRight size={13} />
                      </button>
                    ) : (
                      row.before?.name
                    )}
                  </td>
                  <td>
                    <span
                      className={`lab-score-cell ${!row.before || row.before.score === null ? "missing" : row.before.score < 0.8 ? "low" : ""}`}
                    >
                      {row.before
                        ? percent(
                            row.before.score === null
                              ? null
                              : row.before.score * 100,
                          )
                        : "Not in baseline"}
                    </span>
                  </td>
                  <td>
                    <span
                      className={`lab-score-cell ${!row.after || row.after.score === null ? "missing" : row.after.score < 0.8 ? "low" : ""}`}
                    >
                      {row.after
                        ? percent(
                            row.after.score === null
                              ? null
                              : row.after.score * 100,
                          )
                        : "Not in candidate"}
                    </span>
                  </td>
                  <td
                    className={
                      row.delta !== null && row.delta < -0.1
                        ? "lab-negative"
                        : row.delta !== null && row.delta > 0.1
                          ? "lab-positive"
                          : "lab-muted"
                    }
                  >
                    {row.delta === null
                      ? "Not comparable"
                      : `${row.delta > 0 ? "+" : ""}${row.delta.toFixed(1)} pp`}
                  </td>
                  <td>
                    {row.delta === null ? (
                      <span className="lab-status neutral">
                        {row.before && row.after
                          ? "Score / identity unavailable"
                          : "Coverage change"}
                      </span>
                    ) : row.delta < -0.1 ? (
                      <span className="lab-status failed">Regression</span>
                    ) : row.delta > 0.1 ? (
                      <span className="lab-status passed">Improved</span>
                    ) : (
                      <span className="lab-status neutral">Unchanged</span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Panel>
      <p className="lab-note">
        <ShieldCheck size={16} />
        Descriptive deltas, not statistical significance. Eval-name matching
        does not establish compatible spec or grader revisions; verify those
        before drawing conclusions.
      </p>
    </>
  );
}

function CurrentRuns({ filters }: { filters: Filters }) {
  const [tick, setTick] = useState(0);
  const [playing, setPlaying] = useState(false);
  const jobs: {
    id: string;
    skill: SkillId;
    model: Model;
    total: number;
    offset: number;
    error?: boolean;
  }[] = [
    {
      id: "demo-run-01",
      skill: "azure-deploy",
      model: "gpt-5.4",
      total: 12,
      offset: 3,
    },
    {
      id: "demo-run-02",
      skill: "code-review",
      model: "claude-sonnet-4.6",
      total: 9,
      offset: 0,
    },
    {
      id: "demo-run-03",
      skill: "doc-writer",
      model: "gpt-5.4",
      total: 8,
      offset: -2,
      error: true,
    },
  ];
  useEffect(() => {
    if (!playing || tick >= 16) return;
    const timer = window.setInterval(
      () => setTick((value) => Math.min(value + 1, 16)),
      2000,
    );
    return () => window.clearInterval(timer);
  }, [playing, tick]);
  const visible = jobs.filter(
    (job) =>
      (filters.skill === "all" || job.skill === filters.skill) &&
      (filters.model === "all" || job.model === filters.model) &&
      (!filters.day || filters.day === referenceDate),
  );
  return (
    <>
      <div className="lab-simulation">
        <div>
          <FlaskConical size={19} />
          <span>
            <strong>Playback sandbox</strong>
            <small>
              No evals are executing. These controls advance a deterministic
              browser-only simulation.
            </small>
          </span>
        </div>
        <div className="lab-actions">
          <button
            className="lab-button"
            disabled={tick >= 16}
            onClick={() => setPlaying((value) => !value)}
          >
            {playing && tick < 16 ? <Pause size={14} /> : <Play size={14} />}
            {playing && tick < 16 ? "Pause playback" : "Play simulation"}
          </button>
          <button
            className="lab-button"
            disabled={tick >= 16 || playing}
            onClick={() => setTick((value) => Math.min(value + 1, 16))}
          >
            Advance one step
          </button>
          <button
            className="lab-icon-button"
            aria-label="Reset simulation"
            onClick={() => {
              setTick(0);
              setPlaying(false);
            }}
          >
            <RotateCcw size={16} />
          </button>
        </div>
      </div>
      <div className="lab-live-grid">
        {visible.map((job) => {
          const progress = Math.min(job.total, Math.max(0, tick + job.offset));
          const status =
            job.error && progress >= 6
              ? "Error"
              : progress === job.total
                ? "Completed"
                : progress === 0
                  ? "Queued"
                  : "Running";
          const completed = status === "Error" ? 6 : progress;
          return (
            <Panel
              key={job.id}
              title={job.skill}
              subtitle={`${job.model} · ${job.id}`}
              action={
                <span
                  className={`lab-status ${status === "Error" ? "failed" : status === "Completed" ? "passed" : "running"}`}
                >
                  {status === "Running" && <i className="lab-pulse" />}
                  {status}
                </span>
              }
            >
              <div className="lab-live-body">
                <p>
                  <strong>{completed}</strong> / {job.total} tasks processed
                </p>
                <progress
                  max={job.total}
                  value={completed}
                  aria-label={`${job.skill} simulated progress`}
                />
                <div className="lab-live-meta">
                  <span>
                    Executor<em>Mock Copilot SDK</em>
                  </span>
                  <span>
                    Parallelism<em>3 tasks</em>
                  </span>
                  <span>
                    Billing<em>Unavailable during playback</em>
                  </span>
                </div>
                <p className="lab-note">
                  {status === "Error"
                    ? "Synthetic provider timeout. The failed run remains visible; no success fallback."
                    : status === "Queued"
                      ? "Waiting for a simulated worker slot."
                      : status === "Completed"
                        ? "Playback completed. This is not a real result artifact and is not added to historical reports."
                        : `Processing task ${completed + 1}: verify expected tool usage.`}
                </p>
              </div>
            </Panel>
          );
        })}
      </div>
      {visible.length === 0 && (
        <div className="lab-empty">
          <h3>No simulated jobs match the filters</h3>
          <p>
            Playback jobs are dated {referenceDate}; clear the chart date to see
            them.
          </p>
        </div>
      )}
      <Panel
        title="Activity stream"
        subtitle="Illustrative lifecycle events · newest first"
      >
        <div className="lab-stream">
          {visible.map((job) => (
            <div key={job.id}>
              <span className="lab-model-dot blue" />
              <code>step {tick.toString().padStart(2, "0")}</code>
              <strong>{job.skill}</strong>
              <span>
                {job.error && tick >= 8
                  ? "run_failed: synthetic provider timeout"
                  : tick + job.offset >= job.total
                    ? "run_completed"
                    : tick + job.offset <= 0
                      ? "run_queued"
                      : `task_completed: task-${Math.max(1, tick + job.offset)}`}
              </span>
            </div>
          ))}
        </div>
      </Panel>
    </>
  );
}

function Catalog({
  filters,
  onExplore,
}: {
  filters: Filters;
  onExplore: (skill: SkillId) => void;
}) {
  const visible = skills.filter(
    (skill) => filters.skill === "all" || filters.skill === skill.id,
  );
  return (
    <>
      <p className="lab-note">
        Readiness, requirement coverage, and token budgets below are
        illustrative skill metadata. Model and date filters affect run
        exploration, not these static skill checks.
      </p>
      <div className="lab-catalog-grid">
        {visible.map((skill) => (
          <Panel
            key={skill.id}
            title={skill.name}
            subtitle={`skills/${skill.id}/SKILL.md`}
            action={<BookOpen size={19} />}
          >
            <div className="lab-catalog-body">
              <p>{skill.description}</p>
              <div className="lab-skill-metrics">
                <span>
                  Readiness<strong>{skill.readiness}/100</strong>
                </span>
                <span>
                  Spec coverage
                  <strong>
                    {skill.requirementsCovered}/{skill.requirements}
                  </strong>
                </span>
              </div>
              <div className="lab-budget">
                <span>
                  Skill token budget
                  <strong
                    className={
                      skill.tokens > skill.budget ? "lab-negative" : ""
                    }
                  >
                    {skill.tokens.toLocaleString()} /{" "}
                    {skill.budget.toLocaleString()}
                  </strong>
                </span>
                <progress
                  max={skill.budget}
                  value={Math.min(skill.tokens, skill.budget)}
                  aria-label={`${skill.name} token budget`}
                />
                {skill.tokens > skill.budget && (
                  <small className="lab-negative">
                    Over budget by {skill.tokens - skill.budget} tokens
                  </small>
                )}
              </div>
              <div className="lab-tags">
                {skill.capabilities.map((capability) => (
                  <span key={capability}>{capability}</span>
                ))}
              </div>
              <button
                className="lab-button"
                onClick={() => onExplore(skill.id)}
              >
                Explore evaluation history
                <ArrowRight size={14} />
              </button>
            </div>
          </Panel>
        ))}
      </div>
      <Panel
        title="Waza capability map"
        subtitle="The dashboard connects evidence across the CLI lifecycle; it does not replace the CLI"
      >
        <div className="lab-capability-map">
          {[
            [
              "Author & validate",
              "init · new · suggest · registry · get · migrate",
              "Catalog + versioned suite configuration",
            ],
            [
              "Skill quality",
              "check · dev · tokens · coverage · spec verify",
              "Readiness + budget + requirement coverage",
            ],
            [
              "Execute & grade",
              "run · grade · trigger routing · responders · command mocks",
              "Current runs + grader evidence + policy denials",
            ],
            [
              "Investigate",
              "compare · snapshot · replay · adversarial · telemetry",
              "Baseline comparison + traces + fault evidence",
            ],
            [
              "Publish evidence",
              "CI reports · result artifacts · storage",
              "History + report exports + artifact provenance",
            ],
          ].map(([title, commands, surface]) => (
            <div key={title}>
              <ShieldCheck size={17} />
              <div>
                <strong>{title}</strong>
                <p>{commands}</p>
                <small>{surface}</small>
              </div>
              <span className="lab-status neutral">Illustrative</span>
            </div>
          ))}
        </div>
      </Panel>
    </>
  );
}

function ObservedCatalog({
  items,
  onExplore,
}: {
  items: LabRun[];
  onExplore: (skill: string) => void;
}) {
  const suites = [...new Set(items.map((run) => run.evalId))].sort();
  return (
    <div className="lab-health-list">
      {suites.map((suite) => {
        const suiteRuns = items.filter((run) => run.evalId === suite);
        const first = suiteRuns[0];
        if (!first) return null;
        return (
          <button key={suite} onClick={() => onExplore(first.skill)}>
            <span className="lab-health-icon">
              <Layers3 size={17} />
            </span>
            <div>
              <strong>{suite || "Eval identity unavailable"}</strong>
              <p>
                {suiteRuns.length} runs ·{" "}
                {[...new Set(suiteRuns.map((run) => run.model))].join(", ")}
              </p>
            </div>
            <span className="lab-status neutral">Readiness unavailable</span>
          </button>
        );
      })}
      {!suites.length && (
        <p className="lab-note">No saved suites in this scope.</p>
      )}
    </div>
  );
}

function Reports({
  items,
  filters,
  demo,
}: {
  items: LabRun[];
  filters: Filters;
  demo: boolean;
}) {
  const [notice, setNotice] = useState("");
  const [exporting, setExporting] = useState(false);
  const stats = summarize(items);
  const report = {
    format: "waza-dashboard-lab-report-v1",
    synthetic: demo,
    referenceDate: demo ? referenceDate : new Date().toISOString(),
    filters,
    summary: stats,
    runs: items,
    limitations: [
      demo
        ? "Synthetic fixtures, not EvaluationOutcome artifacts"
        : "Task scores aggregate repetitions; grader evidence and traces describe the first repetition",
      "Credits are a known subtotal; unavailable billing is not zero",
      "Simulation is excluded",
      "No statistical significance claim",
    ],
  };
  async function exportReport(format: "json" | "csv") {
    setExporting(true);
    setNotice("Preparing report...");
    try {
      if (format === "json") {
        const details = demo ? items : await reportDetails(items);
        downloadText(
          JSON.stringify(
            {
              ...report,
              summary: summarize(details),
              runs: demo ? details : details.map((run) => run.api),
            },
            null,
            2,
          ),
          "waza-lab-report.json",
          "application/json",
        );
      } else
        downloadText(
          toCSV(
            [
              "Synthetic",
              "Run ID",
              "Eval",
              "Revision",
              "Model",
              "Judge",
              "Date UTC",
              "Tasks",
              "Pass Rate %",
              "Weighted Score %",
              "Tokens",
              "AI Credits",
            ],
            items.map((run) => [
              String(demo),
              run.id,
              run.evalId,
              run.revision,
              run.model,
              run.judge,
              run.date,
              String(run.summary?.taskCount ?? run.tasks.length),
              passRate(run)?.toFixed(2) ?? "",
              runScore(run)?.toFixed(2) ?? "",
              String(
                run.summary?.tokens ??
                  run.tasks.reduce((sum, task) => sum + (task.tokens ?? 0), 0),
              ),
              run.aiCredits === undefined ? "" : String(run.aiCredits),
            ]),
          ),
          "waza-lab-report.csv",
          "text/csv;charset=utf-8;",
        );
      setNotice(
        `${format.toUpperCase()} report downloaded with ${items.length} ${demo ? "synthetic" : "saved"} runs.`,
      );
    } catch (error) {
      setNotice(
        `Report failed; no partial report downloaded. ${error instanceof Error ? error.message : String(error)}`,
      );
    } finally {
      setExporting(false);
    }
  }
  return (
    <div className="lab-report">
      <Panel
        title="Evaluation health report"
        subtitle={`${demo ? "Synthetic workspace" : "Saved results"} · current filters applied`}
        action={<span className="lab-status neutral">Preview</span>}
      >
        <div className="lab-report-body">
          <div className="lab-report-heading">
            <div>
              <p className="lab-eyebrow">WAZA / EVALUATION REPORT</p>
              <h3>Evidence you can share.</h3>
              <p>
                Quality, regressions, and usage in one reproducible snapshot.
              </p>
            </div>
            <FileBarChart2 size={44} />
          </div>
          <Metrics items={items} />
          <div className="lab-report-filters">
            <strong>Scope</strong>
            <span>Skill: {filters.skill}</span>
            <span>Model: {filters.model}</span>
            <span>
              Window: {filters.days === 0 ? "All time" : `${filters.days} days`}
            </span>
            <span>Chart date: {filters.day ?? "All"}</span>
          </div>
          <div className="lab-report-findings">
            <h3>Summary findings</h3>
            <p>
              {stats.runs} {demo ? "completed synthetic" : "saved"} runs contain{" "}
              {stats.tasks} task results. Task pass rate is{" "}
              {percent(stats.passRate)} and mean weighted quality is{" "}
              {percent(stats.score)}.
            </p>
            <p>
              {stats.creditCoverage} of {stats.runs} runs report{" "}
              {demo ? "synthetic" : "authoritative"}
              billing, totaling{" "}
              {stats.credits === null
                ? "unavailable"
                : formatAICredits(stats.credits)}{" "}
              known AI Credits. Missing usage is not estimated from tokens.
            </p>
            <p>
              Simulation jobs are excluded. Aggregated results describe this
              selected sample; they do not establish statistical significance or
              production readiness.
            </p>
          </div>
          <div className="lab-actions lab-report-actions">
            <button
              className="lab-button primary"
              disabled={!items.length || exporting}
              onClick={() => exportReport("json")}
            >
              <Download size={15} />
              Download JSON
            </button>
            <button
              className="lab-button"
              disabled={!items.length || exporting}
              onClick={() => exportReport("csv")}
            >
              <Download size={15} />
              Download CSV
            </button>
            <button className="lab-button" onClick={() => window.print()}>
              Print / save PDF
            </button>
          </div>
          <p role="status" className="lab-note">
            {notice ||
              "Exports stay in your browser. JSON includes task/grader evidence; CSV contains run-level metrics."}
          </p>
        </div>
      </Panel>
    </div>
  );
}

export default function DashboardLab({ demo = false }: { demo?: boolean }) {
  const source = useLabSource(demo);
  const allRuns = source.runs;
  const routeBase = demo ? "/lab/demo" : "/lab";
  const availableModels = demo
    ? models
    : [...new Set(allRuns.map((run) => run.model))].sort();
  const availableSkills = demo
    ? skills.map((skill) => ({ id: skill.id, name: skill.name }))
    : [...new Set(allRuns.map((run) => run.skill))]
        .sort()
        .map((id) => ({ id, name: id }));
  const [section, setSection] = useState<Section>(currentSection);
  const [filters, setFilters] = useState<Filters>({
    skill: "all",
    model: "all",
    days: demo ? 14 : 0,
    day: null,
  });
  const [search, setSearch] = useState("");
  const [outcome, setOutcome] = useState("all");
  const [selected, setSelected] = useState<string[]>([]);
  const [candidateId, setCandidateId] = useState("run-13-azure-deploy-0");
  const [baselineId, setBaselineId] = useState("run-12-azure-deploy-0");
  const [inspector, setInspector] = useState<{
    run: LabRun;
    task?: string;
  } | null>(null);
  const [about, setAbout] = useState(false);
  useEffect(() => {
    const handleHash = () => setSection(currentSection());
    window.addEventListener("hashchange", handleHash);
    return () => window.removeEventListener("hashchange", handleHash);
  }, []);

  const reference = demo
    ? referenceDate
    : new Date().toISOString().slice(0, 10);
  const filtered = filterRuns(allRuns, filters, "", reference);
  const history = filterRuns(allRuns, filters, search, reference).filter(
    (run) =>
      outcome === "all" ||
      (isFailedRun(run)
        ? "failed"
        : run.summary
          ? /^pass/i.test(run.summary.outcome)
            ? "passed"
            : run.summary.outcome
          : "passed") === outcome,
  );
  const selectedVisible = selected.filter((id) =>
    history.some((run) => run.id === id),
  );
  const candidate =
    filtered.find((run) => run.id === candidateId) ?? filtered[0];
  const baselines = candidate
    ? filtered.filter(
        (run) =>
          run.evalId !== "" &&
          run.evalId === candidate.evalId &&
          run.id !== candidate.id,
      )
    : [];
  const baseline =
    baselines.find((run) => run.id === baselineId) ??
    baselines.find(
      (run) => run.model === candidate?.model && run.date < candidate.date,
    ) ??
    baselines[0];
  const detailQueries = useLabDetails(
    section === "compare" && candidate && baseline
      ? [baseline.id, candidate.id]
      : [],
    demo,
  );
  const chartRuns = filterRuns(
    allRuns,
    { ...filters, day: null },
    "",
    reference,
  );
  const failingTasks = filtered
    .flatMap((run) =>
      run.tasks
        .filter((task) => task.outcome === "failed")
        .map((task) => ({ run, task })),
    )
    .slice(0, 4);
  const activeSection = sections.find((item) => item.id === section)!;

  function inspect(run: LabRun, task?: string) {
    setInspector({ run, task });
  }
  function navigate(next: Section) {
    window.location.hash = `${routeBase}/${next}`;
  }
  function showRegression() {
    setFilters({ skill: "azure-deploy", model: "all", days: 14, day: null });
    setCandidateId("run-13-azure-deploy-0");
    setBaselineId("run-12-azure-deploy-0");
    navigate("compare");
  }
  const chosenRuns = selectedVisible
    .map((id) => history.find((run) => run.id === id))
    .filter((run) => run !== undefined);
  const comparableSelection =
    chosenRuns.length === 2 &&
    chosenRuns[0]?.evalId !== "" &&
    chosenRuns[0]?.evalId === chosenRuns[1]?.evalId;

  return (
    <div className="lab-root">
      <a
        href="#lab-main"
        className="lab-skip"
        onClick={(event) => {
          event.preventDefault();
          document.getElementById("lab-main")?.focus();
        }}
      >
        Skip to dashboard content
      </a>
      <aside className="lab-sidebar">
        <a className="lab-brand" href={`#${routeBase}`}>
          <span className="lab-logo">
            <Activity size={21} />
          </span>
          <span>
            waza<small>EVALUATION WORKSPACE</small>
          </span>
        </a>
        <div className="lab-workspace">
          <span className="lab-workspace-avatar">S</span>
          <div>
            <strong>Skills workspace</strong>
            <small>{demo ? "Synthetic demo" : "Saved results"}</small>
          </div>
          <FlaskConical size={16} />
        </div>
        <p className="lab-nav-label">WORKSPACE</p>
        <nav aria-label="Prototype navigation">
          {sections.map((item) => (
            <a
              key={item.id}
              href={`#${routeBase}/${item.id}`}
              aria-current={section === item.id ? "page" : undefined}
            >
              <item.icon size={18} />
              {item.label}
              {demo && item.id === "current" && (
                <span className="lab-nav-count">3</span>
              )}
            </a>
          ))}
        </nav>
        <div className="lab-sidebar-bottom">
          <div className="lab-local-indicator">
            <i />
            {demo ? "Browser-only sandbox" : "Results workspace"}
            <small>
              {demo
                ? "No credentials or backend required"
                : "Refreshes every 15 seconds"}
            </small>
          </div>
          <a href="#/">
            <ArrowLeft size={16} />
            Original dashboard
          </a>
          <button onClick={() => setAbout((value) => !value)}>
            <CircleHelp size={16} />
            About this lab
          </button>
        </div>
      </aside>
      <div className="lab-workbench">
        <header className="lab-topbar">
          <div>
            <span>Skills workspace</span>
            <ChevronRight size={14} />
            <strong>{activeSection.label}</strong>
          </div>
          <span className="lab-environment">
            <i />
            {demo ? "LOCAL · SYNTHETIC DATA" : "SAVED WAZA RESULTS"}
          </span>
          <a href={`#${routeBase}/catalog`} aria-label="Explore skill catalog">
            <BookOpen size={18} />
          </a>
        </header>
        <main id="lab-main" className="lab-main" tabIndex={-1}>
          <div className="lab-demo-banner">
            <FlaskConical size={16} />
            <span>
              <strong>Dashboard lab</strong> ·{" "}
              {demo
                ? `Synthetic interactive demo. Reference date: ${referenceDate}.`
                : "Experimental dashboard using your saved results. The original dashboard remains available."}
            </span>
            <button onClick={() => setAbout((value) => !value)}>
              What’s real?
              <ArrowRight size={13} />
            </button>
          </div>
          {about && (
            <div
              className="lab-about"
              role="region"
              aria-label="Prototype boundaries"
            >
              <button
                aria-label="Close prototype information"
                className="lab-icon-button"
                onClick={() => setAbout(false)}
              >
                <X size={16} />
              </button>
              <h2>
                {demo
                  ? "Real interactions. Synthetic evidence."
                  : "Real results. Explicit boundaries."}
              </h2>
              {!demo && (
                <p>
                  History, comparisons, traces, and reports use saved Waza
                  results. Activity is reconstructed event replay, not worker
                  telemetry. Readiness, coverage, git revisions, and execution
                  controls are unavailable. No cloud authentication or hosting
                  is implemented.{" "}
                  <a href="#/lab/demo">
                    Open the clearly labeled synthetic demo
                  </a>
                  .
                </p>
              )}
              {demo && (
                <>
                  <p>
                    Navigation, linked filters, task inspection, comparison,
                    report downloads, and print work. Every run, trace, usage
                    value, readiness check, and CI label is synthetic. Playback
                    never starts an eval. No workspace data is read or sent
                    anywhere.
                  </p>
                </>
              )}
              <p>
                This static UI can be hosted, but production cloud operation
                still requires authenticated APIs, durable metadata/artifacts,
                ingestion, and execution workers. Mock task identities and
                configuration are illustrative, not the current Waza API schema.
                Charts use native SVG inspired by Flint; Flint itself is not
                installed.
              </p>
            </div>
          )}
          <div className="lab-page-heading">
            <div>
              <p className="lab-eyebrow">EVALUATION INTELLIGENCE</p>
              <h1>
                {section === "overview"
                  ? "Know where your skills stand."
                  : activeSection.label}
              </h1>
              <p>
                {section === "overview"
                  ? "From the big picture to the evidence behind every result."
                  : section === "current"
                    ? "Follow progress, investigate failures, and keep execution visible."
                    : section === "history"
                      ? "A searchable record of what ran, what changed, and what it cost."
                      : section === "compare"
                        ? "Make a change. Measure its impact. Inspect the evidence."
                        : section === "reports"
                          ? "Turn evaluation evidence into a clear, shareable snapshot."
                          : "Connect skill readiness, evaluation coverage, and execution evidence."}
              </p>
            </div>
            <button
              className="lab-button"
              onClick={() =>
                navigate(section === "reports" ? "history" : "reports")
              }
            >
              {section === "reports" ? (
                <History size={15} />
              ) : (
                <FileBarChart2 size={15} />
              )}
              {section === "reports" ? "Explore runs" : "Build report"}
            </button>
          </div>
          <div className="lab-filterbar">
            <span>
              <SlidersHorizontal size={15} />
              Scope
            </span>
            <label>
              {demo ? "Skill" : "Skill / eval"}
              <select
                aria-label="Filter skill"
                value={filters.skill}
                onChange={(event) =>
                  setFilters({
                    ...filters,
                    skill: event.target.value as Filters["skill"],
                  })
                }
              >
                <option value="all">All skills</option>
                {availableSkills.map((skill) => (
                  <option key={skill.id} value={skill.id}>
                    {skill.name}
                  </option>
                ))}
              </select>
            </label>
            <label>
              Model
              <select
                aria-label="Filter model"
                value={filters.model}
                onChange={(event) =>
                  setFilters({
                    ...filters,
                    model: event.target.value as Filters["model"],
                  })
                }
              >
                <option value="all">All models</option>
                {availableModels.map((model) => (
                  <option key={model} value={model}>
                    {model}
                  </option>
                ))}
              </select>
            </label>
            <label>
              Window
              <select
                aria-label="Filter time window"
                value={filters.days}
                onChange={(event) =>
                  setFilters({
                    ...filters,
                    days: Number(event.target.value),
                    day: null,
                  })
                }
              >
                <option value={0}>All time</option>
                <option value={14}>Last 14 days</option>
                <option value={7}>Last 7 days</option>
                <option value={1}>Latest day</option>
              </select>
            </label>
            {filters.day && (
              <button
                className="lab-filter-chip"
                onClick={() => setFilters({ ...filters, day: null })}
              >
                {shortDate(filters.day)}
                <X size={13} aria-label="Clear chart date" />
              </button>
            )}
            <button
              className="lab-filter-reset"
              onClick={() => {
                setFilters({
                  skill: "all",
                  model: "all",
                  days: demo ? 14 : 0,
                  day: null,
                });
                setSearch("");
                setOutcome("all");
                setSelected([]);
              }}
            >
              Reset filters
            </button>
          </div>
          {!demo && (
            <div className="lab-history-toolbar">
              <p role={source.isError ? "alert" : "status"}>
                {source.isError
                  ? `Results could not be refreshed: ${source.error.message}. ${allRuns.length ? "Previously loaded results remain visible." : "No synthetic fallback is used."}`
                  : source.isPending
                    ? "Loading Waza results..."
                    : `${allRuns.length} saved runs · automatic refresh every 15 seconds`}
              </p>
              <button
                className="lab-button"
                disabled={source.isFetching}
                onClick={() => void source.refetch()}
              >
                {source.isFetching ? "Refreshing..." : "Refresh results"}
              </button>
            </div>
          )}
          {section === "overview" && (
            <>
              <Metrics items={filtered} />
              <div className="lab-overview-grid">
                <Panel
                  title="Quality over time"
                  subtitle="Mean weighted task score · select a day to cross-filter"
                >
                  <QualityChart
                    runs={chartRuns}
                    selectedDay={filters.day}
                    onSelect={(day) =>
                      setFilters({
                        ...filters,
                        day: filters.day === day ? null : day,
                      })
                    }
                  />
                </Panel>
                <Panel
                  title="Model performance"
                  subtitle="Task pass rate in the selected scope"
                >
                  <ModelChart
                    runs={filtered}
                    onSelect={(model) => setFilters({ ...filters, model })}
                  />
                  <div className="lab-panel-footer">
                    <GitCompareArrows size={15} />
                    <button onClick={() => navigate("compare")}>
                      Open baseline comparison
                      <ArrowRight size={14} />
                    </button>
                  </div>
                </Panel>
              </div>
              <div className="lab-overview-grid lab-bottom-grid">
                <Panel
                  title="Needs attention"
                  subtitle="Start with the failures that deserve a closer look"
                  action={
                    <span className="lab-status failed">
                      {filtered.filter(isFailedRun).length} failing runs
                    </span>
                  }
                >
                  <div className="lab-attention-list">
                    {demo &&
                      filtered.some(
                        (run) => run.id === "run-13-azure-deploy-0",
                      ) && (
                        <button
                          className="lab-regression-alert"
                          onClick={showRegression}
                        >
                          <ArrowDownRight size={19} />
                          <div>
                            <strong>
                              Azure deploy: investigate a regression
                            </strong>
                            <p>
                              Workflow validation and tool-policy evidence
                              changed in b7c9e21.
                            </p>
                            <small>
                              Compare against a42f8d0
                              <ArrowRight size={12} />
                            </small>
                          </div>
                        </button>
                      )}
                    {failingTasks.map(({ run, task }) => (
                      <button
                        key={`${run.id}-${task.id}`}
                        onClick={() => inspect(run, task.id)}
                      >
                        <span className="lab-failure-mark">
                          <X size={12} />
                        </span>
                        <div>
                          <strong>{task.name}</strong>
                          <p>
                            {run.skill} · {run.model}
                          </p>
                        </div>
                        <ChevronRight size={16} />
                      </button>
                    ))}
                    {!demo &&
                      filtered
                        .filter(isFailedRun)
                        .slice(0, 4)
                        .map((run) => (
                          <button key={run.id} onClick={() => inspect(run)}>
                            <span className="lab-failure-mark">
                              <X size={12} />
                            </span>
                            <div>
                              <strong>{run.skill}</strong>
                              <p>
                                {run.model} · {run.summary?.passCount}/
                                {run.summary?.taskCount} passed
                              </p>
                            </div>
                            <ChevronRight size={16} />
                          </button>
                        ))}
                    {!(demo
                      ? failingTasks.length
                      : filtered.filter(isFailedRun).length) && (
                      <div className="lab-empty">
                        <Check size={22} />
                        <p>
                          No failing {demo ? "tasks" : "runs"} in this scope.
                        </p>
                      </div>
                    )}
                  </div>
                </Panel>
                <Panel
                  title="Workspace health"
                  subtitle={
                    demo
                      ? "Skill-level checks · illustrative, independent of run filters"
                      : "Observed eval suites · readiness checks unavailable"
                  }
                >
                  <div className="lab-health-list">
                    {!demo && (
                      <ObservedCatalog
                        items={filtered}
                        onExplore={(skill) => {
                          setFilters({ ...filters, skill });
                          navigate("catalog");
                        }}
                      />
                    )}
                    {demo &&
                      skills
                        .filter(
                          (skill) =>
                            filters.skill === "all" ||
                            filters.skill === skill.id,
                        )
                        .map((skill) => (
                          <button
                            key={skill.id}
                            onClick={() => {
                              setFilters({ ...filters, skill: skill.id });
                              navigate("catalog");
                            }}
                          >
                            <span className="lab-health-icon">
                              <ShieldCheck size={17} />
                            </span>
                            <div>
                              <strong>{skill.name}</strong>
                              <p>
                                {skill.requirementsCovered}/{skill.requirements}{" "}
                                requirements covered
                              </p>
                            </div>
                            <span
                              className={`lab-status ${skill.tokens > skill.budget ? "failed" : "passed"}`}
                            >
                              {skill.tokens > skill.budget
                                ? "Over budget"
                                : `${skill.readiness}/100`}
                            </span>
                          </button>
                        ))}
                  </div>
                  <div className="lab-panel-footer">
                    <Sparkles size={15} />
                    <span>
                      {demo
                        ? "Readiness is not the same as eval pass rate."
                        : "Run history cannot establish skill readiness or spec coverage."}
                    </span>
                  </div>
                </Panel>
              </div>
              <Panel
                title="Recent evaluations"
                subtitle={`${filtered.length} runs in scope · newest first`}
                action={
                  <button
                    className="lab-text-button"
                    onClick={() => navigate("history")}
                  >
                    View all history
                    <ArrowRight size={14} />
                  </button>
                }
              >
                <RunsTable
                  items={filtered.slice(0, 5)}
                  onInspect={inspect}
                  compact
                />
              </Panel>
            </>
          )}
          {section === "current" &&
            (demo ? (
              <CurrentRuns filters={filters} />
            ) : (
              <RunActivity runs={filtered} />
            ))}
          {section === "history" && (
            <>
              <Metrics items={history} />
              <Panel
                title="Evaluation history"
                subtitle={`${history.length} matching runs`}
                action={
                  <span className="lab-status neutral">
                    {demo ? "Synthetic archive" : "Saved-result archive"}
                  </span>
                }
              >
                <div className="lab-history-toolbar">
                  <label className="lab-search">
                    <Search size={16} />
                    <input
                      aria-label="Search run history"
                      placeholder="Search skills, models, revisions, source..."
                      value={search}
                      onChange={(event) => setSearch(event.target.value)}
                    />
                  </label>
                  <select
                    aria-label="Filter outcome"
                    value={outcome}
                    onChange={(event) => setOutcome(event.target.value)}
                  >
                    <option value="all">All outcomes</option>
                    <option value="failed">Failed</option>
                    <option value="passed">Passed</option>
                  </select>
                  <button
                    className="lab-button"
                    disabled={!comparableSelection}
                    onClick={() => {
                      const sorted = [...chosenRuns].sort((a, b) =>
                        b.date.localeCompare(a.date),
                      );
                      if (sorted[0] && sorted[1]) {
                        setCandidateId(sorted[0].id);
                        setBaselineId(sorted[1].id);
                        navigate("compare");
                      }
                    }}
                  >
                    Compare selected ({selectedVisible.length})
                  </button>
                </div>
                {chosenRuns.length === 2 && !comparableSelection && (
                  <p role="status" className="lab-note lab-negative">
                    Select runs from the same evaluation suite to compare shared
                    task IDs.
                  </p>
                )}
                <RunsTable
                  items={history}
                  onInspect={inspect}
                  selected={selectedVisible}
                  onSelect={(id) =>
                    setSelected(
                      selectedVisible.includes(id)
                        ? selectedVisible.filter((value) => value !== id)
                        : [...selectedVisible, id],
                    )
                  }
                />
              </Panel>
            </>
          )}
          {section === "compare" && (
            <>
              {candidate && baseline ? (
                <>
                  <div className="lab-compare-picker">
                    <label>
                      <span>BASELINE</span>
                      <select
                        aria-label="Baseline run"
                        value={baseline.id}
                        onChange={(event) => setBaselineId(event.target.value)}
                      >
                        {baselines.map((run) => (
                          <option key={run.id} value={run.id}>
                            {run.skill} · {run.model} · {shortDate(run.date)} ·{" "}
                            {run.revision}
                          </option>
                        ))}
                      </select>
                      <small>
                        {baseline.id} · judge {baseline.judge}
                      </small>
                    </label>
                    <GitCompareArrows size={24} />
                    <label>
                      <span>CANDIDATE</span>
                      <select
                        aria-label="Candidate run"
                        value={candidate.id}
                        onChange={(event) => setCandidateId(event.target.value)}
                      >
                        {filtered.map((run) => (
                          <option key={run.id} value={run.id}>
                            {run.skill} · {run.model} · {shortDate(run.date)} ·{" "}
                            {run.revision}
                          </option>
                        ))}
                      </select>
                      <small>
                        {candidate.id} · judge {candidate.judge}
                      </small>
                    </label>
                  </div>
                  {demo ? (
                    <Comparison
                      baseline={baseline}
                      candidate={candidate}
                      onInspect={inspect}
                    />
                  ) : detailQueries.some((query) => query.isError) ? (
                    <div className="lab-empty" role="alert">
                      <p>
                        Comparison evidence could not be loaded.{" "}
                        {
                          detailQueries.find((query) => query.isError)?.error
                            ?.message
                        }
                      </p>
                      <button
                        className="lab-button"
                        onClick={() => {
                          detailQueries.forEach(
                            (query) => void query.refetch(),
                          );
                        }}
                      >
                        Retry comparison
                      </button>
                    </div>
                  ) : detailQueries[0]?.data && detailQueries[1]?.data ? (
                    <Comparison
                      baseline={detailQueries[0].data}
                      candidate={detailQueries[1].data}
                      onInspect={inspect}
                    />
                  ) : (
                    <p role="status" className="lab-note">
                      Loading comparison evidence...
                    </p>
                  )}
                </>
              ) : (
                <div className="lab-empty">
                  <GitCompareArrows size={25} />
                  <h3>Two runs from the same eval are needed</h3>
                  <p>
                    Broaden the model or time filters to choose a baseline and
                    candidate.
                  </p>
                </div>
              )}
            </>
          )}
          {section === "reports" && (
            <Reports items={filtered} filters={filters} demo={demo} />
          )}
          {section === "catalog" &&
            (demo ? (
              <Catalog
                filters={filters}
                onExplore={(skill) => {
                  setFilters({ ...filters, skill });
                  navigate("history");
                }}
              />
            ) : (
              <Panel
                title="Observed skills & evals"
                subtitle="Derived from saved runs, not a complete installed-skill registry"
              >
                <ObservedCatalog
                  items={filtered}
                  onExplore={(skill) => {
                    setFilters({ ...filters, skill });
                    navigate("history");
                  }}
                />
                <p className="lab-note">
                  Readiness, token budgets, requirement coverage, and version
                  provenance are unavailable from the results API. Use waza
                  check, tokens, coverage, and spec verify to produce that
                  evidence separately.
                </p>
              </Panel>
            ))}
          <footer className="lab-footer">
            <span>
              <FlaskConical size={13} />
              Waza dashboard lab ·{" "}
              {demo ? "Synthetic fixtures" : "Saved results"}
            </span>
            <span>
              <Clock3 size={13} />
              {demo
                ? "Dates and usage are illustrative, not live telemetry"
                : "No live worker discovery or execution controls"}
            </span>
          </footer>
        </main>
      </div>
      {inspector && (
        <RunInspector
          key={`${inspector.run.id}-${inspector.task ?? ""}`}
          run={inspector.run}
          demo={demo}
          initialTask={inspector.task}
          onClose={() => setInspector(null)}
        />
      )}
    </div>
  );
}
