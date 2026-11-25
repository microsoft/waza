import { expect, test } from '@playwright/test';

test('dashboard-explore images load', async ({ page }) => {
  const response = await page.goto('guides/dashboard-explore/');
  expect(response?.status()).toBe(200);
  await page.waitForLoadState('networkidle');

  const brokenImages = await page.$$eval('main img', (images: HTMLImageElement[]) =>
    images
      .filter((img) => !img.complete || img.naturalWidth === 0)
      .map((img) => img.getAttribute('src') || '')
  );

  expect(await page.locator('main img').count()).toBeGreaterThan(0);
  expect(brokenImages).toEqual([]);
});
