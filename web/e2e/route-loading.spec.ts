import { test, expect } from "@playwright/test";
import { mockAllAPIs } from "./helpers/api-mock";

test("home loads without a route loading state", async ({ page }) => {
  await mockAllAPIs(page);
  await page.goto("/");

  await expect(page.getByText("code-explainer")).toBeVisible();
  await expect(page.getByRole("status")).toHaveCount(0);
});

test("lazy route loading is announced without moving keyboard focus", async ({
  page,
}) => {
  await mockAllAPIs(page);
  let releaseCompareChunk: (() => void) | undefined;
  const compareChunkRequested = new Promise<void>((resolve) => {
    releaseCompareChunk = resolve;
  });

  await page.route(/\/assets\/CompareView-[^/]+\.js$/, async (route) => {
    await compareChunkRequested;
    await route.continue();
  });
  await page.goto("/");

  const compareLink = page.getByRole("link", { name: "Compare" });
  await compareLink.focus();
  await page.keyboard.press("Enter");

  await expect(page.getByRole("status")).toHaveText("Loading compare...");
  await expect(compareLink).toBeFocused();

  releaseCompareChunk?.();
  await expect(
    page.getByRole("heading", { name: "Compare Runs" }),
  ).toBeVisible();
  await expect(compareLink).toBeFocused();
});

test("a non-home deep link loads its route", async ({ page }) => {
  await mockAllAPIs(page);
  await page.goto("/#/trends");

  await expect(page.getByRole("heading", { name: "Trends" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Trends" })).toBeVisible();
});
