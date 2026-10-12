import { expect, test } from '@playwright/test';

test('scenario guide exposes exact versions, discovery controls and resource roots', async ({ page }) => {
  await page.goto('/waza/guides/eval-yaml/');
  const main = page.locator('main');
  await expect(main).toContainText('waza new eval inventory --scenario --template repository');
  await expect(main).toContainText('schemaVersion: "2.0"');
  await expect(main).toContainText('skill_directories: []');
  await expect(main).toContainText('--no-skills');
  await expect(main).toContainText('inputs.context.fixture: fixtures/inventory.txt');
  await expect(main).toContainText('bypasses result caching');
  await expect(main).toContainText('not agent quality');
});

test('CLI reference distinguishes scenario outputs from legacy trigger tasks', async ({ page }) => {
  await page.goto('/waza/reference/cli/');
  const main = page.locator('main');
  await expect(main).toContainText('workflow<files.taskFileSuffix>');
  await expect(main).toContainText('Wildcard-directory globs that select the default tasks/ path remain supported.');
  await expect(main).toContainText('refuses to overwrite any generated file');
  await expect(main).toContainText('The three trigger tasks above belong to the default skill flow.');
});
