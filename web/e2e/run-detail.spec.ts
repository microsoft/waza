import { test, expect } from "@playwright/test";
import { mockAllAPIs } from "./helpers/api-mock";
import { RUN_DETAIL } from "./fixtures/mock-data";

test.describe("Run Detail", () => {
  test("clicking a table row navigates to /runs/:id", async ({ page }) => {
    await mockAllAPIs(page);
    await page.goto("/");

    // Wait for the table to render
    await expect(page.getByText("code-explainer")).toBeVisible();

    // Click the first run row
    await page.getByText("code-explainer").click();

    // Should navigate to hash route
    await expect(page).toHaveURL(/#\/runs\/run-001/);
  });

  test("run detail page shows task list", async ({ page }) => {
    await mockAllAPIs(page);
    await page.goto("/#/runs/run-001");

    // The detail page should show the spec name
    await expect(page.getByRole("heading", { name: "code-explainer" })).toBeVisible();

    // Should show all 4 tasks
    await expect(page.getByText("explain-fibonacci")).toBeVisible();
    await expect(page.getByText("explain-quicksort")).toBeVisible();
    await expect(page.getByText("explain-binary-search")).toBeVisible();
    await expect(page.getByText("explain-merge-sort")).toBeVisible();
  });

  test("task expansion shows grader results", async ({ page }) => {
    await mockAllAPIs(page);
    await page.goto("/#/runs/run-001");

    // Wait for tasks to render
    await expect(page.getByText("explain-fibonacci")).toBeVisible();

    // Click to expand the first task
    await page.getByText("explain-fibonacci").click();

    // Should show grader result rows
    await expect(page.getByText("output-exists")).toBeVisible();
    await expect(page.getByText("mentions-recursion")).toBeVisible();
    // Grader type badges
    await expect(page.getByText("code").first()).toBeVisible();
    await expect(page.getByText("text").first()).toBeVisible();
  });

  test("back navigation returns to dashboard", async ({ page }) => {
    await mockAllAPIs(page);
    await page.goto("/#/runs/run-001");

    // Wait for detail page
    await expect(page.getByRole("heading", { name: "code-explainer" })).toBeVisible();

    // Click back link
    await page.getByText("Back to runs").click();

    // Should be back on dashboard
    await expect(page).toHaveURL(/#\//);
    await expect(page.getByText("Eval Runs")).toBeVisible();
  });

  test("invalid run ID shows error state", async ({ page }) => {
    await mockAllAPIs(page);
    await page.goto("/#/runs/nonexistent-run");

    // The API mock returns 404 for unknown IDs → fetchJSON throws → react-query catches it
    // react-query retries 3x by default, so we need a longer timeout
    await expect(page.getByText(/Failed to load run|API error/)).toBeVisible({ timeout: 15_000 });
    await expect(page.getByText("Retry")).toBeVisible();
  });
});
