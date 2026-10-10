import { expect, test } from '@playwright/test';
import { fileURLToPath } from 'node:url';

test('grader challenges disclose offline scope and candidate limitations', async ({ page }) => {
  await page.goto('/waza/guides/graders/');

  await expect(page.getByRole('heading', { name: 'Challenge the checks, not just the answer', exact: true })).toBeVisible();
  await expect(page.locator('pre').filter({ hasText: "go test ./internal/graders -run '^TestBaselineChallenge'" })).toBeVisible();
  await expect(page.getByText('Candidate coverage is not reviewed assurance', { exact: true })).toBeVisible();
  await expect(page.getByText(/These labels are not independently human-reviewed/)).toBeVisible();
  await expect(page.getByText('Missing required evidence is insufficient evidence', { exact: false })).toBeVisible();
  await expect(page.getByRole('link', { name: 'challenge inventory', exact: true })).toHaveAttribute(
    'href',
    'https://github.com/microsoft/waza/blob/main/docs/GRADER-CHALLENGES.md',
  );
  await expect(page.getByRole('heading', { name: 'Strict preserved-file assurance', exact: true })).toBeVisible();
  await expect(page.getByText(/Any wrongly accepted targeted critical bad case fails independently/)).toBeVisible();
  await expect(page.getByText(/makes zero paid calls and leaves usage\/credits null/)).toBeVisible();
  await page.screenshot({
    path: fileURLToPath(new URL('../../docs/images/site-graders.png', import.meta.url)),
    fullPage: true,
  });

});

test('assure CLI documents explicit review and unavailable paid calibration', async ({ page }) => {
  await page.goto('/waza/reference/cli/');
  const section = page.locator('.sl-markdown-content');
  await expect(page.getByRole('heading', { name: 'waza assure', exact: true })).toBeVisible();
  await expect(section).toContainText('--accept-review-source');
  await expect(section).toContainText('Currently unavailable');
  await expect(section).toContainText('null usage/credits');
  await expect(section).toContainText('Existing grade, run and golden-task semantics are unchanged');
});

test('evaluator calibration and ledger inspection remain explicitly separate from CLI execution', async ({ page }) => {
  await page.goto('/waza/guides/graders/');
  await expect(page.getByText(/The separate evaluator API/)).toContainText('assurance.Calibrate');
  await expect(page.getByText(/It has no default factory/)).toBeVisible();
  await page.getByRole('link', { name: 'dashboard inspection', exact: true }).click();
  await expect(page.getByText(/Calibrated report 1.1 claims require the separate/)).toBeVisible();
  await expect(page.locator('.sl-markdown-content')).toContainText('Inspection neither runs nor authorizes paid calibration');
});
