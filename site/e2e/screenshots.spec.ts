import { expect, test } from '@playwright/test';
import path from 'path';
import { fileURLToPath } from 'url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const outputDir = path.resolve(__dirname, '../../docs/images');

test.skip(process.env.WAZA_REFRESH_DOCS_MEDIA !== '1', 'Run npm run refresh:docs-media to update documentation screenshots.');

const pages = [
  { name: 'home', path: '/' },
  { name: 'getting-started', path: '/getting-started/' },
  { name: 'graders', path: '/guides/graders/' },
  { name: 'cli-reference', path: '/reference/cli/' },
];

for (const pg of pages) {
  test(`screenshot: ${pg.name}`, async ({ page }) => {
    const response = await page.goto(`/waza${pg.path}`);
    expect(response?.ok(), `screenshot page ${pg.name} must load successfully`).toBe(true);
    await expect(page.locator('main h1').first()).toBeVisible();
    await page.waitForLoadState('networkidle');
    await page.screenshot({
      path: path.join(outputDir, `site-${pg.name}.png`),
      fullPage: true,
    });
  });
}
