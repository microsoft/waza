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
  await expect(page.getByRole('heading', { name: 'Migration and container walkthrough', exact: true })).toBeVisible();
  await expect(page.getByRole('main')).toContainText('no dashboard UI change');
});
