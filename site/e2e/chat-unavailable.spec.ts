import { expect, test } from '@playwright/test';

test('production cannot send even with all public flags and DOM controls enabled', async ({ page }) => {
  const external: string[] = [];
  await page.route('**/*', (route) => {
    const url = new URL(route.request().url());
    if (url.hostname !== 'localhost' || /\/api\/(?:health|session|chat|ask)$/.test(url.pathname)) {
      external.push(url.href);
      return route.abort();
    }
    return route.continue();
  });
  await page.goto('/waza/quick-start/');
  const launcher = page.getByRole('button', { name: 'Ask Waza', exact: true });
  await launcher.click();
  await expect(page.getByRole('dialog')).toBeVisible();
  await expect(page.getByRole('status')).toContainText('Public chat is unavailable');
  await expect(page.getByRole('dialog')).not.toContainText('Development fixtures');
  await expect(page.getByRole('button', { name: 'Send', exact: true })).toBeDisabled();
  await expect(page.getByLabel('Question or error message')).toBeDisabled();
  const url = page.url();
  const prevented = await page.locator('[data-chat-form]').evaluate((form: HTMLFormElement) => {
    for (const control of form.querySelectorAll<HTMLButtonElement | HTMLTextAreaElement>('button, textarea')) {
      control.disabled = false;
    }
    form.querySelector<HTMLTextAreaElement>('textarea')!.value = 'Never send this';
    const event = new Event('submit', { bubbles: true, cancelable: true });
    form.dispatchEvent(event);
    return event.defaultPrevented;
  });
  expect(prevented).toBe(true);
  await expect(page).toHaveURL(url);
  expect(external).toEqual([]);
  await expect(page.getByRole('link', { name: 'Browse the CLI reference' })).toHaveAttribute('href', '/waza/reference/cli/');
  await page.keyboard.press('Escape');
  await expect(launcher).toBeFocused();
  await page.getByRole('button', { name: 'Search', exact: false }).first().click();
  const search = page.getByRole('dialog', { name: 'Search', exact: true });
  await search.getByRole('textbox', { name: 'Search', exact: true }).fill('waza run');
  await expect(search.getByRole('link').first()).toHaveAttribute('href', /\/waza\//);
});
