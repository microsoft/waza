import { expect, test } from '@playwright/test';

test('release links the narrow assertion adapter without claiming suite support', async ({ page }) => {
  await page.goto('/waza/reference/releases/');
  const main = page.locator('main');
  await expect(main).toContainText('Current published standalone release: v0.40.1');
  await expect(main).toContainText('full external task/suite interoperability remain incomplete');
  await expect(main).toContainText('waza interop import/export');
  await expect(main).toContainText('This is assertion conformance, not whole-suite or agent-quality evidence.');
  const preview = page.getByRole('link', { name: 'selected AgentV assertion adapter', exact: true });
  await expect(preview).toHaveAttribute('href', '../../guides/external-interoperability/');
  await preview.click();
  await expect(page).toHaveURL(/\/waza\/guides\/external-interoperability\/$/);
  await expect(page.locator('main')).toContainText('Issue #789 remains open');
});

test('external assertion guide preserves navigation, exact pin and unsupported boundaries', async ({ page }) => {
  await page.goto('/waza/reference/cli/');
  await page.getByRole('link', { name: 'External Assertion Interoperability', exact: true }).first().click();
  await expect(page).toHaveURL(/\/waza\/guides\/external-interoperability\/$/);
  await expect(page.getByRole('heading', { name: 'External Assertion Interoperability', exact: true })).toBeVisible();
  const main = page.locator('main');
  await expect(main).toContainText('agentv-68df6d2206cea61f721d0445b405efcb5e94aff0-contains-v1');
  await expect(main).toContainText('ASSERTION-ONLY PROJECTION');
  await expect(main).toContainText('not converted or validated');
  await expect(main).toContainText('Issue #789 remains open');
  await expect(main).toContainText('No adapter; real task success depends on container and executable verifier semantics');
  await expect(main).toContainText('positive and negative');
  await expect(main).toContainText('Validation/conversion errors before output delivery return exit 2 with no converted stdout.');
  await expect(main).toContainText('failed or short writes can expose partial output that cannot be rolled back.');
  await expect(main).toContainText('A broken pipe may terminate the process instead of returning exit 2.');
  await expect(page.getByRole('link', { name: 'verified compatibility matrix, source pins, licensing and remaining acceptance gaps', exact: true }))
    .toHaveAttribute('href', 'https://github.com/microsoft/waza/blob/main/docs/INTEROPERABILITY.md');
  await expect(page.getByRole('link', { name: 'native eval authoring guide', exact: true }))
    .toHaveAttribute('href', '/waza/guides/eval-yaml/');
});
