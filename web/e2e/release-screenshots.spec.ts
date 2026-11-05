import { test, expect } from "@playwright/test";
import { RELEASE_COLLECTIONS } from "./fixtures/release-data";
import { nativeReleaseCollections } from "./fixtures/native-release-data";

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

test("native-release-evidence screenshot from producer retry fixture", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.route(/\/api\/release-collections$/, async (route) =>
    route.fulfill({ json: await nativeReleaseCollections("retry") }));
  await page.goto("/#/release");
  const observations = page.getByRole("region", { name: "Native attempt observations", exact: true });
  await expect(observations.locator("tbody tr")).toHaveCount(4);
  const recovered = observations.locator("tbody tr").nth(1);
  await recovered.locator("summary").filter({ hasText: /^Actual output/ }).click();
  await recovered.locator("summary").filter({ hasText: /^Lifecycle/ }).click();
  await expect(recovered.locator("pre")).toHaveText("hello");
  await page.screenshot({
    path: "../docs/images/native-release-evidence.png",
    animations: "disabled",
    fullPage: true,
  });
});
