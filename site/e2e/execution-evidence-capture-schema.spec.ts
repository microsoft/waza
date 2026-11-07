import { expect, test } from '@playwright/test';

test('capture guide links to the actual versioned evidence index schema', async ({ page, request }) => {
  await page.goto('/waza/guides/execution-evidence/');
  const link = page.getByRole('link', { name: 'recorded capture index schema', exact: true });
  const href = await link.getAttribute('href');
  if (href === null) throw new Error('Missing recorded capture schema link');
  expect(new URL(href, page.url()).pathname).toBe('/waza/schemas/execution-evidence-capture-1.0.schema.json');
  await link.click();
  const response = await request.get(page.url());
  expect(response.ok()).toBe(true);
  expect(response.headers()['content-type']).toContain('application/schema+json');
  const schema = await response.json();
  expect(schema.$id).toBe('https://microsoft.github.io/waza/schemas/execution-evidence-capture-1.0.schema.json');
  expect(schema.properties.kind.const).toBe('waza.execution-evidence-capture');
  expect(schema.properties.binding.$ref).toBe('artifact-manifest-1.0.schema.json#/properties/binding');
  expect(schema.properties.categories.$ref).toBe('execution-evidence-1.0.schema.json#/properties/categories');
  expect(schema.properties.empty_payloads.properties.conversation.const).toEqual([]);
  expect(schema.properties.empty_payloads.properties.grader.const).toEqual({});
  expect(schema.properties.origin_manifest).toBeUndefined();
  expect(schema.additionalProperties).toBe(false);
});
