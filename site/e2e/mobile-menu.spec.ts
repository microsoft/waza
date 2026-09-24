import { expect, test } from '@playwright/test';

for (const javaScriptEnabled of [true, false]) {
  test.describe(`mobile menu with JavaScript ${javaScriptEnabled ? 'enabled' : 'disabled'}`, () => {
    test.use({ javaScriptEnabled, viewport: { width: 390, height: 844 } });

    test('uses the custom text color and toggles the sidebar', async ({ page }) => {
      await page.goto('/waza/getting-started/');
      const menu = page.locator('.sl-menu-button');
      await expect(menu).toBeVisible();

      // A distinct theme value catches a stale selector even if default colors match.
      await page.evaluate(() => {
        document.documentElement.style.setProperty('--waza-text', 'rgb(123, 45, 67)');
      });
      await expect(menu).toHaveCSS('color', 'rgb(123, 45, 67)');

      const openSidebar = page.locator('sl-sidebar-pane:popover-open');
      await expect(openSidebar).toHaveCount(0);
      await menu.click();
      await expect(openSidebar).toBeVisible();
      await menu.click();
      await expect(openSidebar).toHaveCount(0);
    });
  });
}
