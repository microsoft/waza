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

test('pending release navigation does not promote unavailable downloads', async ({ page }) => {
  await page.goto('/waza/reference/releases/');
  const main = page.locator('main');
  await expect(main).toContainText('Current published standalone release: v0.40.1');
  const downloads = main.locator('a[href*="/releases/download/v0.40.1/waza-"]');
  await expect(downloads).toHaveCount(6);
  await expect(page.locator('a[href*="/releases/download/v0.41.0/"]')).toHaveCount(0);
  await main.getByRole('link', { name: 'Waza v0.41.0 preview: prepare the release, preserve the evidence', exact: true }).click();
  await expect(page).toHaveURL(/\/waza\/releases\/v0-41-0\/$/);
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Waza v0.41.0 preview: prepare the release, preserve the evidence');
  await expect(page.locator('main')).toContainText('Draft — publication pending.');
  await expect(page.locator('main')).toContainText('6750ff6115b3a98a5609bd751227bca629839a9c');
  await expect(page.locator('main')).toContainText('Finalize only after the remaining admissions');
  await expect(page.locator('a[href*="/releases/download/v0.41.0/"]')).toHaveCount(0);
  await page.locator('#starlight__sidebar').getByRole('link', { name: 'Waza v0.41.0 (pending)', exact: true }).click();
  await expect(page).toHaveURL(/\/waza\/releases\/v0-41-0\/$/);
  await page.locator('main').getByRole('link', { name: 'current published standalone and azd releases', exact: true }).click();
  await expect(page).toHaveURL(/\/waza\/reference\/releases\/$/);
});

test('beginner learning order and published release availability are visible', async ({ page }) => {
  await page.goto('/waza/');
  await expect(page.locator('main')).toContainText('New in v0.40.1');
  await expect(page.locator('main')).not.toContainText('building upcoming main');
  await page.goto('/waza/quick-start/');
  const navigation = page.getByRole('navigation', { name: 'Main' });
  for (const name of ['Concepts and Learning Path', 'Project Configuration', 'Statistical Fields']) {
    await expect(navigation.getByRole('link', { name, exact: true })).toBeVisible();
  }
  await expect(page.locator('main')).toContainText('Published standalone v0.40.1');
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
    await expect(announcement).toHaveAttribute('href', '/waza/releases/v0-40-1/');
    await expect(announcement).toBeInViewport();
    await expect(main.getByRole('heading', { name: 'What can waza do?', exact: true })).toBeInViewport();
    const paragraph = announcement.locator('..');
    expect(await paragraph.evaluate((element) => element.getBoundingClientRect().height)).toBeLessThan(150);
    await expect(main).toContainText('A mock pass proves harness wiring, not agent quality');
    await expect(main).toContainText('authentication, permissions and possible-cost prerequisites');
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await page.screenshot({ path: join(screenshotDir, `site-v040-home-${viewport.width}.png`), fullPage: true });

    await announcement.click();
    await expect(page).toHaveURL(/\/waza\/releases\/v0-40-1\/$/);
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Waza v0.40.1: make release workflows match their contracts');
    for (const text of [
      'Released November 6, 2026',
      'zero tasks and Partial coverage',
      'tokens check --strict',
      'group_by: tags',
      'top-level eval inputs',
      'nanosecond-resolution run IDs',
      'adversarial --artifacts-dir',
      'without canceling the active request',
      'file descriptor 0',
    ]) await expect(page.locator('main')).toContainText(text);

    await page.goto('/waza/releases/v0-40-0/');
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
    const outcomes = page.getByRole('list', { name: 'Runner terminal outcomes', exact: true }).getByRole('listitem');
    await expect(outcomes).toHaveCount(7);
    for (const [index, [outcome, meaning]] of [
      ['success', 'All admitted checks passed in a complete observed attempt.'],
      ['evaluation_failure', 'At least one observed check failed.'],
      ['infrastructure_failure', 'Startup, execution, cleanup or output failed.'],
      ['cancelled', 'Cancellation was observed.'],
      ['timeout', 'An applicable deadline expired.'],
      ['unsupported_capability', 'Required runner version or capability is unavailable.'],
      ['invalid_input', 'The request or selected input is invalid.'],
    ].entries()) {
      const item = outcomes.nth(index);
      await expect(item).toHaveText(`${outcome} (exit ${index}): ${meaning}`);
      await item.scrollIntoViewIfNeeded();
      await expect(item.locator('strong')).toBeInViewport();
    }
    const clippedOutcomes = () => outcomes.evaluateAll((items) => items.flatMap((item, index) => {
      const walker = document.createTreeWalker(item, NodeFilter.SHOW_TEXT);
      for (let node = walker.nextNode(); node; node = walker.nextNode()) {
        if (!node.textContent?.trim()) continue;
        const range = document.createRange();
        range.selectNodeContents(node);
        for (const rect of range.getClientRects()) {
          if (rect.left < -1 || rect.right > window.innerWidth + 1) return [index];
          for (let parent = node.parentElement; parent; parent = parent.parentElement) {
            if (!/^(auto|scroll|hidden|clip)$/.test(getComputedStyle(parent).overflowX)) continue;
            const bounds = parent.getBoundingClientRect();
            const left = bounds.left + parent.clientLeft;
            if (rect.left < left - 1 || rect.right > left + parent.clientWidth + 1) return [index];
          }
        }
      }
      return [];
    }));
    expect(await clippedOutcomes(), 'every outcome label and description fits without horizontal scrolling').toEqual([]);
    if (viewport.width === 390) {
      const original = await outcomes.first().evaluate((item) => {
        const saved = { html: item.innerHTML, style: item.getAttribute('style') };
        item.setAttribute('style', 'width: 120px; overflow-x: auto');
        const wide = document.createElement('div');
        wide.style.cssText = 'width: 220px; white-space: normal';
        while (item.firstChild) wide.appendChild(item.firstChild);
        item.appendChild(wide);
        return saved;
      });
      try {
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
        const injected = await outcomes.first().evaluate((item) => {
          const walker = document.createTreeWalker(item, NodeFilter.SHOW_TEXT);
          const rects: { left: number; right: number }[] = [];
          for (let node = walker.nextNode(); node; node = walker.nextNode()) {
            if (!node.textContent?.trim()) continue;
            const range = document.createRange();
            range.selectNodeContents(node);
            rects.push(...Array.from(range.getClientRects(), (rect) => ({ left: rect.left, right: rect.right })));
          }
          const bounds = item.getBoundingClientRect();
          return {
            rects,
            viewportWidth: window.innerWidth,
            clipLeft: bounds.left + item.clientLeft,
            clipRight: bounds.left + item.clientLeft + item.clientWidth,
          };
        });
        expect(injected.rects.length).toBeGreaterThan(0);
        expect(injected.rects.every((rect) => rect.left >= 0 && rect.right <= injected.viewportWidth),
          'negative-control text fits the viewport without using the viewport-overflow branch').toBe(true);
        expect(injected.rects.some((rect) => rect.left < injected.clipLeft - 1 || rect.right > injected.clipRight + 1),
          'negative-control text is clipped only by its narrower overflow ancestor').toBe(true);
        expect(await clippedOutcomes(), 'detect text hidden inside a horizontal scroll container').toContain(0);
      } finally {
        await outcomes.first().evaluate((item, saved) => {
          item.innerHTML = saved.html;
          if (saved.style === null) item.removeAttribute('style');
          else item.setAttribute('style', saved.style);
        }, original);
      }
      expect(await clippedOutcomes(), 'restored production outcomes fit after the negative control').toEqual([]);
    }
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await page.screenshot({ path: join(screenshotDir, `site-v040-article-${viewport.width}.png`), fullPage: true });
    await page.locator('main').getByRole('link', { name: 'runner contract', exact: true }).click();
    await expect(page).toHaveURL(/\/waza\/guides\/runner-contract\/$/);
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Runner contract and capability negotiation');

    await page.goto('/waza/reference/releases/');
    await page.locator('main').getByRole('link', { name: 'Waza v0.40.1: make release workflows match their contracts', exact: true }).click();
    await expect(page).toHaveURL(/\/waza\/releases\/v0-40-1\/$/);
    if (viewport.width < 600) await page.locator('.sl-menu-button').click();
    const navigation = page.getByRole('navigation', { name: 'Main', exact: true });
    for (const name of ['Waza v0.40.1', 'Waza v0.40.0', 'Waza v0.39.0', 'Introducing Dashboard Lab']) {
      await expect(navigation.getByRole('link', { name, exact: true })).toBeVisible();
    }
    await navigation.getByRole('link', { name: 'Overview & downloads', exact: true }).click();
    await expect(page).toHaveURL(/\/waza\/reference\/releases\/$/);
    if (viewport.width < 600) await page.locator('.sl-menu-button').click();
    await page.getByRole('navigation', { name: 'Main', exact: true }).getByRole('link', { name: 'Waza v0.40.1', exact: true }).click();
    await expect(page).toHaveURL(/\/waza\/releases\/v0-40-1\/$/);
  });
}
