import { expect, test } from "@playwright/test";
import { mockAllAPIs } from "./helpers/api-mock";
import { RUN_DETAIL } from "./fixtures/mock-data";

test("shows safe trial diagnostics independently of graders", async ({ page }) => {
  await mockAllAPIs(page);
  await page.route(/\/api\/runs\/run-001$/, (route) => route.fulfill({ json: {
    ...RUN_DETAIL,
    tasks: [{
      ...RUN_DETAIL.tasks[0], name: "capture failure", outcome: "error", graderResults: [],
      infrastructureRuns: [
        { runNumber: 1, attempts: 2, diagnostic: { code: "workspace_capture_file_bytes", limit: 16777216 } },
        { runNumber: 2, attempts: 1 },
        { runNumber: 3, attempts: 4, diagnostic: { code: "/private/secret-token", limit: 9999999999999 } },
        { runNumber: 4, attempts: 1, diagnostic: { code: "workspace_capture_total_bytes", limit: -1 } },
      ],
    }, {
      ...RUN_DETAIL.tasks[0], name: "legacy failure", outcome: "error", graderResults: [],
      error_msg: "/private/secret-token",
    }],
  } }));
  await page.goto("/#/runs/run-001");
  await page.getByRole("row").filter({ hasText: "capture failure" }).click();
  await expect(page.getByText("Trial 1, attempt 2: Workspace capture exceeded the per-file byte limit (16777216).")).toBeVisible();
  await expect(page.getByText("Trial 3, attempt 4: An infrastructure error occurred.")).toBeVisible();
  await expect(page.getByText("Trial 4, attempt 1: Workspace capture exceeded the total byte limit.")).toBeVisible();
  await expect(page.getByText("Trial 2, attempt 1:")).toHaveCount(0);
  await page.getByRole("row").filter({ hasText: "legacy failure" }).click();
  await expect(page.getByText("An infrastructure error occurred.", { exact: true })).toBeVisible();
  await expect(page.locator("body")).not.toContainText("secret-token");
  await expect(page.locator("body")).not.toContainText("/private");
  await expect(page.locator("body")).not.toContainText("9999999999999");
});
