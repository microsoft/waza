import { test, expect, type Page } from "@playwright/test";
import type { RunSummary } from "../src/api/client";
import { RUNS } from "./fixtures/mock-data";
import { mockAllAPIs } from "./helpers/api-mock";

async function mockTableRuns(page: Page, runs: RunSummary[]) {
  await mockAllAPIs(page);
  await page.route(/\/api\/runs(\?|$)/, (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(runs),
    }),
  );
}

test.describe("Runs table v9 features", () => {
  test("natural sorting cycles through ascending, descending, and original order", async ({
    page,
  }) => {
    await mockTableRuns(page, [
      { ...RUNS[0]!, spec: "run10" },
      { ...RUNS[1]!, spec: "run1" },
      { ...RUNS[2]!, spec: "run2" },
    ]);
    await page.goto("/");

    const specs = page.locator("tbody tr td:nth-child(2)");
    const header = page.getByRole("button", { name: "Spec", exact: true });
    await expect(specs).toHaveText(["run10", "run1", "run2"]);

    await header.click();
    await expect(specs).toHaveText(["run1", "run2", "run10"]);
    await header.click();
    await expect(specs).toHaveText(["run10", "run2", "run1"]);
    await header.click();
    await expect(specs).toHaveText(["run10", "run1", "run2"]);

    await header.click();
    await expect(specs).toHaveText(["run1", "run2", "run10"]);
    await page.locator("tbody tr").last().click();
    await expect(page).toHaveURL(/#\/runs\/run-001$/);
  });

  test("text and numeric sorting retain their original first-click directions", async ({
    page,
  }) => {
    await mockTableRuns(page, [
      { ...RUNS[0]!, spec: "first", model: "Zeta", judgeModel: "Zeta", tokens: 20 },
      { ...RUNS[1]!, spec: "second", model: "alpha", judgeModel: "alpha", tokens: 3 },
      { ...RUNS[2]!, spec: "third", model: "Beta", tokens: 100 },
    ]);
    await page.goto("/");

    const specs = page.locator("tbody tr td:nth-child(2)");
    await expect(specs).toHaveText(["first", "second", "third"]);
    await page.getByRole("button", { name: "Model", exact: true }).click();
    await expect(specs).toHaveText(["second", "third", "first"]);

    const tokens = page.getByRole("button", { name: "Tokens", exact: true });
    await tokens.click();
    await expect(specs).toHaveText(["third", "first", "second"]);
    await tokens.click();
    await expect(specs).toHaveText(["second", "first", "third"]);
    await tokens.click();
    await expect(specs).toHaveText(["first", "second", "third"]);
  });

  test("timestamp sorting orders ISO strings chronologically in both directions", async ({
    page,
  }) => {
    await mockTableRuns(page, [
      { ...RUNS[0]!, spec: "middle", timestamp: "2026-10-12T12:00:00.000Z" },
      { ...RUNS[1]!, spec: "oldest", timestamp: "2026-10-01T12:00:00.000Z" },
      { ...RUNS[2]!, spec: "newest", timestamp: "2026-10-13T12:00:00.000Z" },
    ]);
    await page.goto("/");

    const specs = page.locator("tbody tr td:nth-child(2)");
    await expect(specs).toHaveText(["middle", "oldest", "newest"]);
    const when = page.getByRole("button", { name: "When", exact: true });
    await when.click();
    await expect(specs).toHaveText(["oldest", "middle", "newest"]);
    await when.click();
    await expect(specs).toHaveText(["newest", "middle", "oldest"]);
  });

  test("sizing and visible cells preserve display columns and filtered API data", async ({
    page,
  }) => {
    await mockTableRuns(page, [
      {
        ...RUNS[1]!,
        taskCount: 0,
        passCount: 0,
        weightedScore: undefined,
        aiCredits: undefined,
      },
    ]);
    await page.goto("/");

    const rows = page.locator("tbody tr");
    await expect(rows).toHaveCount(1);
    const cells = rows.locator("td");
    await expect(cells).toHaveCount(10);
    await expect(cells.nth(1)).toHaveText("skill-checker");
    await expect(cells.nth(2)).toContainText("claude-sonnet-4");
    await expect(
      cells.nth(2).locator("[title='Judge: claude-opus-4.6']"),
    ).toBeVisible();
    await expect(cells.nth(3)).toHaveText("0%");
    await expect(cells.nth(4)).toHaveText("—");
    await expect(cells.nth(7)).toHaveText("—");
    await expect(cells.first().locator("svg.text-red-500")).toBeVisible();
    await expect(page.locator("thead th").first()).toHaveAttribute(
      "style",
      "width: 40px;",
    );
    await expect(
      page.getByRole("columnheader", { name: "Pass Rate", exact: true }).getByRole("button"),
    ).toHaveCount(0);
    await expect(
      page.getByRole("columnheader", { name: "W. Score", exact: true }).getByRole("button"),
    ).toHaveCount(0);
  });
});
