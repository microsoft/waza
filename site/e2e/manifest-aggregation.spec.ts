import { expect, test } from '@playwright/test';

test('aggregate navigation, operational commands and schema preserve the deployed base', async ({ page, request }) => {
  await page.goto('/waza/reference/cli/');
  await page.getByRole('link', { name: 'completed-manifest examples and migration', exact: true }).click();
  await expect(page).toHaveURL(/\/waza\/guides\/manifest-aggregation\/$/);
  await expect(page.getByText('Unreleased v0.41 source preview.', { exact: false })).toBeVisible();
  await expect(page.locator('main')).toContainText('membership');
  await expect(page.locator('main')).toContainText('infrastructure_failure');
  const schema = page.getByRole('link', { name: 'aggregate schema', exact: true });
  const url = new URL((await schema.getAttribute('href'))!, page.url());
  expect(url.pathname).toBe('/waza/schemas/manifest-aggregate-1.0.schema.json');
  const response = await request.get(url.toString());
  expect(response.status()).toBe(200);
  const value = await response.json();
  expect(value.properties.kind.const).toBe('waza.manifest-aggregate');
  expect(value.properties.publication.const).toBe('derived');
  expect(value.additionalProperties).toBe(false);
  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Completed Manifest Aggregation', exact: true }).click();
  await expect(page).toHaveURL(/\/waza\/guides\/manifest-aggregation\/$/);
});
