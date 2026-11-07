import { test, expect, type Page } from "@playwright/test";

const start = Date.UTC(2026, 0, 1);

test.use({ timezoneId: "UTC" });

function makeRuns(count: number, passCount = (index: number) => index % 101) {
  return Array.from({ length: count }, (_, index) => ({
    id: `run-${index}`,
    spec: "large-history",
    model: "gpt-5.4",
    outcome: "passed",
    passCount: passCount(index),
    taskCount: 100,
    tokens: 1000 + index,
    premiumRequests: 1,
    aiCredits: 1 + index / 1000,
    cost: 0.1,
    duration: 30 + index,
    timestamp: new Date(start + index * 3_600_000).toISOString(),
  }));
}

async function mockRuns(page: Page, runs: ReturnType<typeof makeRuns>) {
  await page.route(/\/api\/runs(\?|$)/, (route) =>
    route.fulfill({ json: runs }),
  );
}

test("large trend histories keep bounded DOM and exact endpoint tooltips", async ({
  page,
}) => {
  const count = 2000;
  await mockRuns(page, makeRuns(count));

  await page.goto("/#/trends");
  await expect(
    page.getByRole("heading", { name: "Duration per Run" }),
  ).toBeVisible();

  const pointPaths = page.getByTestId("trend-points");
  await expect(pointPaths).toHaveCount(4);
  for (const path of await pointPaths.all()) {
    await expect(path).toHaveAttribute("data-point-count", String(count));
    const data = await path.getAttribute("d");
    expect(data?.match(/\b[ML] /g)).toHaveLength(count);
    const marker = await path.getAttribute("marker-start");
    expect(marker).toMatch(/^url\(#trend-point-.+\)$/);
    await expect(path).toHaveAttribute("marker-mid", marker!);
    await expect(path).toHaveAttribute("marker-end", marker!);
  }
  expect(await page.locator("*").count()).toBeLessThan(500);

  const chart = pointPaths.first().locator("xpath=..");
  const box = await chart.boundingBox();
  expect(box).not.toBeNull();
  await page.mouse.move(box!.x + 56, box!.y + box!.height / 2);
  await expect(page.getByTestId("trend-tooltip").first()).toHaveText(
    /^0%\s*1\/1$/,
  );
  await page.mouse.move(box!.x + box!.width - 16, box!.y + box!.height / 2);
  await expect(page.getByTestId("trend-tooltip").first()).toHaveText(
    /^80%\s*3\/25$/,
  );
});

test("two-point midpoint ties select the earlier point", async ({ page }) => {
  await mockRuns(page, makeRuns(2, (index) => (index === 0 ? 25 : 75)));
  await page.goto("/#/trends");

  const chart = page.getByTestId("trend-points").first().locator("xpath=..");
  const box = await chart.boundingBox();
  expect(box).not.toBeNull();
  const plotMidpoint = box!.x + 56 + (box!.width - 56 - 16) / 2;
  await page.mouse.move(plotMidpoint, box!.y + box!.height / 2);
  await expect(page.getByTestId("trend-tooltip").first()).toHaveText(
    /^25%\s*1\/1$/,
  );
});

test("dense flat histories paint the last marker center above earlier outlines", async ({
  page,
}) => {
  const count = 5000;
  await mockRuns(page, makeRuns(count, () => 50));
  await page.goto("/#/trends");

  const chart = page.getByTestId("trend-points").first().locator("xpath=..");
  await expect(chart).toBeVisible();
  const pixel = await chart.evaluate(async (svg) => {
    const source = svg.cloneNode(true) as SVGSVGElement;
    const { width, height } = svg.getBoundingClientRect();
    source.setAttribute("xmlns", "http://www.w3.org/2000/svg");
    source.setAttribute("width", String(width));
    source.setAttribute("height", String(height));
    const image = new Image();
    const loaded = new Promise<void>((resolve, reject) => {
      image.onload = () => resolve();
      image.onerror = () => reject(new Error("failed to rasterize trend SVG"));
    });
    image.src = `data:image/svg+xml;charset=utf-8,${encodeURIComponent(
      new XMLSerializer().serializeToString(source),
    )}`;
    await loaded;
    const canvas = document.createElement("canvas");
    canvas.width = width;
    canvas.height = height;
    const context = canvas.getContext("2d");
    if (!context) throw new Error("2D canvas is unavailable");
    context.drawImage(image, 0, 0, width, height);
    return Array.from(
      context.getImageData(Math.round(width - 16), 94, 1, 1).data,
    );
  });
  expect(pixel).toEqual([59, 130, 246, 255]);
});
