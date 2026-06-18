import type { Page } from "@playwright/test";
import { SUMMARY, RUNS, RUN_DETAIL, RUN_DETAIL_B, HEALTH } from "../fixtures/mock-data";

/**
 * Intercept all /api/* routes and return deterministic mock data.
 * Uses regex patterns so query strings are handled correctly.
 * Must be called BEFORE page.goto().
 */
export async function mockAllAPIs(page: Page) {
  await page.route(/\/api\/health/, (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(HEALTH) }),
  );

  await page.route(/\/api\/summary/, (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(SUMMARY) }),
  );

  // Run detail — must be registered before the list route (Playwright matches LIFO)
  await page.route(/\/api\/runs\/(.+)/, (route) => {
    const url = new URL(route.request().url());
    const id = url.pathname.split("/").pop()!;
    if (id === "run-001") {
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(RUN_DETAIL) });
    }
    if (id === "run-002") {
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(RUN_DETAIL_B) });
    }
    return route.fulfill({
      status: 404,
      contentType: "application/json",
      body: JSON.stringify({ error: "run not found" }),
    });
  });

  // Runs list — matches /api/runs and /api/runs?sort=...&order=...
  await page.route(/\/api\/runs(\?|$)/, (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(RUNS) }),
  );
}

/**
 * Mock API to return empty data sets — useful for empty-state tests.
 */
export async function mockEmptyAPIs(page: Page) {
  await page.route(/\/api\/summary/, (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ totalRuns: 0, totalTasks: 0, passRate: 0, avgTokens: 0, avgPremiumRequests: 0, avgCost: 0, avgDuration: 0 }),
    }),
  );

  await page.route(/\/api\/runs(\?|$)/, (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify([]) }),
  );
}
