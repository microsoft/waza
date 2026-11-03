import { expect, test } from '@playwright/test';

test('local compatibility navigation, profiles and executable examples retain /waza', async ({ page }) => {
  await page.goto('/waza/reference/cli/');
  await page.getByRole('link', { name: 'local compatibility and migration', exact: true }).click();
  await expect(page).toHaveURL(/\/waza\/guides\/runner-compatibility\/$/);
  const table = page.getByRole('table').first();
  await expect(table).toContainText('run --runner-compat');
  await expect(table).toContainText('schema 1.7');
  await expect(table).toContainText('Unknown', { ignoreCase: true });
  const evalExample = page.getByRole('figure', { name: 'custom-suite.yaml', exact: true });
  await expect(evalExample.locator('code')).toContainText('tasks: ["tasks/selected.case.yaml"]');
  await expect(evalExample.locator('code')).toContainText('executor: mock');
  await expect(page.getByRole('figure', { name: 'tasks/selected.case.yaml', exact: true }).locator('code')).toContainText('output_contains: [hello]');
  await expect(page.getByText('No Lab dashboard change is required for #761.', { exact: true })).toBeVisible();
  await expect(page.locator('main')).toContainText('No success-shaped fallback');
  await page.getByRole('link', { name: 'terminal JSON and exits 0-6', exact: true }).click();
  await expect(page).toHaveURL(/\/waza\/guides\/runner-terminal\/$/);
  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Local Runner Compatibility', exact: true }).click();
  await expect(page).toHaveURL(/\/waza\/guides\/runner-compatibility\/$/);
});
