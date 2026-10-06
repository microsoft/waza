import type { RunDetail, RunSummary, TaskResult } from "../api/client";

export type Model = string;
export type SkillId = string;
export type Outcome = string;

export interface LabGrader {
  name: string;
  type: string;
  score: number;
  passed: boolean;
  weight?: number;
  evidence: string;
}

export interface LabTask {
  id: string;
  name: string;
  outcome: Outcome;
  score: number | null;
  duration: number;
  tokens?: number;
  graders: LabGrader[];
  api?: TaskResult;
  comparable?: boolean;
}

export interface LabRun {
  id: string;
  skill: SkillId;
  evalId: string;
  revision: string;
  model: Model;
  judge: Model;
  date: string;
  source: string;
  repetitions?: number;
  tasks: LabTask[];
  aiCredits?: number;
  summary?: RunSummary;
  api?: RunDetail;
}

export interface LabSkill {
  id: SkillId;
  name: string;
  description: string;
  readiness: number;
  requirementsCovered: number;
  requirements: number;
  tokens: number;
  budget: number;
  capabilities: string[];
}

export const models: Model[] = ["gpt-5.4", "claude-sonnet-4.6"];
export const referenceDate = "2026-10-06";
export const skills: LabSkill[] = [
  {
    id: "azure-deploy",
    name: "Azure deploy",
    description: "Plan, validate, and deploy an application safely.",
    readiness: 92,
    requirementsCovered: 11,
    requirements: 12,
    tokens: 3840,
    budget: 5000,
    capabilities: [
      "Trigger routing",
      "Command mocks",
      "Tool policy",
      "Adversarial",
      "Snapshot / replay",
      "Spec coverage",
    ],
  },
  {
    id: "code-review",
    name: "Code review",
    description: "Find actionable defects and explain the evidence.",
    readiness: 96,
    requirementsCovered: 9,
    requirements: 9,
    tokens: 2460,
    budget: 4000,
    capabilities: [
      "Prompt graders",
      "Responders",
      "Weighted scoring",
      "Bootstrap intervals",
      "CI gates",
    ],
  },
  {
    id: "doc-writer",
    name: "Documentation writer",
    description: "Turn implementation details into useful documentation.",
    readiness: 84,
    requirementsCovered: 7,
    requirements: 10,
    tokens: 4210,
    budget: 4000,
    capabilities: [
      "File graders",
      "Token limits",
      "Registry graders",
      "Regrading",
      "Custom agents",
    ],
  },
];

const taskNames = [
  "Recognize the right intent",
  "Ask for missing context",
  "Follow the skill workflow",
  "Use the required tools",
  "Handle an invalid input",
  "Respect the tool policy",
  "Verify the final artifact",
  "Explain the outcome",
];

function makeTask(
  index: number,
  day: number,
  skillIndex: number,
  modelIndex: number,
): LabTask {
  const regression =
    skillIndex === 0 &&
    modelIndex === 0 &&
    day === 13 &&
    (index === 2 || index === 5);
  const failed =
    regression || (index + day * 3 + skillIndex * 2 + modelIndex) % 17 === 0;
  const quality = failed
    ? regression
      ? 0.4
      : 0.6
    : 0.9 + ((index + day) % 3) * 0.05;
  const graders: LabGrader[] = [
    {
      name: "Task completion",
      type: "prompt",
      score: quality,
      passed: !failed,
      weight: 2,
      evidence: regression
        ? "The assistant skipped the validation step before proposing deployment. The expected validate call is missing."
        : failed
          ? "The answer did not satisfy the expected task requirement."
          : "The final output satisfies the task requirement.",
    },
    {
      name: "Tool policy",
      type: "tool_constraint",
      score: regression && index === 5 ? 0 : 1,
      passed: !(regression && index === 5),
      weight: 1,
      evidence:
        regression && index === 5
          ? "Denied tool: bash. This custom agent allows read_file and apply_patch only. Denial is preserved as evidence, not hidden."
          : "Observed calls comply with the configured tool policy.",
    },
    {
      name: "Token budget",
      type: "token_usage",
      score: 1,
      passed: true,
      weight: 1,
      evidence: "Task usage is below the configured 8,000-token limit.",
    },
  ];
  const score =
    graders.reduce(
      (sum, grader) => sum + grader.score * (grader.weight ?? 0),
      0,
    ) / 4;
  return {
    id: `task-${index + 1}`,
    name: taskNames[index] ?? "Reject a false-positive trigger",
    outcome: failed ? "failed" : "passed",
    score,
    duration: 18 + index * 4 + modelIndex * 9 + (day % 5),
    tokens: 1200 + index * 210 + modelIndex * 130,
    graders,
  };
}

export const runs: LabRun[] = Array.from({ length: 14 }, (_, day) =>
  skills.flatMap((skill, skillIndex) =>
    models.map((model, modelIndex) => {
      const date = new Date(`${referenceDate}T12:00:00Z`);
      date.setUTCDate(date.getUTCDate() - 13 + day);
      const tasks = Array.from(
        { length: day === 13 && skillIndex === 0 ? 9 : 8 },
        (_, index) => makeTask(index, day, skillIndex, modelIndex),
      );
      return {
        id: `run-${day}-${skill.id}-${modelIndex}`,
        skill: skill.id,
        evalId: `${skill.id}/core`,
        revision: day === 13 ? "b7c9e21" : "a42f8d0",
        model,
        judge: "gpt-5.4" as const,
        date: date.toISOString(),
        source: day % 3 === 0 ? ("Local" as const) : ("CI" as const),
        repetitions: 3,
        tasks,
        aiCredits:
          day % 6 === 0
            ? undefined
            : Number(
                (
                  1.8 +
                  skillIndex * 0.4 +
                  modelIndex * 0.3 +
                  tasks.length * 0.08
                ).toFixed(2),
              ),
      };
    }),
  ),
)
  .flat()
  .sort((a, b) => b.date.localeCompare(a.date) || a.id.localeCompare(b.id));

export interface Filters {
  skill: SkillId | "all";
  model: Model | "all";
  days: number;
  day: string | null;
}

export function filterRuns(
  items: LabRun[],
  filters: Filters,
  search = "",
  reference = referenceDate,
) {
  const cutoff = new Date(`${reference}T00:00:00Z`);
  cutoff.setUTCDate(cutoff.getUTCDate() - filters.days + 1);
  return items.filter(
    (run) =>
      (filters.skill === "all" || run.skill === filters.skill) &&
      (filters.model === "all" || run.model === filters.model) &&
      (filters.days === 0 || new Date(run.date) >= cutoff) &&
      (!filters.day || run.date.startsWith(filters.day)) &&
      `${run.id} ${run.skill} ${run.model} ${run.revision} ${run.source}`
        .toLowerCase()
        .includes(search.toLowerCase()),
  );
}

export function passRate(run: LabRun) {
  if (run.summary) {
    return run.summary.taskCount > 0
      ? (run.summary.passCount / run.summary.taskCount) * 100
      : null;
  }
  return (
    (run.tasks.filter((task) => task.outcome === "passed").length /
      run.tasks.length) *
    100
  );
}

export function runScore(run: LabRun) {
  if (run.summary)
    return run.summary.weightedScore === undefined
      ? null
      : run.summary.weightedScore * 100;
  if (!run.tasks.length || run.tasks.some((task) => task.score === null))
    return null;
  return (
    (run.tasks.reduce((sum, task) => sum + (task.score ?? 0), 0) /
      run.tasks.length) *
    100
  );
}

export function summarize(items: LabRun[]) {
  const taskCount = items.reduce(
    (sum, run) => sum + (run.summary?.taskCount ?? run.tasks.length),
    0,
  );
  const passed = items.reduce(
    (sum, run) =>
      sum +
      (run.summary?.passCount ??
        run.tasks.filter((task) => task.outcome === "passed").length),
    0,
  );
  const scores = items.filter((run) => runScore(run) !== null);
  const scoredTasks = scores.reduce(
    (sum, run) => sum + (run.summary?.taskCount ?? run.tasks.length),
    0,
  );
  const credits = items.filter((run) => run.aiCredits !== undefined);
  return {
    runs: items.length,
    tasks: taskCount,
    passRate: taskCount ? (passed / taskCount) * 100 : null,
    score: scoredTasks
      ? scores.reduce(
          (sum, run) =>
            sum +
            (runScore(run) ?? 0) * (run.summary?.taskCount ?? run.tasks.length),
          0,
        ) / scoredTasks
      : null,
    scoreCoverage: scores.length,
    credits: credits.length
      ? credits.reduce((sum, run) => sum + (run.aiCredits ?? 0), 0)
      : null,
    creditCoverage: credits.length,
    tokens: items.reduce(
      (sum, run) =>
        sum +
        (run.summary?.tokens ??
          run.tasks.reduce((total, task) => total + (task.tokens ?? 0), 0)),
      0,
    ),
    duration: items.length
      ? items.reduce(
          (sum, run) =>
            sum +
            (run.summary?.duration ??
              run.tasks.reduce((total, task) => total + task.duration, 0)),
          0,
        ) / items.length
      : null,
  };
}

export function alignTasks(baseline: LabRun, candidate: LabRun) {
  const ids = new Set([
    ...baseline.tasks.map((task) => task.id),
    ...candidate.tasks.map((task) => task.id),
  ]);
  return [...ids].map((id) => {
    const before = baseline.tasks.find((task) => task.id === id);
    const after = candidate.tasks.find((task) => task.id === id);
    return {
      id,
      before,
      after,
      delta:
        baseline.evalId !== "" &&
        baseline.evalId === candidate.evalId &&
        before &&
        after &&
        before.comparable !== false &&
        after.comparable !== false &&
        before.score !== null &&
        after.score !== null
          ? (after.score - before.score) * 100
          : null,
    };
  });
}

export function isFailedRun(run: LabRun) {
  return run.summary
    ? /^(fail|error)/i.test(run.summary.outcome)
    : run.tasks.some((task) => task.outcome === "failed");
}

export function percent(value: number | null) {
  return value === null ? "Unavailable" : `${value.toFixed(1)}%`;
}

export function duration(value: number | null) {
  if (value === null) return "Unavailable";
  return `${Math.floor(value / 60)}m ${Math.round(value % 60)}s`;
}

export function shortDate(date: string) {
  return new Date(date).toLocaleDateString("en-US", {
    month: "short",
    day: "numeric",
    timeZone: "UTC",
  });
}
