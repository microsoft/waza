import { test, expect } from "@playwright/test";
import { mockAllAPIs } from "./helpers/api-mock";

test.describe("Confidence Intervals", () => {
  test("does not label a task's own score as significant", async ({ page }) => {
    await mockAllAPIs(page);
    await page.goto("/#/runs/run-001");

    // The badge compared a CI on one task's own score with zero, which is not
    // a significance test, so it was removed.
    await expect(page.locator('[data-testid="ci-range"]').first()).toBeVisible();
    await expect(page.locator('[data-testid="significance-badge"]')).toHaveCount(0);
  });

  test("shows CI range inline for tasks with bootstrap CI", async ({ page }) => {
    await mockAllAPIs(page);
    await page.goto("/#/runs/run-001");

    const ciRanges = page.locator('[data-testid="ci-range"]');
    // 3 tasks have bootstrapCI set
    await expect(ciRanges).toHaveCount(3);

    // explain-fibonacci: lower=0.82, upper=0.98 → [82.0%, 98.0%]
    await expect(ciRanges.nth(0)).toContainText("[82.0%, 98.0%]");

    // explain-binary-search: lower=-0.05, upper=0.15 → [-5.0%, 15.0%]
    await expect(ciRanges.nth(2)).toContainText("[-5.0%, 15.0%]");
  });

  test("does not show a CI range for tasks without CI data", async ({ page }) => {
    await mockAllAPIs(page);
    await page.goto("/#/runs/run-001");

    // explain-merge-sort has no CI data — 4 task rows total but only 3 ranges
    await expect(page.locator('[data-testid="ci-range"]')).toHaveCount(3);
  });

  test("CI range has tooltip with full interval", async ({ page }) => {
    await mockAllAPIs(page);
    await page.goto("/#/runs/run-001");

    const ciRange = page.locator('[data-testid="ci-range"]').first();
    await expect(ciRange).toHaveAttribute("title", /95% CI/);
  });
});
