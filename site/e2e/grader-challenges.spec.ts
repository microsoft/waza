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
  await page.screenshot({
    path: fileURLToPath(new URL('../../docs/images/site-graders.png', import.meta.url)),
    fullPage: true,
  });
});
