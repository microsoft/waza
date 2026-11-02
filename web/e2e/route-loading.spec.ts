import { test, expect } from "@playwright/test";
import { mockAllAPIs } from "./helpers/api-mock";

test("home loads without a route loading state", async ({ page }) => {
  await mockAllAPIs(page);
  await page.addInitScript(() => {
    const loadingStatuses: string[] = [];
    Object.defineProperty(window, "__routeLoadingStatuses", {
      value: loadingStatuses,
    });
    new MutationObserver((records) => {
      for (const record of records) {
        for (const node of record.addedNodes) {
          if (!(node instanceof Element)) continue;
          const statuses = node.matches('[role="status"]')
            ? [node]
            : Array.from(node.querySelectorAll('[role="status"]'));
          loadingStatuses.push(
            ...statuses.map((status) => status.textContent ?? ""),
          );
        }
      }
    }).observe(document, { childList: true, subtree: true });
  });
  await page.goto("/");

  await expect(page.getByText("code-explainer")).toBeVisible();
  await expect(page.getByRole("status")).toHaveCount(0);
  expect(
    await page.evaluate(
      () =>
        (
          window as typeof window & {
            __routeLoadingStatuses: string[];
          }
        ).__routeLoadingStatuses,
    ),
  ).toEqual([]);
});

test("lazy route loading is announced without moving keyboard focus", async ({
  page,
}) => {
  await mockAllAPIs(page);
  let blockChunks = false;
  let releaseLazyChunks: (() => void) | undefined;
  const lazyChunksReleased = new Promise<void>((resolve) => {
    releaseLazyChunks = resolve;
  });

  await page.route(/\/assets\/[^/]+\.js$/, async (route) => {
    if (blockChunks) await lazyChunksReleased;
    await route.continue();
  });
  await page.goto("/");
  blockChunks = true;

  const compareLink = page.getByRole("link", { name: "Compare" });
  await compareLink.focus();
  await page.keyboard.press("Enter");

  await expect(page.getByRole("status")).toHaveText(
    "Loading run comparison...",
  );
  await expect(compareLink).toBeFocused();

  releaseLazyChunks?.();
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
