import { test, expect } from "@playwright/test";

test("private fault harness guide does not imply public runtime or platform serving", async ({ page }) => {
  await page.goto("/waza/guides/private-fault-fixtures/");
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Private Dependency-Fault Harness");
  await expect(page.locator("main")).toContainText("not registered in public eval/runtime configuration");
  await expect(page.locator("main")).toContainText("explicit positive worker capacity");
  await expect(page.locator("main")).toContainText("capacity overflow reject before preparation");
  await expect(page.locator("main")).toContainText("cross-compilation is not execution");
  await expect(page.locator("main")).toContainText("Responder metadata is not integration proof");
  await expect(page.locator("main")).toContainText("not filesystem or network sandboxing");
  await expect(page.locator("main")).toContainText("no runnable public finite recovery example");
  await expect(page.locator("main pre")).toContainText(
    "ENABLE_COPILOT_TESTS=false ENABLE_LIVE_INTEGRATION_TESTS=false NO_COLOR=1 go test"
  );
  await expect(page.locator("main")).toContainText(
    "the live integration switch does not disable Copilot SDK tests"
  );
});

test("reference pages link to the private guide under the site base path", async ({ page }) => {
  for (const reference of ["cli", "schema"]) {
    await page.goto(`/waza/reference/${reference}/`);
    await page.locator("main").getByRole("link", { name: "Private Fault Harness", exact: true }).click();
    await expect(page.getByRole("heading", { level: 1 })).toHaveText("Private Dependency-Fault Harness");
  }
  await page.locator("main").getByRole("link", { name: "compatibility corpus", exact: true }).click();
  await expect(page).toHaveURL(/\/waza\/guides\/compatibility\/$/);
});
