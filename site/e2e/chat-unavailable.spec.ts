import { expect, test, type Page } from '@playwright/test';

async function serveProductionDocs(page: Page) {
  const unexpected: string[] = [];
  await page.route('**/*', (route) => {
    unexpected.push(route.request().url());
    return route.abort();
  });
  await page.route('https://microsoft.github.io/waza/**', async (route) => {
    const url = new URL(route.request().url());
    const response = await route.fetch({ url: `http://localhost:4323${url.pathname}${url.search}` });
    await route.fulfill({ response });
  });
  return unexpected;
}

test('configured production transport cannot send from an untrusted origin even with DOM controls enabled', async ({ page }) => {
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
  await expect(page.getByRole('link', { name: 'CLI reference', exact: true })).toHaveAttribute('href', '/waza/reference/cli/');
  await page.keyboard.press('Escape');
  await expect(launcher).toBeFocused();
  await page.getByRole('button', { name: 'Search', exact: false }).first().click();
  const search = page.getByRole('dialog', { name: 'Search', exact: true });
  await search.getByRole('textbox', { name: 'Search', exact: true }).fill('waza run');
  await expect(search.getByRole('link').first()).toHaveAttribute('href', /\/waza\//);
});

test('production transport without a sitekey exchanges an anonymous session without challenge requests', async ({ page, request }) => {
  const metadataResponse = await request.get('/waza/chat-meta.json');
  expect(metadataResponse.ok()).toBe(true);
  const knowledge = await metadataResponse.json();
  const bodies: Record<string, unknown>[] = [];
  const unexpected = await serveProductionDocs(page);
  let sessions = 0;
  const api = 'https://func-chat-fskwox3ttikuc.azurewebsites.net/api';
  await page.route(`${api}/session`, async (route) => {
    sessions++;
    expect(route.request().postDataJSON()).toEqual({ challengeToken: '' });
    await route.fulfill({ json: { version: 1, sessionToken: 'simulated-session', expiresAt: Date.now() + 60000 } });
  });
  await page.route(`${api}/chat`, async (route) => {
    expect(route.request().headers().authorization).toBe('Bearer simulated-session');
    bodies.push(route.request().postDataJSON());
    await route.fulfill({ json: {
      version: 1, answer: 'Simulated production-contract answer: use waza run.',
      citations: [{ id: 'cli', title: 'CLI reference', url: 'https://microsoft.github.io/waza/reference/cli/' }],
      knowledge: { commit: knowledge.commit, fingerprint: knowledge.fingerprint },
    } });
  });
  await page.goto('https://microsoft.github.io/waza/quick-start/');
  await page.getByRole('button', { name: 'Ask Waza', exact: true }).click();
  await expect(page.getByRole('status')).toHaveText('Ready for your question.');
  await page.getByLabel('Question or error message').fill('What runs an evaluation?');
  await page.getByRole('button', { name: 'Send', exact: true }).click();
  await expect(page.locator('[data-chat-messages]')).toContainText('Simulated production-contract answer: use waza run.');
  await expect(page.getByRole('status')).toHaveText('Answer ready. Check the linked sources before applying suggestions.');
  await expect(page.locator('[data-chat-messages]').getByRole('link', { name: 'CLI reference' }))
    .toHaveAttribute('href', 'https://microsoft.github.io/waza/reference/cli/');
  expect(bodies).toHaveLength(1);
  expect(bodies[0]).toMatchObject({ question: 'What runs an evaluation?', history: [] });
  expect(bodies[0].requestId).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
  await page.getByLabel('Question or error message').fill('How do I read the results?');
  await page.getByRole('button', { name: 'Send', exact: true }).click();
  await expect(page.getByRole('status')).toHaveText('Answer ready. Check the linked sources before applying suggestions.');
  expect(sessions).toBe(1);
  expect(bodies).toHaveLength(2);
  expect(bodies[1].history).toEqual([
    { role: 'user', content: 'What runs an evaluation?' },
    { role: 'assistant', content: 'Simulated production-contract answer: use waza run.' },
  ]);
  expect(bodies[1].requestId).not.toBe(bodies[0].requestId);
  await expect(page.locator('script[src*="turnstile"], iframe[src*="challenges.cloudflare.com"]')).toHaveCount(0);
  expect(await page.evaluate(() => 'turnstile' in window)).toBe(false);
  expect(unexpected).toEqual([]);
  expect(await page.evaluate(() => sessionStorage.length)).toBe(0);
  expect(await page.evaluate(() => Object.keys(localStorage).filter((key) => key !== 'starlight-theme'))).toEqual([]);
});

test('a disabled anonymous session reports unavailable without a challenge, generation, or automatic retry', async ({ page }) => {
  const unexpected = await serveProductionDocs(page);
  let sessions = 0;
  await page.route('https://func-chat-fskwox3ttikuc.azurewebsites.net/api/session', async (route) => {
    sessions++;
    expect(route.request().postDataJSON()).toEqual({ challengeToken: '' });
    await route.fulfill({ status: 503, json: {
      version: 1, error: { code: 'DISABLED', message: 'backend-private-canary' },
    } });
  });
  await page.goto('https://microsoft.github.io/waza/quick-start/');
  await page.getByRole('button', { name: 'Ask Waza', exact: true }).click();
  await page.getByLabel('Question or error message').fill('What is Waza?');
  await page.getByRole('button', { name: 'Send', exact: true }).click();
  await expect(page.getByRole('status')).toHaveText('Public chat is unavailable. Please use documentation search.');
  await expect(page.locator('[data-chat-messages] > li')).toHaveCount(0);
  await expect(page.getByRole('dialog')).not.toContainText('backend-private-canary');
  await expect(page.locator('script[src*="turnstile"], iframe[src*="challenges.cloudflare.com"]')).toHaveCount(0);
  expect(sessions).toBe(1);
  expect(unexpected).toEqual([]);
});
