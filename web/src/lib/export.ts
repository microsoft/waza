import type { RunSummary, RunDetail } from "../api/client";

function escapeCSV(value: string): string {
  // Neutralize spreadsheet formula injection (including leading whitespace bypass)
  const FORMULA_RE = /^[\s]*[=+\-@]/;
  let safe = value;
  if (FORMULA_RE.test(safe)) {
    safe = "'" + safe;
  }
  if (safe.includes(",") || safe.includes('"') || safe.includes("\n")) {
    return `"${safe.replace(/"/g, '""')}"`;
  }
  return safe;
}

function toCSV(headers: string[], rows: string[][]): string {
  const headerLine = headers.map(escapeCSV).join(",");
  const dataLines = rows.map((row) => row.map(escapeCSV).join(","));
  return [headerLine, ...dataLines].join("\n");
}

function downloadCSV(csv: string, filename: string) {
  const blob = new Blob([csv], { type: "text/csv;charset=utf-8;" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  a.click();
  URL.revokeObjectURL(url);
}

export function exportRunsToCSV(runs: RunSummary[]) {
  const headers = [
    "ID",
    "Spec",
    "Model",
    "Outcome",
    "Pass Count",
    "Task Count",
    "Pass Rate",
    "Tokens",
    "Cost",
    "Duration (s)",
    "Timestamp",
  ];
  const rows = runs.map((r) => [
    r.id,
    r.spec,
    r.model,
    r.outcome,
    String(r.passCount),
    String(r.taskCount),
    r.taskCount > 0
      ? `${Math.round((r.passCount / r.taskCount) * 100)}%`
      : "0%",
    String(r.tokens),
    `$${r.cost.toFixed(2)}`,
    String(Math.round(r.duration)),
    r.timestamp,
  ]);
  downloadCSV(toCSV(headers, rows), "waza-runs.csv");
}

export function exportRunDetailToCSV(run: RunDetail) {
  const headers = [
    "Task",
    "Outcome",
    "Score",
    "Duration (s)",
    "Grader",
    "Grader Type",
    "Grader Passed",
    "Grader Score",
    "Grader Message",
  ];
  const rows: string[][] = [];
  for (const task of run.tasks) {
    if (task.graderResults.length === 0) {
      rows.push([
        task.name,
        task.outcome,
        `${Math.round(task.score * 100)}%`,
        String(Math.round(task.duration)),
        "",
        "",
        "",
        "",
        "",
      ]);
    } else {
      for (const g of task.graderResults) {
        rows.push([
          task.name,
          task.outcome,
          `${Math.round(task.score * 100)}%`,
          String(Math.round(task.duration)),
          g.name,
          g.type,
          g.passed ? "yes" : "no",
          `${Math.round(g.score * 100)}%`,
          g.message,
        ]);
      }
    }
  }
  const filename = `waza-run-${run.spec}-${run.id.slice(0, 8)}.csv`;
  downloadCSV(toCSV(headers, rows), filename);
}
