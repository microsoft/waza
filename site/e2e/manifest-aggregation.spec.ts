import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';

test('aggregate schema is published at its declared URL with exact source content', async ({ request }) => {
  const filename = 'manifest-aggregate-1.0.schema.json';
  const schema = JSON.parse(readFileSync(new URL(`../../schemas/${filename}`, import.meta.url), 'utf8'));
  const response = await request.get(new URL(schema.$id).pathname);
  expect(response.status()).toBe(200);
  expect(response.headers()['content-type']).toMatch(/^application\/(?:schema\+)?json(?:;|$)/);
  expect(await response.json()).toEqual(schema);
});

test('aggregate guide navigation preserves the deployed base and population contract', async ({ page }) => {
  await page.goto('/waza/guides/immutable-artifacts/');
  const link = page.getByRole('link', { name: 'Aggregate Completed Manifests', exact: true });
  await expect(link).toHaveAttribute('href', '/waza/guides/manifest-aggregation/');
  await link.click();
  await expect(page).toHaveURL(/\/waza\/guides\/manifest-aggregation\/$/);
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Aggregate completed manifests');
  const content = page.locator('.sl-markdown-content');
  await expect(content).toContainText('input_members');
  await expect(content).toContainText('unique_verified_manifests');
  await expect(content).toContainText('unique_verified_execution_records');
  await expect(content).toContainText('Compatible multiple views remain visible');
  await expect(content).toContainText('Incomplete/conflicting reports are still emitted and saved');
  await expect(content).toContainText('historical_usage');
  await expect(content).toContainText('Unknown versions are explicit unsupported/invalid inputs');
  await expect(content.getByRole('link', { name: 'Aggregate schema 1.0', exact: true }))
    .toHaveAttribute('href', '../../schemas/manifest-aggregate-1.0.schema.json');
  await expect(content.getByRole('link', { name: 'immutable publication workflow' }))
    .toHaveAttribute('href', '../immutable-artifacts/');
});
