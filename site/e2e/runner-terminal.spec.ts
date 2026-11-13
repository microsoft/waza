import { expect, test } from '@playwright/test';
import { readFileSync } from 'node:fs';

test('checkpoint source commands link to bounded recovery and protected HTTP limits', async ({ page }) => {
  await page.goto('/waza/reference/cli/');
  await expect(page.getByRole('heading', { name: 'Checkpoint commands (v0.41.0+)', exact: true })).toBeVisible();
  await page.getByRole('link', { name: 'complete workflow and recovery limits', exact: true }).click();
  await expect(page).toHaveURL(/\/waza\/guides\/immutable-artifacts\/#checkpoint-public-source-preview$/);
  const content = page.locator('main');
  await expect(content).toContainText('Build the owning CLI from a clean Git tree with VCS stamping enabled.');
  await expect(content).toContainText('zero new submission engine calls');
  await expect(content).toContainText('Unavailable progress is null, never an invented zero.');
  await expect(content).toContainText('POST /api/v1/checkpoints');
  await expect(content).toContainText('POST /api/v1/checkpoints/{digest}/resume');
  await expect(content).toContainText('Unsupported active providers fail closed');
  await expect(content).toContainText('no raw checkpoint GET endpoint is available');
  await expect(content).toContainText('A previously dispatched source is refused, not replayed.');
  await expect(content).toContainText('available in the Lab');
  await expect(content).toContainText('Full native-platform acceptance is not claimed');
  await expect(content.locator('pre').filter({ hasText: 'waza runner checkpoint create' })).toContainText('schema_version:"1.0"');
  await expect(content.locator('pre').filter({ hasText: 'waza runner checkpoint resume' })).toContainText('restored-result.json');
  await page.goto('/waza/guides/dashboard-lab/');
  const image = page.getByRole('img', { name: 'Source Lab showing a recorded protected mock API response: failed original terminal reused with zero new calls and exact JSON download.' });
  await expect(image).toBeVisible();
  expect(new URL((await image.getAttribute('src'))!, page.url()).pathname).toBe('/waza/images/dashboard-lab-checkpoint.png');
  expect(await image.evaluate((img) => img instanceof HTMLImageElement && img.complete && img.naturalWidth > 0)).toBe(true);
  await expect(page.locator('main')).toContainText('not the synthetic demo or live inference');
});

test('attempt preview links serve exact independent sidecar schemas below the deployed base', async ({ page, request }) => {
  await page.goto('/waza/guides/immutable-artifacts/');
  await expect(page.getByRole('heading', { name: 'Logical attempts: embedding preview', exact: true })).toBeVisible();
  for (const [label, filename] of [
    ['attempt/decision schema', 'execution-attempt-1.0.schema.json'],
    ['completion schema', 'execution-completion-1.0.schema.json'],
    ['submission schema', 'execution-submission-1.0.schema.json'],
    ['submission-result schema', 'execution-submission-result-1.0.schema.json'],
    ['checkpoint schema', 'execution-checkpoint-1.0.schema.json'],
    ['checkpoint-result schema', 'runner-checkpoint-result-1.0.schema.json'],
    ['checkpoint operation error schema', 'runner-checkpoint-result-1.1.schema.json'],
  ]) {
    const link = page.getByRole('link', { name: label, exact: true });
    const href = await link.getAttribute('href');
    expect(href).not.toBeNull();
    const url = new URL(href!, page.url());
    expect(url.pathname).toBe(`/waza/schemas/${filename}`);
    const response = await request.get(url.toString());
    expect(response.status()).toBe(200);
    expect(response.headers()['content-type']).toMatch(/^application\/(?:schema\+)?json(?:;|$)/);
    const original = JSON.parse(readFileSync(new URL(`../../schemas/${filename}`, import.meta.url), 'utf8'));
    expect(await response.json()).toEqual(original);
  }
});

test('terminal guide navigation, all outcome examples, schema and actual Lab screenshot retain /waza', async ({ page, request }) => {
  await page.goto('/waza/reference/cli/');
  await page.getByRole('link', { name: 'terminal outcomes, schema and migration', exact: true }).click();
  await expect(page).toHaveURL(/\/waza\/guides\/runner-terminal\/$/);
  const table = page.getByRole('table').first();
  for (const [exit, outcome] of ['success', 'evaluation_failure', 'infrastructure_failure', 'cancelled', 'timeout', 'unsupported_capability', 'invalid_input'].entries()) {
    await expect(table.getByRole('row').filter({ has: page.getByRole('cell', { name: outcome, exact: true }) })).toContainText(String(exit));
    const example = page.getByRole('region', { name: `Example ${outcome}`, exact: true });
    const envelope = JSON.parse((await example.locator('code').textContent())!);
    expect(envelope.kind).toBe('waza.runner-terminal');
    expect(envelope.outcome).toBe(outcome);
    expect(envelope.exit_code).toBe(exit);
  }
  const schema = page.getByRole('link', { name: 'terminal envelope 1.0 schema', exact: true });
  const url = new URL((await schema.getAttribute('href'))!, page.url());
  expect(url.pathname).toBe('/waza/schemas/runner-terminal-1.0.schema.json');
  const response = await request.get(url.toString());
  expect(response.status()).toBe(200);
  const value = await response.json();
  expect(value.properties.kind.const).toBe('waza.runner-terminal');
  expect(value.additionalProperties).toBe(false);
  await expect(page.getByText('synthetic schema examples', { exact: false })).toBeVisible();
  const image = page.getByRole('img', { name: /Actual mock producer terminal evaluation failure/ });
  await expect(image).toBeVisible();
  expect(new URL((await image.getAttribute('src'))!, page.url()).pathname).toBe('/waza/images/dashboard-lab-terminal.png');
  expect(await image.evaluate((img) => img instanceof HTMLImageElement && img.complete && img.naturalWidth > 0)).toBe(true);
  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Runner Contract & API', exact: true }).click();
  await expect(page).toHaveURL(/\/waza\/guides\/runner-contract\/$/);
});
