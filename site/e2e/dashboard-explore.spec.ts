import { expect, test } from '@playwright/test';

test('dashboard-explore images load', async ({ page }) => {
  await page.goto('/guides/dashboard-explore/');
  await page.waitForLoadState('networkidle');

  const brokenImages = await page.$$eval('main img', (images) =>
    images
      .filter((img) => !img.complete || img.naturalWidth === 0)
      .map((img) => img.getAttribute('src') || '')
  );

  expect(brokenImages).toEqual([]);
});
