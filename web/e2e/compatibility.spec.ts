import { test, expect } from "@playwright/test";
import { readFileSync } from "node:fs";
import { mockAllAPIs } from "./helpers/api-mock";

const legacy = JSON.parse(readFileSync(
  new URL("../../internal/testdata/compatibility/v1/dashboard-legacy.json", import.meta.url),
  "utf8",
));

for (const state of ["legacy", "cached", "legacy-usage", "reported-zero"] as const) {
  test(`compatibility: ${state} historical results remain readable`, async ({ page }) => {
    await mockAllAPIs(page);
    const detail = {
      ...legacy,
      ...(state === "legacy-usage" ? { tokens: 120, aiCredits: 5 } : {}),
      ...(state === "reported-zero" ? { tokens: 30, aiCredits: 0 } : {}),
      tasks: legacy.tasks.map((task: { sessionDigest: object }) => ({
        ...task,
        sessionDigest: {
          ...task.sessionDigest,
          ...(state !== "legacy" ? { tokensIn: 100, tokensOut: 20, tokensTotal: 120 } : {}),
        },
      })),
    };
    await page.route(/\/api\/runs\/compatibility-run$/, (route) => route.fulfill({ json: detail }));
    await page.goto("/#/runs/compatibility-run");
    await expect(page.getByRole("heading", { name: "compatibility", exact: true })).toBeVisible();
    const credits = page.getByText("AI Credits", { exact: true }).locator("..");
    await expect(credits).toContainText(state === "legacy-usage" ? "5.00" : state === "reported-zero" ? "0.00" : "—");
    await expect(page.getByText("Per-model usage unavailable for this run.")).toBeVisible();
    await expect(page.getByTestId("judge-model-badge")).toHaveCount(0);
    await page.getByText("Compatibility task", { exact: true }).click();
    await expect(page.getByText("answer", { exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Prompts", exact: true }).click();
    await expect(page.getByText(/No prompt was recorded for this task/)).toBeVisible();
    await page.getByRole("button", { name: "Trajectory", exact: true }).click();
    await page.getByRole("button", { name: "Compatibility task pass", exact: true }).click();
    await expect(page.getByText("No transcript data — showing grader-based summary")).toBeVisible();
  });
}
