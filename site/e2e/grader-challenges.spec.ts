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

test('evaluator calibration and paid command stay explicit while inspection remains read-only', async ({ page }) => {
  await page.goto('/waza/guides/graders/');
  await expect(page.getByText(/The separate evaluator API/)).toContainText('assurance.Calibrate');
  await expect(page.getByText(/It has no default factory/)).toBeVisible();
  await expect(page.locator('.sl-markdown-content')).toContainText('waza assure calibrate');
  await expect(page.locator('.sl-markdown-content')).toContainText('--accept-paid-calls');
  await page.getByRole('link', { name: 'dashboard inspection', exact: true }).click();
  await expect(page.getByText(/Calibrated report 1.1 claims require the separate/)).toBeVisible();
  await expect(page.locator('.sl-markdown-content')).toContainText('Inspection neither runs nor authorizes paid calibration');
});

test('paid calibration reference preserves legacy selection and explicit consent', async ({ page }) => {
  await page.goto('/waza/reference/cli/');
  const section = page.locator('.sl-markdown-content');
  await expect(page.getByRole('heading', { name: 'waza assure calibrate', exact: true })).toBeVisible();
  await expect(section).toContainText('not enabled by the existing assure --calibrate flag');
  await expect(section).toContainText('Default false; explicit acknowledgment');
  await expect(section).toContainText('default 10m; not a spend cap');
  await expect(section).toContainText('partial report is output even when the API also returns an error');
});

test('preserved-native profile stays separate from default readers and calibration', async ({ page }) => {
  await page.goto('/waza/guides/graders/');
  await expect(page.getByRole('heading', { name: 'Separately selected preserved-native assurance', exact: true })).toBeVisible();
  const section = page.locator('.sl-markdown-content');
  await expect(section).toContainText('preserved_native_mechanical_assurance');
  await expect(section).toContainText('Missing/null arguments are not an empty object');
  await expect(section).toContainText('256 selected paths');
  await expect(section).toContainText('cannot be promoted by synthetic complete fixtures');
  await expect(section).toContainText('No engine factory or paid execution');
  await expect(section).toContainText('Historical grader correspondence is not certified');
});
