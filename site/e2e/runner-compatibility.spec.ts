import { expect, test } from '@playwright/test';

test('local compatibility navigation, profiles and executable examples retain /waza', async ({ page }) => {
  await page.goto('/waza/reference/cli/');
  await page.getByRole('link', { name: 'local compatibility and migration', exact: true }).click();
  await expect(page).toHaveURL(/\/waza\/guides\/runner-compatibility\/$/);
  const table = page.getByRole('table').first();
  await expect(table).toContainText('run --runner-compat');
  await expect(table.getByRole('row').filter({ hasText: 'Local results' })).toContainText('schema 1.8');
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

test('release preview links open the source-build runner guides under /waza', async ({ page }) => {
  await page.goto('/waza/');
  await expect(page.locator('main')).toContainText('Runner execution supports mock/text and explicitly opted-in Copilot/text with tools and skills disabled.');
  await expect(page.locator('main')).toContainText('General native file/tool/skill workflows, pinned published-image acceptance and the external schema adapter remain incomplete.');
  for (const [name, url] of [
    ['runner contract and exact work-unit guide', /\/waza\/guides\/runner-contract\/$/],
    ['logical identities', /\/waza\/guides\/runner-identities\/$/],
    ['terminal outcomes', /\/waza\/guides\/runner-terminal\/$/],
    ['local compatibility and migration', /\/waza\/guides\/runner-compatibility\/$/],
    ['task-level MCP mocks', /\/waza\/guides\/eval-yaml\/#task-level-mcp-mocks$/],
    ['program-grader context', /\/waza\/guides\/graders\/#program$/],
    ['host lifecycle evidence', /\/waza\/reference\/schema-changes\/#host-lifecycle-evidence-in-results-18$/],
    ['Lab conversations', /\/waza\/guides\/dashboard-lab\/#read-a-multi-turn-conversation$/],
  ]) {
    await page.goto('/waza/reference/releases/');
    await expect(page.getByRole('heading', { name: 'Source-build v0.40.0 preview' })).toBeVisible();
    await page.getByRole('link', { name, exact: true }).click();
    await expect(page).toHaveURL(url);
    const fragment = new URL(page.url()).hash.slice(1);
    if (fragment) await expect(page.locator(`[id="${fragment}"]`)).toBeAttached();
  }
});
