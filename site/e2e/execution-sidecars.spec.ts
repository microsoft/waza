import { expect, test } from '@playwright/test';
import { readFileSync } from 'node:fs';

test('codec-preview guidance links to actual versioned sidecar schemas', async ({ page, request }) => {
  await page.goto('/waza/guides/immutable-artifacts/');
  await expect(page.getByRole('heading', { name: 'Logical work and executions: codec preview', exact: true })).toBeVisible();
  await expect(page.getByText('codec-only source preview', { exact: true })).toBeVisible();

  const expected = [
    ['attempt and duplicate-decision schema', 'execution-attempt-1.0.schema.json', 'waza.execution-attempt'],
    ['completion-reference schema', 'execution-completion-1.0.schema.json', 'waza.execution-completion'],
  ] as const;
  for (const [label, filename, kind] of expected) {
    const link = page.getByRole('link', { name: label, exact: true });
    const href = await link.getAttribute('href');
    if (href === null) throw new Error(`Missing schema link target: ${label}`);
    expect(new URL(href, page.url()).pathname).toBe(`/waza/schemas/${filename}`);
    await link.click();
    await expect(page).toHaveURL(new RegExp(`/waza/schemas/${filename.replaceAll('.', '\\.')}$`));
    const response = await request.get(page.url());
    expect(response.ok()).toBe(true);
    expect(response.headers()['content-type']?.split(';')[0]).toBe('application/json');
    const schema = await response.json();
    const original = JSON.parse(readFileSync(new URL(`../../schemas/${filename}`, import.meta.url), 'utf8'));
    expect(schema).toEqual(original);
    expect(schema.$schema).toBe('https://json-schema.org/draft/2020-12/schema');
    expect(schema.$id).toBe(`https://microsoft.github.io/waza/schemas/${filename}`);
    expect(JSON.stringify(schema)).toContain(kind);
    await page.goBack();
  }

  const attempt = await request.get('/waza/schemas/execution-attempt-1.0.schema.json');
  const attemptSchema = await attempt.json();
  expect(attemptSchema.$defs.attempt.required).toContain('binding');
  expect(attemptSchema.$defs.decision.required).toContain('policy');
  expect(attemptSchema.$defs.attempt.properties.binding.$ref).toBe('artifact-manifest-1.0.schema.json#/properties/binding');
  const completion = await request.get('/waza/schemas/execution-completion-1.0.schema.json');
  const completionSchema = await completion.json();
  expect(completionSchema.required).toEqual(['kind', 'schema_version', 'execution_id', 'reservation_digest', 'manifest_digest']);
  expect(completionSchema.additionalProperties).toBe(false);
  const manifest = await request.get('/waza/schemas/artifact-manifest-1.0.schema.json');
  expect(manifest.ok()).toBe(true);
});
