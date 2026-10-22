import { expect, test } from '@playwright/test';

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

test('production transport exchanges a session and renders a public answer on the docs origin', async ({ page, request }) => {
  const metadataResponse = await request.get('/waza/chat-meta.json');
  expect(metadataResponse.ok()).toBe(true);
  const knowledge = await metadataResponse.json();
  const bodies: Record<string, unknown>[] = [];
  const unexpected: string[] = [];
  await page.route('**/*', (route) => {
    if (new URL(route.request().url()).origin === 'https://microsoft.github.io') return route.continue();
    unexpected.push(route.request().url());
    return route.abort();
  });
  await page.route('https://microsoft.github.io/waza/**', async (route) => {
    const url = new URL(route.request().url());
    const response = await route.fetch({ url: `http://localhost:4323${url.pathname}${url.search}` });
    await route.fulfill({ response });
  });
  await page.addInitScript(() => {
    Object.assign(window, { turnstile: {
      render: (_element: HTMLElement, config: { callback: (token: string) => void }) => {
        config.callback('simulated-production-challenge');
        return 'simulated-widget';
      },
      reset: () => {},
    } });
  });
  const api = 'https://func-chat-fskwox3ttikuc.azurewebsites.net/api';
  await page.route(`${api}/session`, async (route) => {
    expect(route.request().postDataJSON()).toEqual({ challengeToken: 'simulated-production-challenge' });
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
  await expect(page.locator('[data-chat-messages]').getByRole('link', { name: 'CLI reference' }))
    .toHaveAttribute('href', 'https://microsoft.github.io/waza/reference/cli/');
  expect(bodies).toHaveLength(1);
  expect(bodies[0]).toMatchObject({ question: 'What runs an evaluation?', history: [] });
  expect(bodies[0].requestId).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
  expect(unexpected).toEqual([]);
  expect(await page.evaluate(() => sessionStorage.length)).toBe(0);
  expect(await page.evaluate(() => Object.keys(localStorage).filter((key) => key !== 'starlight-theme'))).toEqual([]);
});
