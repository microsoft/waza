import { expect, test } from '@playwright/test';

test('container quickstart is navigable and distinguishes local IDs from publication', async ({ page }) => {
  await page.goto('/waza/reference/cli/');
  await page.getByRole('link', { name: 'local runner container reference', exact: true }).click();
  await expect(page).toHaveURL(/\/waza\/guides\/runner-container\/$/);
  await expect(page.getByRole('heading', { name: 'Execute and inspect local outputs', exact: true })).toBeVisible();
  await expect(page.locator('main')).toContainText('Published native reference from v0.40.0 source; accepted on Linux AMD64.');
  await expect(page.locator('main')).toContainText('Publication and public-image acceptance are separate gates');
  await expect(page.locator('main')).toContainText('publisher run alone does not establish public visibility or caller acceptance');
  await expect(page.locator('main')).toContainText('manual verifier must anonymously pull the exact digest');
  await expect(page.locator('main')).toContainText('verify its source, platform and signed provenance');
  await expect(page.locator('main')).toContainText('ghcr.io/microsoft/waza-runner-reference@sha256:ded068a636dc721b8e38dc2a68bcbcbdb0d4b33ce5da8d182d562d189a712a39');
  await expect(page.locator('main')).toContainText('93e80d7fdcbf9206215366d6ecca7ed08d26b2b3');
  await expect(page.locator('main')).toContainText('1/1 test, 0 skipped');
  await expect(page.locator('main')).toContainText('waived for this one publication, not performed');
  await expect(page.locator('main')).toContainText('Dockerfile does not inject main.version; the source default is dev');
  await expect(page.locator('pre').filter({ hasText: 'docker pull --platform linux/amd64' }))
    .toContainText('native_image=');
  await expect(page.getByRole('link', { name: 'verifier result', exact: true }))
    .toHaveAttribute('href', 'https://github.com/microsoft/waza/actions/runs/41826111839');
  await expect(page.locator('pre').filter({ hasText: '--iidfile' })).toContainText('examples/runner/container/Dockerfile');
  const offlineRuns = page.locator('pre').filter({ hasText: '--pull=never' });
  await expect(offlineRuns).toHaveCount(2);
  await expect(offlineRuns.filter({ hasText: '--network=none' })).toHaveCount(2);
  await expect(page.locator('pre').filter({ hasText: 'node --test examples/runner/container/test.mjs' })).toHaveCount(1);
  await expect(page.locator('pre').filter({ hasText: 'node --test examples/runner/container/native-test.mjs' })).toHaveCount(1);
  await expect(page.locator('pre').filter({ hasText: 'node --test examples/runner/container/native-bundle-test.mjs' })).toHaveCount(1);
  await expect(page.locator('main')).toContainText('zero provider requests');
  await expect(page.locator('main')).toContainText('synthetic');
  await expect(page.locator('main')).toContainText('session.options.update');
  await expect(page.locator('main')).toContainText('success does not assert aggregate thresholds');
  await expect(page.locator('main')).toContainText('native-reference');
  await expect(page.locator('main')).toContainText('cross-UID host readability');
  await expect(page.locator('main')).toContainText('not inference-quality');
  const contract = page.getByRole('link', { name: 'public runner contract', exact: true });
  expect(new URL((await contract.getAttribute('href'))!, page.url()).pathname).toBe('/waza/guides/runner-contract/');
  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Runner Contract & API', exact: true }).click();
  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Local Runner Container', exact: true }).click();
  await expect(page).toHaveURL(/\/waza\/guides\/runner-container\/$/);
  await page.goto('/waza/reference/releases/');
  await page.getByRole('link', { name: 'local container foundation', exact: true }).click();
  await expect(page).toHaveURL(/\/waza\/guides\/runner-container\/$/);
});
