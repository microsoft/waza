import { expect, test } from '@playwright/test';
import { readFileSync } from 'node:fs';

const canonical = JSON.parse(readFileSync(new URL('../../internal/runneridentity/testdata/vectors.json', import.meta.url), 'utf8'));
const producer = JSON.parse(readFileSync(new URL('../../internal/runnerunit/testdata/identities.json', import.meta.url), 'utf8'));

test('identity guide, frozen vectors, complete hierarchy and migration preserve deployed navigation', async ({ page, request }) => {
  await page.goto('/waza/guides/runner-contract/');
  await page.getByRole('link', { name: 'input-derived identities', exact: true }).click();
  await expect(page).toHaveURL(/\/waza\/guides\/runner-identities\/$/);
  for (const heading of ['Source and numeric grammar', 'Canonical encoding and namespaces', 'Frozen conformance vectors', 'Release and migration notes']) {
    await expect(page.getByRole('heading', { name: heading, exact: true })).toBeVisible();
  }
  for (const value of Object.values(producer[0].identity)) {
    await expect(page.locator('main')).toContainText(String(value));
  }
  for (const [name, expected, count] of [
    ['29 canonical vectors', canonical, 29],
    ['11 admitted producer vectors', producer, 11],
  ] as const) {
    const href = await page.getByRole('link', { name, exact: true }).getAttribute('href');
    expect(href).not.toBeNull();
    const url = new URL(href!, page.url());
    expect(url.pathname).toMatch(/^\/waza\/runner-identities\/(canonical|producer)\.json$/);
    const response = await request.get(url.toString());
    expect(response.ok()).toBe(true);
    const vectors = await response.json();
    expect(vectors).toHaveLength(count);
    expect(vectors).toEqual(expected);
  }
  const image = page.getByRole('img', { name: /Live Runner contract validation showing input-derived/ });
  await expect(image).toBeVisible();
  expect(new URL((await image.getAttribute('src'))!, page.url()).pathname).toBe('/waza/images/dashboard-lab-selection.png');
  expect(await image.evaluate((img) => img instanceof HTMLImageElement && img.complete && img.naturalWidth > 0)).toBe(true);
  await page.getByRole('link', { name: 'Runner Logical Identities', exact: true }).click();
  await expect(page).toHaveURL(/\/waza\/guides\/runner-identities\/$/);
  await page.getByRole('link', { name: 'runner migration guide', exact: true }).click();
  await expect(page).toHaveURL(/\/waza\/guides\/runner-contract\/#compatibility-migration-and-evolution$/);
});
