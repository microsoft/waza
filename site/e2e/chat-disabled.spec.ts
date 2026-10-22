import { expect, test } from '@playwright/test';

test('chat remains in the header instead of the sidebar without enabling public requests', async ({ page }) => {
  const requests: string[] = [];
  page.on('request', (request) => {
    if (/\/api\/(?:health|session|chat|ask)(?:[?#]|$)|turnstile|chat-meta\.json/.test(request.url())) {
      requests.push(request.url());
    }
  });
  await page.goto('/waza/quick-start/');
  await expect(page.getByRole('link', { name: 'Ask Waza (Optional)', exact: true })).toHaveCount(0);
  const launcher = page.getByRole('button', { name: 'Ask Waza', exact: true });
  await expect(launcher).toBeVisible();
  await launcher.click();
  await expect(page.getByRole('dialog')).toBeVisible();
  await expect(page.getByRole('status')).toContainText('Public chat is unavailable');
  await expect(page.getByLabel('Question or error message')).toBeDisabled();
  await expect(page.getByRole('button', { name: 'Send', exact: true })).toBeDisabled();
  const privacy = page.getByRole('link', { name: 'Privacy & limits', exact: true });
  await expect(privacy).toHaveAttribute('href', '/waza/guides/docs-assistant/');
  expect(requests).toEqual([]);
  await privacy.click();
  await expect(page.getByRole('heading', { name: 'Ask Waza (Optional)', exact: true })).toBeVisible();
});

test('public corpus metadata contains no private configuration', async ({ request }) => {
  const response = await request.get('/waza/chat-meta.json');
  expect(response.ok()).toBeTruthy();
  const value = await response.json();
  expect(Object.keys(value).sort()).toEqual(['commit', 'fingerprint', 'version']);
  expect(value.version).toBe(1);
  expect(value.commit).toMatch(/^[a-f0-9]{40}$/);
  expect(value.fingerprint).toMatch(/^[a-f0-9]{64}$/);
});
