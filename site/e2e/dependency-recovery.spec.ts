import { test, expect } from "@playwright/test";

test("dependency recovery guide states public support and safety boundaries", async ({ page }) => {
  await page.goto("/waza/guides/dependency-recovery/");
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Deterministic Dependency Recovery");
  await expect(page.locator("main")).toContainText("authored mocked interactions");
  await expect(page.locator("main")).toContainText("notifications/cancelled");
  await expect(page.locator("main")).toContainText("at most 64 pending requests");
  await expect(page.locator("main")).toContainText("canceled calls receive no fabricated response");
  await expect(page.locator("main")).toContainText("Sequence state is scoped to one task attempt");
  await expect(page.locator("main")).toContainText("Every task, trial, and retry starts at the");
  await expect(page.locator("main")).toContainText("no command-line flag or wrapper is required");
  await expect(page.locator("main")).toContainText("If cleanup cannot complete, the run fails");
  await expect(page.locator("main")).toContainText("tasks without MCP mocks keep their existing keys");
  await expect(page.locator("main")).toContainText("exit_code omitted");
  await expect(page.locator("main")).toContainText("Writing the expected report without making the required calls does not pass");
  await expect(page.locator("main")).toContainText("undeclared executables may resolve through the host PATH");
  await expect(page.locator("main")).toContainText("not actual invocation or verified component behavior");
  await expect(page.locator("main")).toContainText("prohibited file side effect");
  await expect(page.locator("main a[href*='examples/scenarios/recovery/mcp/eval.yaml']")).toBeVisible();
  await expect(page.locator("main a[href*='examples/scenarios/recovery/cli/eval.yaml']")).toBeVisible();
});

test("reference pages link to the recovery guide under the site base path", async ({ page }) => {
  for (const reference of ["cli", "schema"]) {
    await page.goto(`/waza/reference/${reference}/`);
    await page.locator("main").getByRole("link", { name: "Dependency Recovery", exact: true }).click();
    await expect(page.getByRole("heading", { level: 1 })).toHaveText("Deterministic Dependency Recovery");
    await expect(page).toHaveURL(/\/waza\/guides\/dependency-recovery\/$/);
  }
});
