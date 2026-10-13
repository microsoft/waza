import { test, expect, type Page } from "@playwright/test";
import type { RunDetail } from "../src/api/client";
import { mockAllAPIs } from "./helpers/api-mock";
import { RUN_DETAIL } from "./fixtures/mock-data";
import { RECORDED_RELIABILITY as recorded } from "./fixtures/reliability-data";

async function showDetail(page: Page, detail: RunDetail) {
  await mockAllAPIs(page);
  await page.route(/\/api\/runs\/run-001$/, (route) => route.fulfill({ json: detail }));
  await page.goto("/#/runs/run-001");
}

test.describe("Recorded trial reliability", () => {
  test("labels expanded grader metadata instead of placing it under task metric columns", async ({ page }) => {
    await showDetail(page, {
      ...recorded,
      tasks: [{
        ...recorded.tasks[0],
        graderResults: [{
          name: "file-present", type: "file", passed: true,
          score: 1, weight: 2, message: "record persisted",
        }],
      }],
    });
    await page.getByRole("row").filter({ hasText: "mixed-trials" }).click();
    const row = page.getByRole("row").filter({ hasText: "file-present" });
    await expect(row.locator("td")).toHaveCount(1);
    await expect(row.getByTestId("grader-details")).toHaveAttribute("colspan", "6");
    await expect(row).toContainText("Score: 100%");
    await expect(row).toContainText("Weight: ×2");
    await expect(row).toContainText("Message: record persisted");
    await expect(row).not.toContainText("Wilson");
    await expect(row).not.toContainText("trials passed");
  });

  test("shows distinct pass-rate and weighted-score intervals with recorded evidence", async ({ page }) => {
    await showDetail(page, recorded);
    const task = page.getByTestId("task-pass-rate").first();
    await expect(task).toContainText("66.7%");
    await expect(task).toContainText("95% Wilson CI: [20.8%, 93.8%]");
    await expect(task).toContainText("2/3 trials passed");
    const scoreCI = page.getByTestId("ci-range");
    await expect(scoreCI).toHaveCount(1);
    await expect(scoreCI).toHaveText("[72.0%, 95.0%]");
    await expect(scoreCI).toHaveAttribute("title", /bootstrap weighted-score/);
    const suite = page.getByTestId("suite-success-rate-ci");
    await expect(suite).toContainText("50.0%");
    await expect(suite).toContainText("95% Wilson CI: [9.4%, 90.5%]");
    await expect(suite).toContainText("1/2 tasks passed (each task is one sample)");
    const curve = page.getByTestId("pass-hat-k");
    await expect(curve).toContainText("pass^1: 83.3%");
    await expect(curve).toContainText("pass^2: 66.7%");
    await expect(curve).toContainText("pass^3: 50.0%");
    await expect(curve).toContainText("not pass@k or a guarantee");
    await expect(page.getByTestId("significance-badge")).toHaveCount(0);
  });

  test("keeps historical absence unassessed even with scores and successful outcomes", async ({ page }) => {
    await showDetail(page, RUN_DETAIL);
    await expect(page.getByTestId("task-pass-rate")).toHaveCount(4);
    for (const cell of await page.getByTestId("task-pass-rate").all()) {
      await expect(cell).toHaveText("Not assessed");
    }
    await expect(page.getByTestId("suite-success-rate-ci")).toContainText("Not assessed");
    await expect(page.getByTestId("pass-hat-k")).toContainText("Not assessed");
    await expect(page.getByTestId("pass-rate-ci")).toHaveCount(0);
    await expect(page.getByTestId("ci-range")).toHaveCount(3);
  });

  test("renders reported zero estimates, rather than treating them as missing", async ({ page }) => {
    await showDetail(page, {
      ...recorded, passCount: 0,
      statistics: {
        successRateCI: { lower: 0, upper: 0.6576, mean: 0, confidenceLevel: 0.95 },
        passHatK: [0, 0, 0],
      },
      tasks: recorded.tasks.map((task) => ({
        ...task, outcome: "failed",
        trials: { passed: 0, total: 3 },
        passRateCI: { lower: 0, upper: 0.5615, mean: 0, confidenceLevel: 0.95 },
      })),
    });
    await expect(page.getByTestId("task-pass-rate").first()).toContainText("0.0%");
    await expect(page.getByTestId("task-pass-rate").first()).toContainText("0/3 trials passed");
    await expect(page.getByTestId("suite-success-rate-ci")).toContainText("[0.0%, 65.8%]");
    await expect(page.getByTestId("suite-success-rate-ci")).toContainText("0/2 tasks passed");
    await expect(page.getByTestId("pass-hat-k")).toContainText("pass^3: 0.0%");
  });

  test("shows single-trial evidence without creating an interval or curve", async ({ page }) => {
    await showDetail(page, {
      ...recorded, statistics: undefined, taskCount: 1, passCount: 1, outcome: "passed",
      tasks: [{
        name: "single-trial", outcome: "passed", score: 1, duration: 1,
        graderResults: [], trials: { passed: 1, total: 1 },
      }],
    });
    await expect(page.getByTestId("task-pass-rate")).toContainText("Not assessed");
    await expect(page.getByTestId("task-pass-rate")).toContainText("1/1 trials passed");
    await expect(page.getByTestId("pass-rate-ci")).toHaveCount(0);
    await expect(page.getByTestId("pass-hat-k")).toContainText("Not assessed");
  });

  test("uses recorded confidence levels and renders partial metadata independently", async ({ page }) => {
    await showDetail(page, {
      ...recorded,
      statistics: { successRateCI: { lower: 0.1209, upper: 0.8791, mean: 0.5, confidenceLevel: 0.9 } },
      tasks: recorded.tasks.map((task) => ({ ...task, trials: undefined })),
    });
    await expect(page.getByTestId("suite-success-rate-ci")).toContainText("90% Wilson CI");
    await expect(page.getByTestId("pass-hat-k")).toContainText("Not assessed");
    await expect(page.getByTestId("task-pass-rate").first()).not.toContainText("trials passed");
  });

  test("shows a stored one-element curve without inferring a suite interval", async ({ page }) => {
    await showDetail(page, { ...recorded, statistics: { passHatK: [5 / 6] } });
    await expect(page.getByTestId("suite-success-rate-ci")).toContainText("Not assessed");
    await expect(page.getByTestId("pass-hat-k")).toContainText("pass^1: 83.3%");
    await expect(page.getByTestId("pass-hat-k").locator("li")).toHaveCount(1);
  });
});
