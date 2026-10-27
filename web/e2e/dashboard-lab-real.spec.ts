import { expect, test, type Page } from "@playwright/test";
import { readFile } from "node:fs/promises";
import { once } from "node:events";
import { createServer } from "node:http";
import type { RunDetail, RunDetailWire, RunSummary } from "../src/api/client";
import { RUN_DETAIL, RUNS } from "./fixtures/mock-data";
import { evidenceRun } from "./fixtures/evidence-data";
import promptJudging from "./fixtures/prompt-judging.json" with { type: "json" };
import { RECORDED_RELIABILITY } from "./fixtures/reliability-data";
import { duration } from "../src/lab/data";

const savedRuns: RunSummary[] = RUNS.map((run, index) => ({
  ...run,
  timestamp: `2024-01-0${3 - index}T12:00:00Z`,
  model: index === 0 ? "custom-model-v1" : run.model,
  spec: index < 2 ? "shared-eval" : run.spec,
  skill: index < 2 ? "custom-skill" : run.spec,
  source: index === 1 ? "azure-blob" : "local",
  repetitions: 3,
  aiCredits: index === 0 ? 2.500000001 : run.aiCredits,
}));

function detail(run: RunSummary): RunDetail {
  const candidate = run.id === "run-001";
  return {
    ...run,
    tasks: [
      {
        ...RUN_DETAIL.tasks[0],
        graderResults: RUN_DETAIL.tasks[0]!.graderResults.map((grader) => ({ ...grader })),
        id: "stable-task",
        name: candidate ? "Renamed task" : "Original name",
        weightedScore: candidate ? 0.8 : 0.5,
        ...(candidate
          ? {
              dependencyModes: [
                { kind: "mcp", name: "inventory", mode: "mocked" as const },
                { kind: "mcp", name: "github", mode: "live" as const },
              ],
              commandInvocations: [
                {
                  command: "az",
                  args: ["account", "show"],
                  exit_code: 1,
                  response_index: 0,
                },
                {
                  command: "az",
                  args: ["account", "show"],
                  exit_code: 0,
                  response_index: 1,
                },
                {
                  command: "az",
                  args: ["account", "show"],
                  response_index: 0,
                },
              ],
            }
          : {}),
      },
      {
        name: candidate ? "Added task" : "Removed task",
        id: candidate ? "added" : "removed",
        outcome: "passed",
        score: 0.9,
        weightedScore: 0.9,
        duration: 1,
        graderResults: [],
      },
    ],
  };
}

async function mockResults(
  page: Page,
  runs = savedRuns,
  details: RunDetailWire[] = runs.map(detail),
) {
  const requests: string[] = [];
  await page.route("**/api/v1/lab/runs", (route) =>
    route.fulfill({ json: runs }),
  );
  await page.route(/\/api\/runs\/[^/]+$/, (route) => {
    const id = decodeURIComponent(
      new URL(route.request().url()).pathname.split("/").pop()!,
    );
    requests.push(id);
    const found = details.find((run) => run.id === id);
    return route.fulfill(
      found ? { json: found } : { status: 404, json: { error: "run missing" } },
    );
  });
  return requests;
}

function generationDetail(run: RunSummary, generation: string, score: number): RunDetail {
  return {
    ...detail(run), weightedScore: score, taskCount: 1, passCount: 1,
    tasks: [{
      ...detail(run).tasks[0]!, name: `${run.id} task`, weightedScore: score,
      prompt: `Inspect ${generation}-${run.id}.txt`,
      graderResults: [{
        name: "artifact", type: "file", score, passed: true, weight: 1,
        message: `${generation} ${run.id} grader`,
      }],
      transcript: [
        { type: "ToolExecutionStart", toolCallId: "read", toolName: "read_file", arguments: { path: `${generation}-${run.id}.txt` } },
        { type: "ToolExecutionComplete", toolCallId: "read", toolResult: `${generation} ${run.id} artifact`, success: true },
        { type: "Error", message: `${generation} ${run.id} trajectory` },
      ],
    }],
  };
}

async function mockEventSource(page: Page) {
  await page.addInitScript(() => {
    const sources: {
      url: string;
      closed: boolean;
      readyState: number;
      onopen?: (event: Event) => void;
      onerror?: (event: Event) => void;
      onmessage?: (event: MessageEvent) => void;
    }[] = [];
    Object.defineProperty(window, "EventSource", {
      value: class {
        static CONNECTING = 0;
        static OPEN = 1;
        static CLOSED = 2;
        url: string;
        closed = false;
        readyState = 0;
        onopen?: (event: Event) => void;
        onerror?: (event: Event) => void;
        onmessage?: (event: MessageEvent) => void;
        constructor(url: string) {
          this.url = url;
          sources.push(this);
        }
        close() {
          this.closed = true;
          this.readyState = 2;
        }
      },
    });
    Object.defineProperty(window, "labEmit", {
      value: (payload: unknown, kind = "message") => {
        const active = sources.at(-1);
        if (!active) return;
        if (kind === "open") {
          active.readyState = 1;
          active.onopen?.(new Event("open"));
        } else if (kind === "error" || kind === "closed") {
          active.readyState = kind === "closed" ? 2 : 0;
          active.onerror?.(new Event("error"));
        } else
          active?.onmessage?.(
            new MessageEvent("message", { data: JSON.stringify(payload) }),
          );
      },
    });
    Object.defineProperty(window, "labSources", {
      value: () =>
        sources.map((source) => ({ url: source.url, closed: source.closed })),
    });
  });
}

async function emit(page: Page, payload: unknown, kind?: string) {
  await page.evaluate(
    ({ payload, kind }) => {
      const emit = Reflect.get(window, "labEmit") as (
        payload: unknown,
        kind?: string,
      ) => void;
      emit(payload, kind);
    },
    { payload, kind },
  );
}

test.describe("Real dashboard lab", () => {
  test("duration rounding carries into minutes without changing lab formatting", () => {
    for (const [seconds, label] of [
      [null, "Unavailable"], [0, "0m 0s"], [0.4, "0m 0s"], [0.5, "0m 1s"],
      [12.4, "0m 12s"], [12.5, "0m 13s"], [59.4, "0m 59s"],
      [59.5, "1m 0s"], [59.6, "1m 0s"], [60, "1m 0s"],
      [119.4, "1m 59s"], [119.5, "2m 0s"], [119.6, "2m 0s"], [120, "2m 0s"],
    ] as const) expect(duration(seconds)).toBe(label);
  });

  test("history renders carried minute boundaries from actual API seconds", async ({ page }) => {
    const durations = [59.4, 59.6, 119.4, 119.6];
    await mockResults(page, durations.map((duration, index) => ({
      ...savedRuns[0]!, id: `duration-${index}`, duration,
    })));
    await page.goto("/#/lab/history");
    for (const [index, label] of ["0m 59s", "1m 0s", "1m 59s", "2m 0s"].entries()) {
      const row = page.locator("tbody tr").filter({
        has: page.getByRole("checkbox", { name: `Select duration-${index}`, exact: true }),
      });
      await expect(row.getByText(label, { exact: true })).toBeVisible();
    }
    await expect(page.locator("tbody")).not.toContainText("m 60s");
  });

  for (const trigger of ["manual", "poll", "focus", "reconnect"] as const) {
    test(`${trigger} comparison refresh retains both prior generations on partial failure and retries atomically`, async ({ page }) => {
      await page.clock.install();
      const localRuns = savedRuns.slice(0, 2).map((run, index) => ({
        ...run, source: "local", skill: index ? "baseline-skill" : "candidate-skill",
      }));
      let phase: "initial" | "partial" | "success" = "initial";
      let candidateReplied!: () => void;
      const candidateReady = new Promise<void>((resolve) => { candidateReplied = resolve; });
      const requests: string[] = [];
      await mockResults(page, localRuns);
      await page.route(/\/api\/runs\/[^/]+$/, async (route) => {
        const run = localRuns.find((run) => route.request().url().endsWith(`/${run.id}`));
        if (!run) throw new Error("Unexpected comparison run request");
        requests.push(run.id);
        if (phase === "partial" && run.id === "run-002") {
          await candidateReady;
          return route.fulfill({ status: 503, json: { error: "baseline generation unavailable" } });
        }
        const generation = phase === "initial" ? "A" : "B";
        const score = run.id === "run-001" ? (generation === "A" ? 0.8 : 0.9) : (generation === "A" ? 0.5 : 0.7);
        await route.fulfill({ json: generationDetail(run, generation, score) });
        if (phase === "partial" && run.id === "run-001") candidateReplied();
      });
      await page.goto("/#/lab/compare");
      const matrix = page.locator(".lab-matrix tbody tr");
      await expect(matrix.locator("td").nth(1)).toHaveText("50.0%");
      await expect(matrix.locator("td").nth(2)).toHaveText("80.0%");
      const initialInspector = page.waitForResponse("**/api/runs/run-001");
      await page.getByRole("button", { name: "run-001 task", exact: true }).click();
      await initialInspector;
      const mounted = await page.getByRole("dialog").elementHandle();
      if (!mounted) throw new Error("Comparison inspector did not mount");
      await expect(page.getByRole("dialog").getByText("A run-001 grader", { exact: true })).toBeVisible();
      const beforeRefresh = requests.length;
      phase = "partial";
      if (trigger === "manual") {
        await page.getByRole("button", { name: "Refresh results", exact: true, includeHidden: true })
          .evaluate((element: HTMLButtonElement) => element.click());
      } else if (trigger === "poll") {
        await page.clock.fastForward(15_000);
      } else {
        if (trigger === "focus") await page.clock.setSystemTime(new Date("2030-01-01T00:00:00Z"));
        await page.evaluate((trigger) => {
          if (trigger === "focus") document.dispatchEvent(new Event("visibilitychange", { bubbles: true }));
          else {
            window.dispatchEvent(new Event("offline"));
            window.dispatchEvent(new Event("online"));
          }
        }, trigger);
      }
      const warning = page.locator(".lab-root [role='alert']").filter({ hasText: "Previously loaded comparison" });
      await expect(warning).toContainText("503");
      expect(requests.slice(beforeRefresh).sort()).toEqual(["run-001", "run-002"]);
      expect(await mounted.evaluate((element) => element.isConnected)).toBe(true);
      await expect(matrix.locator("td").nth(1)).toHaveText("50.0%");
      await expect(matrix.locator("td").nth(2)).toHaveText("80.0%");
      await expect(page.getByText("+30.0 pp", { exact: true })).toHaveCount(2);
      const assertEvidence = async (id: string, generation: string) => {
        const dialog = page.getByRole("dialog");
        await expect(dialog.getByText(`${generation} ${id} grader`, { exact: true })).toBeVisible();
        await dialog.getByRole("tab", { name: "Trajectory", exact: true }).click();
        if (id === "run-001") {
          await expect(dialog.getByText("az attempt 1 · exit 1 · response index 0", { exact: true })).toBeVisible();
          await expect(dialog.getByText("az attempt 2 · exit 0 · response index 1", { exact: true })).toBeVisible();
          await expect(dialog.getByText("az attempt 3 · exit unavailable · response index 0", { exact: true })).toBeVisible();
        }
        await dialog.getByRole("button", { name: "Events", exact: true }).click();
        await expect(dialog.getByText(`${generation} ${id} trajectory`, { exact: true })).toBeVisible();
        await dialog.getByRole("button", { name: "Show details", exact: true }).first().click();
        await dialog.getByRole("button", { name: "Arguments", exact: true }).click();
        await expect(dialog.locator(".lab-recorded-trajectory pre").filter({ hasText: `"${generation}-${id}.txt"` })).toBeVisible();
        await dialog.getByRole("button", { name: "Result", exact: true }).click();
        await expect(dialog.getByText(`${generation} ${id} artifact`, { exact: true })).toBeVisible();
        await dialog.getByRole("tab", { name: "Configuration", exact: true }).click();
        const configuration = JSON.parse(await dialog.locator("pre").innerText());
        expect(configuration.prompt).toBe(`Inspect ${generation}-${id}.txt`);
        if (id === "run-001") {
          expect(configuration.dependency_modes).toEqual([
            { kind: "mcp", name: "inventory", mode: "mocked" },
            { kind: "mcp", name: "github", mode: "live" },
          ]);
        }
        await page.keyboard.press("Escape");
      };
      await assertEvidence("run-001", "A");
      await page.getByRole("link", { name: "Run history", exact: true }).click();
      await page.getByRole("button", { name: "baseline-skill", exact: true }).click();
      await assertEvidence("run-002", "A");
      await page.getByRole("link", { name: "Compare", exact: true }).click();
      await expect(page.getByText("+30.0 pp", { exact: true })).toHaveCount(2);
      phase = "success";
      await page.getByRole("button", { name: "Refresh results", exact: true }).click();
      await expect(matrix.locator("td").nth(1)).toHaveText("70.0%");
      await expect(matrix.locator("td").nth(2)).toHaveText("90.0%");
      await expect(page.getByText("+20.0 pp", { exact: true })).toHaveCount(2);
      await expect(page.getByRole("alert")).toHaveCount(0);
      await page.getByRole("button", { name: "run-001 task", exact: true }).click();
      await assertEvidence("run-001", "B");
      await page.getByRole("link", { name: "Run history", exact: true }).click();
      await page.getByRole("button", { name: "baseline-skill", exact: true }).click();
      await assertEvidence("run-002", "B");
    });
  }

  test("a failed initial remote comparison waits for explicit retry, including after reconnect", async ({ page }) => {
    await page.clock.install();
    const remoteRuns = savedRuns.slice(0, 2).map((run) => ({ ...run, source: "azure-blob" }));
    let ready = false;
    let requests = 0;
    await mockResults(page, remoteRuns);
    await page.route(/\/api\/runs\/[^/]+$/, (route) => {
      requests++;
      const run = remoteRuns.find((run) => route.request().url().endsWith(`/${run.id}`));
      if (!run) throw new Error("Unexpected remote comparison run");
      return route.fulfill(ready ? { json: detail(run) } : { status: 503 });
    });
    await page.goto("/#/lab/compare");
    await expect(page.getByRole("alert")).toContainText("Comparison evidence could not be loaded");
    await expect(page.locator(".lab-matrix")).toHaveCount(0);
    const before = requests;
    await page.clock.fastForward(60_000);
    await page.evaluate(() => {
      document.dispatchEvent(new Event("visibilitychange", { bubbles: true }));
      window.dispatchEvent(new Event("offline"));
      window.dispatchEvent(new Event("online"));
    });
    await page.waitForTimeout(100);
    expect(requests).toBe(before);
    ready = true;
    await page.getByRole("button", { name: "Retry comparison", exact: true }).click();
    await expect(page.getByText("+30.0 pp", { exact: true })).toHaveCount(2);
    expect(requests).toBe(before + 2);
  });

  test("a late canceled refresh cannot replace a newly selected comparison or its inspector", async ({ page }) => {
    const localRuns = [
      ...savedRuns.slice(0, 2).map((run) => ({ ...run, source: "local" })),
      { ...savedRuns[0]!, id: "run-004", model: "third-model", source: "local", timestamp: "2024-01-01T12:00:00Z" },
    ];
    let phase: "initial" | "held" | "current" = "initial";
    let heldCount = 0;
    let release!: () => void;
    let settled = 0;
    const gate = new Promise<void>((resolve) => { release = resolve; });
    await mockResults(page, localRuns);
    await page.route(/\/api\/runs\/[^/]+$/, async (route) => {
      const run = localRuns.find((run) => route.request().url().endsWith(`/${run.id}`));
      if (!run) throw new Error("Unexpected selected comparison run");
      const requestPhase = phase;
      if (requestPhase === "held") {
        heldCount++;
        await gate;
      }
      const score = requestPhase === "held" ? 0.1 : run.id === "run-004" ? 0.9 : run.id === "run-001" ? 0.8 : 0.5;
      await route.fulfill({ json: generationDetail(run, requestPhase, score) });
      if (requestPhase === "held") settled++;
    });
    try {
      await page.goto("/#/lab/compare");
      await expect(page.getByText("+30.0 pp", { exact: true })).toHaveCount(2);
      phase = "held";
      await page.getByRole("button", { name: "Refresh results", exact: true }).click();
      await expect.poll(() => heldCount).toBe(2);
      phase = "current";
      await page.getByRole("combobox", { name: "Candidate run", exact: true }).selectOption("run-004");
      await expect(page.getByRole("combobox", { name: "Baseline run", exact: true })).toHaveValue("run-001");
      await expect(page.getByText("+10.0 pp", { exact: true })).toHaveCount(2);
      await page.getByRole("button", { name: "run-004 task", exact: true }).click();
      const dialog = page.getByRole("dialog");
      await expect(dialog.getByText("current run-004 grader", { exact: true })).toBeVisible();
      release();
      await expect.poll(() => settled).toBe(2);
      await expect(dialog.getByText("current run-004 grader", { exact: true })).toBeVisible();
      await expect(dialog.getByText(/held .* grader/)).toHaveCount(0);
      await page.keyboard.press("Escape");
      await expect(page.getByText("+10.0 pp", { exact: true })).toHaveCount(2);
      await expect(page.locator(".lab-matrix tbody tr td").nth(1)).toHaveText("80.0%");
      await expect(page.locator(".lab-matrix tbody tr td").nth(2)).toHaveText("90.0%");
      await page.getByRole("link", { name: "Run history", exact: true }).click();
      const row = page.locator("tbody tr").filter({
        has: page.getByRole("checkbox", { name: "Select run-001", exact: true }),
      });
      await row.getByRole("button", { name: "custom-skill", exact: true }).click();
      await expect(dialog.getByText("current run-001 grader", { exact: true })).toBeVisible();
    } finally {
      release();
    }
  });

  test("keeps candidate selection available for singleton suites and filters baselines", async ({
    page,
  }) => {
    const singleton = { ...savedRuns[2]!, timestamp: "2024-01-04T12:00:00Z" };
    await mockResults(page, [singleton, ...savedRuns.slice(0, 2)]);
    await page.goto("/#/lab/compare");
    const candidate = page.getByRole("combobox", { name: "Candidate run" });
    const baseline = page.getByRole("combobox", { name: "Baseline run" });
    await expect(candidate).toHaveValue(singleton.id);
    await expect(baseline).toBeDisabled();
    await expect(page.getByText("Two runs from the same eval are needed")).toBeVisible();
    await candidate.selectOption("run-001");
    await expect(baseline).toBeEnabled();
    await expect(baseline.locator("option")).toHaveCount(1);
    await expect(baseline).toHaveValue("run-002");
    await expect(page.getByText("1 comparable tasks")).toBeVisible();
    await candidate.selectOption(singleton.id);
    await expect(baseline).toBeDisabled();
    await expect(page.locator(".lab-matrix")).toHaveCount(0);
    await candidate.selectOption("run-002");
    await expect(baseline).toHaveValue("run-001");
    await expect(baseline.locator("option")).toHaveCount(1);
    await expect(page.getByText("1 comparable tasks")).toBeVisible();
  });

  test("manual refresh updates same-ID same-timestamp comparison and preserves failed refresh evidence", async ({
    page,
  }) => {
    await mockResults(page);
    await page.goto("/#/lab/compare");
    await expect(page.getByText("+30.0 pp", { exact: true })).toHaveCount(2);
    const regraded = detail(savedRuns[0]!);
    regraded.tasks[0]!.weightedScore = 0.6;
    await page.route("**/api/runs/run-001", (route) =>
      route.fulfill({ json: regraded }),
    );
    await page.getByRole("button", { name: "Refresh results" }).click();
    await expect(page.getByText("+10.0 pp", { exact: true })).toHaveCount(2);
    await expect(page.getByText("+30.0 pp", { exact: true })).toHaveCount(0);
    await page.route("**/api/v1/lab/runs", (route) =>
      route.fulfill({ status: 500 }),
    );
    await page.getByRole("button", { name: "Refresh results" }).click();
    await expect(page.getByRole("alert")).toContainText("Previously loaded results");
    await expect(page.getByText("+10.0 pp", { exact: true })).toHaveCount(2);
    await page.route("**/api/v1/lab/runs", (route) =>
      route.fulfill({ json: savedRuns }),
    );
    await page.route("**/api/runs/run-001", (route) =>
      route.fulfill({ status: 503 }),
    );
    await page.getByRole("button", { name: "Refresh results" }).click();
    await expect(page.getByRole("alert")).toContainText("Previously loaded comparison");
    await expect(page.getByText("+10.0 pp", { exact: true })).toHaveCount(2);
  });

  for (const fallback of ["failed", "first"] as const) {
    test(`mounted inspector retains valid tasks and resolves a removed task to the ${fallback} fallback on manual refresh`, async ({
      page,
    }) => {
      const base = detail(savedRuns[0]!);
      const template = base.tasks[0]!;
      const recordedTask = (id: string, name: string, outcome: string) => ({
        ...template,
        id,
        name,
        outcome,
        prompt: `Inspect ${id}-artifact.txt`,
        graderResults: [{
          name: `${id}-artifact`,
          type: "file",
          passed: outcome === "pass",
          score: outcome === "pass" ? 1 : 0,
          weight: 1,
          message: `Verified ${id}-artifact.txt`,
        }],
        transcript: [
          { type: "ToolExecutionStart", toolCallId: `${id}-call`, toolName: "read_file", arguments: { path: `${id}-artifact.txt` } },
          { type: "ToolExecutionComplete", toolCallId: `${id}-call`, toolResult: `Recorded ${id}-artifact contents`, success: true },
          { type: "Error", message: `${id} recorded trajectory` },
        ],
      });
      const selected = recordedTask("selected-task", "Selected task", "pass");
      const first = recordedTask("remaining-first", "Remaining first task", "pass");
      const second = recordedTask("remaining-second", "Remaining second task", fallback === "failed" ? "fail" : "pass");
      let current: RunDetail = {
        ...base, weightedScore: undefined, tasks: [first, selected], taskCount: 2, passCount: 2,
      };
      await mockResults(page, [current], [current]);
      await page.route("**/api/v1/lab/runs", (route) => route.fulfill({ json: [current] }));
      await page.route("**/api/runs/run-001", (route) => route.fulfill({ json: current }));
      await page.goto("/#/lab/history");
      await page.getByRole("button", { name: "custom-skill", exact: true }).click();
      const dialog = page.getByRole("dialog");
      const select = dialog.getByRole("combobox", { name: "Task", exact: true });
      await select.selectOption("id:selected-task");
      await expect(dialog.getByText("Verified selected-task-artifact.txt", { exact: true })).toBeVisible();
      const mounted = await dialog.elementHandle();
      if (!mounted) throw new Error("Inspector did not mount");
      const refresh = async () => {
        const button = page.getByRole("button", { name: "Refresh results", exact: true, includeHidden: true });
        await expect(button).toBeEnabled();
        // The modal makes the archive button inert to pointer clicks; invoke its real refresh action.
        await button.evaluate((element: HTMLButtonElement) => element.click());
      };
      current = {
        ...current,
        tasks: [second, {
          ...selected,
          graderResults: [{
            ...selected.graderResults[0]!,
            message: "Refreshed selected-task-artifact.txt",
          }],
        }, first],
        taskCount: 3,
        passCount: fallback === "failed" ? 2 : 3,
        outcome: fallback === "failed" ? "fail" : "pass",
      };
      await refresh();
      await expect(dialog.getByText("Refreshed selected-task-artifact.txt", { exact: true })).toBeVisible();
      await expect(select).toHaveValue("id:selected-task");
      current = {
        ...current,
        tasks: [first, second],
        taskCount: 2,
        passCount: fallback === "failed" ? 1 : 2,
      };
      await refresh();
      const expected = fallback === "failed" ? second : first;
      await expect(select).toHaveValue(`id:${expected.id}`);
      await expect(select.locator("option:checked")).toContainText(expected.name);
      await expect(dialog.getByText(`Verified ${expected.id}-artifact.txt`, { exact: true })).toBeVisible();
      await expect(dialog.getByText("Refreshed selected-task-artifact.txt", { exact: true })).toHaveCount(0);
      expect(current.id).toBe(base.id);
      expect(current.timestamp).toBe(base.timestamp);
      expect(await dialog.evaluate((element, original) => element === original, mounted)).toBe(true);
      await mounted.dispose();
      await dialog.getByRole("tab", { name: "Trajectory", exact: true }).click();
      await dialog.getByRole("button", { name: "Events", exact: true }).click();
      await expect(dialog.getByText(`${expected.id} recorded trajectory`, { exact: true })).toBeVisible();
      await dialog.getByRole("tab", { name: "Configuration", exact: true }).click();
      expect(JSON.parse(await dialog.locator("pre").innerText()).prompt).toBe(expected.prompt);
      current = { ...current, tasks: [], taskCount: 0, passCount: 0, outcome: "pass" };
      await refresh();
      await expect(select).toBeDisabled();
      await expect(select).toHaveValue("");
      await expect(select.locator("option")).toHaveCount(0);
      await expect(dialog.getByText("No recorded tasks in this result.", { exact: true })).toBeVisible();
      const configuration = JSON.parse(await dialog.locator("pre").innerText());
      expect(Object.hasOwn(configuration, "prompt")).toBe(false);
      await dialog.getByRole("tab", { name: "Grader evidence", exact: true }).click();
      await expect(dialog.locator(".lab-grader")).toHaveCount(0);
    });
  }

  for (const fallback of ["failed", "first"] as const) {
    test(`mounted inspector persists the ${fallback} fallback when a removed task returns and after empty reload`, async ({ page }) => {
      const summary = { ...savedRuns[0]!, source: "local" };
      const recorded = (id: string, outcome: string, score: number) => ({
        ...generationDetail(summary, id, score).tasks[0]!, id, name: id, outcome,
      });
      const first = recorded("remaining-first", "pass", 0.4);
      const second = recorded("remaining-second", fallback === "failed" ? "fail" : "pass", 0.2);
      const selected = recorded("selected-task", "pass", 0.8);
      const expected = fallback === "failed" ? second : first;
      let current: RunDetail = { ...detail(summary), weightedScore: undefined, tasks: [], taskCount: 0, passCount: 0 };
      const replaceTasks = (tasks: RunDetail["tasks"]) => {
        const passCount = tasks.filter((task) => task.outcome === "pass").length;
        current = { ...current, tasks, taskCount: tasks.length, passCount, outcome: passCount === tasks.length ? "pass" : "fail" };
      };
      replaceTasks([first, selected, second]);
      let release!: () => void;
      const ready = new Promise<void>((resolve) => { release = resolve; });
      await mockResults(page, [summary]);
      await page.route("**/api/v1/lab/runs", (route) => route.fulfill({ json: [current] }));
      await page.route("**/api/runs/run-001", async (route) => {
        await ready;
        await route.fulfill({ json: current });
      });
      try {
        await page.goto("/#/lab/history");
        await page.getByRole("button", { name: "custom-skill", exact: true }).click();
        const dialog = page.getByRole("dialog");
        await expect(dialog.getByText("Loading task evidence...", { exact: true })).toBeVisible();
        release();
        const select = dialog.getByRole("combobox", { name: "Task", exact: true });
        await expect(select).toHaveValue(`id:${expected.id}`);
        await select.selectOption("id:selected-task");
        await expect(dialog.getByText("selected-task run-001 grader", { exact: true })).toBeVisible();
        const mounted = await dialog.elementHandle();
        if (!mounted) throw new Error("Inspector did not mount");
        const refresh = async () => {
          const button = page.getByRole("button", { name: "Refresh results", exact: true, includeHidden: true });
          await expect(button).toBeEnabled();
          await button.evaluate((element: HTMLButtonElement) => element.click());
        };
        replaceTasks([first, second]);
        await refresh();
        await expect(select).toHaveValue(`id:${expected.id}`);
        await expect(dialog.getByText(`${expected.id} run-001 grader`, { exact: true })).toBeVisible();
        replaceTasks([selected, first, second]);
        await refresh();
        await expect(select.locator('option[value="id:selected-task"]')).toHaveCount(1);
        await expect(select).toHaveValue(`id:${expected.id}`);
        await expect(select.locator("option:checked")).toContainText(expected.name);
        await expect(dialog.getByText(`${expected.id} run-001 grader`, { exact: true })).toBeVisible();
        await select.selectOption("id:selected-task");
        await expect(dialog.getByText("selected-task run-001 grader", { exact: true })).toBeVisible();
        replaceTasks([first, second, selected]);
        await refresh();
        await expect(select.locator("option").last()).toHaveAttribute("value", "id:selected-task");
        await expect(select).toHaveValue("id:selected-task");
        replaceTasks([]);
        await refresh();
        await expect(select).toHaveValue("");
        await expect(select).toBeDisabled();
        await expect(select.locator("option")).toHaveCount(0);
        replaceTasks([first, second, selected]);
        await refresh();
        await expect(select.locator("option")).toHaveCount(3);
        await expect(select).toHaveValue(`id:${expected.id}`);
        await expect(dialog.getByText(`${expected.id} run-001 grader`, { exact: true })).toBeVisible();
        expect(current.id).toBe(summary.id);
        expect(current.timestamp).toBe(summary.timestamp);
        expect(await dialog.evaluate((element, original) => element === original, mounted)).toBe(true);
        await mounted.dispose();
      } finally {
        release();
      }
    });
  }

  test("summary polling refreshes shared comparison and inspector details once per active run", async ({
    page,
  }) => {
    await page.clock.install();
    const localRuns = savedRuns.map((run) => ({ ...run, source: "local" }));
    const requests = await mockResults(page, localRuns);
    await page.goto("/#/lab/compare");
    await expect(page.getByText("+30.0 pp", { exact: true })).toHaveCount(2);
    await expect(page.locator(".lab-local-indicator small")).toHaveText("Refreshes every 15 seconds");
    const inspectorResponse = page.waitForResponse("**/api/runs/run-001");
    await page.getByRole("button", { name: "Renamed task", exact: true }).click();
    await inspectorResponse;
    const dialog = page.getByRole("dialog");
    await expect(dialog.getByText("Output file found")).toBeVisible();
    const initialRequests = requests.length;
    const regraded = detail(savedRuns[0]!);
    regraded.tasks[0]!.weightedScore = 0.4;
    regraded.tasks[0]!.graderResults[0]!.message = "Regraded recorded evidence";
    await page.route("**/api/runs/run-001", (route) => {
      requests.push("run-001");
      return route.fulfill({ json: regraded });
    });
    await page.clock.fastForward(15_000);
    await expect(dialog.getByText("Regraded recorded evidence")).toBeVisible();
    await expect(dialog.getByText(/Weighted score 40.0%/)).toBeVisible();
    expect(requests.length).toBe(initialRequests + 2);
    expect(requests.slice(initialRequests).sort()).toEqual(["run-001", "run-002"]);
    await page.route("**/api/v1/lab/runs", (route) =>
      route.fulfill({ status: 500 }),
    );
    await page.clock.fastForward(15_000);
    await expect(page.locator(".lab-root [role='alert']")).toContainText("Previously loaded results");
    await expect(dialog.getByText("Regraded recorded evidence")).toBeVisible();
    expect(requests.length).toBe(initialRequests + 2);
  });

  test("remote archives do not poll or refetch mounted details until explicit refresh", async ({
    page,
  }) => {
    await page.clock.install();
    const remoteRuns = savedRuns.map((run) => ({ ...run, source: "azure-blob" }));
    let listings = 0;
    const requests = await mockResults(page, remoteRuns);
    // Keep the counting route newest without altering the result payload.
    await page.route("**/api/v1/lab/runs", (route) => {
      listings++;
      return route.fulfill({ json: remoteRuns });
    });
    await page.goto("/#/lab/compare");
    await expect(page.getByText("+30.0 pp", { exact: true })).toHaveCount(2);
    await expect(page.getByText(/remote archive: refresh on demand/)).toBeVisible();
    await expect(page.locator(".lab-local-indicator small")).toHaveText("Refresh on demand");
    const initialDetails = requests.length;
    const initialListings = listings;
    expect(initialListings).toBeGreaterThan(0);
    await page.clock.fastForward(60_000);
    await page.evaluate(() => {
      document.dispatchEvent(new Event("visibilitychange", { bubbles: true }));
      window.dispatchEvent(new Event("offline"));
      window.dispatchEvent(new Event("online"));
    });
    await page.waitForTimeout(100);
    expect(listings).toBe(initialListings);
    expect(requests.length).toBe(initialDetails);
    await page.getByRole("button", { name: "Refresh results" }).click();
    await expect.poll(() => listings).toBe(initialListings + 1);
    await expect.poll(() => requests.length).toBe(initialDetails + 2);
  });

  test("filters multiple evaluation suites of one skill independently", async ({
    page,
  }) => {
    await mockResults(page, [
      { ...savedRuns[0]!, skill: "shared-skill", spec: "suite-one" },
      { ...savedRuns[1]!, skill: "shared-skill", spec: "suite-two" },
      { ...savedRuns[2]!, skill: "other-skill", spec: "scenario-suite" },
    ]);
    await page.goto("/#/lab/history");
    const skill = page.getByRole("combobox", { name: "Filter skill" });
    const evaluation = page.getByRole("combobox", { name: "Filter eval" });
    await skill.selectOption("shared-skill");
    await expect(page.locator("tbody tr")).toHaveCount(2);
    await evaluation.selectOption("suite-one");
    await expect(page.locator("tbody tr")).toHaveCount(1);
    await expect(page.getByRole("checkbox", { name: "Select run-001", exact: true })).toBeVisible();
    await evaluation.selectOption("suite-two");
    await expect(page.locator("tbody tr")).toHaveCount(1);
    await expect(page.getByRole("checkbox", { name: "Select run-002", exact: true })).toBeVisible();
    await expect(page.locator("tbody")).toContainText("azure-blob");
    await evaluation.selectOption("");
    await expect(page.locator("tbody tr")).toHaveCount(2);
    await page.getByRole("combobox", { name: "Filter model" }).selectOption("custom-model-v1");
    await expect(page.locator("tbody tr")).toHaveCount(1);
    await evaluation.selectOption("suite-one");
    await page.getByRole("button", { name: "Reset filters" }).click();
    await expect(evaluation).toHaveValue("");
    await expect(skill).toHaveValue("all");
    await expect(page.locator("tbody tr")).toHaveCount(3);
    await evaluation.selectOption("scenario-suite");
    await expect(page.getByRole("checkbox", { name: "Select run-003", exact: true })).toBeVisible();
    await expect(page.locator("tbody tr")).toHaveCount(1);
  });

  test("eval filters distinguish overlapping skill names, absent skills and an eval named all", async ({
    page,
  }) => {
    await mockResults(page, [
      { ...savedRuns[0]!, skill: "shared-identity", spec: "first-eval" },
      { ...savedRuns[1]!, skill: "first-eval", spec: "shared-identity" },
      { ...savedRuns[2]!, skill: undefined, spec: "eval-without-skill" },
      { ...savedRuns[0]!, id: "run-004", skill: "sentinel-skill", spec: "all" },
    ]);
    await page.goto("/#/lab/history");
    const skill = page.getByRole("combobox", { name: "Filter skill" });
    const evaluation = page.getByRole("combobox", { name: "Filter eval" });
    await evaluation.selectOption("shared-identity");
    await expect(page.getByRole("checkbox", { name: "Select run-002", exact: true })).toBeVisible();
    await expect(page.locator("tbody tr")).toHaveCount(1);
    await skill.selectOption("shared-identity");
    await expect(page.locator("tbody tr")).toHaveCount(0);
    await evaluation.selectOption("");
    await expect(page.getByRole("checkbox", { name: "Select run-001", exact: true })).toBeVisible();
    await expect(page.locator("tbody tr")).toHaveCount(1);
    await page.getByRole("button", { name: "Reset filters" }).click();
    await evaluation.selectOption("eval-without-skill");
    await expect(page.getByRole("checkbox", { name: "Select run-003", exact: true })).toBeVisible();
    await expect(page.locator("tbody tr")).toHaveCount(1);
    await evaluation.selectOption("all");
    await expect(page.getByRole("checkbox", { name: "Select run-004", exact: true })).toBeVisible();
    await expect(page.locator("tbody tr")).toHaveCount(1);
    await page.getByRole("link", { name: "Reports", exact: true }).click();
    await expect(page.locator(".lab-report-filters")).toContainText("Eval: all");
    const download = page.waitForEvent("download");
    await page.getByRole("button", { name: "Download JSON" }).click();
    const path = await (await download).path();
    const report = JSON.parse(await readFile(path!, "utf8"));
    expect(report.filters.evalId).toBe("all");
    expect(report.runs.map((run: RunDetail) => run.id)).toEqual(["run-004"]);
    expect(report.runs[0].spec).toBe("all");
  });

  test("history search indexes actual eval identity independently of skill and eval selection", async ({
    page,
  }) => {
    await mockResults(page, [
      { ...savedRuns[0]!, skill: "distinct-skill", spec: "actual-eval-name" },
      { ...savedRuns[1]!, skill: "another-skill", spec: "other-eval" },
    ]);
    await page.goto("/#/lab/history");
    const search = page.getByRole("textbox", { name: "Search run history" });
    const evaluation = page.getByRole("combobox", { name: "Filter eval" });
    await search.fill("AcTuAl-EvAl-NaMe");
    await expect(page.locator("tbody tr")).toHaveCount(1);
    await expect(page.getByRole("checkbox", { name: "Select run-001", exact: true })).toBeVisible();
    await expect(evaluation).toHaveValue("");
    await expect(page.getByRole("combobox", { name: "Filter skill" })).toHaveValue("all");
    await evaluation.selectOption("other-eval");
    await expect(page.locator("tbody tr")).toHaveCount(0);
    await search.fill("");
    await expect(page.getByRole("checkbox", { name: "Select run-002", exact: true })).toBeVisible();
    await evaluation.selectOption("");
    await expect(page.locator("tbody tr")).toHaveCount(2);
    await search.fill("DISTINCT-SKILL");
    await expect(page.getByRole("checkbox", { name: "Select run-001", exact: true })).toBeVisible();
    await expect(page.locator("tbody tr")).toHaveCount(1);
    await search.fill("azure-blob");
    await expect(page.getByRole("checkbox", { name: "Select run-002", exact: true })).toBeVisible();
    await expect(page.locator("tbody tr")).toHaveCount(1);
  });

  for (const missing of ["absent", "blank"] as const) {
    test(`${missing} skill metadata stays distinct from an authored Unavailable skill`, async ({
      page,
    }) => {
      const evalId = `legacy-${missing}-eval`;
      await mockResults(page, [
        { ...savedRuns[0]!, skill: missing === "blank" ? "" : undefined, spec: evalId },
        { ...savedRuns[1]!, skill: "Unavailable", spec: "authored-skill-eval" },
      ]);
      await page.goto("/#/lab/history");
      const skill = page.getByRole("combobox", { name: "Filter skill" });
      const evaluation = page.getByRole("combobox", { name: "Filter eval" });
      await expect(page.locator("tbody tr")).toHaveCount(2);
      await expect(skill.locator("option")).toHaveText(["All skills", "Unavailable"]);
      const legacyRow = page.locator("tbody tr").filter({
        has: page.getByRole("checkbox", { name: "Select run-001", exact: true }),
      });
      await expect(legacyRow.getByRole("button", { name: "Unavailable", exact: true })).toBeVisible();
      await skill.selectOption("Unavailable");
      await expect(page.locator("tbody tr")).toHaveCount(1);
      await expect(page.getByRole("checkbox", { name: "Select run-002", exact: true })).toBeVisible();
      await expect(page.getByRole("checkbox", { name: "Select run-001", exact: true })).toHaveCount(0);
      await page.getByRole("button", { name: "Unavailable", exact: true }).click();
      const dialog = page.getByRole("dialog");
      await dialog.getByRole("tab", { name: "Configuration", exact: true }).click();
      expect(JSON.parse(await dialog.locator("pre").innerText()).skill).toBe("Unavailable");
      await page.keyboard.press("Escape");
      await page.getByRole("button", { name: "Reset filters" }).click();
      await page.getByRole("textbox", { name: "Search run history" }).fill(evalId.toUpperCase());
      await expect(page.locator("tbody tr")).toHaveCount(1);
      await expect(evaluation).toHaveValue("");
      await legacyRow.getByRole("button", { name: "Unavailable", exact: true }).click();
      await expect(dialog.getByRole("heading", { name: "Unavailable", exact: true })).toBeVisible();
      await dialog.getByRole("tab", { name: "Configuration", exact: true }).click();
      const configuration = JSON.parse(await dialog.locator("pre").innerText());
      expect(configuration.eval).toBe(evalId);
      expect(Object.hasOwn(configuration, "skill")).toBe(missing === "blank");
      if (missing === "blank") expect(configuration.skill).toBe("");
      await page.keyboard.press("Escape");
      await evaluation.selectOption(evalId);
      await expect(skill).toHaveValue("all");
      await page.getByRole("link", { name: "Reports", exact: true }).click();
      const download = page.waitForEvent("download");
      await page.getByRole("button", { name: "Download JSON" }).click();
      const path = await (await download).path();
      const report = JSON.parse(await readFile(path!, "utf8"));
      expect(report.filters.skill).toBe("all");
      expect(report.runs.map((run: RunDetail) => run.id)).toEqual(["run-001"]);
      expect(report.runs[0].spec).toBe(evalId);
      expect(Object.hasOwn(report.runs[0], "skill")).toBe(missing === "blank");
      if (missing === "blank") expect(report.runs[0].skill).toBe("");
    });
  }

  test("unskilled suites explore by actual eval and retain stable comparison and activity labels", async ({
    page,
  }) => {
    await mockEventSource(page);
    await mockResults(page, [
      { ...savedRuns[0]!, skill: undefined, spec: "legacy-suite" },
      { ...savedRuns[1]!, skill: undefined, spec: "legacy-suite" },
      { ...savedRuns[2]!, skill: undefined, spec: "another-suite" },
    ]);
    await page.goto("/#/lab/catalog");
    const skill = page.getByRole("combobox", { name: "Filter skill" });
    await expect(skill.locator("option")).toHaveText(["All skills"]);
    await page.getByRole("button", { name: /legacy-suite/ }).click();
    await expect(page.getByRole("combobox", { name: "Filter eval" })).toHaveValue("legacy-suite");
    await expect(skill).toHaveValue("all");
    await expect(page.locator("tbody tr")).toHaveCount(2);
    await page.getByRole("link", { name: "Compare", exact: true }).click();
    await expect(page.getByText("+30.0 pp", { exact: true })).toHaveCount(2);
    await expect(page.getByRole("combobox", { name: "Baseline run" }).locator("option")).toHaveText([/^Unavailable ·/]);
    await expect(page.getByRole("combobox", { name: "Candidate run" }).locator("option")).toHaveText([/^Unavailable ·/, /^Unavailable ·/]);
    await page.getByRole("link", { name: "Current runs", exact: true }).click();
    await expect(page.getByRole("combobox", { name: "Activity run" }).locator("option")).toHaveText([/^Unavailable ·/, /^Unavailable ·/]);
  });

  test("sidebar waits for archive evidence before reporting a refresh policy", async ({
    page,
  }) => {
    const remote = { ...savedRuns[0]!, source: "azure-blob" };
    await mockResults(page, [remote]);
    let release!: () => void;
    const ready = new Promise<void>((resolve) => { release = resolve; });
    await page.route("**/api/v1/lab/runs", async (route) => {
      await ready;
      return route.fulfill({ json: [remote] });
    });
    try {
      await page.goto("/#/lab/history");
      await expect(page.locator(".lab-local-indicator small")).toHaveText("Loading refresh policy...");
      await expect(page.locator(".lab-local-indicator")).not.toContainText("Refreshes every 15 seconds");
      release();
      await expect(page.locator(".lab-local-indicator small")).toHaveText("Refresh on demand");
    } finally {
      release();
    }
  });

  test("inspector tabs implement roving focus and labelled keyboard selection", async ({
    page,
  }) => {
    await mockResults(page);
    await page.goto("/#/lab/history");
    await page.getByRole("button", { name: "custom-skill", exact: true }).first().click();
    const dialog = page.getByRole("dialog");
    const graders = dialog.getByRole("tab", { name: "Grader evidence" });
    const conversation = dialog.getByRole("tab", { name: "Conversation", exact: true });
    const trajectory = dialog.getByRole("tab", { name: "Trajectory", exact: true });
    const configuration = dialog.getByRole("tab", { name: "Configuration" });
    const evidence = dialog.getByRole("tab", { name: "Recorded evidence" });
    await expect(graders).toHaveAttribute("tabindex", "0");
    await expect(conversation).toHaveAttribute("tabindex", "-1");
    await expect(trajectory).toHaveAttribute("tabindex", "-1");
    await expect(configuration).toHaveAttribute("tabindex", "-1");
    await expect(evidence).toHaveAttribute("tabindex", "-1");
    await graders.focus();
    for (const [key, selected, name] of [
      ["ArrowRight", conversation, "Conversation"],
      ["ArrowRight", trajectory, "Trajectory"],
      ["End", evidence, "Recorded evidence"],
      ["ArrowRight", graders, "Grader evidence"],
      ["ArrowLeft", evidence, "Recorded evidence"],
      ["Home", graders, "Grader evidence"],
    ] as const) {
      await page.keyboard.press(key);
      await expect(selected).toBeFocused();
      await expect(selected).toHaveAttribute("aria-selected", "true");
      await expect(dialog.getByRole("tab", { selected: true })).toHaveCount(1);
      await expect(dialog.locator('[role="tab"][tabindex="0"]')).toHaveCount(1);
      const id = await selected.getAttribute("id");
      await expect(dialog.getByRole("tabpanel", { name, exact: true })).toHaveAttribute("aria-labelledby", id!);
      await expect(selected).toHaveAttribute("aria-controls", "lab-evidence-panel");
    }
    await page.keyboard.press("Escape");
    await expect(dialog).not.toBeVisible();
  });

  for (const nullable of ["tasks", "graderResults"] as const) {
    test(`nullable ${nullable} remain empty evidence in inspector and JSON reports`, async ({
      page,
    }) => {
      const summary = {
        ...savedRuns[0]!,
        weightedScore: undefined,
        taskCount: nullable === "tasks" ? 0 : 1,
        passCount: nullable === "tasks" ? 0 : 1,
      };
      const wire: RunDetailWire = {
        ...detail(summary),
        weightedScore: undefined,
        tasks: nullable === "tasks" ? null : [{
          ...detail(summary).tasks[0]!,
          weightedScore: undefined,
          graderResults: null,
        }],
      };
      await mockResults(page, [summary], [wire]);
      await page.goto("/#/lab/history");
      await page.getByRole("button", { name: "custom-skill", exact: true }).click();
      const dialog = page.getByRole("dialog");
      await expect(dialog.locator(".lab-grader")).toHaveCount(0);
      if (nullable === "tasks") {
        await expect(dialog.getByText("No recorded tasks in this result.")).toBeVisible();
        await expect(dialog.getByRole("combobox", { name: "Task", exact: true })).toBeDisabled();
      } else {
        await expect(dialog.getByText(/Weighted score Unavailable/)).toBeVisible();
      }
      await page.keyboard.press("Escape");
      await page.getByRole("link", { name: "Reports" }).click();
      const download = page.waitForEvent("download");
      await page.getByRole("button", { name: "Download JSON" }).click();
      const path = await (await download).path();
      const report = JSON.parse(await readFile(path!, "utf8"));
      expect(report.runs[0].weightedScore).toBeUndefined();
      if (nullable === "tasks") {
        expect(report.runs[0].tasks).toEqual([]);
      } else {
        expect(report.runs[0].tasks[0].graderResults).toEqual([]);
        expect(report.runs[0].tasks[0].weightedScore).toBeUndefined();
      }
    });
  }

  test("light lab preserves readable recorded content, arguments and results", async ({
    page,
  }) => {
    await mockResults(page);
    await page.goto("/#/lab/history");
    await page.getByRole("button", { name: "custom-skill", exact: true }).first().click();
    const dialog = page.getByRole("dialog");
    await expect(dialog.getByText("Output file found")).toBeVisible();
    await page.getByRole("tab", { name: "Trajectory", exact: true }).click();
    await dialog.getByRole("button", { name: "Events", exact: true }).click();
    await dialog.getByRole("button", { name: "Show details", exact: true }).nth(0).click();
    await dialog.getByRole("button", { name: "Show details", exact: true }).nth(0).click();
    await dialog.getByRole("button", { name: "Arguments", exact: true }).first().click();
    await dialog.getByRole("button", { name: "Result", exact: true }).first().click();
    const blocks = dialog.locator(".lab-recorded-trajectory pre");
    await expect(blocks).toHaveCount(3);
    await expect(blocks.filter({ hasText: "I'll analyze" })).toHaveCount(1);
    await expect(blocks.filter({ hasText: "fibonacci.py" })).toHaveCount(1);
    await expect(blocks.filter({ hasText: "def fib" })).toHaveCount(1);
    const contrasts = await blocks.evaluateAll((elements) => {
      const luminance = (color: string) => {
        const canvas = document.createElement("canvas");
        canvas.width = canvas.height = 1;
        const context = canvas.getContext("2d", { willReadFrequently: true })!;
        context.fillStyle = color;
        context.fillRect(0, 0, 1, 1);
        const channels = Array.from(context.getImageData(0, 0, 1, 1).data).slice(0, 3).map((value) => {
          const v = value / 255;
          return v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4;
        });
        return channels[0]! * 0.2126 + channels[1]! * 0.7152 + channels[2]! * 0.0722;
      };
      return elements.map((element) => {
        const style = getComputedStyle(element);
        const text = getComputedStyle(element.querySelector("code") ?? element);
        const a = luminance(style.backgroundColor);
        const b = luminance(text.color);
        return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
      });
    });
    contrasts.forEach((contrast) => expect(contrast).toBeGreaterThan(4.5));
    await page.getByRole("tab", { name: "Configuration" }).click();
    await expect(dialog.locator("pre")).toHaveCSS("background-color", "rgb(246, 248, 251)");
    await expect(dialog.locator("pre")).toHaveCSS("color", "rgb(36, 49, 70)");
    await expect(dialog.locator("pre")).toContainText("Explain how the Fibonacci");
  });

  test("inspector and JSON report expose recorded reliability and evidence without upgrading claims", async ({
    page,
  }) => {
    const summary = {
      ...savedRuns[0]!,
      taskCount: 2,
      passCount: 1,
      weightedScore: 0.925,
    };
    const savedDetail = detail(summary);
    const recordedTask = {
      ...RECORDED_RELIABILITY.tasks[0]!,
      id: "report-task",
      evidenceRuns: [evidenceRun()],
    };
    const savedEvidence: RunDetail = {
      ...savedDetail,
      statistics: RECORDED_RELIABILITY.statistics,
      tasks: [recordedTask, { ...RECORDED_RELIABILITY.tasks[1]!, id: "all-trials-passed" }],
    };
    await mockResults(page, [summary], [savedEvidence]);
    await page.goto("/#/lab/history");
    await page.getByRole("button", { name: "custom-skill", exact: true }).click();
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("tab", { name: "Recorded evidence" }).click();

    await expect(dialog).toContainText("2/3 passed");
    await expect(dialog).toContainText("pass^1 83.3%");
    await expect(dialog).toContainText("Task pass-rate Wilson interval: 66.7%");
    await expect(dialog).toContainText("Source origin: eval source-evaluation");
    await expect(dialog).toContainText("cached source evidence");
    await expect(dialog).toContainText("Requirement required-report");
    await expect(dialog).toContainText("Prompt judging unassessed:");
    await expect(dialog).not.toContainText("PRIVATE_FILE_BODY");
    await page.keyboard.press("Escape");

    await page.getByRole("link", { name: "Reports", exact: true }).click();
    const download = page.waitForEvent("download");
    await page.getByRole("button", { name: "Download JSON" }).click();
    const path = await (await download).path();
    const report = JSON.parse(await readFile(path!, "utf8"));
    expect(report.runs[0].statistics.passHatK).toEqual([5 / 6, 2 / 3, 0.5]);
    expect(report.runs[0].tasks[0].trials).toEqual({ passed: 2, total: 3 });
    expect(report.runs[0].tasks[0].evidenceRuns[0].assessment).toBe("recorded");
    expect(report.runs[0].tasks[0].promptJudgingRuns).toBeUndefined();
    expect(report.runs[0].tasks[0].evidenceRuns[0]).toMatchObject({ cached: true, attempts: 2 });
  });

  test("producer-projected prompt judging remains separate from cached evidence in the inspector and report", async ({ page }) => {
    await mockResults(page, [promptJudging], [promptJudging]);
    await page.goto("/#/lab/history");
    await page.getByRole("button", { name: "Unavailable", exact: true }).click();
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("tab", { name: "Recorded evidence" }).click();
    await expect(dialog).toContainText("Evidence unassessed:");
    await expect(dialog).not.toContainText("cached source evidence");
    await expect(dialog).toContainText("inspection not observable");
    await expect(dialog).toContainText("judge AI Credits 0");
    await expect(dialog).toContainText("Current regrade usage is separate");
    await expect(dialog).not.toContainText("PRIVATE_FILE_BODY");
    await page.keyboard.press("Escape");
    await page.getByRole("link", { name: "Reports", exact: true }).click();
    const download = page.waitForEvent("download");
    await page.getByRole("button", { name: "Download JSON" }).click();
    const path = await (await download).path();
    const report = JSON.parse(await readFile(path!, "utf8"));
    expect(report.runs[0].tasks[0].promptJudgingRuns).toEqual(promptJudging.tasks[0]!.promptJudgingRuns);
    expect(report.runs[0].tasks[0].evidenceRuns).toBeUndefined();
    expect(report.runs[0].regradeUsage.aiCredits).toBe(0);
  });

  for (const [name, interval] of [
    ["null", null],
    ["string", "bad"],
    ["array", []],
    ["reversed endpoints", { lower: 0.9, upper: 0.1, mean: 0.5, confidenceLevel: 0.95 }],
    ["out-of-range endpoints", { lower: -0.1, upper: 1.1, mean: 0.5, confidenceLevel: 0.95 }],
    ["out-of-range mean", { lower: 0, upper: 1, mean: 2, confidenceLevel: 0.95 }],
    ["out-of-range confidence", { lower: 0, upper: 1, mean: 0.5, confidenceLevel: 1.5 }],
    ["zero confidence", { lower: 0, upper: 1, mean: 0.5, confidenceLevel: 0 }],
    ["unit confidence", { lower: 0, upper: 1, mean: 0.5, confidenceLevel: 1 }],
    ["null endpoint", { lower: null, upper: 1, mean: 0.5, confidenceLevel: 0.95 }],
    ["string endpoint", { lower: "0", upper: 1, mean: 0.5, confidenceLevel: 0.95 }],
    ["missing confidence", { lower: 0, upper: 1, mean: 0.5 }],
  ] as const) {
    test(`invalid ${name} reliability intervals are withheld without hiding valid estimates`, async ({ page }) => {
      const summary = { ...savedRuns[0]!, taskCount: 1 };
      const recorded = detail(summary);
      Object.assign(recorded.tasks[0]!, { passRateCI: interval, bootstrapCI: interval });
      Object.assign(recorded, { statistics: { successRateCI: interval, passHatK: [1, 0.5, 0] } });
      const errors: string[] = [];
      page.on("pageerror", (error) => errors.push(error.message));
      await mockResults(page, [summary], [recorded]);
      await page.goto("/#/lab/history");
      await page.getByRole("button", { name: "custom-skill", exact: true }).click();
      const dialog = page.getByRole("dialog");
      await dialog.getByRole("tab", { name: "Recorded evidence" }).click();
      for (const label of ["Task pass-rate Wilson interval", "Weighted-score bootstrap interval", "Suite success-rate Wilson interval"]) {
        await expect(dialog).toContainText(`${label}: Withheld: invalid recorded interval`);
      }
      await expect(dialog).toContainText("pass^1 100.0% · pass^2 50.0% · pass^3 0.0%");
      expect(errors).toEqual([]);
    });
  }

  for (const [name, passHatK] of [
    ["string", "bad"],
    ["object", { length: 1 }],
    ["null", null],
    ["null entry", [null]],
    ["out-of-range entries", [-0.25, 1.5]],
    ["string entry", ["0"]],
  ] as const) {
    test(`invalid ${name} pass curves are withheld while valid zero intervals remain visible`, async ({ page }) => {
      const summary = { ...savedRuns[0]!, taskCount: 1 };
      const recorded = detail(summary);
      const zero = { lower: 0, upper: 0, mean: 0, confidenceLevel: 0.95 };
      recorded.tasks[0]!.passRateCI = zero;
      recorded.tasks[0]!.bootstrapCI = zero;
      Object.assign(recorded, { statistics: { successRateCI: zero, passHatK } });
      const errors: string[] = [];
      page.on("pageerror", (error) => errors.push(error.message));
      await mockResults(page, [summary], [recorded]);
      await page.goto("/#/lab/history");
      await page.getByRole("button", { name: "custom-skill", exact: true }).click();
      const dialog = page.getByRole("dialog");
      await dialog.getByRole("tab", { name: "Recorded evidence" }).click();
      await expect(dialog).toContainText("Withheld: invalid recorded pass^k estimates");
      for (const label of ["Task pass-rate Wilson interval", "Weighted-score bootstrap interval", "Suite success-rate Wilson interval"]) {
        await expect(dialog).toContainText(`${label}: 0.0% · 95% CI 0.0%–0.0%`);
      }
      expect(errors).toEqual([]);
    });
  }

  test("empty pass curves remain unassessed rather than malformed", async ({ page }) => {
    const summary = { ...savedRuns[0]!, taskCount: 1 };
    const recorded = detail(summary);
    recorded.statistics = { passHatK: [] };
    await mockResults(page, [summary], [recorded]);
    await page.goto("/#/lab/history");
    await page.getByRole("button", { name: "custom-skill", exact: true }).click();
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("tab", { name: "Recorded evidence" }).click();
    await expect(dialog).toContainText("pass^k (all k trials pass): Not assessed");
    await expect(dialog).not.toContainText("Withheld:");
  });

  for (const [name, trials] of [
    ["null", null],
    ["string", "bad"],
    ["negative passes", { passed: -1, total: 3 }],
    ["excess passes", { passed: 4, total: 3 }],
    ["empty trials", { passed: 0, total: 0 }],
    ["fractional passes", { passed: 0.5, total: 3 }],
    ["fractional total", { passed: 0, total: 3.5 }],
    ["unsafe total", { passed: 0, total: Number.MAX_SAFE_INTEGER + 1 }],
    ["string passes", { passed: "0", total: 3 }],
  ] as const) {
    test(`invalid ${name} trial counts are withheld while valid zero passes remain recorded`, async ({ page }) => {
      const summary = { ...savedRuns[0]!, taskCount: 1 };
      const recorded = detail(summary);
      const task = recorded.tasks[0]!;
      Object.assign(task, { trials });
      let valid = false;
      await mockResults(page, [summary], [recorded]);
      await page.route(/\/api\/runs\/[^/]+$/, (route) => route.fulfill({
        json: valid
          ? { ...recorded, tasks: [{ ...task, trials: { passed: 0, total: 3 } }] }
          : recorded,
      }));
      await page.goto("/#/lab/history");
      await page.getByRole("button", { name: "custom-skill", exact: true }).click();
      const dialog = page.getByRole("dialog");
      await dialog.getByRole("tab", { name: "Recorded evidence" }).click();
      await expect(dialog).toContainText("Task trials: Withheld: invalid recorded trial counts");
      await page.keyboard.press("Escape");
      valid = true;
      await page.reload();
      await page.getByRole("button", { name: "custom-skill", exact: true }).click();
      await dialog.getByRole("tab", { name: "Recorded evidence" }).click();
      await expect(dialog).toContainText("Task trials: 0/3 passed");
      await expect(dialog).not.toContainText("invalid recorded trial counts");
    });
  }

  test("legacy and invalid evidence remain unassessed or withheld in the Lab", async ({
    page,
  }) => {
    const summary = { ...savedRuns[0]!, taskCount: 1, passCount: 0 };
    const legacy = detail(summary);
    const invalid = detail({ ...savedRuns[1]!, taskCount: 1, passCount: 0 });
    invalid.tasks[0]!.id = "invalid-task";
    invalid.tasks[0]!.evidenceRuns = [{
      ...evidenceRun(),
      assessment: "invalid",
      manifest: undefined,
      explanations: undefined,
      message: "Evidence identity or metadata is invalid.",
    }];
    invalid.tasks[0]!.promptJudgingRuns = [{
      runNumber: 1,
      attempts: 1,
      cached: false,
      assessment: "invalid",
      observations: [],
      message: "Prompt judging metadata is invalid.",
    }];
    await mockResults(page, [summary, savedRuns[1]!], [legacy, invalid]);
    await page.goto("/#/lab/history");

    await page.getByRole("button", { name: "custom-skill", exact: true }).first().click();
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("tab", { name: "Recorded evidence" }).click();
    await expect(dialog).toContainText("Task trials: Not assessed");
    await expect(dialog).toContainText("Suite success-rate Wilson interval: Not assessed");
    await expect(dialog).toContainText("Evidence unassessed: no portable evidence metadata was recorded.");
    await expect(dialog).toContainText("Prompt judging unassessed: no frozen-judging metadata was recorded.");
    await page.keyboard.press("Escape");

    await page.getByRole("button", { name: "custom-skill", exact: true }).nth(1).click();
    const invalidDialog = page.getByRole("dialog");
    await invalidDialog.getByRole("tab", { name: "Recorded evidence" }).click();
    await expect(invalidDialog).toContainText("invalid");
    await expect(invalidDialog).toContainText("Evidence identity or metadata is invalid.");
    await expect(invalidDialog).toContainText("Prompt judging metadata is invalid.");
    await expect(invalidDialog).not.toContainText("Source origin:");
    await expect(invalidDialog).not.toContainText("Delivery SHA-256:");
  });

  for (const [name, evidenceRuns, promptJudgingRuns] of [
    ["null evidence and object judging", null, { content: "PRIVATE_CANARY" }],
    ["object evidence and null judging", { content: "PRIVATE_CANARY" }, null],
  ] as const) {
    test(`malformed ${name} run lists are withheld instead of called historical absence`, async ({
      page,
    }) => {
      const summary = { ...savedRuns[0]!, taskCount: 1 };
      const malformed = detail(summary);
      Object.assign(malformed.tasks[0], { evidenceRuns, promptJudgingRuns });
      const errors: string[] = [];
      page.on("pageerror", (error) => errors.push(error.message));
      await mockResults(page, [summary], [malformed]);
      await page.goto("/#/lab/history");
      await page.getByRole("button", { name: "custom-skill", exact: true }).click();
      const dialog = page.getByRole("dialog");
      await dialog.getByRole("tab", { name: "Recorded evidence" }).click();

      await expect(dialog).toContainText("Portable evidence metadata withheld: invalid or unsupported run list.");
      await expect(dialog).toContainText("Prompt judging metadata withheld: invalid or unsupported run list.");
      await expect(dialog).not.toContainText("no portable evidence metadata was recorded");
      await expect(dialog).not.toContainText("no frozen-judging metadata was recorded");
      await expect(dialog).not.toContainText("PRIVATE_CANARY");
      expect(errors).toEqual([]);
    });
  }

  test("null evidence and prompt-judging rows are withheld without crashing the inspector", async ({
    page,
  }) => {
    const summary = { ...savedRuns[0]!, taskCount: 1 };
    const malformed = detail(summary);
    Object.assign(malformed.tasks[0], {
      evidenceRuns: [null],
      promptJudgingRuns: [null],
    });
    const errors: string[] = [];
    page.on("pageerror", (error) => errors.push(error.message));
    await mockResults(page, [summary], [malformed]);
    await page.goto("/#/lab/history");
    await page.getByRole("button", { name: "custom-skill", exact: true }).click();
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("tab", { name: "Recorded evidence" }).click();

    await expect(dialog).toContainText("Evidence metadata withheld: invalid or unsupported run record.");
    await expect(dialog).toContainText("Prompt judging metadata withheld: invalid or unsupported metadata.");
    await expect(dialog).not.toContainText("Evidence unassessed:");
    await expect(dialog).not.toContainText("Prompt judging unassessed:");
    expect(errors).toEqual([]);
  });

  for (const [field, value] of [
    ["runNumber", {}],
    ["attempts", {}],
    ["cached", "false"],
    ["assessment", {}],
    ["message", {}],
  ] as const) {
    test(`malformed ${field} envelopes are withheld without hiding valid siblings`, async ({ page }) => {
      const summary = { ...savedRuns[0]!, taskCount: 1 };
      const malformed = detail(summary);
      const evidence = { ...evidenceRun(), [field]: value };
      const judging = { ...promptJudging.tasks[0]!.promptJudgingRuns[0]!, [field]: value };
      Object.assign(malformed.tasks[0], {
        evidenceRuns: [evidence, evidenceRun(2)],
        promptJudgingRuns: [judging, {
          runNumber: 2, attempts: 2, cached: true, assessment: "unassessed",
          observations: [], message: "Cached judging remains unassessed.",
        }],
      });
      const errors: string[] = [];
      page.on("pageerror", (error) => errors.push(error.message));
      await mockResults(page, [summary], [malformed]);
      await page.goto("/#/lab/history");
      await page.getByRole("button", { name: "custom-skill", exact: true }).click();
      const dialog = page.getByRole("dialog");
      await dialog.getByRole("tab", { name: "Recorded evidence" }).click();

      await expect(dialog).toContainText("Evidence metadata withheld: invalid or unsupported run record.");
      await expect(dialog).toContainText("Prompt judging metadata withheld: invalid or unsupported metadata.");
      await expect(dialog).toContainText("Run 2 · 2 attempts · recorded · cached source evidence");
      await expect(dialog).toContainText("Cached judging remains unassessed.");
      await expect(dialog).toContainText("Source origin: eval source-evaluation");
      expect(errors).toEqual([]);
    });
  }

  for (const [name, explanations] of [
    ["null explanation list", null],
    ["null requirement", [null]],
    ["null check", [{ task_id: "report-task", requirement_id: "required-report", checks: [null] }]],
    ["null check declaration", [{
      task_id: "report-task",
      requirement_id: "required-report",
      checks: [{ check: null, observation: "unresolved", category: "insufficient_evidence", message: "Unavailable." }],
    }]],
    ["null reference", [{
      task_id: "report-task",
      requirement_id: "required-report",
      checks: [{
        check: { scope: "task", grader: "report-check" },
        observation: "unresolved",
        category: "insufficient_evidence",
        message: "Unavailable.",
        references: [null],
      }],
    }]],
  ] as const) {
    test(`malformed ${name} requirement metadata is withheld without crashing`, async ({ page }) => {
      const summary = { ...savedRuns[0]!, taskCount: 1 };
      const malformed = detail(summary);
      const evidence = evidenceRun();
      Object.assign(evidence, { explanations });
      malformed.tasks[0]!.evidenceRuns = [evidence];
      const errors: string[] = [];
      page.on("pageerror", (error) => errors.push(error.message));
      await mockResults(page, [summary], [malformed]);
      await page.goto("/#/lab/history");
      await page.getByRole("button", { name: "custom-skill", exact: true }).click();
      const dialog = page.getByRole("dialog");
      await dialog.getByRole("tab", { name: "Recorded evidence" }).click();

      await expect(dialog).toContainText("Source origin: eval source-evaluation");
      await expect(dialog).toContainText("Requirement observations withheld: invalid or unsupported metadata.");
      await expect(dialog).not.toContainText("PRIVATE_CANARY");
      expect(errors).toEqual([]);
    });
  }

  test("valid null check collections remain unassessed rather than invalid", async ({ page }) => {
    const summary = { ...savedRuns[0]!, taskCount: 1 };
    const recorded = detail(summary);
    const evidence = evidenceRun();
    Object.assign(evidence, {
      explanations: [{
        task_id: "report-task",
        requirement_id: "required-report",
        checks: null,
      }],
    });
    recorded.tasks[0]!.evidenceRuns = [evidence];
    await mockResults(page, [summary], [recorded]);
    await page.goto("/#/lab/history");
    await page.getByRole("button", { name: "custom-skill", exact: true }).click();
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("tab", { name: "Recorded evidence" }).click();

    await expect(dialog).toContainText("Check observations unavailable; requirement satisfaction is unassessed.");
    await expect(dialog).not.toContainText("Requirement observations withheld:");
  });

  test("permanently closed activity streams require manual reconnect and release prior resources", async ({
    page,
  }) => {
    await mockEventSource(page);
    await mockResults(page);
    await page.goto("/#/lab/current");
    await expect(page.getByRole("heading", { name: "Run activity", exact: true })).toBeVisible();
    await emit(page, {
      runId: "run-001",
      sequence: 1,
      type: "run_started",
      timestamp: "2024-01-03T12:00:00Z",
      data: { totalTasks: 4 },
    });
    await emit(page, null, "closed");
    await expect(page.locator(".lab-status").filter({ hasText: /^Error$/ })).toBeVisible();
    await expect(page.getByRole("alert")).toContainText("Use Reconnect stream");
    await expect(page.getByRole("alert")).not.toContainText("will reconnect");
    await expect(page.locator(".lab-stream > div")).toHaveCount(1);
    await page.getByRole("button", { name: "Reconnect stream" }).click();
    await expect(page.locator(".lab-stream > div")).toHaveCount(0);
    const sources = await page.evaluate(
      () => (Reflect.get(window, "labSources") as () => { closed: boolean }[])(),
    );
    expect(sources.length).toBeGreaterThan(1);
    expect(sources.at(-2)?.closed).toBe(true);
    expect(sources.at(-1)?.closed).toBe(false);
    await emit(page, null, "open");
    await expect(page.getByText("Connected", { exact: true })).toBeVisible();
    await expect(page.getByRole("alert")).toHaveCount(0);
  });

  test("uses UTC calendar dates consistently for chart selection and history", async ({
    page,
  }) => {
    await mockResults(page, [
      { ...savedRuns[0]!, timestamp: "2024-01-03T23:30:00-05:00" },
      savedRuns[1]!,
    ]);
    await page.goto("/#/lab");
    await page.getByRole("button", { name: "Jan 4", exact: true }).click();
    await expect(
      page.locator(".lab-metrics").getByText("1", { exact: true }),
    ).toBeVisible();
    await page.getByRole("link", { name: "Run history" }).click();
    await expect(page.locator("tbody tr")).toHaveCount(1);
    await expect(page.locator("tbody")).toContainText("04:30");
  });

  test("preserves reported zero scores and credits instead of treating them as unavailable", async ({
    page,
  }) => {
    await mockResults(page, [
      {
        ...savedRuns[0]!,
        taskCount: 1,
        passCount: 0,
        outcome: "failed",
        weightedScore: 0,
        aiCredits: 0,
      },
    ]);
    await page.goto("/#/lab");
    await expect(
      page.locator(".lab-metrics").getByText("0.0%", { exact: true }),
    ).toHaveCount(2);
    await expect(
      page.locator(".lab-metrics").getByText("0.00", { exact: true }),
    ).toBeVisible();
    await expect(page.locator(".lab-metrics")).not.toContainText("Unavailable");
  });

  test("uses authoritative counts and arbitrary models, without loading every trace", async ({
    page,
  }) => {
    const requests = await mockResults(page);
    await page.goto("/#/lab");
    await expect(page.getByText("SAVED WAZA RESULTS")).toBeVisible();
    await expect(
      page.locator(".lab-metrics").getByText("3", { exact: true }),
    ).toBeVisible();
    await expect(
      page.locator(".lab-metrics").getByText("75.0%", { exact: true }),
    ).toBeVisible();
    await expect(
      page.locator(".lab-metrics").getByText("7.250000001", { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Filter to custom-model-v1" }),
    ).toBeVisible();
    expect(requests).toEqual([]);
    await page
      .getByRole("button", { name: "Filter to custom-model-v1" })
      .click();
    await page.getByRole("link", { name: "Run history" }).click();
    await expect(page.locator("tbody tr")).toHaveCount(1);
    await expect(page.locator("tbody")).toContainText("38s");
    await page.getByRole("button", { name: "Reset filters" }).click();
    await page
      .getByRole("combobox", { name: "Filter outcome" })
      .selectOption("failed");
    await expect(page.locator("tbody tr")).toHaveCount(1);
    await expect(page.locator("tbody")).toContainText("azure-blob");
  });

  test("shows loading, errors and retries without falling back to synthetic data", async ({
    page,
  }) => {
    await page.route("**/api/v1/lab/runs", (route) =>
      route.fulfill({ status: 503, json: { error: "unavailable" } }),
    );
    await page.goto("/#/lab");
    await expect(page.getByRole("alert")).toContainText("503");
    await expect(page.getByRole("alert")).toContainText(
      "No synthetic fallback",
    );
    await expect(page.locator(".lab-local-indicator small")).toHaveText("Refresh policy unavailable");
    await expect(
      page.locator(".lab-metrics").getByText("84", { exact: true }),
    ).toHaveCount(0);
    await mockResults(page);
    await page.getByRole("button", { name: "Refresh results" }).click();
    await expect(page.getByText("3 saved runs")).toBeVisible();
  });

  test("refresh discovers new files and preserves stale data on refresh failure", async ({
    page,
  }) => {
    await mockResults(page, savedRuns.slice(0, 1));
    await page.goto("/#/lab/history");
    await expect(page.locator("tbody tr")).toHaveCount(1);
    await page.route("**/api/v1/lab/runs", (route) =>
      route.fulfill({ json: savedRuns }),
    );
    await page.getByRole("button", { name: "Refresh results" }).click();
    await expect(page.locator("tbody tr")).toHaveCount(3);
    await page.route("**/api/v1/lab/runs", (route) =>
      route.fulfill({ status: 500 }),
    );
    await page.getByRole("button", { name: "Refresh results" }).click();
    await expect(page.getByRole("alert")).toContainText(
      "Previously loaded results",
    );
    await expect(page.locator("tbody tr")).toHaveCount(3);
  });

  test("handles empty archives and reports unavailable catalog checks honestly", async ({
    page,
  }) => {
    await mockResults(page, []);
    await page.goto("/#/lab/history");
    await expect(
      page.getByRole("heading", { name: "No runs match this view" }),
    ).toBeVisible();
    await page.getByRole("link", { name: "Current runs" }).click();
    await expect(
      page.getByRole("heading", { name: "No saved runs in this scope" }),
    ).toBeVisible();
    await page.getByRole("link", { name: "Skills & evals" }).click();
    await expect(
      page.getByText("No saved suites in this scope."),
    ).toBeVisible();
    await expect(
      page.getByText(/Readiness, token budgets, requirement coverage/),
    ).toBeVisible();
  });

  test("loads real evidence lazily with first-repetition provenance and actual trajectory", async ({
    page,
  }) => {
    const requests = await mockResults(page);
    await page.goto("/#/lab/history");
    await page
      .getByRole("button", { name: "custom-skill", exact: true })
      .first()
      .click();
    const dialog = page.getByRole("dialog");
    await expect(dialog.getByText("Output file found")).toBeVisible();
    await expect(
      dialog.getByText("SAVED RESULT EVIDENCE", { exact: false }),
    ).toBeVisible();
    await expect(
      dialog.getByText(/describe the first repetition/),
    ).toBeVisible();
    expect(requests).toEqual(["run-001"]);
    await page.getByRole("tab", { name: "Trajectory", exact: true }).click();
    await expect(dialog).toContainText("Rate limit exceeded");
    await expect(dialog).not.toContainText("Illustrative task trace");
    await page.getByRole("tab", { name: "Configuration" }).click();
    await expect(dialog.locator("pre")).toContainText(
      "Explain how the Fibonacci",
    );
    await expect(dialog.locator("pre")).not.toContainText(
      "trigger_skill_routing",
    );
    await page.keyboard.press("Escape");
    await expect(dialog).not.toBeVisible();
  });

  test("reports detail errors explicitly and retries", async ({ page }) => {
    await mockResults(page, savedRuns, []);
    await page.goto("/#/lab/history");
    await page
      .getByRole("button", { name: "custom-skill", exact: true })
      .first()
      .click();
    await expect(page.getByRole("dialog").getByRole("alert")).toContainText(
      "404",
    );
    await page.route("**/api/runs/run-001", (route) =>
      route.fulfill({ json: detail(savedRuns[0]!) }),
    );
    await page.getByRole("button", { name: "Retry evidence" }).click();
    await expect(
      page.getByRole("dialog").getByText("Output file found"),
    ).toBeVisible();
  });

  test("aligns stable IDs across renamed tasks and does not score added tasks as zero", async ({
    page,
  }) => {
    await mockResults(page);
    await page.goto("/#/lab/compare");
    await expect(page.getByText("+30.0 pp", { exact: true })).toHaveCount(2);
    await expect(page.getByText("1 comparable tasks")).toBeVisible();
    await expect(page.locator(".lab-matrix tbody tr")).toHaveCount(3);
    await expect(
      page.getByText("Coverage change", { exact: true }),
    ).toHaveCount(2);
    await page.getByRole("button", { name: "Renamed task" }).click();
    await expect(
      page.getByRole("combobox", { name: "Task", exact: true }),
    ).toHaveValue("id:stable-task");
  });

  for (const legacy of [false, true]) {
    test(`duplicate ${legacy ? "legacy names" : "task IDs"} are not silently paired`, async ({
      page,
    }) => {
      const duplicate = savedRuns.slice(0, 2).map((run) => {
        const result = detail(run);
        result.tasks = [0, 1].map(() => ({
          ...result.tasks[0]!,
          id: legacy ? undefined : "duplicate",
          name: "Same name",
        }));
        return result;
      });
      await mockResults(page, savedRuns.slice(0, 2), duplicate);
      await page.goto("/#/lab/compare");
      await expect(page.getByText("0 comparable tasks")).toBeVisible();
      await expect(page.locator(".lab-matrix tbody tr")).toHaveCount(4);
    });
  }

  test("unique legacy names align, while absent scores stay unavailable", async ({
    page,
  }) => {
    const details = savedRuns.slice(0, 2).map((run) => {
      const result = detail(run);
      result.tasks = [
        { ...result.tasks[0]!, id: undefined, name: "Unique legacy name" },
      ];
      return result;
    });
    await mockResults(page, savedRuns.slice(0, 2), details);
    await page.goto("/#/lab/compare");
    await expect(page.getByText("1 comparable tasks")).toBeVisible();
    await page.route("**/api/runs/run-002", (route) =>
      route.fulfill({
        json: {
          ...details[1],
          tasks: [{ ...details[1]!.tasks[0], weightedScore: undefined }],
        },
      }),
    );
    await page.reload();
    await expect(page.getByText("0 comparable tasks")).toBeVisible();
    await expect(page.getByText("Score / identity unavailable")).toBeVisible();
  });

  test("exports real details and exact credits, leaving unknown CSV cells empty", async ({
    page,
  }) => {
    const requests = await mockResults(page);
    await page.goto("/#/lab/reports");
    await expect(page.getByText("3 saved runs contain")).toBeVisible();
    const jsonDownload = page.waitForEvent("download");
    await page.getByRole("button", { name: "Download JSON" }).click();
    const jsonPath = await (await jsonDownload).path();
    const report = JSON.parse(await readFile(jsonPath!, "utf8"));
    expect(report.synthetic).toBe(false);
    expect(report.summary.tasks).toBe(12);
    expect(report.summary.creditCoverage).toBe(2);
    expect(report.runs).toHaveLength(3);
    expect(report.runs[0].tasks[0].id).toBe("stable-task");
    expect(report.runs[0].aiCredits).toBe(2.500000001);
    expect(requests.sort()).toEqual(["run-001", "run-002", "run-003"]);
    const csvDownload = page.waitForEvent("download");
    await page.getByRole("button", { name: "Download CSV" }).click();
    const csvPath = await (await csvDownload).path();
    const csv = await readFile(csvPath!, "utf8");
    expect(csv).toContain("2.500000001");
    expect(csv.split("\n").find((row) => row.includes("run-003"))).toContain(
      ",,9800,",
    );
    expect(csv.split("\n").find((row) => row.includes("run-001"))).toContain(
      ",4,100.00,92.00,12400,",
    );
  });

  test("does not download a partial report after a detail request fails", async ({
    page,
  }) => {
    await mockResults(page, savedRuns, [detail(savedRuns[0]!)]);
    const downloads: string[] = [];
    page.on("download", (download) =>
      downloads.push(download.suggestedFilename()),
    );
    await page.goto("/#/lab/reports");
    await page.getByRole("button", { name: "Download JSON" }).click();
    await expect(
      page.getByText("Report failed; no partial report downloaded.", {
        exact: false,
      }),
    ).toBeVisible();
    expect(downloads).toEqual([]);
    await expect(
      page.getByRole("button", { name: "Download JSON" }),
    ).toBeEnabled();
  });

  test("activity deduplicates events, surfaces reconnects and closes at terminal replay", async ({
    page,
  }) => {
    await mockEventSource(page);
    await mockResults(page);
    await page.goto("/#/lab/current");
    await expect(
      page.getByRole("heading", { name: "Run activity", exact: true }),
    ).toBeVisible();
    await emit(page, null, "open");
    const event = {
      runId: "run-001",
      sequence: 1,
      type: "run_started",
      timestamp: "2024-01-03T12:00:00Z",
      data: { totalTasks: 4 },
    };
    await emit(page, event);
    await emit(page, event);
    await expect(page.locator(".lab-stream > div")).toHaveCount(1);
    await emit(page, null, "error");
    await expect(page.getByRole("alert")).toContainText("Last-Event-ID");
    await emit(page, null, "open");
    await emit(page, {
      ...event,
      sequence: 2,
      type: "run_failed",
      data: { completedTasks: 2, totalTasks: 4 },
    });
    await expect(page.getByText("Run failed (replayed)")).toBeVisible();
    await emit(page, null, "closed");
    await expect(page.getByText("Run failed (replayed)")).toBeVisible();
    expect(
      await page.evaluate(
        () =>
          (
            Reflect.get(window, "labSources") as () => { closed: boolean }[]
          )().at(-1)?.closed,
      ),
    ).toBe(true);
    await page
      .getByRole("combobox", { name: "Activity run" })
      .selectOption("run-002");
    await expect(page.locator(".lab-stream > div")).toHaveCount(0);
    await emit(page, { ...event, runId: "wrong-run" });
    await expect(page.getByRole("alert")).toContainText("invalid run event");
    await page.getByRole("button", { name: "Reconnect stream" }).click();
    await emit(page, {
      ...event,
      runId: "run-002",
      data: { totalTasks: "invalid" },
    });
    await expect(page.getByRole("alert")).toContainText("invalid run event");
  });

  test("returns to the classic dashboard without lab style leakage", async ({
    page,
  }) => {
    await mockResults(page);
    await page.goto("/#/lab");
    await expect(page.getByText("3 saved runs")).toBeVisible();
    await page.getByRole("link", { name: "Original dashboard" }).click();
    await expect(page.locator(".lab-root")).toHaveCount(0);
    expect(
      await page
        .locator("html")
        .evaluate((element) => getComputedStyle(element).colorScheme),
    ).toBe("dark");
  });

  test("native EventSource resumes via Last-Event-ID and terminates without reconnecting again", async ({
    page,
  }) => {
    await mockResults(page);
    const headers: string[] = [];
    const endpoint = "**/api/v1/runs/run-001/events";
    const server = createServer((request, response) => {
      const cursor = request.headers["last-event-id"];
      headers.push(Array.isArray(cursor) ? cursor.join(",") : (cursor ?? ""));
      const sequence = cursor === "1" ? 2 : 1;
      const event = {
        runId: "run-001",
        sequence,
        type: sequence === 1 ? "run_started" : "run_completed",
        timestamp: "2024-01-03T12:00:00Z",
        data: { totalTasks: 4, completedTasks: sequence === 1 ? 0 : 4 },
      };
      response.writeHead(200, {
        "Content-Type": "text/event-stream",
        "Cache-Control": "no-store",
        "Access-Control-Allow-Origin": "*",
        Connection: "close",
      });
      response.end(`retry: 50\nid: ${sequence}\ndata: ${JSON.stringify(event)}\n\n`);
    });
    try {
      const ready = once(server, "listening");
      server.listen(0, "127.0.0.1");
      await ready;
      const address = server.address();
      if (!address || typeof address === "string") throw new Error("SSE fixture did not bind a TCP port");
      // Observe the wire header rather than the interception snapshot.
      await page.route(endpoint, (route) => route.continue({
        url: `http://127.0.0.1:${address.port}/api/v1/runs/run-001/events`,
      }));
      await page.goto("/#/lab/current");
      await expect(page.getByText("Run completed (replayed)")).toBeVisible();
      await expect(page.getByText("4 / 4 stored tasks replayed")).toBeVisible();
      await expect(page.locator(".lab-stream > div")).toHaveCount(2);
      expect(headers).toEqual(["", "1"]);
      // Multiple advertised retry periods expose an incorrectly retained source.
      await page.waitForTimeout(200);
      expect(headers).toHaveLength(2);
      await page.getByRole("link", { name: "Run history" }).click();
      await page.waitForTimeout(200);
      expect(headers).toHaveLength(2);
    } finally {
      try {
        await page.unroute(endpoint);
      } finally {
        server.closeAllConnections();
        if (server.listening) {
          await new Promise<void>((resolve, reject) =>
            server.close((error) => error ? reject(error) : resolve()),
          );
        }
      }
    }
  });
});
