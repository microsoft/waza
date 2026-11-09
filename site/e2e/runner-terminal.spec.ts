import { expect, test } from '@playwright/test';

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
