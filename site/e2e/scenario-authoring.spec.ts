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
  await expect(main).toContainText('symlinked roots or generated task/fixture parents are rejected');
  await expect(main).toContainText('retained directory handles');
  await expect(main).toContainText('byte-exact final preservation of inventory.txt');
  await expect(main).toContainText('not transient writes or edit history');
  await expect(main).toContainText('Presence of the scenario key is explicit in YAML and JSON');
  await expect(main).toContainText('Git attributes do not rewrite existing working copies.');
  await expect(main).toContainText('The glob must select only generated tasks, not evals, fixtures or metadata.');
  await expect(main).toContainText('fixtures/*.yaml remains supported');
  await expect(main).toContainText('Narrow the glob before regenerating.');
});

test('CLI reference distinguishes scenario outputs from legacy trigger tasks', async ({ page }) => {
  await page.goto('/waza/reference/cli/');
  const main = page.locator('main');
  await expect(main).toContainText('workflow<files.taskFileSuffix>');
  await expect(main).toContainText('Wildcard-directory globs that select the default tasks/ path remain supported.');
  await expect(main).toContainText('refuses to overwrite any generated file');
  await expect(main).toContainText('The three trigger tasks above belong to the default skill flow.');
  await expect(main).toContainText('rejects symlinked parents in the selected output path');
  await expect(main).toContainText('This parent policy applies to scenario generation, not legacy skill/eval scaffolding.');
  await expect(main).toContainText('CLI/MCP scenarios retain two graders.');
  await expect(main).toContainText('Metadata files are also protected against overwrite.');
  await expect(main).toContainText('fixtures/*.yaml is supported, but fixtures/* selects .gitkeep');
});
