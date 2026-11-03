import { expect, test } from '@playwright/test';

test('runner CLI/API navigation, schemas and screenshot retain deployed base', async ({ page, request }) => {
  await page.goto('/waza/reference/cli/');
  await page.getByRole('link', { name: 'Runner contract, schemas, API and migration', exact: true }).click();
  await expect(page).toHaveURL(/\/waza\/guides\/runner-contract\/$/);
  await expect(page.getByRole('heading', { name: 'HTTP API', exact: true })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Execute one exact work unit', exact: true })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Opt-in production Copilot text execution', exact: true })).toBeVisible();
  await expect(page.locator('main')).toContainText('examples/runner/copilot-unit.json');
  await expect(page.locator('main')).toContainText('allow_paid_execution: true');
  await expect(page.locator('main')).toContainText('The eval executor is copilot-sdk; the terminal mode is separately copilot.');
  await expect(page.locator('main')).toContainText('session.options.update');
  await expect(page.locator('main')).toContainText('rejection fails before any prompt is sent');
  await expect(page.locator('main')).toContainText('An effective nonzero first_event_timeout_seconds is rejected before runtime construction');
  await expect(page.locator('main')).toContainText('uncomputed, identity-bound metadata');
  await expect(page.locator('main')).toContainText('Paid native HTTP execution is local-only.');
  await expect(page.locator('main')).toContainText('not server-owner authorization');
  await expect(page.locator('main')).toContainText('native_http_denied');
  await expect(page.locator('main')).toContainText('isolated runner sessions do not resume solely to retrieve shutdown usage');
  await expect(page.locator('main')).toContainText('it does not report aggregate metric values, metric-threshold decisions or whole-evaluation success');
  for (const name of ['Negotiation request 1.0 schema', 'Negotiation response 1.0 schema', 'Complete runner request 1.0 schema', 'Capability advertisement 1.0 schema', 'Exact unit receipt 1.0 schema']) {
    const href = await page.getByRole('link', { name, exact: true }).getAttribute('href');
    expect(href).not.toBeNull();
    const url = new URL(href!, page.url());
    expect(url.pathname).toMatch(/^\/waza\/schemas\/runner-.+\.schema\.json$/);
    const response = await request.get(url.toString());
    expect(response.status()).toBe(200);
    const schema = await response.json();
    expect(schema.$schema).toBe('https://json-schema.org/draft/2020-12/schema');
    expect(schema.additionalProperties).toBe(false);
  }
  const image = page.getByRole('img', { name: /Live Runner contract view showing an unavailable native/ });
  await expect(image).toBeVisible();
  expect(new URL((await image.getAttribute('src'))!, page.url()).pathname).toBe('/waza/images/dashboard-lab-negotiation.png');
  expect(await image.evaluate((img) => img instanceof HTMLImageElement && img.complete && img.naturalWidth > 0)).toBe(true);
  const selection = page.getByRole('img', { name: /Live Runner contract view showing the requested and resolved exact tuple/ });
  await expect(selection).toBeVisible();
  expect(new URL((await selection.getAttribute('src'))!, page.url()).pathname).toBe('/waza/images/dashboard-lab-selection.png');
  expect(await selection.evaluate((img) => img instanceof HTMLImageElement && img.complete && img.naturalWidth > 0)).toBe(true);
});
