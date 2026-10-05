import { test, expect } from "@playwright/test";
import { mockAllAPIs } from "./helpers/api-mock";
import { RUNS } from "./fixtures/mock-data";

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
      fullPage: false,
    });
    for (const path of ["../docs/images/explore/run-detail-tasks.png", "../site/public/images/explore/run-detail-tasks.png"]) {
      await page.screenshot({ path, animations: "disabled", fullPage: false });
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
