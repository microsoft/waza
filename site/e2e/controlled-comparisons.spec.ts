import { expect, test } from '@playwright/test';

test('controlled comparison guide exposes the real offline boundary and distinct API contract', async ({ page }) => {
  const response = await page.goto('/waza/guides/controlled-comparisons/');
  expect(response?.status()).toBe(200);
  await expect(page.getByRole('heading', { name: 'Controlled comparisons', exact: true })).toBeVisible();
  await expect(page.locator('main')).toContainText('Mock observations validate the harness, not agent quality');
  await expect(page.locator('main')).toContainText('waza.release-decision-view');
  await expect(page.locator('main')).toContainText('one deterministic cluster does not justify a release claim');
  await expect(page.locator('main')).toContainText('Old executables reject the new unknown flags');
  await expect(page.getByRole('heading', { name: 'Opt in separately to offline assurance', exact: true })).toBeVisible();
  await expect(page.locator('main')).toContainText('waza.release-assured-decision');
  await expect(page.locator('main')).toContainText('fresh exact offline1.0 reports');
  await expect(page.getByRole('heading', { name: 'Internal native groundwork is not a public producer', exact: true })).toBeVisible();
  await expect(page.locator('main')).toContainText('Static possible slots are not actual starts, skips or consumed retries');
  await expect(page.locator('main')).toContainText('not independently witnessed execution');
  await expect(page.locator('main')).toContainText('support Linux/macOS only, not Windows');
  await expect(page.locator('main')).toContainText('legacy decoders and command defaults remain unchanged');
});

test('CLI reference exposes dedicated offline planning and collection', async ({ page }) => {
  const response = await page.goto('/waza/reference/cli/');
  expect(response?.status()).toBe(200);
  await expect(page.getByRole('heading', { name: 'waza compare-plan', exact: true })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'waza compare-collect', exact: true })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'waza compare-assurance-plan', exact: true })).toBeVisible();
  await expect(page.locator('main')).toContainText('--collection-dir');
  await expect(page.locator('main')).toContainText('--assurance-contract');
  await expect(page.locator('main')).toContainText('no historical adoption or resume');
});
