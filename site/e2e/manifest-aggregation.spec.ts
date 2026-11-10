import { expect, test } from '@playwright/test';

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
  await expect(content.getByRole('link', { name: 'immutable publication workflow' }))
    .toHaveAttribute('href', '../immutable-artifacts/');
});
