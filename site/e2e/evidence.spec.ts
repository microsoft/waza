import { expect, test } from '@playwright/test';

test('task authoring distinguishes requirement intent from recorded evidence', async ({ page }) => {
  await page.goto('/waza/guides/eval-yaml/');
  await expect(page.getByRole('heading', { name: 'Link intent to existing checks', exact: true })).toBeVisible();
  const main = page.locator('main');
  await expect(main).toContainText('grader: report-check');
  await expect(main).toContainText('runs[].requirement_explanations');
  await expect(main).toContainText('does not erase an overall operational error');
  await expect(main.getByRole('link', { name: 'portable evidence', exact: true }))
    .toHaveAttribute('href', '/waza/guides/snapshot-replay/#scoped-requirement-observations');
});

test('supplied snapshot guidance distinguishes capture budgets from materialization', async ({ page }) => {
  await page.goto('/waza/guides/snapshot-replay/');
  const main = page.locator('main');
  await expect(main).toContainText('does not reapply those capture budgets');
  await expect(main).toContainText('never observations of newly declared checks');
  await expect(main.getByRole('link', { name: 'task authoring example', exact: true }))
    .toHaveAttribute('href', '/waza/guides/eval-yaml/#link-intent-to-existing-checks');
  await expect(main.getByRole('link', { name: 'storage and portable evidence', exact: true }))
    .toHaveAttribute('href', '/waza/guides/azure-storage/#portable-evidence');
});

test('configured result storage does not transfer separate snapshot contents', async ({ page }) => {
  await page.goto('/waza/guides/azure-storage/');
  await expect(page.getByRole('heading', { name: 'Portable evidence', exact: true })).toBeVisible();
  const main = page.locator('main');
  await expect(main).toContainText('Existing explicitly configured result storage uploads the serialized evaluation result');
  await expect(main).toContainText('including per-run evidence, requirement_explanations and snapshot_path metadata when present');
  await expect(main).toContainText('does not follow that path or upload separate snapshot files');
  await expect(main).toContainText('snapshot redaction is not a confidentiality guarantee for the entire result');
});

test('filesystem evidence guide states bounded admission and observational limits', async ({ page }) => {
  await page.goto('/waza/guides/snapshot-replay/');
  await expect(page.getByRole('heading', { name: 'Filesystem admission and observation limits', exact: true }))
    .toBeVisible();
  const main = page.locator('main');
  await expect(main).toContainText('256 entry inspections and depth 32');
  await expect(main).toContainText('before any selected file content is read');
  await expect(main).toContainText('pruning the inventory or raising process limits');
  await expect(main).toContainText('Unrelated hard-linked public files remain eligible');
  await expect(main).toContainText('not an atomic or authentic snapshot');
  await expect(main).toContainText('not continued integrity after the workspace is returned');
  await expect(main).toContainText('retains that path with unavailable capture evidence');
});
