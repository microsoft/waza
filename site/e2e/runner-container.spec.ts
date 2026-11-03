import { expect, test } from '@playwright/test';

test('container quickstart is navigable and distinguishes local IDs from publication', async ({ page }) => {
  await page.goto('/waza/reference/cli/');
  await page.getByRole('link', { name: 'local runner container reference', exact: true }).click();
  await expect(page).toHaveURL(/\/waza\/guides\/runner-container\/$/);
  await expect(page.getByRole('heading', { name: 'Execute and inspect local outputs', exact: true })).toBeVisible();
  await expect(page.getByText('no published v0.40.0 runner image or registry digest', { exact: false })).toBeVisible();
  await expect(page.locator('pre').filter({ hasText: '--iidfile' })).toContainText('examples/runner/container/Dockerfile');
  await expect(page.locator('pre').filter({ hasText: '--pull=never' })).toContainText('--network=none');
  await expect(page.locator('pre').filter({ hasText: 'node --test' })).toContainText('examples/runner/container/test.mjs');
  const contract = page.getByRole('link', { name: 'public runner contract', exact: true });
  expect(new URL((await contract.getAttribute('href'))!, page.url()).pathname).toBe('/waza/guides/runner-contract/');
  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Runner Contract & API', exact: true }).click();
  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Local Runner Container', exact: true }).click();
  await expect(page).toHaveURL(/\/waza\/guides\/runner-container\/$/);
});
