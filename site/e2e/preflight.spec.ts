import { expect, test } from '@playwright/test';

test('offline preflight guide separates static references from runtime assurance', async ({ page }) => {
  await page.goto('/waza/guides/preflight/');
  await expect(page.getByRole('heading', { name: 'Offline Eval Preflight', exact: true })).toBeVisible();
  await expect(page.locator('main')).toContainText('Verified never means requirement satisfied');
  await expect(page.locator('main')).toContainText('uncovered/unresolved');
  await expect(page.locator('main')).toContainText('grader.result_collision');
  await expect(page.locator('main')).toContainText('waza.preflight');
});

test('CLI reference documents opt-in preflight exits', async ({ page }) => {
  await page.goto('/waza/reference/cli/');
  await expect(page.getByRole('heading', { name: 'waza preflight', exact: true })).toBeVisible();
  await expect(page.locator('main')).toContainText('Usage/output errors use exit 2');
});
