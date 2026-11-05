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
  await expect(page.getByRole('heading', { name: 'Opt in to native Copilot collection', exact: true })).toBeVisible();
  await expect(page.locator('main')).toContainText('fixed named models, single turns, no skills, deny-all client tools and native text/string checks');
  await expect(page.locator('main')).toContainText('does not support hooks, MCP, repositories, cache, concurrency, resume, checkpoints, multiturn or other graders');
  await expect(page.locator('main')).toContainText('Planning does not invoke the SDK; collection may incur provider usage');
  await expect(page.locator('main')).toContainText('Required prior usage that is unavailable blocks further starts');
  await expect(page.locator('main')).toContainText('already-running work can overshoot limits asynchronously');
  await expect(page.locator('main')).toContainText('Required unsupported runtime, assurance or currency evidence does not pass');
  await expect(page.locator('main')).toContainText('Static possible slots or caller-supplied tape joins do not themselves prove actual execution, durable acknowledgment, retry permission, currentness or release authority');
  await expect(page.locator('main')).toContainText('Native durable creation supports Linux/macOS only; Windows supplied-reader checks do not certify writing');
  await expect(page.locator('main')).toContainText('Client tool denial is not server isolation');
  await expect(page.locator('main')).toContainText('required collection_profile: "copilot_sdk_single_turn_text_v1" discriminator');
  await expect(page.locator('main')).toContainText('Frozen core-only readers reject both the outer envelope and an extracted core policy');
  await expect(page.locator('main')).toContainText('selected source files and ordinary evaluator input files to 16 MiB each, while the captured evaluator executable is limited to 256 MiB. Aggregate sources are limited to 512 MiB and 16,384 entries, and the full capture wire to 1 GiB.');
  await expect(page.locator('main')).toContainText('artifact-admission bounds, not process-memory caps; decoding large captures may require additional memory');
  await expect(page.locator('main')).toContainText('Native tapes and record streams are limited to 16 MiB');
  await expect(page.locator('main')).toContainText('Operationally incomplete native collection returns nonzero even if core publication completed');
  await expect(page.locator('main')).toContainText('native-stop.json');
  await expect(page.locator('main')).toContainText('may coexist with its attempts');
  await expect(page.locator('main')).toContainText('Ordinary mock policy and gate defaults do not change');
  await expect(page.locator('main')).toContainText('decision.native');
  await expect(page.locator('main')).toContainText('Snapshot-only metadata cannot satisfy selected required release evidence');
  await expect(page.locator('main')).toContainText('not the mapped running image or RAM');
});

test('dashboard guide preserves native attempt errors and scopes base assurance behavior', async ({ page }) => {
  const response = await page.goto('/waza/guides/dashboard/');
  expect(response?.status()).toBe(200);
  const main = page.locator('main');
  await expect(main).toContainText('optional decision.native data exposes the profile/version');
  await expect(main).toContainText('Collection errors are preserved alongside attempts');
  await expect(main).toContainText('JSON export must preserve these optional observations and errors');
  await expect(main).toContainText('For legacy/mock collections, this view intentionally preserves base 1.0 decisions');
  await expect(main).toContainText('A selected native collection can pass its supported base decision within the bounded profile, but it cannot satisfy required assurance');
});

test('Lab guide distinguishes native release evidence from descriptive comparisons and run reports', async ({ page }) => {
  const response = await page.goto('/waza/guides/dashboard-lab/');
  expect(response?.status()).toBe(200);
  const main = page.locator('main');
  await expect(main).toContainText('it does not assess controlled release policies or load native collection sidecars');
  await expect(main).toContainText('decision JSON and native-attempt CSV exports, separate from Lab');
  await expect(main).toContainText('Viewing saved evidence does not start collection');
  await expect(main).toContainText('Native assurance is unsupported');
  await expect(main).toContainText('sanitized mocked CLI attempts in Trajectory');
  await main.getByRole('link', { name: 'release evidence view', exact: true }).click();
  await expect(page).toHaveURL(/\/waza\/guides\/dashboard\/#release-policies$/);
  await expect(page.getByRole('heading', { name: 'Release policies', exact: true })).toBeVisible();
  await page.goBack();
  await main.getByRole('link', { name: 'assurance inspector', exact: true }).click();
  await expect(page).toHaveURL(/\/waza\/guides\/dashboard\/#grader-assurance$/);
  await expect(page.getByRole('heading', { name: 'Grader Assurance', exact: true })).toBeVisible();
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
