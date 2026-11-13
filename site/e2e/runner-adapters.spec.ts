import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';

for (const name of ['adapter-work-descriptor', 'adapter-translation', 'adapter-result']) {
  test(`${name} schema is published at its declared URL with exact source content`, async ({ request }) => {
    const schema = JSON.parse(readFileSync(new URL(`../../schemas/${name}-1.0.schema.json`, import.meta.url), 'utf8'));
    const response = await request.get(new URL(schema.$id).pathname);
    expect(response.status()).toBe(200);
    expect(await response.json()).toEqual(schema);
  });
}

test('adapter guide is reachable from the sidebar and states the translation-only boundary', async ({ page }) => {
  await page.goto('/waza/guides/runner-container/');
  const link = page.getByRole('link', { name: 'External Execution Adapters', exact: true });
  await expect(link).toHaveAttribute('href', '/waza/guides/runner-adapters/');
  await link.click();
  await expect(page).toHaveURL(/\/waza\/guides\/runner-adapters\/$/);
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Portable external execution adapters');
  const content = page.locator('.sl-markdown-content');
  await expect(content).toContainText('exactly one');
  await expect(content).toContainText('descriptor_field_unsupported');
  await expect(content).toContainText('waza runner adapter dispatch');
  await expect(content.getByRole('link', { name: 'adapter-result-1.0' }))
    .toHaveAttribute('href', '../../schemas/adapter-result-1.0.schema.json');
});
