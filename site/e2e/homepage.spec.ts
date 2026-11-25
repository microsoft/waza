import { expect, test, type Locator } from '@playwright/test';

async function contrastAgainstBackground(
  locator: Locator,
  property: 'color' | 'outlineColor',
  backgroundSelector: string
) {
  return locator.evaluate((element, { property, backgroundSelector }) => {
    const background = element.closest(backgroundSelector);
    if (!background) throw new Error(`Missing background: ${backgroundSelector}`);
    const luminance = (color: string) => {
      const values = color.match(/\d+(?:\.\d+)?/g);
      if (!values || values.length < 3) throw new Error(`Unsupported computed color: ${color}`);
      const [red, green, blue] = values.slice(0, 3).map((value) => {
        const component = Number(value) / 255;
        return component <= 0.04045 ? component / 12.92 : ((component + 0.055) / 1.055) ** 2.4;
      });
      return 0.2126 * red + 0.7152 * green + 0.0722 * blue;
    };
    const foreground = luminance(getComputedStyle(element)[property]);
    const surface = luminance(getComputedStyle(background).backgroundColor);
    return (Math.max(foreground, surface) + 0.05) / (Math.min(foreground, surface) + 0.05);
  }, { property, backgroundSelector });
}

test('homepage exposes the full feedback loop with working documentation CTAs', async ({ page, request }) => {
  const response = await page.goto('/waza/');
  expect(response?.status()).toBe(200);

  await expect(page).toHaveTitle('waza — Test behavior. Ship confidence.');
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Test behavior.Ship confidence.');
  await expect(page.locator('.hero-description')).toHaveText('Evaluate skills, prompts and agent workflows against real tasks. Inspect tool use, compare models, refine instructions and ship with evidence.');

  const capabilities = page.getByRole('region', { name: 'More than a score. A way to get better.' });
  for (const name of [
    'Scaffold a suite', 'Suggest evals', 'Explore graders', 'Test skill triggers',
    'Configure skills and tools', 'Compare models', 'Run a baseline', 'Check readiness',
    'Improve a skill', 'Manage token budgets', 'Find optimizations', 'Explore the dashboard',
    'Inspect session logs', 'Set up CI', 'Tune evaluation runs', 'Share results',
    'Browse and compare runs',
  ]) {
    await expect(capabilities.getByRole('link', { name, exact: true })).toBeVisible();
  }
  await expect(page.getByRole('link', { name: 'CLI installation options' })).toBeVisible();
  await expect(page.getByRole('link', { name: 'Install as an azd extension' })).toBeVisible();
  for (const fragment of [
    'the-way-of-technique--measure-refine-master-', 'what-can-waza-do', 'quick-start',
    'key-features', 'structured-yaml-benchmarks', 'pluggable-validators',
    'fixture-isolation', 'cross-model-comparison', 'web-dashboard', 'cicd-ready',
  ]) {
    await expect(page.locator(`[id="${fragment}"]`)).toHaveCount(1);
  }

  const hrefs = await page.locator('a').evaluateAll((anchors) =>
    [...new Set(anchors.map((anchor) => anchor.href))].filter((href) =>
      new URL(href).origin === location.origin
    )
  );
  for (const href of hrefs) {
    const url = new URL(href);
    const response = await request.get(href);
    expect(response.status(), href).toBe(200);
    if (url.hash) {
      const html = await response.text();
      expect(html, href).toContain(`id="${decodeURIComponent(url.hash.slice(1))}"`);
    }
  }
});

test('homepage theme follows the system and persists into docs', async ({ page }) => {
  await page.emulateMedia({ colorScheme: 'light' });
  await page.goto('/waza/');
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');

  await page.emulateMedia({ colorScheme: 'dark' });
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  await page.emulateMedia({ colorScheme: 'light' });
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  await page.getByRole('button', { name: 'Switch color theme' }).click();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  await page.reload();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  await page.getByRole('link', { name: 'Run your first eval' }).click();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  await page.goBack();
  await page.getByRole('button', { name: 'Switch color theme' }).click();
  await page.getByRole('link', { name: 'Run your first eval' }).click();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
});

test('homepage retains indexed documentation search', async ({ page }) => {
  await page.goto('/waza/');
  await page.getByRole('button', { name: 'Search', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: 'Search' });
  await expect(dialog).toBeVisible();
  const input = dialog.getByRole('textbox', { name: 'Search' });
  await expect(input).toBeVisible();
  await input.fill('Ship confidence');
  await expect(dialog.locator('.pagefind-ui__result-link[href="/waza/"]')).toBeVisible();
  await input.fill('preflight');
  const result = dialog.locator('.pagefind-ui__result-link[href="/waza/guides/preflight/"]');
  await expect(result).toBeVisible();
  await result.click();
  await expect(page).toHaveURL(/\/waza\/guides\/preflight\/$/);
});

test('homepage retains the unavailable public chat entry point without making requests', async ({ page }) => {
  const requests: string[] = [];
  page.on('request', (request) => {
    if (/\/api\/(?:health|session|chat|ask)(?:[?#]|$)|turnstile|chat-meta\.json/.test(request.url())) {
      requests.push(request.url());
    }
  });
  await page.goto('/waza/');
  await page.getByRole('button', { name: 'Ask Waza', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: 'Ask Waza' });
  await expect(dialog).toContainText('Public chat is unavailable');
  await expect(dialog.getByLabel('Question or error message')).toBeDisabled();
  await expect(dialog.getByRole('link', { name: 'Privacy & limits' })).toHaveAttribute('href', '/waza/guides/docs-assistant/');
  expect(requests).toEqual([]);
});

test('homepage chat links have a visible focus indicator in both themes', async ({ page }) => {
  await page.goto('/waza/');
  for (const theme of ['light', 'dark']) {
    await page.evaluate((value) => localStorage.setItem('starlight-theme', value), theme);
    await page.reload();
    await page.getByRole('button', { name: 'Ask Waza', exact: true }).click();
    const link = page.getByRole('dialog', { name: 'Ask Waza' }).getByRole('link', { name: 'CLI reference' });
    await page.keyboard.press('Tab');
    await link.focus();
    await expect(link).toHaveCSS('outline-style', 'solid');
    expect(await contrastAgainstBackground(link, 'outlineColor', 'dialog'), `${theme} chat focus contrast`)
      .toBeGreaterThanOrEqual(3);
    await page.keyboard.press('Escape');
  }
});

test('homepage search results retain hover and focus contrast in both themes', async ({ page }) => {
  await page.goto('/waza/');
  for (const theme of ['light', 'dark']) {
    await page.evaluate((value) => localStorage.setItem('starlight-theme', value), theme);
    await page.reload();
    await page.getByRole('button', { name: 'Search', exact: true }).click();
    const dialog = page.getByRole('dialog', { name: 'Search' });
    await dialog.getByRole('textbox', { name: 'Search' }).fill('preflight');
    const link = dialog.locator('.pagefind-ui__result-link[href="/waza/guides/preflight/"]');
    await expect(link).toBeVisible();
    await link.hover();
    expect(await contrastAgainstBackground(link, 'color', '.pagefind-ui__result-title'), `${theme} search hover contrast`)
      .toBeGreaterThanOrEqual(4.5);
    await page.keyboard.press('Tab');
    await link.focus();
    await expect(link).toHaveCSS('outline-style', 'solid');
    expect(await contrastAgainstBackground(link, 'outlineColor', '.pagefind-ui__result-title'), `${theme} search focus contrast`)
      .toBeGreaterThanOrEqual(3);
    await page.keyboard.press('Escape');
  }
});

test('docs links meet text contrast in both themes and reduced motion is respected', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await page.goto('/waza/');
  expect(await page.evaluate(() => getComputedStyle(document.documentElement).scrollBehavior)).toBe('auto');
  await page.goto('/waza/quick-start/');
  for (const theme of ['light', 'dark']) {
    await page.evaluate((value) => localStorage.setItem('starlight-theme', value), theme);
    await page.reload();
    const contrast = await contrastAgainstBackground(
      page.locator('main .sl-markdown-content a').first(), 'color', 'body'
    );
    expect(contrast, `${theme} documentation link contrast`).toBeGreaterThanOrEqual(4.5);
  }
});

for (const width of [320, 390, 768, 1280]) {
  test(`homepage content fits at ${width}px in both themes`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 900 });
    await page.goto('/waza/');

    for (const theme of ['light', 'dark']) {
      await page.evaluate((value) => {
        localStorage.setItem('starlight-theme', value);
      }, theme);
      await page.reload();
      await expect(page.locator('html')).toHaveAttribute('data-theme', theme);
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      const heading = page.getByRole('heading', { level: 1 });
      await expect(heading).toBeVisible();
      const bounds = await heading.boundingBox();
      if (!bounds) throw new Error('Homepage heading has no rendered bounds');
      expect(bounds.x + bounds.width).toBeLessThanOrEqual(width);
      await page.screenshot({
        path: testInfo.outputPath(`homepage-${width}-${theme}.png`),
        fullPage: true,
      });
    }
  });
}
