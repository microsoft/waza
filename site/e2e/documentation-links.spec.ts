import { expect, test } from '@playwright/test';
import { readdirSync, readFileSync } from 'node:fs';
import { join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';

const dist = fileURLToPath(new URL('../dist/', import.meta.url));
const screenshotDir = fileURLToPath(new URL('../../docs/images/', import.meta.url));
function htmlFiles(directory: string): string[] {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const path = join(directory, entry.name);
    return entry.isDirectory() ? htmlFiles(path) : entry.name === 'index.html' ? [path] : [];
  });
}

test('all built internal page routes, fragments and image assets resolve under the deployed base', async ({ page, request }) => {
  test.setTimeout(120_000);
  const links = new Map<string, string>();
  const images = new Set<string>();
  const failures: string[] = [];
  for (const file of htmlFiles(dist)) {
    const route = `/waza/${relative(dist, file).replace(/index\.html$/, '')}`;
    const response = await page.goto(route);
    expect(response?.status(), route).toBe(200);
    const targets = await page.locator('html').evaluate((document) => ({
      links: [...document.querySelectorAll<HTMLAnchorElement>('a[href]')].map((a) => a.href),
      images: [...document.querySelectorAll<HTMLImageElement>('img[src]')].map((img) => img.src),
    }));
    for (const target of targets.links) {
      const url = new URL(target);
      if (url.origin !== new URL(page.url()).origin) continue;
      if (!/^\/waza(?:\/|$)/.test(url.pathname)) failures.push(`${route}: missing base in ${target}`);
      links.set(url.pathname + url.hash, route);
    }
    for (const target of targets.images) {
      const url = new URL(target);
      if (url.origin === new URL(page.url()).origin) images.add(url.pathname);
    }
  }
  for (const [target, source] of links) {
    const url = new URL(target, 'http://localhost:4321');
    const response = await request.get(url.pathname);
    if (response.status() !== 200) failures.push(`${source} -> ${target}: HTTP ${response.status()}`);
    if (url.hash) {
      const id = decodeURIComponent(url.hash.slice(1));
      if (!(await response.text()).includes(`id="${id}"`)) failures.push(`${source} -> ${target}: missing fragment`);
    }
  }
  for (const image of images) expect((await request.get(image)).status(), image).toBe(200);
  console.log(`Checked ${htmlFiles(dist).length} built pages, ${links.size} distinct internal route/fragments and ${images.size} local images.`);
  expect(failures).toEqual([]);
});

test('beginner learning order and published release availability are visible', async ({ page }) => {
  await page.goto('/waza/');
  await expect(page.locator('main')).toContainText('New in v0.40.0');
  await expect(page.locator('main')).not.toContainText('building upcoming main');
  await page.goto('/waza/quick-start/');
  const navigation = page.getByRole('navigation', { name: 'Main' });
  for (const name of ['Concepts and Learning Path', 'Project Configuration', 'Statistical Fields']) {
    await expect(navigation.getByRole('link', { name, exact: true })).toBeVisible();
  }
  await expect(page.locator('main')).toContainText('Published standalone v0.40.0');
  await expect(page.locator('main')).toContainText('no source build is required');
  await expect(page.locator('main')).toContainText('If you prefer to build from source');
  await expect(page.locator('main')).toContainText('expect exit 1');
  await page.goto('/waza/getting-started/');
  const text = await page.locator('main').innerText();
  const realRun = text.indexOf('waza run evals/inventory/eval.yaml');
  for (const prerequisite of ['Authenticate before real execution', 'Before running', 'restore']) {
    expect(text.indexOf(prerequisite)).toBeGreaterThanOrEqual(0);
    expect(text.indexOf(prerequisite)).toBeLessThan(realRun);
  }
  await expect(page.locator('main')).toContainText('if [ ! -f evals/inventory/eval.yaml ]; then');
  await expect(page.locator('main')).toContainText('not a provider spending cap');
  expect(readFileSync(join(dist, 'concepts/index.html'), 'utf8')).toContain('A graded result is not release authority');
});

for (const viewport of [{ width: 1280, height: 900 }, { width: 390, height: 844 }]) {
  test(`compact homepage links to the release story at ${viewport.width}px`, async ({ page }) => {
    await page.setViewportSize(viewport);
    await page.goto('/waza/');
    const main = page.locator('main');
    await expect(main.locator('.starlight-aside')).toHaveCount(0);
    const announcement = main.getByRole('link', { name: 'Read the release article', exact: true });
    await expect(announcement).toHaveAttribute('href', '/waza/releases/v0-40-0/');
    await expect(announcement).toBeInViewport();
    await expect(main.getByRole('heading', { name: 'What can waza do?', exact: true })).toBeInViewport();
    const paragraph = announcement.locator('..');
    expect(await paragraph.evaluate((element) => element.getBoundingClientRect().height)).toBeLessThan(150);
    await expect(main).toContainText('A mock pass proves harness wiring, not agent quality');
    await expect(main).toContainText('authentication, permissions and possible-cost prerequisites');
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await page.screenshot({ path: join(screenshotDir, `site-v040-home-${viewport.width}.png`), fullPage: true });

    await announcement.click();
    await expect(page).toHaveURL(/\/waza\/releases\/v0-40-0\/$/);
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Waza v0.40.0: run exactly the work you declared');
    for (const text of [
      'Released November 4, 2026',
      'selectors, not loop counts',
      'input-derived identities, not execution attestations',
      'tools and skills disabled',
      '200 even when execution fails',
      'Linux AMD64',
      'not live-provider reliability',
      'WAZA_GRADING_CONTEXT',
      'from the first repetition in saved order',
      'whole-task/suite interoperability and full evidence/provenance round trips remain unsupported',
      'not included in the published v0.40.0 artifacts',
      'main-only and unreleased',
      'Those main changes have not been republished as v0.40.0',
    ]) await expect(page.locator('main')).toContainText(text);
    const outcomeRows = page.locator('main table').first().locator('tbody tr');
    await expect(outcomeRows).toHaveCount(7);
    for (const [index, outcome] of [
      'success', 'evaluation_failure', 'infrastructure_failure', 'cancelled',
      'timeout', 'unsupported_capability', 'invalid_input',
    ].entries()) {
      await expect(outcomeRows.nth(index).locator('td').nth(0)).toHaveText(outcome);
      await expect(outcomeRows.nth(index).locator('td').nth(1)).toHaveText(String(index));
    }
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await page.screenshot({ path: join(screenshotDir, `site-v040-article-${viewport.width}.png`), fullPage: true });
    await page.locator('main').getByRole('link', { name: 'runner contract', exact: true }).click();
    await expect(page).toHaveURL(/\/waza\/guides\/runner-contract\/$/);
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Runner contract and capability negotiation');

    await page.goto('/waza/reference/releases/');
    await page.locator('main').getByRole('link', { name: 'Waza v0.40.0: run exactly the work you declared', exact: true }).click();
    await expect(page).toHaveURL(/\/waza\/releases\/v0-40-0\/$/);
    if (viewport.width < 600) await page.locator('.sl-menu-button').click();
    const navigation = page.getByRole('navigation', { name: 'Main', exact: true });
    for (const name of ['Waza v0.40.0', 'Waza v0.39.0', 'Introducing Dashboard Lab']) {
      await expect(navigation.getByRole('link', { name, exact: true })).toBeVisible();
    }
    await navigation.getByRole('link', { name: 'Overview & downloads', exact: true }).click();
    await expect(page).toHaveURL(/\/waza\/reference\/releases\/$/);
    if (viewport.width < 600) await page.locator('.sl-menu-button').click();
    await page.getByRole('navigation', { name: 'Main', exact: true }).getByRole('link', { name: 'Waza v0.40.0', exact: true }).click();
    await expect(page).toHaveURL(/\/waza\/releases\/v0-40-0\/$/);
  });
}
