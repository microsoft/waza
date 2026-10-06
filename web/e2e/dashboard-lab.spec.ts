import { expect, test } from "@playwright/test";
import { readFile } from "node:fs/promises";
import { mockAllAPIs } from "./helpers/api-mock";

test.describe("Dashboard lab", () => {
  test("isolates the prototype and makes no API requests", async ({ page }) => {
    const apiRequests: string[] = [];
    page.on("request", (request) => {
      if (new URL(request.url()).pathname.startsWith("/api/"))
        apiRequests.push(request.url());
    });
    await page.goto("/#/lab/demo");
    await expect(
      page.getByRole("heading", { name: "Know where your skills stand." }),
    ).toBeVisible();
    await expect(
      page.locator(".lab-metrics").getByText("84", { exact: true }),
    ).toBeVisible();
    await expect(page.getByText("LOCAL · SYNTHETIC DATA")).toBeVisible();
    await page.getByRole("link", { name: "Run history" }).click();
    await expect(page.locator("tbody tr")).toHaveCount(84);
    await page.getByRole("link", { name: "Reports", exact: true }).click();
    await expect(
      page.getByRole("heading", { name: "Evaluation health report" }),
    ).toBeVisible();
    expect(apiRequests).toEqual([]);
  });

  test("cross-filters charts, metrics, history, and reports", async ({
    page,
  }) => {
    await page.goto("/#/lab/demo");
    await page
      .getByRole("button", { name: "Filter to gpt-5.4", exact: true })
      .click();
    await expect(
      page.locator(".lab-metrics").getByText("42", { exact: true }),
    ).toBeVisible();
    await page.getByRole("button", { name: "Oct 6", exact: true }).click();
    await expect(
      page.locator(".lab-metrics").getByText("3", { exact: true }),
    ).toBeVisible();
    await page.getByRole("link", { name: "Run history" }).click();
    await expect(page.locator("tbody tr")).toHaveCount(3);
    await page.getByRole("link", { name: "Reports", exact: true }).click();
    await expect(
      page.getByText("3 completed synthetic runs contain"),
    ).toBeVisible();
    await page.getByRole("button", { name: "Reset filters" }).click();
    await expect(
      page.getByText("84 completed synthetic runs contain"),
    ).toBeVisible();
  });

  test("supports searching, outcome filtering, and empty states", async ({
    page,
  }) => {
    await page.goto("/#/lab/demo/history");
    await page
      .getByRole("textbox", { name: "Search run history" })
      .fill("azure-deploy");
    await expect(page.locator("tbody tr")).toHaveCount(28);
    await page
      .getByRole("combobox", { name: "Filter time window" })
      .selectOption("1");
    await expect(page.locator("tbody tr")).toHaveCount(2);
    await page
      .getByRole("combobox", { name: "Filter outcome" })
      .selectOption("passed");
    await expect(page.locator("tbody tr")).toHaveCount(1);
    await page
      .getByRole("textbox", { name: "Search run history" })
      .fill("does-not-exist");
    await expect(
      page.getByRole("heading", { name: "No runs match this view" }),
    ).toBeVisible();
    await expect(
      page.locator(".lab-metrics").getByText("Unavailable"),
    ).toHaveCount(3);
  });

  test("compares aligned tasks without treating added coverage as zero", async ({
    page,
  }) => {
    await page.goto("/#/lab/demo");
    await page
      .getByRole("button", { name: /Azure deploy: investigate a regression/ })
      .click();
    await expect(page.getByText("-8.8 pp", { exact: true })).toBeVisible();
    await expect(
      page.getByText("8 comparable tasks", { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByText("Not comparable", { exact: true }),
    ).toBeVisible();
    await expect(page.locator(".lab-matrix tbody tr")).toHaveCount(9);
    await page.getByRole("button", { name: "Respect the tool policy" }).click();
    const dialog = page.getByRole("dialog");
    await expect(dialog.getByText(/Denied tool: bash/)).toBeVisible();
    await page.getByRole("tab", { name: "Trajectory", exact: true }).click();
    await expect(
      dialog.getByText(
        "Illustrative task trace, not a recorded Copilot session.",
      ),
    ).toBeVisible();
    await page.getByRole("tab", { name: "Configuration" }).click();
    await expect(dialog.locator("pre")).toContainText('"repetitions": 3');
    await page.keyboard.press("Escape");
    await expect(dialog).not.toBeVisible();
    await expect(
      page.getByRole("button", { name: "Respect the tool policy" }),
    ).toBeFocused();
  });

  test("keeps native modal focus inside the inspector", async ({ page }) => {
    await page.goto("/#/lab/demo");
    await page
      .getByRole("button", { name: "azure-deploy", exact: true })
      .first()
      .click();
    const dialog = page.getByRole("dialog");
    await expect(dialog).toBeVisible();
    for (let index = 0; index < 12; index++) {
      await page.keyboard.press("Tab");
      expect(
        await dialog.evaluate((element) =>
          element.contains(document.activeElement),
        ),
      ).toBe(true);
    }
    await page.getByRole("button", { name: "Close run inspector" }).click();
    await expect(dialog).not.toBeVisible();
  });

  test("restricts history comparisons to the same evaluation", async ({
    page,
  }) => {
    await page.goto("/#/lab/demo/history");
    await page
      .getByRole("checkbox", {
        name: "Select run-13-azure-deploy-0",
        exact: true,
      })
      .check();
    await page
      .getByRole("checkbox", {
        name: "Select run-13-code-review-0",
        exact: true,
      })
      .check();
    await expect(
      page.getByRole("button", { name: "Compare selected (2)" }),
    ).toBeDisabled();
    await expect(
      page.getByText("Select runs from the same evaluation suite"),
    ).toBeVisible();
    await page
      .getByRole("checkbox", {
        name: "Select run-13-code-review-0",
        exact: true,
      })
      .uncheck();
    await page
      .getByRole("checkbox", {
        name: "Select run-12-azure-deploy-0",
        exact: true,
      })
      .check();
    await page.getByRole("button", { name: "Compare selected (2)" }).click();
    await expect(
      page.getByRole("combobox", { name: "Candidate run" }),
    ).toHaveValue("run-13-azure-deploy-0");
    await expect(
      page.getByRole("combobox", { name: "Baseline run" }),
    ).toHaveValue("run-12-azure-deploy-0");
    await page
      .getByRole("combobox", { name: "Filter time window" })
      .selectOption("1");
    await page
      .getByRole("combobox", { name: "Filter model" })
      .selectOption("gpt-5.4");
    await expect(
      page.getByRole("heading", {
        name: "Two runs from the same eval are needed",
      }),
    ).toBeVisible();
  });

  test("simulation advances, terminates, resets, and stays separate from reports", async ({
    page,
  }) => {
    await page.goto("/#/lab/demo/current");
    const advance = page.getByRole("button", { name: "Advance one step" });
    await page.getByRole("button", { name: "Play simulation" }).click();
    await expect(
      page.getByRole("button", { name: "Pause playback" }),
    ).toBeVisible();
    await expect(advance).toBeDisabled();
    await page.getByRole("button", { name: "Pause playback" }).click();
    for (let index = 0; index < 16; index++) await advance.click();
    await expect(advance).toBeDisabled();
    await expect(page.getByText("Completed", { exact: true })).toHaveCount(2);
    await expect(page.getByText("Error", { exact: true })).toBeVisible();
    await expect(
      page.getByText(/run_failed: synthetic provider timeout/),
    ).toBeVisible();
    await page.getByRole("button", { name: "Reset simulation" }).click();
    await expect(page.getByText("Queued", { exact: true })).toHaveCount(2);
    await expect(advance).toBeEnabled();
    await page
      .getByRole("combobox", { name: "Filter skill" })
      .selectOption("azure-deploy");
    await expect(page.locator(".lab-live-grid .lab-panel")).toHaveCount(1);
    await page.getByRole("link", { name: "Reports", exact: true }).click();
    await expect(
      page.getByText("28 completed synthetic runs contain"),
    ).toBeVisible();
  });

  test("exports the exact filtered scope with unavailable billing preserved", async ({
    page,
  }) => {
    await page.goto("/#/lab/demo/reports");
    await page
      .getByRole("combobox", { name: "Filter skill" })
      .selectOption("azure-deploy");
    await page
      .getByRole("combobox", { name: "Filter model" })
      .selectOption("gpt-5.4");
    const jsonDownload = page.waitForEvent("download");
    await page.getByRole("button", { name: "Download JSON" }).click();
    const jsonPath = await (await jsonDownload).path();
    const report = JSON.parse(await readFile(jsonPath!, "utf8"));
    expect(report.synthetic).toBe(true);
    expect(report.summary.runs).toBe(14);
    expect(report.runs).toHaveLength(14);
    expect(
      report.runs.every(
        (run: { skill: string; model: string }) =>
          run.skill === "azure-deploy" && run.model === "gpt-5.4",
      ),
    ).toBe(true);
    expect(report.summary.creditCoverage).toBe(11);
    expect(
      report.runs.some(
        (run: { aiCredits?: number }) => run.aiCredits === undefined,
      ),
    ).toBe(true);
    expect(
      report.runs.some((run: { id: string }) => run.id.startsWith("demo")),
    ).toBe(false);
    const csvDownload = page.waitForEvent("download");
    await page.getByRole("button", { name: "Download CSV" }).click();
    const csvPath = await (await csvDownload).path();
    const csv = await readFile(csvPath!, "utf8");
    expect(csv.trim().split("\n")).toHaveLength(15);
    expect(csv).toContain("Synthetic,Run ID,Eval");
    expect(csv.split("\n").filter((row) => row.endsWith(","))).toHaveLength(3);
    await page.evaluate(() => {
      window.print = () => {
        document.body.dataset.printRequested = "yes";
      };
    });
    await page.getByRole("button", { name: "Print / save PDF" }).click();
    await expect(page.locator("body")).toHaveAttribute(
      "data-print-requested",
      "yes",
    );
    await page.emulateMedia({ media: "print" });
    await expect(page.locator(".lab-sidebar")).toBeHidden();
    await expect(
      page.getByRole("heading", { name: "Evaluation health report" }),
    ).toBeVisible();
  });

  test("covers skill metadata and history navigation", async ({ page }) => {
    await page.goto("/#/lab/demo/catalog");
    await expect(page.getByText("Over budget by 210 tokens")).toBeVisible();
    await expect(
      page.getByRole("heading", { name: "Waza capability map" }),
    ).toBeVisible();
    const card = page.locator(".lab-catalog-grid .lab-panel").filter({
      has: page.getByRole("heading", { name: "Code review", exact: true }),
    });
    await card
      .getByRole("button", { name: "Explore evaluation history" })
      .click();
    await expect(
      page.getByRole("combobox", { name: "Filter skill" }),
    ).toHaveValue("code-review");
    await expect(page.locator("tbody tr")).toHaveCount(28);
  });

  test("is responsive and returns to the unchanged original dashboard", async ({
    page,
  }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto("/#/lab/demo");
    await expect(
      page.getByRole("heading", { name: "Know where your skills stand." }),
    ).toBeVisible();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);
    await page.getByRole("link", { name: "Compare", exact: true }).click();
    await expect(
      page.getByRole("heading", { name: "Task-level score matrix" }),
    ).toBeVisible();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);
    await page.setViewportSize({ width: 1440, height: 1000 });
    await mockAllAPIs(page);
    await page.getByRole("link", { name: "Original dashboard" }).click();
    await expect(page.getByText("Total Runs", { exact: true })).toBeVisible();
    await expect(
      page.getByText("eval dashboard", { exact: true }),
    ).toBeVisible();
    await expect(page.locator(".lab-root")).toHaveCount(0);
    expect(
      await page
        .locator("html")
        .evaluate((element) => getComputedStyle(element).colorScheme),
    ).toBe("dark");
  });
});
