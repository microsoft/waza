import { expect, test } from '@playwright/test';

test('compatibility guide exposes the offline gate and recorded-evidence limitations', async ({ page }) => {
  await page.goto('/waza/guides/compatibility/');
  await expect(page.getByRole('heading', { name: 'Preserving Existing Workflows', exact: true })).toBeVisible();
  await expect(page.locator('main')).toContainText('NO_COLOR=1 make test-compat');
  await expect(page.locator('main')).toContainText('not standalone runnable eval suites');
  await expect(page.getByRole('link', { name: 'engineering inventory', exact: true }))
    .toHaveAttribute('href', 'https://github.com/microsoft/waza/blob/main/docs/COMPATIBILITY.md');
});
