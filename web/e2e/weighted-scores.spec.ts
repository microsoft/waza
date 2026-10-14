import { test, expect } from "@playwright/test";
import { mockAllAPIs } from "./helpers/api-mock";

test.describe("Weighted Scores", () => {
  test("runs table shows W. Score column with values", async ({ page }) => {
    await mockAllAPIs(page);
    await page.goto("/");

    // Header should include W. Score (display column, not sortable)
    await expect(page.locator("th", { hasText: "W. Score" })).toBeVisible();

    // run-001 has weightedScore 0.92 → "92%"
    const rows = page.locator("tbody tr");
    await expect(rows.first().getByText("92%")).toBeVisible();
  });

  test("runs table shows dash when weightedScore is absent", async ({ page }) => {
    await mockAllAPIs(page);
    await page.goto("/");

    // run-003 has no weightedScore → "—". The row also renders "—" for its
    // unavailable AI Credits, so scope to the first dash (W. Score column).
    const rows = page.locator("tbody tr");
    await expect(rows.nth(2).getByText("—").first()).toBeVisible();
  });

  test("run detail task table shows W. Score column", async ({ page }) => {
    await mockAllAPIs(page);
    await page.goto("/#/runs/run-001");

    await expect(page.getByText("W. Score")).toBeVisible();

    // explain-fibonacci has weightedScore 1.0 → "100%"
    // explain-binary-search has weightedScore 0.33 → "33%"
    await expect(page.getByText("explain-fibonacci")).toBeVisible();
    await expect(page.getByText("33%")).toBeVisible();
  });

  test("grader expansion shows weight per grader", async ({ page }) => {
    await mockAllAPIs(page);
    await page.goto("/#/runs/run-001");

    // Expand explain-fibonacci to see grader results
    await page.getByText("explain-fibonacci").click();

    // output-exists has weight 1.0 → "×1"
    await expect(page.getByText("×1")).toBeVisible();
    // mentions-recursion has weight 2.0 → "×2"
    await expect(page.getByText("×2")).toBeVisible();
  });
});
