import { test, expect } from "@playwright/test";
import { copyFile } from "node:fs/promises";
import { mockAllAPIs } from "./helpers/api-mock";
import { RUNS } from "./fixtures/mock-data";
import { evidenceDetail } from "./fixtures/evidence-data";
import promptJudging from "./fixtures/prompt-judging.json" with { type: "json" };

import { RECORDED_RELIABILITY } from "./fixtures/reliability-data";
import { CONVERSATION_DETAIL, CONVERSATION_SUMMARY } from "./fixtures/conversation-data";

test.describe("Dashboard lab screenshots", () => {
  test.use({ viewport: { width: 1440, height: 1000 } });

  test("dashboard-lab-overview", async ({ page }) => {
    await page.goto("/#/lab/demo");
    await expect(page.getByRole("heading", { name: "Know where your skills stand." })).toBeVisible();
    for (const path of ["../docs/images/dashboard-lab-overview.png", "../site/public/images/dashboard-lab-overview.png"]) {
      await page.screenshot({ path, animations: "disabled", fullPage: true });
    }
  });

  test("dashboard-lab-compare", async ({ page }) => {
    await page.goto("/#/lab/demo/compare");
    await expect(page.getByRole("heading", { name: "Task-level score matrix" })).toBeVisible();
    for (const path of ["../docs/images/dashboard-lab-compare.png", "../site/public/images/dashboard-lab-compare.png"]) {
      await page.screenshot({ path, animations: "disabled", fullPage: true });
    }
  });

  test("dashboard-lab-conversation", async ({ page }) => {
    await page.route("**/api/v1/lab/runs", (route) =>
      route.fulfill({ json: [CONVERSATION_SUMMARY] }),
    );
    await page.route("**/api/runs/conversation", (route) =>
      route.fulfill({ json: CONVERSATION_DETAIL }),
    );
    await page.goto("/#/lab/history");
    await page.getByRole("button", { name: "conversation-review", exact: true }).click();
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("tab", { name: "Conversation", exact: true }).click();
    await expect(dialog.getByRole("list", { name: "Recorded conversation" }).getByRole("listitem")).toHaveCount(6);
    await expect(dialog).toContainText("Responder recorded 2 simulated-user follow-ups.");
    const path = "../docs/images/dashboard-lab-conversation.png";
    await page.screenshot({ path, animations: "disabled", fullPage: true });
    await copyFile(path, "../site/public/images/dashboard-lab-conversation.png");
  });

  test("dashboard-lab-evidence", async ({ page }) => {
    const summary = {
      ...RUNS[0]!,
      model: "mock-model",
      aiCredits: undefined,
      skill: "saved-scenario",
      spec: "evidence-judge",
      outcome: "failed",
      timestamp: "2026-10-16T12:00:00Z",
      repetitions: 3,
      taskCount: 2,
      passCount: 1,
      weightedScore: 0.925,
      source: "local",
    };
    const recorded = evidenceDetail();
    const run = {
      ...recorded,
      ...summary,
      statistics: RECORDED_RELIABILITY.statistics,
      tasks: [{
        ...RECORDED_RELIABILITY.tasks[0]!,
        id: "report-task",
        evidenceRuns: recorded.tasks[0]!.evidenceRuns,
      }, { ...RECORDED_RELIABILITY.tasks[1]!, id: "all-trials-passed" }],
    };
    expect(run.taskCount).toBe(run.tasks.length);
    expect(run.passCount).toBe(run.tasks.filter((task) => task.outcome === "passed").length);
    expect(run.statistics.successRateCI!.mean).toBe(run.passCount / run.taskCount);
    for (const task of run.tasks) {
      expect(task.passRateCI!.mean).toBe(task.trials!.passed / task.trials!.total);
    }
    await page.route("**/api/v1/lab/runs", (route) => route.fulfill({ json: [summary] }));
    await page.route(/\/api\/runs\/[^/]+$/, (route) => route.fulfill({ json: run }));
    await page.goto("/#/lab/history");
    await page.getByRole("button", { name: "saved-scenario", exact: true }).click();
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("tab", { name: "Recorded evidence" }).click();
    await expect(dialog).toContainText("Source origin: eval source-evaluation");
    await expect(dialog).toContainText("2/3 passed");
    await expect(dialog).toContainText("cached source evidence");
    await expect(dialog).toContainText("Prompt judging unassessed:");
    const evidenceScreenshot = "../docs/images/dashboard-lab-evidence.png";
    await page.screenshot({ path: evidenceScreenshot, animations: "disabled", fullPage: true });
    await copyFile(evidenceScreenshot, "../site/public/images/dashboard-lab-evidence.png");
  });
});

/**
 * Screenshot capture for dashboard documentation.
 * Generates deterministic PNGs at 1280×720 using mock data.
 * Run: npx playwright test e2e/screenshots.spec.ts --project=chromium
 */
test.describe("Screenshots", () => {
  test.use({ viewport: { width: 1280, height: 720 } });

  test.beforeEach(async ({ page }) => {
    const now = new Date("2026-09-14T12:00:00Z");
    await page.clock.install({ time: now });
    await mockAllAPIs(page);
    await page.route(/\/api\/runs(\?|$)/, (route) => {
      const runs = RUNS.map((run, i) => ({
        ...run,
        timestamp: new Date(now.getTime() - [3600_000, 7200_000, 86400_000][i]).toISOString(),
      }));
      if (new URL(route.request().url()).searchParams.get("order") === "asc") {
        runs.sort((a, b) => a.timestamp.localeCompare(b.timestamp));
      }
      return route.fulfill({ json: runs });
    });
  });

  test("dashboard-overview", async ({ page }) => {
    await page.goto("/");

    // Wait for KPI cards and table to render
    await expect(page.getByText("Total Runs")).toBeVisible();
    await expect(page.getByText("code-explainer")).toBeVisible();

    await page.screenshot({
      path: "../docs/images/dashboard-overview.png",
      animations: "disabled",
      fullPage: false,
    });
    for (const path of ["../docs/images/explore/runs-overview.png", "../site/public/images/explore/runs-overview.png"]) {
      await page.screenshot({ path, animations: "disabled", fullPage: false });
    }
  });

  test("run-detail", async ({ page }) => {
    await page.goto("/#/runs/run-001");

    // Wait for detail page with tasks
    await expect(page.getByRole("heading", { name: "code-explainer" })).toBeVisible();
    await expect(page.getByText("explain-fibonacci")).toBeVisible();

    // Expand first task to show grader results
    await page.getByText("explain-fibonacci").click();
    await expect(page.getByText("output-exists")).toBeVisible();

    await page.screenshot({
      path: "../docs/images/run-detail.png",
      animations: "disabled",
      fullPage: true,
    });

    for (const path of ["../docs/images/explore/run-detail-tasks.png", "../site/public/images/explore/run-detail-tasks.png"]) {
      await page.screenshot({ path, animations: "disabled", fullPage: true });
    }
  });

  test("recorded-reliability", async ({ page }) => {
    await page.route(/\/api\/runs\/run-001$/, (route) =>
      route.fulfill({
        json: { ...RECORDED_RELIABILITY, timestamp: "2026-09-14T11:00:00Z" },
      }),
    );
    await page.goto("/#/runs/run-001");
    await expect(page.getByTestId("task-pass-rate").first()).toContainText("2/3 trials passed");
    await expect(page.getByTestId("pass-hat-k")).toContainText("pass^3: 50.0%");
    for (const path of ["../docs/images/explore/recorded-reliability.png", "../site/public/images/explore/recorded-reliability.png"]) {
      await page.screenshot({ path, animations: "disabled", fullPage: true });
    }
  });

  test("statistical-confidence", async ({ page }) => {
    await page.goto("/#/runs/run-001");

    const tasks = page.locator("table", { has: page.getByText("explain-merge-sort") });
    await expect(tasks.locator('[data-testid="ci-range"]').first()).toBeVisible();

    for (const path of [
      "../docs/images/explore/statistical-confidence.png",
      "../site/public/images/explore/statistical-confidence.png",
      "../docs/images/explore/weighted-scores.png",
      "../site/public/images/explore/weighted-scores.png",
    ]) {
      await tasks.screenshot({ path, animations: "disabled" });
    }
  });

  test("evidence-tab", async ({ page }) => {
    await page.route(/\/api\/runs\/run-001$/, (route) =>
      route.fulfill({ json: {
        ...evidenceDetail(),
        regradeUsage: promptJudging.regradeUsage,
        tasks: [...evidenceDetail().tasks, promptJudging.tasks[0]!],
      } }),
    );
    await page.goto("/#/runs/run-001");
    await page.getByRole("button", { name: "Evidence", exact: true }).click();
    await expect(page.getByTestId("evidence-view")).toContainText("Source origin:");
    await expect(page.getByTestId("evidence-view")).toContainText("Run 2");
    await expect(page.getByTestId("evidence-view")).toContainText("a-judge · run · submitted");
    await page.setViewportSize({ width: 1280, height: 1050 });
    for (const path of ["../docs/images/explore/evidence-tab.png", "../site/public/images/explore/evidence-tab.png"]) {
      await page.screenshot({ path, animations: "disabled", fullPage: true });
    }
  });

  test("prompts-tab", async ({ page }) => {
    await page.goto("/#/runs/run-001");

    await page.getByRole("button", { name: "Prompts" }).click();
    await expect(page.getByText("Raw resolved prompt sent to the agent · JSON")).toBeVisible();
    await expect(page.getByText('"task": "Explain the quicksort implementation"')).toBeVisible();

    await page.screenshot({
      path: "../docs/images/explore/prompts-tab.png",
      animations: "disabled",
      fullPage: false,
    });
    await page.screenshot({
      path: "../site/public/images/explore/prompts-tab.png",
      animations: "disabled",
      fullPage: false,
    });
  });

  test("compare", async ({ page }) => {
    await page.goto("/#/compare");

    // Select both runs to populate the comparison table
    const selects = page.locator("select");
    await selects.nth(0).selectOption("run-001");
    await selects.nth(1).selectOption("run-002");

    // Wait for comparison table and metrics to fully render
    await expect(page.getByText("Per-Task Comparison")).toBeVisible();
    await expect(page.getByText("explain-fibonacci")).toBeVisible();
    await expect(page.getByText("Metrics Comparison")).toBeVisible();

    // Use taller viewport to capture run cards, metrics, and full per-task table
    await page.setViewportSize({ width: 1280, height: 1050 });

    await page.screenshot({
      path: "../docs/images/explore/compare-runs.png",
      animations: "disabled",
      fullPage: false,
    });
    await page.screenshot({
      path: "../site/public/images/explore/compare-runs.png",
      animations: "disabled",
      fullPage: false,
    });
  });

  test("trends", async ({ page }) => {
    await page.goto("/#/trends");

    // Wait for trend charts to render (TrendsPage uses /api/runs with sort params)
    await expect(page.getByRole("heading", { name: "Trends" })).toBeVisible();
    await expect(page.getByText("Pass Rate")).toBeVisible();
    await expect(page.getByText("Tokens per Run")).toBeVisible();
    await expect(page.getByText("AI Credits per Run")).toBeVisible();
    await expect(page.getByRole("status")).toContainText("1 of 3 runs have unavailable");

    await page.screenshot({
      path: "../docs/images/trends.png",
      animations: "disabled",
      fullPage: false,
    });
    for (const path of ["../docs/images/explore/trends.png", "../site/public/images/explore/trends.png"]) {
      await page.screenshot({ path, animations: "disabled", fullPage: false });
    }
  });
});
