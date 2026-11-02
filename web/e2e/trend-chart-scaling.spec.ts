import { test, expect } from "@playwright/test";

test("large trend histories keep bounded DOM and exact endpoint tooltips", async ({
  page,
}) => {
  const count = 2000;
  const start = Date.UTC(2026, 0, 1);
  const runs = Array.from({ length: count }, (_, index) => ({
    id: `run-${index}`,
    spec: "large-history",
    model: "gpt-5.4",
    outcome: "passed",
    passCount: index % 101,
    taskCount: 100,
    tokens: 1000 + index,
    premiumRequests: 1,
    aiCredits: 1 + index / 1000,
    cost: 0.1,
    duration: 30 + index,
    timestamp: new Date(start + index * 3_600_000).toISOString(),
  }));
  await page.route(/\/api\/runs(\?|$)/, (route) =>
    route.fulfill({ json: runs }),
  );

  await page.goto("/#/trends");
  await expect(
    page.getByRole("heading", { name: "Duration per Run" }),
  ).toBeVisible();

  const pointPaths = page.getByTestId("trend-points");
  await expect(pointPaths).toHaveCount(4);
  for (const path of await pointPaths.all()) {
    const data = await path.getAttribute("d");
    expect(data?.match(/\bM /g)).toHaveLength(count);
  }
  expect(await page.locator("*").count()).toBeLessThan(500);

  const chart = pointPaths.first().locator("xpath=..");
  const box = await chart.boundingBox();
  expect(box).not.toBeNull();
  await page.mouse.move(box!.x + 56, box!.y + box!.height / 2);
  await expect(page.getByTestId("trend-tooltip").first()).toContainText("1/1");
  await page.mouse.move(box!.x + box!.width - 16, box!.y + box!.height / 2);
  await expect(page.getByTestId("trend-tooltip").first()).toContainText("3/25");
});
