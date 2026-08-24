import { test, expect } from "@playwright/test";
import { mockAllAPIs } from "./helpers/api-mock";

/**
 * Screenshot capture for dashboard documentation.
 * Generates deterministic PNGs at 1280×720 using mock data.
 * Run: npx playwright test e2e/screenshots.spec.ts --project=chromium
 */
test.describe("Screenshots", () => {
  test.use({ viewport: { width: 1280, height: 720 } });

  test.beforeEach(async ({ page }) => {
    await mockAllAPIs(page);
  });

  test("dashboard-overview", async ({ page }) => {
    await page.goto("/");

    // Wait for KPI cards and table to render
    await expect(page.getByText("Total Runs")).toBeVisible();
    await expect(page.getByText("code-explainer")).toBeVisible();

    await page.screenshot({
      path: "../docs/images/dashboard-overview.png",
      fullPage: false,
    });
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
      fullPage: false,
    });
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
      fullPage: false,
    });
  });

  test("trends", async ({ page }) => {
    await page.goto("/#/trends");

    // Wait for trend charts to render (TrendsPage uses /api/runs with sort params)
    await expect(page.getByRole("heading", { name: "Trends" })).toBeVisible();
    await expect(page.getByText("Pass Rate")).toBeVisible();
    await expect(page.getByText("Tokens per Run")).toBeVisible();

    await page.screenshot({
      path: "../docs/images/trends.png",
      fullPage: false,
    });
  });
});
