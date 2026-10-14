import { test, expect, type Page } from "@playwright/test";
import { readFile } from "node:fs/promises";
import { mockAllAPIs, mockEmptyAPIs } from "./helpers/api-mock";
import { RUNS, RUN_DETAIL, RUN_DETAIL_B } from "./fixtures/mock-data";

async function compareCredits(page: Page, a: number | null, b: number | null) {
  await mockAllAPIs(page);
  await page.route(/\/api\/runs\/run-00[12]$/, (route) =>
    route.fulfill({
      json: route.request().url().endsWith("run-001")
        ? { ...RUN_DETAIL, aiCredits: a }
        : { ...RUN_DETAIL_B, aiCredits: b },
    }),
  );
  await page.goto("/#/compare");
  await page.locator("select").nth(0).selectOption("run-001");
  await page.locator("select").nth(1).selectOption("run-002");
  await expect(page.getByText("Metrics Comparison")).toBeVisible();
  return page.getByText("AI Credits", { exact: true }).locator("..");
}

test("CSV export preserves numeric credits, nano precision, and empty unavailable cells", async ({ page }) => {
  await mockAllAPIs(page);
  await page.route(/\/api\/runs(\?|$)/, (route) =>
    route.fulfill({ json: [
      { ...RUNS[0], aiCredits: 0.00001 },
      { ...RUNS[1], aiCredits: 1.123456789 },
      RUNS[2],
    ] }),
  );
  await page.goto("/");
  await expect(page.getByText("code-explainer")).toBeVisible();
  await expect(page.locator("tbody").getByText("0.00001", { exact: true })).toBeVisible();
  await expect(page.locator("tbody").getByText("1.123456789", { exact: true })).toBeVisible();
  const downloadEvent = page.waitForEvent("download");
  await page.getByRole("button", { name: "Export CSV" }).click();
  const download = await downloadEvent;
  expect(download.suggestedFilename()).toBe("waza-runs.csv");
  const path = await download.path();
  expect(path).not.toBeNull();
  const csv = await readFile(path!, "utf8");
  const [header, ...rows] = csv.split("\n").map((line) => line.split(","));
  expect(header).toContain("AI Credits");
  expect(header.join(",")).not.toMatch(/Cost|USD/);
  const index = header.indexOf("AI Credits");
  expect(rows.map((row) => row[index])).toEqual(["0.00001", "1.123456789", ""]);
  expect(Number(rows[0][index])).toBe(0.00001);
  expect(Number(rows[1][index])).toBe(1.123456789);
});

test("detail CSV export still downloads task and grader diagnostics without estimated cost", async ({ page }) => {
  await mockAllAPIs(page);
  await page.goto("/#/runs/run-001");
  await expect(page.getByRole("heading", { name: "code-explainer" })).toBeVisible();
  const downloadEvent = page.waitForEvent("download");
  await page.getByRole("button", { name: "Export CSV" }).click();
  const download = await downloadEvent;
  const path = await download.path();
  const csv = await readFile(path!, "utf8");
  expect(csv).toContain("Task,Outcome,Score");
  expect(csv).toContain("explain-fibonacci");
  expect(csv).not.toMatch(/Est\. Cost|USD/);
});

test("AI Credits comparison shows both values and an increase delta", async ({ page }) => {
  const card = await compareCredits(page, 2.5, 4.75);
  await expect(card.locator("span.text-lg").nth(0)).toHaveText("2.50");
  await expect(card.locator("span.text-lg").nth(1)).toHaveText("4.75");
  await expect(card).toContainText("↑ 2.25");
  await expect(card.locator(".text-red-500")).toContainText("2.25");
  await expect(page.getByText(/Est\. Cost/)).toHaveCount(0);
});

test("AI Credits comparison preserves nano-unit delta precision and direction", async ({ page }) => {
  const card = await compareCredits(page, 0.000000002, 0.000000001);
  await expect(card.locator("span.text-lg").nth(0)).toHaveText("0.000000002");
  await expect(card.locator("span.text-lg").nth(1)).toHaveText("0.000000001");
  await expect(card.locator(".text-green-500")).toContainText("↓ 0.000000001");
});

test("equal AI Credits comparison has no fabricated delta", async ({ page }) => {
  const card = await compareCredits(page, 0, 0);
  await expect(card.locator("span.text-lg")).toHaveText(["0.00", "0.00"]);
  await expect(card).toContainText("—");
});

for (const missing of ["A", "B", "both"]) {
  test(`AI Credits comparison is unavailable with ${missing} missing`, async ({ page }) => {
    const card = await compareCredits(page, missing === "B" ? 2.5 : null, missing === "A" ? 4.75 : null);
    await expect(card.locator("span.text-lg").nth(0)).toHaveText(missing === "B" ? "2.50" : "—");
    await expect(card.locator("span.text-lg").nth(1)).toHaveText(missing === "A" ? "4.75" : "—");
    await expect(card.locator("span.text-zinc-400")).toHaveText("—");
    await expect(card).not.toContainText(/[↑↓]/);
  });
}

for (const availability of ["reported", "mixed", "unavailable"] as const) {
  test(`AI Credits trends handle ${availability} runs`, async ({ page }) => {
    await mockAllAPIs(page);
    const credits = availability === "reported" ? [0, 0.00001, 1.123456789]
      : availability === "mixed" ? [2.5, null, 4.75] : [null, null, null];
    await page.route(/\/api\/runs(\?|$)/, (route) => route.fulfill({
      json: RUNS.map((run, i) => ({ ...run, timestamp: `2026-09-0${i + 1}T12:00:00Z`, aiCredits: credits[i] })),
    }));
    await page.goto("/#/trends");
    const chart = page.getByTestId("ai-credits-trend");
    await expect(chart.getByText("AI Credits per Run")).toBeVisible();
    await expect(page.getByText(/Est\. Cost/)).toHaveCount(0);
    const missing = credits.filter((value) => value == null).length;
    await expect(chart.getByRole("status")).toHaveText(`${missing} of 3 runs have unavailable AI Credit usage and are not charted.`);
    await expect(chart.locator("svg circle")).toHaveCount(3 - missing);
    if (availability === "unavailable") {
      await expect(chart.getByText("AI Credit usage unavailable for these runs.")).toBeVisible();
      await expect(chart.locator("svg")).toHaveCount(0);
    } else {
      await expect(chart.locator("svg")).toBeVisible();
      await expect(chart.locator("svg")).toContainText("9/1");
      await expect(chart.locator("svg")).toContainText("9/3");
      if (availability === "mixed") {
        await expect(chart.locator("svg")).not.toContainText("9/2");
        await page.locator("select").selectOption("claude-sonnet-4");
        await expect(chart.getByRole("status")).toContainText("1 of 1");
        await expect(chart.getByText("AI Credit usage unavailable for these runs.")).toBeVisible();
      }
    }
  });
}

test("average credits tooltip states the reporting-run denominator", async ({ page }) => {
  await mockAllAPIs(page);
  await page.goto("/");
  const tooltip = page.getByRole("button", { name: /Average final AI Credit usage/ });
  await expect(tooltip).toHaveAttribute("title", /only runs with complete reported totals/);
  await expect(tooltip).toHaveAttribute("title", /excluded from the denominator/);
});

test("unavailable average credits tooltip describes all runs, not one legacy run", async ({ page }) => {
  await mockEmptyAPIs(page);
  await page.goto("/");
  await expect(page.getByRole("button", { name: /Average AI Credit usage unavailable/ }))
    .toHaveAttribute("title", /none of the runs report a complete final total/);
});
