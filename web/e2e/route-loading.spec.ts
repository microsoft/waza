import { test, expect } from "@playwright/test";
import { gzipSync } from "node:zlib";
import { mockAllAPIs } from "./helpers/api-mock";

const lazyRoutes = [
  { path: "/runs/run-001", label: "run details", heading: "code-explainer" },
  { path: "/compare", label: "run comparison", heading: "Compare Runs" },
  { path: "/trends", label: "trends", heading: "Trends" },
  { path: "/live", label: "live view", heading: "Live" },
  { path: "/assurance", label: "report inspector", heading: "Grader assurance" },
  { path: "/release", label: "release policies", heading: "Release policies" },
  { path: "/lab/demo", label: "dashboard lab", heading: "Know where your skills stand." },
];

test("initial production JavaScript stays within 105 KB gzip", async ({ page }) => {
  await mockAllAPIs(page);
  const scripts = new Map<string, Promise<number>>();
  page.on("response", (response) => {
    if (new URL(response.url()).pathname.match(/^\/assets\/[^/]+\.js$/)) {
      scripts.set(
        response.url(),
        response.body().then((body) => gzipSync(body).byteLength),
      );
    }
  });
  await page.goto("/");
  await expect(page.getByText("code-explainer")).toBeVisible();
  const sizes = await Promise.all(scripts.values());
  expect(sizes.length, "must measure production JavaScript, including any separate runtime").toBeGreaterThan(0);
  const total = sizes.reduce((sum, size) => sum + size, 0);
  await test.info().attach("initial-javascript-gzip.json", {
    body: JSON.stringify({ assets: [...scripts.keys()], gzipBytes: sizes, total }),
    contentType: "application/json",
  });
  expect(total).toBeLessThanOrEqual(105_000);
});

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

for (const target of lazyRoutes) {
  test(`${target.path} loads directly`, async ({ page }) => {
    await mockAllAPIs(page);
    await page.goto(`/#${target.path}`);
    await expect(
      page.getByRole("heading", { name: target.heading, exact: true }),
    ).toBeVisible();
  });

  test(`${target.path} announces delayed in-app loading`, async ({ page }) => {
    await mockAllAPIs(page);
    let blockChunks = false;
    let releaseLazyChunks!: () => void;
    const lazyChunksReleased = new Promise<void>((resolve) => {
      releaseLazyChunks = resolve;
    });

    await page.route(
      (url) => /^\/assets\/[^/]+\.js$/.test(url.pathname),
      async (route) => {
        if (blockChunks) await lazyChunksReleased;
        await route.continue();
      },
    );
    await page.goto("/");
    await expect(page.getByText("code-explainer")).toBeVisible();
    blockChunks = true;

    const link = page.locator(`a[href="#${target.path}"]`).first();
    try {
      if (target.path === "/lab/demo" || target.path === "/runs/run-001") {
        // These routes have no persistent navigation anchor in the original dashboard.
        await page.evaluate((path) => { window.location.hash = path; }, target.path);
      } else {
        await link.focus();
        await page.keyboard.press("Enter");
      }

      await expect(page.getByRole("status")).toHaveText(`Loading ${target.label}...`);
      if (target.path === "/lab/demo") {
        await expect(page.getByRole("status")).toHaveClass("p-8 text-zinc-100");
      } else if (target.path !== "/runs/run-001") {
        await expect(link).toBeFocused();
      }
    } finally {
      releaseLazyChunks();
    }
    await expect(
      page.getByRole("heading", { name: target.heading, exact: true }),
    ).toBeVisible();
    // Run-detail links belong to the home table, which is intentionally unmounted.
    if (target.path !== "/lab/demo" && target.path !== "/runs/run-001") {
      await expect(link).toBeFocused();
    }
  });
}

for (const target of lazyRoutes.filter(({ path }) => path === "/compare" || path === "/lab/demo")) {
  test(`${target.path} recovers from a failed chunk download`, async ({ page }) => {
    await mockAllAPIs(page);
    let failChunks = false;
    await page.route(
      (url) => /^\/assets\/[^/]+\.js$/.test(url.pathname),
      (route) => failChunks ? route.abort("failed") : route.continue(),
    );
    await page.goto("/");
    await expect(page.getByText("code-explainer")).toBeVisible();
    failChunks = true;
    await page.evaluate((path) => { window.location.hash = path; }, target.path);

    await expect(page.getByRole("alert")).toContainText("This dashboard view could not be loaded.");
    if (target.path === "/compare") {
      await expect(page.getByRole("link", { name: "Compare", exact: true })).toBeVisible();
    }
    await page.getByRole("link", { name: "Return to runs", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Eval Runs", exact: true })).toBeVisible();

    failChunks = false;
    await page.getByRole("link", { name: "Trends", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Trends", exact: true })).toBeVisible();
    await page.evaluate((path) => { window.location.hash = path; }, target.path);
    await expect(page.getByRole("alert")).toBeVisible();
    await page.getByRole("button", { name: "Reload view", exact: true }).click();
    await expect(
      page.getByRole("heading", { name: target.heading, exact: true }),
    ).toBeVisible();
  });
}
