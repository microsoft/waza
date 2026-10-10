import { test, expect } from "@playwright/test";
import { RELEASE_COLLECTIONS } from "./fixtures/release-data";

test("release-policies screenshot", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 720 });
  await page.route(/\/api\/release-collections$/, (route) => route.fulfill({ json: RELEASE_COLLECTIONS }));
  await page.goto("/#/release");
  await expect(page.getByText("Selected policy: NOT PASSED", { exact: true })).toBeVisible();
  await page.screenshot({
    path: "../docs/images/release-policies.png",
    animations: "disabled",
    fullPage: true,
  });
});
