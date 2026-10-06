import { expect, test, type Page } from "@playwright/test";
import { readFile } from "node:fs/promises";
import type { RunDetail, RunSummary } from "../src/api/client";
import { RUN_DETAIL, RUNS } from "./fixtures/mock-data";

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
        id: "stable-task",
        name: candidate ? "Renamed task" : "Original name",
        weightedScore: candidate ? 0.8 : 0.5,
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
  details = runs.map(detail),
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

async function mockEventSource(page: Page) {
  await page.addInitScript(() => {
    const sources: {
      url: string;
      closed: boolean;
      onopen?: (event: Event) => void;
      onerror?: (event: Event) => void;
      onmessage?: (event: MessageEvent) => void;
    }[] = [];
    Object.defineProperty(window, "EventSource", {
      value: class {
        url: string;
        closed = false;
        onopen?: (event: Event) => void;
        onerror?: (event: Event) => void;
        onmessage?: (event: MessageEvent) => void;
        constructor(url: string) {
          this.url = url;
          sources.push(this);
        }
        close() {
          this.closed = true;
        }
      },
    });
    Object.defineProperty(window, "labEmit", {
      value: (payload: unknown, kind = "message") => {
        const active = sources.at(-1);
        if (kind === "open") active?.onopen?.(new Event("open"));
        else if (kind === "error") active?.onerror?.(new Event("error"));
        else
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
    await page.route("**/api/v1/runs/run-001/events", (route) => {
      headers.push(route.request().headers()["last-event-id"] ?? "");
      const sequence = headers.length === 1 ? 1 : 2;
      const event = {
        runId: "run-001",
        sequence,
        type: sequence === 1 ? "run_started" : "run_completed",
        timestamp: "2024-01-03T12:00:00Z",
        data: { totalTasks: 4, completedTasks: sequence === 1 ? 0 : 4 },
      };
      return route.fulfill({
        contentType: "text/event-stream",
        body: `retry: 50\nid: ${sequence}\ndata: ${JSON.stringify(event)}\n\n`,
      });
    });
    await page.goto("/#/lab/current");
    await expect(page.getByText("Run completed (replayed)")).toBeVisible();
    await expect(page.getByText("4 / 4 stored tasks replayed")).toBeVisible();
    await expect(page.locator(".lab-stream > div")).toHaveCount(2);
    expect(headers).toEqual(["", "1"]);
    await page.getByRole("link", { name: "Run history" }).click();
    expect(headers).toHaveLength(2);
  });
});
