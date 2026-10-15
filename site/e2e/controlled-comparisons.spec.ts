import { expect, test } from '@playwright/test';

test('controlled comparison and compatibility guides are discoverable in navigation', async ({ page }) => {
  await page.goto('/waza/quick-start/');
  const navigation = page.getByRole('navigation', { name: 'Main' });
  const controlled = navigation.getByRole('link', { name: 'Controlled comparisons', exact: true });
  const compatibility = navigation.getByRole('link', { name: 'Preserving Existing Workflows', exact: true });
  await expect(controlled).toHaveAttribute('href', '/waza/guides/controlled-comparisons/');
  await expect(compatibility).toHaveAttribute('href', '/waza/guides/compatibility/');
  await controlled.click();
  await expect(page.getByRole('heading', { name: 'Controlled comparisons', exact: true })).toBeVisible();
  await compatibility.click();
  await expect(page.getByRole('heading', { name: 'Preserving Existing Workflows', exact: true })).toBeVisible();
});

test('controlled comparison guide exposes the real offline boundary and distinct API contract', async ({ page }) => {
  const response = await page.goto('/waza/guides/controlled-comparisons/');
  expect(response?.status()).toBe(200);
  await expect(page.getByRole('heading', { name: 'Controlled comparisons', exact: true })).toBeVisible();
  await expect(page.locator('main')).toContainText('Mock observations validate the harness, not agent quality');
  await expect(page.locator('main')).toContainText('waza.release-decision-view');
  await expect(page.locator('main')).toContainText('confinement, not snapshot immutability or authenticity');
  await expect(page.locator('main')).toContainText('Owned handles close before publication');
  await expect(page.locator('main')).toContainText('Windows supplied-artifact reading is not support for durable collection');
  await expect(page.locator('main')).toContainText('one deterministic cluster does not justify a release claim');
  await expect(page.locator('main')).toContainText('Old executables reject the new unknown flags');
  await expect(page.getByRole('heading', { name: 'Opt in separately to offline assurance', exact: true })).toBeVisible();
  await expect(page.locator('main')).toContainText('waza.release-assured-decision');
  await expect(page.locator('main')).toContainText('fresh exact offline1.0 reports');
  await expect(page.getByRole('heading', { name: 'Internal native groundwork is not a public producer', exact: true })).toBeVisible();
  await expect(page.locator('main')).toContainText('Static possible slots are not actual starts, skips or consumed retries');
  await expect(page.locator('main')).toContainText('not independently witnessed execution');
  await expect(page.locator('main')).toContainText('support Linux/macOS only, not Windows');
  await expect(page.locator('main')).toContainText('legacy decoders and command defaults remain unchanged');
  await expect(page.locator('main')).toContainText('snapshot-only metadata cannot satisfy selected required evidence');
  await expect(page.locator('main')).toContainText('not the mapped running image or RAM');
});

test('snapshot guide states the actual selected-preservation and replay limits', async ({ page }) => {
  const response = await page.goto('/waza/guides/snapshot-replay/');
  expect(response?.status()).toBe(200);
  await expect(page.getByRole('heading', { name: 'Snapshot & Replay', exact: true })).toBeVisible();
  const main = page.locator('main');
  await expect(main).toContainText('Snapshots preserve recorded observations, not a reconstructible workspace');
  await expect(main).toContainText('256 entry inspections and depth 32');
  await expect(main).toContainText('the same aggregate budget');
  await expect(main).toContainText('not the existing recursive fixture-digest inventory');
  await expect(main).toContainText('supported on Linux and macOS only');
  await expect(main).toContainText('not an atomic or authentic snapshot');
  await expect(main).toContainText('typed snapshot decoding ignores unknown fields rather than preserving them verbatim');
  await expect(main).toContainText('rules replace defaults unless extend: true');
});

test('CLI reference exposes dedicated offline planning and collection', async ({ page }) => {
  const response = await page.goto('/waza/reference/cli/');
  expect(response?.status()).toBe(200);
  await expect(page.getByRole('heading', { name: 'waza compare-plan', exact: true })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'waza compare-collect', exact: true })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'waza compare-assurance-plan', exact: true })).toBeVisible();
  await expect(page.locator('main')).toContainText('--collection-dir');
  await expect(page.locator('main')).toContainText('--assurance-contract');
  await expect(page.locator('main')).toContainText('no historical adoption or resume');
});
