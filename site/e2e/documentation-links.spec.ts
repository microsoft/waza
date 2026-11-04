import { expect, test } from '@playwright/test';
import { readdirSync, readFileSync } from 'node:fs';
import { join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';

const dist = fileURLToPath(new URL('../dist/', import.meta.url));
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
  await expect(page.locator('main')).toContainText('v0.39.1 (October 26, 2026)');
  await expect(page.locator('main')).toContainText('a source build is optional');
  await expect(page.locator('main')).not.toContainText('building upcoming main');
  await page.goto('/waza/quick-start/');
  const navigation = page.getByRole('navigation', { name: 'Main' });
  for (const name of ['Concepts and Learning Path', 'Project Configuration', 'Statistical Fields']) {
    await expect(navigation.getByRole('link', { name, exact: true })).toBeVisible();
  }
  await expect(page.locator('main')).toContainText('Published standalone v0.39.1');
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
