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

test('release links open the bounded runner guides under /waza', async ({ page }) => {
  await page.goto('/waza/releases/v0-40-0/');
  await expect(page).toHaveURL(/\/waza\/releases\/v0-40-0\/$/);
  const article = page.locator('main');
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Waza v0.40.0: run exactly the work you declared');
  await expect(article).toContainText('The shipped profiles are mock/text and explicitly opted-in Copilot/text.');
  await expect(article).toContainText('Copilot requires a model and paid-execution consent, with tools and skills disabled.');
  await expect(article).toContainText('General native file/tool/skill workflows remain unsupported.');
  await expect(article).toContainText('whole-task/suite interoperability and full evidence/provenance round trips remain unsupported');
  await expect(article).toContainText('legacy multi-trial timeout issue #865');
  await expect(article).toContainText('post-v0.40.0 source behavior, not included in the published v0.40.0 artifacts');
  await expect(article.getByRole('link', { name: '#865', exact: true }))
    .toHaveAttribute('href', 'https://github.com/microsoft/waza/issues/865');
  await expect(article.getByRole('link', { name: '#870', exact: true }))
    .toHaveAttribute('href', 'https://github.com/microsoft/waza/pull/870');
  for (const [name, url] of [
    ['runner contract', /\/waza\/guides\/runner-contract\/$/],
    ['identity guide and frozen vectors', /\/waza\/guides\/runner-identities\/$/],
    ['terminal guide', /\/waza\/guides\/runner-terminal\/$/],
    ['compatibility table', /\/waza\/guides\/runner-compatibility\/$/],
    [/^container guide['\u2019]s published-image record$/, /\/waza\/guides\/runner-container\/#published-image-and-acceptance-evidence$/],
    ['Task-level MCP mocks', /\/waza\/guides\/eval-yaml\/#task-level-mcp-mocks$/],
    ['Program graders', /\/waza\/guides\/graders\/#program$/],
    ['Host lifecycle evidence', /\/waza\/reference\/schema-changes\/#host-lifecycle-evidence-in-results-18$/],
    ['Conversation tab', /\/waza\/guides\/dashboard-lab\/#read-a-multi-turn-conversation$/],
    ['assertion adapter guide', /\/waza\/guides\/external-interoperability\/$/],
  ]) {
    await page.goto('/waza/releases/v0-40-0/');
    await page.locator('main').getByRole('link', { name, exact: true }).click();
    await expect(page).toHaveURL(url);
    const fragment = new URL(page.url()).hash.slice(1);
    if (fragment) await expect(page.locator(`[id="${fragment}"]`)).toBeAttached();
  }
});

test('release separates standalone assets, extension sync and image acceptance', async ({ page }) => {
  await page.goto('/waza/reference/releases/');
  const main = page.locator('main');
  await expect(main).toContainText('Current published standalone release: v0.40.1');
  await expect(main).toContainText('azd extension artifact and registry sync are independent gates');
  await expect(main).toContainText('legacy multi-trial timeout issue #865');
  await expect(main).toContainText('post-v0.40.0 source behavior, not included in the published v0.40.0 artifacts');
  await expect(main).toContainText('v0.40.0 result writers emit 1.8; v0.39.1 writes 1.7.');
  await expect(main).toContainText('schemas remain independently versioned 1.0');
  const downloads = main.locator('a[href*="/releases/download/v0.40.1/waza-"]');
  await expect(downloads).toHaveCount(6);
  for (const download of await downloads.all()) {
    await expect(download).toHaveAttribute('href', /\/releases\/download\/v0\.40\.1\/waza-/);
  }
  await expect(main.getByRole('link', { name: 'checksums.txt', exact: true }))
    .toHaveAttribute('href', 'https://github.com/microsoft/waza/releases/download/v0.40.1/checksums.txt');
  await expect(main).toContainText('Infrastructure-owner review was waived', { ignoreCase: true });
  await expect(main.getByRole('link', { name: '#865', exact: true }))
    .toHaveAttribute('href', 'https://github.com/microsoft/waza/issues/865');
  await expect(main.getByRole('link', { name: 'published extension release', exact: true }))
    .toHaveAttribute('href', 'https://github.com/microsoft/waza/releases/tag/azd-ext-microsoft-azd-waza_0.40.1');
  await expect(main).toContainText('The repository registry lists through 0.40.1.');
  await expect(main).toContainText('generated registry/version updates on release/v0.40.1');
  await expect(main).toContainText('still serves 0.40.0; after the merge it includes 0.40.1');
  await expect(main).toContainText('installation before the merge cannot select 0.40.1 from that source');
});
