import { expect, test } from '@playwright/test';

test('offline preflight guide separates static references from runtime assurance', async ({ page }) => {
  await page.goto('/waza/guides/preflight/');
  await expect(page.getByRole('heading', { name: 'Offline Eval Preflight', exact: true })).toBeVisible();
  await expect(page.locator('main')).toContainText('Verified never means requirement satisfied');
  await expect(page.locator('main')).toContainText('uncovered/unresolved');
  await expect(page.locator('main')).toContainText('grader.result_collision');
  await expect(page.locator('main')).toContainText('waza.preflight');
  await expect(page.locator('main')).toContainText('Exit policy is driven by diagnostics');
  await expect(page.locator('main')).toContainText('no context/cancellation parameter');
  await expect(page.locator('main')).toContainText('nonempty description');
  await expect(page.locator('main')).toContainText('An individual valid task glob may match nothing');
  await expect(page.locator('main')).toContainText('after symlinks resolve');
  await expect(page.locator('main')).toContainText('row-2, not row-1');
  await expect(page.locator('main')).toContainText('Regrading and run analysis use the same CSV loader');
  await expect(page.locator('main')).toContainText('physical equivalence alone does not grant admission');
  await expect(page.locator('main')).toContainText('by omitting --task to grade all selected tasks');
  await expect(page.locator('main')).toContainText('unrelated schema loader failures remain invalid');
  await expect(page.locator('main')).toContainText('including non-string kind values');
  await expect(page.locator('main')).toContainText('filesystem/volume root');
  await expect(page.locator('main')).toContainText('Contained absolute CSV paths retain their actual source');
  await expect(page.locator('main')).toContainText('workspace-root destinations');
  await expect(page.locator('main')).toContainText('Later grader schema-file and MCP input-schema compilation');
  await expect(page.locator('main')).toContainText('containing .. within any cleaned segment');
});

test('scenario admission history documents the intentional compatibility change and migration', async ({ page }) => {
  await page.goto('/waza/reference/schema-changes/');
  await expect(page.getByRole('heading', { name: 'Scenario admission correction (2026-10-12)', exact: true })).toBeVisible();
  await expect(page.locator('main')).toContainText('intentional compatibility change');
  await expect(page.locator('main')).toContainText('scenario: null');
  await expect(page.locator('main')).toContainText('Remove the field for legacy behavior');
  await expect(page.locator('main')).toContainText('Truly absent scenario retains existing legacy defaults');
  await page.goto('/waza/guides/eval-yaml/');
  await expect(page.locator('main')).toContainText('Presence is checked in native YAML/JSON parsing');
  await expect(page.locator('main')).toContainText('Remove the field for a legacy suite');
  await expect(page.locator('main')).toContainText('Fallback IDs/names retain the original row number');
  await expect(page.locator('main')).toContainText('Contained links are allowed');
});

test('CLI reference documents opt-in preflight exits', async ({ page }) => {
  await page.goto('/waza/reference/cli/');
  await expect(page.getByRole('heading', { name: 'waza preflight', exact: true })).toBeVisible();
  await expect(page.locator('main')).toContainText('Usage/output errors use exit 2');
});
