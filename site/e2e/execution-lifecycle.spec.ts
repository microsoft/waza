import { expect, test } from '@playwright/test';

test('lifecycle migration notes retain declaration-only limits and schema links under the deployed base', async ({ page, request }) => {
  await page.goto('/waza/reference/schema-changes/');
  await expect(page.getByRole('heading', {
    name: 'Lifecycle declarations and inspection 1.0 (unreleased source preview)', exact: true,
  })).toBeVisible();
  await expect(page.getByText('Every projected value is caller-declared/unverified;', { exact: false })).toBeVisible();
  for (const [label, file, kind] of [
    ['lifecycle observation 1.0 schema', 'execution-lifecycle-observation-1.0', 'waza.execution-lifecycle-observation'],
    ['lifecycle inspection 1.0 schema', 'execution-lifecycle-inspection-1.0', 'waza.execution-lifecycle-inspection'],
    ['lifecycle projection 1.0 schema', 'execution-lifecycle-1.0', 'waza.execution-lifecycle'],
  ]) {
    const href = await page.getByRole('link', { name: label, exact: true }).getAttribute('href');
    if (href === null) throw new Error(`Missing lifecycle schema link: ${label}`);
    const url = new URL(href, page.url());
    expect(url.pathname).toBe(`/waza/schemas/${file}.schema.json`);
    const response = await request.get(url.toString());
    expect(response.status()).toBe(200);
    expect(response.headers()['content-type']).toContain('application/schema+json');
    const schema = await response.json();
    expect(schema.$id).toBe(`https://microsoft.github.io/waza/schemas/${file}.schema.json`);
    expect(schema.properties.kind.const).toBe(kind);
    expect(schema.properties.schema_version.const).toBe('1.0');
    expect(schema.additionalProperties).toBe(false);
  }
});
