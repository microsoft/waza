import { expect, test } from '@playwright/test';
import checkpointSchema from '../../schemas/execution-checkpoint-1.0.schema.json' with { type: 'json' };

test('checkpoint schema notes preserve scope, version and deployed base', async ({ page, request }) => {
  await page.goto('/waza/reference/schema-changes/');
  await expect(page.getByRole('heading', { name: 'Checkpoint declaration 1.0 (unreleased contract layer)', exact: true })).toBeVisible();
  await expect(page.getByText('It does not yet provide durable checkpoint creation, a resume command or a checkpoint Lab view.', { exact: true })).toBeVisible();
  const link = page.getByRole('link', { name: 'checkpoint declaration 1.0 schema', exact: true });
  const href = await link.getAttribute('href');
  expect(href).not.toBeNull();
  const url = new URL(href!, page.url());
  expect(url.pathname).toBe('/waza/schemas/execution-checkpoint-1.0.schema.json');
  const response = await request.get(url.toString());
  expect(response.status()).toBe(200);
  expect(response.headers()['content-type']).toMatch(/^application\/json(?:;\s*charset=utf-8)?$/i);
  const schema = await response.json();
  expect(schema).toEqual(checkpointSchema);
  expect(schema.$schema).toBe('https://json-schema.org/draft/2020-12/schema');
  expect(schema.$id).toBe('https://microsoft.github.io/waza/schemas/execution-checkpoint-1.0.schema.json');
  expect(schema.properties.kind.const).toBe('waza.execution-checkpoint');
  expect(schema.properties.schema_version.const).toBe('1.0');
  expect(schema.properties.boundary.enum).toEqual(['before-unit', 'finalized-unit']);
  expect(schema.additionalProperties).toBe(false);
  expect(schema.required).toContain('created_at');
  expect(schema.required).toContain('compatibility');
  expect(schema.oneOf[0].not.anyOf).toEqual([{ required: ['completion_digest'] }, { required: ['manifest_digest'] }]);
  expect(schema.oneOf[1].required).toEqual(['completion_digest', 'manifest_digest']);
});
