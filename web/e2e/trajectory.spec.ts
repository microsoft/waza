import { test, expect } from "@playwright/test";
import { mockAllAPIs } from "./helpers/api-mock";
import { RUN_DETAIL } from "./fixtures/mock-data";

test.describe("Trajectory Viewer", () => {
  test.beforeEach(async ({ page }) => {
    await mockAllAPIs(page);
  });

  test("displays tool policy and denied attempts", async ({ page }) => {
    const detail = structuredClone(RUN_DETAIL);
    detail.tasks[0].sessionDigest!.toolPolicyMode = "allow_list";
    detail.tasks[0].sessionDigest!.toolPolicyDenials = [
      { tool: "bash", kind: "shell", reason: "Tool is undeclared" },
    ];
    await page.route(/\/api\/runs\/run-001$/, (route) =>
      route.fulfill({ json: detail }),
    );
    await page.goto("/#/runs/run-001");
    await page.getByRole("button", { name: "Trajectory" }).click();
    await page.getByRole("button", { name: "explain-fibonacci" }).click();
    await expect(page.getByText("Tool Policy", { exact: true })).toBeVisible();
    await expect(page.getByText("allow_list", { exact: true })).toBeVisible();
    await expect(page.getByText("bash (shell): Tool is undeclared")).toBeVisible();
  });

  test("trajectory tab exists and switches view", async ({ page }) => {
    await page.goto("/#/runs/run-001");
    await expect(page.getByRole("heading", { name: "code-explainer" })).toBeVisible();

    // Tab buttons should be visible
    const trajectoryTab = page.getByRole("button", { name: "Trajectory" });
    await expect(trajectoryTab).toBeVisible();

    // Click trajectory tab
    await trajectoryTab.click();

    // Should show task selection prompt
    await expect(page.getByText("Select a task to view its trajectory")).toBeVisible();
  });

  test("task list shows in trajectory tab", async ({ page }) => {
    await page.goto("/#/runs/run-001");

    // Switch to trajectory tab
    await page.getByRole("button", { name: "Trajectory" }).click();

    // All task names should appear as buttons
    await expect(page.getByRole("button", { name: "explain-fibonacci" })).toBeVisible();
    await expect(page.getByRole("button", { name: "explain-quicksort" })).toBeVisible();
    await expect(page.getByRole("button", { name: "explain-binary-search" })).toBeVisible();
    await expect(page.getByRole("button", { name: "explain-merge-sort" })).toBeVisible();
  });

  test("clicking a task opens trajectory viewer with session digest", async ({ page }) => {
    await page.goto("/#/runs/run-001");
    await page.getByRole("button", { name: "Trajectory" }).click();

    // Click the first task (has transcript + digest)
    await page.getByRole("button", { name: "explain-fibonacci" }).click();

    // Session digest card should render
    await expect(page.getByText("Session Digest")).toBeVisible();
    await expect(page.getByText("Turns")).toBeVisible();
    await expect(page.getByText("Tool Calls")).toBeVisible();

    // Digest values — scope to the digest card to avoid ambiguity
    const digestCard = page.locator("div").filter({ hasText: "Session Digest" }).first();
    await expect(digestCard.getByText("4,500")).toBeVisible(); // tokensIn
    await expect(digestCard.getByText("2,100")).toBeVisible(); // tokensOut
    await expect(digestCard.getByText("6,600")).toBeVisible(); // tokensTotal
  });

  test("session digest shows tools used", async ({ page }) => {
    await page.goto("/#/runs/run-001");
    await page.getByRole("button", { name: "Trajectory" }).click();
    await page.getByRole("button", { name: "explain-fibonacci" }).click();

    // Tools Used section — scope to digest card to avoid matching timeline entries
    const digestCard = page.locator("div").filter({ hasText: "Session Digest" }).first();
    await expect(digestCard.getByText("Tools Used")).toBeVisible();
    // Tool badges in the digest card use blue-400 styling (distinct from waterfall rows)
    await expect(digestCard.locator("span.text-blue-400").filter({ hasText: "read_file" })).toBeVisible();
    await expect(digestCard.locator("span.text-blue-400").filter({ hasText: "write_file" })).toBeVisible();
  });

  test("session digest shows errors", async ({ page }) => {
    await page.goto("/#/runs/run-001");
    await page.getByRole("button", { name: "Trajectory" }).click();
    await page.getByRole("button", { name: "explain-fibonacci" }).click();

    // Errors section
    await expect(page.getByText("Errors (1)")).toBeVisible();
    await expect(page.getByText("Rate limit exceeded").last()).toBeVisible();
  });

  test("timeline renders tool call entries", async ({ page }) => {
    await page.goto("/#/runs/run-001");
    await page.getByRole("button", { name: "Trajectory" }).click();
    await page.getByRole("button", { name: "explain-fibonacci" }).click();

    // Switch to the Events view to see individual event badges
    await page.getByRole("button", { name: "Events" }).click();

    // Events view should show event badges
    await expect(page.getByText("assistant turn")).toBeVisible();
    await expect(page.getByText("tool start").first()).toBeVisible();
    await expect(page.getByText("tool complete").first()).toBeVisible();

    // Tool names in timeline
    await expect(page.locator("p.text-sm").getByText("read_file")).toBeVisible();
    await expect(page.locator("p.text-sm").getByText("write_file")).toBeVisible();
  });

  test("tool call expand/collapse shows details", async ({ page }) => {
    await page.goto("/#/runs/run-001");
    await page.getByRole("button", { name: "Trajectory" }).click();
    await page.getByRole("button", { name: "explain-fibonacci" }).click();

    // Switch to the Events view which has expand/collapse rows
    await page.getByRole("button", { name: "Events" }).click();

    // Find a "Show details" button and click it
    const showDetailsBtn = page.getByRole("button", { name: "Show details" }).first();
    await expect(showDetailsBtn).toBeVisible();
    await showDetailsBtn.click();

    // Should now show "Hide details"
    await expect(page.getByRole("button", { name: "Hide details" }).first()).toBeVisible();
  });

  test("error events are highlighted with red styling", async ({ page }) => {
    await page.goto("/#/runs/run-001");
    await page.getByRole("button", { name: "Trajectory" }).click();
    await page.getByRole("button", { name: "explain-fibonacci" }).click();

    // Switch to the Events view to see individual event badges
    await page.getByRole("button", { name: "Events" }).click();

    // The error badge should have red text class
    const errorBadge = page.locator("span").filter({ hasText: /^error$/ });
    await expect(errorBadge).toBeVisible();
    await expect(errorBadge).toHaveClass(/text-red-400/);
  });

  test("fallback for task without transcript shows grader summary", async ({ page }) => {
    await page.goto("/#/runs/run-001");
    await page.getByRole("button", { name: "Trajectory" }).click();

    // Click explain-quicksort which has NO transcript
    await page.getByRole("button", { name: "explain-quicksort" }).click();

    // Should show fallback message
    await expect(page.getByText("No transcript data — showing grader-based summary")).toBeVisible();
  });

  test("back to task list button works", async ({ page }) => {
    await page.goto("/#/runs/run-001");
    await page.getByRole("button", { name: "Trajectory" }).click();
    await page.getByRole("button", { name: "explain-fibonacci" }).click();

    // Should see trajectory content
    await expect(page.getByText("Session Digest")).toBeVisible();

    // Click back
    await page.getByText("Back to task list").click();

    // Should see task list again
    await expect(page.getByText("Select a task to view its trajectory")).toBeVisible();
  });

  test("waterfall trace shows Aspire-style span rows with teal bars", async ({ page }) => {
    await page.goto("/#/runs/run-001");
    await page.getByRole("button", { name: "Trajectory" }).click();
    await page.getByRole("button", { name: "explain-fibonacci" }).click();

    // Trace header should show span count and tool summary
    await expect(page.getByText(/\d+ spans/)).toBeVisible();
    await expect(page.getByText("TRACE")).toBeVisible();

    // Each tool should appear as its own clickable row
    const readFileRow = page.locator("button").filter({ hasText: "read_file" }).first();
    await expect(readFileRow).toBeVisible();
    const writeFileRow = page.locator("button").filter({ hasText: "write_file" }).first();
    await expect(writeFileRow).toBeVisible();
  });

  test("clicking a waterfall span opens the detail panel", async ({ page }) => {
    await page.goto("/#/runs/run-001");
    await page.getByRole("button", { name: "Trajectory" }).click();
    await page.getByRole("button", { name: "explain-fibonacci" }).click();

    // Click the read_file span row
    await page.locator("button").filter({ hasText: "read_file" }).first().click();

    // Detail panel should appear with tool name, status badge, and Attributes section
    await expect(page.getByRole("button", { name: "Close detail panel" })).toBeVisible();
    await expect(page.getByText("Passed")).toBeVisible();
    await expect(page.getByText("Attributes")).toBeVisible();

    // Escape key closes the panel
    await page.keyboard.press("Escape");
    await expect(page.getByRole("button", { name: "Close detail panel" })).not.toBeVisible();
  });
});
