import { expect, test } from '@playwright/test';

test('runner contract is navigable from CLI with schema and migration guidance', async ({ page }) => {
  await page.goto('/waza/reference/cli/');
  await page.getByRole('link', { name: 'public runner guide', exact: true }).click();
  await expect(page).toHaveURL(/\/waza\/guides\/runner-contract\/$/);
  await expect(page.getByRole('heading', { name: 'Public single-unit runner', exact: true })).toBeVisible();
  await expect(page.getByRole('link', { name: 'Request schema', exact: true }))
    .toHaveAttribute('href', 'https://github.com/microsoft/waza/blob/main/schemas/runner-request-1.0.schema.json');
  await expect(page.getByRole('link', { name: 'result schema', exact: true }))
    .toHaveAttribute('href', 'https://github.com/microsoft/waza/blob/main/schemas/runner-result-1.0.schema.json');
  await expect(page.getByRole('link', { name: 'archive page schema', exact: true }))
    .toHaveAttribute('href', 'https://github.com/microsoft/waza/blob/main/schemas/runner-results-1.1.schema.json');
  await expect(page.getByRole('main')).toContainText('current server emits 1.1');
  await expect(page.getByRole('main')).toContainText('overflow-detection byte');
  await expect(page.getByRole('main')).toContainText('publication_failed');
  await expect(page.getByRole('heading', { name: 'Migration and container walkthrough', exact: true })).toBeVisible();
  await expect(page.getByRole('main')).toContainText('no dashboard UI change');
  await page.getByRole('link', { name: 'Runner compatibility and migration', exact: true }).click();
  await expect(page).toHaveURL(/\/waza\/guides\/runner-migration\/$/);
  await expect(page.getByRole('heading', { name: 'Compatibility matrix', exact: true })).toBeVisible();
  await expect(page.getByRole('main')).toContainText('No Lab UI change is required for #761');
  await page.getByRole('link', { name: 'Bounded runner reference', exact: true }).click();
  await expect(page).toHaveURL(/\/waza\/guides\/runner-reference\/$/);
  await expect(page.getByRole('heading', { name: 'Publication status: no digest is available', exact: true })).toBeVisible();
  await expect(page.getByRole('main')).toContainText('No Lab UI change is required for #762');
  await page.getByRole('link', { name: 'v0.40.0 technical notes (unpublished)', exact: true }).click();
  await expect(page).toHaveURL(/\/waza\/releases\/v0-40-0\/$/);
  await expect(page.getByRole('main')).toContainText('no published digest is currently available');
  await page.goto('/waza/guides/runner-contract/');
  await page.getByRole('link', { name: 'the Lab guide', exact: true }).click();
  await expect(page).toHaveURL(/\/waza\/guides\/dashboard-lab\/#inspect-terminal-runner-results$/);
  await expect(page.getByRole('heading', { name: 'Inspect terminal runner results', exact: true })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Runner archive and API troubleshooting', exact: true })).toBeVisible();
  await expect(page.getByRole('main')).toContainText('The current endpoint returns archive page schema 1.1');
  await expect(page.getByRole('main')).toContainText('Caller disconnected after admission');
  await expect(page.getByRole('main')).toContainText('HTTP 200');
  await expect(page.getByRole('main')).toContainText('no-dashboard-UI');
  const screenshot = page.getByRole('img', { name: 'Fixture-backed Lab runner result with a recorded timeout, negotiation, exact work tuple, identity lineage and diagnostics.', exact: true });
  await expect(screenshot).toBeVisible();
  expect(await screenshot.evaluate((image: HTMLImageElement) => image.complete && image.naturalWidth > 0)).toBe(true);
});
