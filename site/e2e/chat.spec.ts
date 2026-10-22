import { expect, test, type Page } from '@playwright/test';

const knowledge = { commit: 'a'.repeat(40), fingerprint: 'b'.repeat(64) };
const validAnswer = {
  version: 1, answer: 'Use waza run with an eval specification.',
  citations: [{ id: 'quick-start', title: 'Quick Start', url: 'https://microsoft.github.io/waza/quick-start/' }],
  knowledge,
};

async function setup(page: Page, options: { noChallenge?: boolean; missingMetadata?: boolean } = {}) {
  await page.route('**/chat-meta.json', (route) => route.fulfill(options.missingMetadata
    ? { status: 503, body: 'unavailable' } : { json: { version: 1, ...knowledge } }));
  await page.route('https://waza-chat.example/api/session', (route) => route.fulfill({
    json: { version: 1, sessionToken: 'test-session', expiresAt: Date.now() + 600000 },
  }));
  await page.route('https://waza-chat.example/api/chat', (route) => route.fulfill({ json: validAnswer }));
  if (!options.noChallenge) {
    await page.addInitScript(() => {
      let verified: ((token: string) => void) | undefined;
      Object.assign(window, { turnstile: {
        render: (_element: HTMLElement, config: { callback: (token: string) => void }) => {
          verified = config.callback;
          config.callback('test-challenge');
          return 'test-widget';
        },
        reset: () => verified?.('fresh-test-challenge'),
      } });
    });
  }
  await page.goto('/waza/quick-start/');
  await page.getByRole('button', { name: 'Ask Waza', exact: true }).click();
}

async function send(page: Page, question = 'How do I run an eval?') {
  await page.getByLabel('Question or error message').fill(question);
  await page.getByRole('button', { name: 'Send', exact: true }).click();
}

test('launcher waits for its bootstrap listener before accepting clicks', async ({ page }) => {
  let release!: () => void;
  let loading = false;
  const delayed = new Promise<void>((resolve) => { release = resolve; });
  await page.route('**/src/components/ChatWidget.astro*', async (route) => {
    loading = true;
    await delayed;
    await route.continue();
  });
  await page.route('**/chat-meta.json', (route) => route.fulfill({ status: 503, body: 'unavailable' }));
  await page.goto('/waza/quick-start/', { waitUntil: 'commit' });
  const launcher = page.getByRole('button', { name: 'Ask Waza', exact: true });
  await expect(launcher).toBeVisible();
  await expect.poll(() => loading).toBe(true);
  await expect(launcher).toBeDisabled();
  release();
  await expect(launcher).toBeEnabled();
  await launcher.click();
  await expect(page.getByRole('dialog')).toBeVisible();
  await expect(page.getByRole('status')).toContainText('metadata is unavailable');
});

test('lazy loads, exchanges challenge once and sends bounded typed conversation', async ({ page }) => {
  const requests: unknown[] = [];
  let sessions = 0;
  page.on('request', (request) => {
    if (request.url().endsWith('/api/chat')) requests.push(request.postDataJSON());
    if (request.url().endsWith('/api/session')) sessions++;
  });
  await setup(page);
  await send(page);
  await expect(page.getByRole('status')).toContainText('Fixture answer ready');
  await expect(page.locator('[data-chat-messages]').getByRole('link', { name: 'Quick Start', exact: true })).toHaveAttribute('href', validAnswer.citations[0].url);
  expect(requests[0]).toMatchObject({ question: 'How do I run an eval?', history: [] });
  await send(page, 'And how do I read the results?');
  await expect(page.getByRole('status')).toContainText('Fixture answer ready');
  expect(requests[1]).toMatchObject({ history: [
    { role: 'user', content: 'How do I run an eval?' },
    { role: 'assistant', content: validAnswer.answer },
  ] });
  expect(sessions).toBe(1);
  expect(await page.evaluate(() => Object.keys(localStorage).filter((key) => key !== 'starlight-theme'))).toEqual([]);
  expect(await page.evaluate(() => JSON.stringify(localStorage))).not.toContain('test-session');
});

test('renders HTML and code as inert text rather than executable Markdown', async ({ page }) => {
  await setup(page);
  await page.route('https://waza-chat.example/api/chat', (route) => route.fulfill({
    json: { ...validAnswer, answer: '<img src=x onerror="window.pwned=1"> [click](javascript:alert(1))' },
  }));
  await send(page);
  await expect(page.locator('[data-chat-messages]')).toContainText('<img src=x');
  await expect(page.locator('[data-chat-messages] img')).toHaveCount(0);
  await expect(page.locator('[data-chat-messages] a')).toHaveCount(1);
});

for (const [label, result] of [
  ['unsafe citation', { ...validAnswer, citations: [{ id: 'x', title: 'Unsafe', url: 'javascript:alert(1)' }] }],
  ['wrong knowledge snapshot', { ...validAnswer, knowledge: { ...knowledge, fingerprint: 'c'.repeat(64) } }],
  ['invalid structure', { answer: 'unvalidated' }],
] as const) {
  test(`rejects ${label} without displaying an answer`, async ({ page }) => {
    await setup(page);
    await page.route('https://waza-chat.example/api/chat', (route) => route.fulfill({ json: result }));
    await send(page);
    await expect(page.getByRole('status')).toContainText(/invalid answer|needs updating/);
    await expect(page.locator('[data-chat-messages] > li')).toHaveCount(0);
  });
}

for (const status of [401, 413, 429, 503, 504]) {
  test(`handles ${status} explicitly without retrying or echoing backend internals`, async ({ page }) => {
    await setup(page);
    let calls = 0;
    await page.route('https://waza-chat.example/api/chat', (route) => {
      calls++;
      return route.fulfill({ status, body: 'secret-canary-never-display' });
    });
    await send(page);
    await expect(page.getByRole('status')).toContainText(/expired|shorten|limit|unavailable|timed out/);
    expect(calls).toBe(1);
    await expect(page.locator('[data-chat-dialog]')).not.toContainText('secret-canary');
  });
}

test('YAML uses the explicit document type and validation does not imply runtime success', async ({ page }) => {
  await setup(page);
  await page.locator('summary').filter({ hasText: 'Check pasted YAML' }).click();
  await page.getByLabel('Document type').selectOption('task');
  await page.getByLabel('YAML without credentials').fill('id: example');
  await page.route('https://waza-chat.example/api/chat', async (route) => {
    expect(route.request().postDataJSON().document).toEqual({ kind: 'task', yaml: 'id: example' });
    await route.fulfill({ json: { ...validAnswer, validation: { valid: true, errors: [] } } });
  });
  await send(page);
  await expect(page.locator('[data-chat-messages]')).toContainText('not proof of runtime correctness');
});

test('unavailable metadata prevents challenge session or model calls', async ({ page }) => {
  await setup(page, { missingMetadata: true });
  await expect(page.getByRole('status')).toContainText('metadata is unavailable');
  await expect(page.getByRole('button', { name: 'Send', exact: true })).toBeDisabled();
});

test('unavailable challenge fails closed', async ({ page }) => {
  await page.route('https://waza-chat.example/challenge.js', (route) => route.abort());
  await setup(page, { noChallenge: true });
  await expect(page.getByRole('status')).toContainText('Bot protection could not load');
  await expect(page.getByRole('button', { name: 'Send', exact: true })).toBeDisabled();
});

test('cancellation does not display a late answer or automatically retry', async ({ page }) => {
  await setup(page);
  let calls = 0;
  await page.route('https://waza-chat.example/api/chat', async (route) => {
    calls++;
    await new Promise((resolve) => setTimeout(resolve, 500));
    await route.fulfill({ json: validAnswer });
  });
  await send(page);
  await expect.poll(() => calls).toBe(1);
  await page.getByRole('button', { name: 'Cancel request', exact: true }).click();
  await expect(page.getByRole('status')).toContainText('cancelled');
  await expect(page.locator('[data-chat-messages] > li')).toHaveCount(0);
  expect(calls).toBe(1);
});

test('mobile dialog is keyboard accessible and Escape returns focus to launcher', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await setup(page);
  await expect(page.getByRole('dialog')).toBeVisible();
  await expect(page.getByLabel('Question or error message')).toBeFocused();
  const bounds = await page.getByRole('dialog').boundingBox();
  expect(bounds).not.toBeNull();
  expect(bounds!.x).toBeGreaterThanOrEqual(0);
  expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(390);
  expect(await page.getByRole('dialog').evaluate((dialog) => dialog.scrollWidth <= dialog.clientWidth)).toBe(true);
  await page.keyboard.press('Escape');
  await expect(page.getByRole('dialog')).not.toBeVisible();
  await expect(page.getByRole('button', { name: 'Ask Waza', exact: true })).toBeFocused();
});

for (const theme of ['light', 'dark']) {
  for (const width of [390, 1280]) {
    test(`chat uses site controls and readable snapshot details in ${theme} at ${width}px`, async ({ page }, testInfo) => {
      await page.setViewportSize({ width, height: 900 });
      await setup(page);
      await page.evaluate((value) => { document.documentElement.dataset.theme = value; }, theme);
      await expect(page.getByRole('button', { name: 'Send', exact: true })).toBeEnabled();
      const colors = await page.evaluate(() => {
        const probe = document.createElement('span');
        probe.style.backgroundColor = 'var(--sl-color-accent-high)';
        probe.style.color = 'var(--sl-color-black)';
        document.body.append(probe);
        const result = { background: getComputedStyle(probe).backgroundColor, text: getComputedStyle(probe).color };
        probe.remove();
        return result;
      });
      const primary = page.getByRole('button', { name: 'Send', exact: true });
      await expect(primary).toHaveCSS('background-color', colors.background);
      await expect(primary).toHaveCSS('color', colors.text);
      for (const button of await page.locator('[data-chat-root] button').all()) {
        const size = await button.boundingBox();
        expect(size?.height).toBeGreaterThanOrEqual(44);
        expect(await button.evaluate((element) => parseFloat(getComputedStyle(element).borderRadius)))
          .toBeGreaterThanOrEqual(size!.height / 2);
      }
      await send(page);
      await expect(page.getByRole('status')).toContainText('Fixture answer ready');
      await expect(page.locator('[data-chat-knowledge]')).not.toContainText(knowledge.commit);
      await expect(page.locator('[data-chat-snapshot]')).not.toBeVisible();
      await primary.scrollIntoViewIfNeeded();
      await page.screenshot({ path: testInfo.outputPath('chat.png') });
      await page.getByText('About these answers', { exact: true }).click();
      await expect(page.locator('[data-chat-snapshot]')).toBeVisible();
      await expect(page.locator('[data-chat-snapshot]')).toContainText(knowledge.commit);
      expect(await page.getByRole('dialog').evaluate((dialog) => dialog.scrollWidth <= dialog.clientWidth)).toBe(true);
      await page.keyboard.press('Tab');
      await page.getByText('About these answers', { exact: true }).focus();
      await expect(page.getByText('About these answers', { exact: true })).toHaveCSS('outline-style', 'solid');
      await page.keyboard.press('Escape');
      const launcher = page.getByRole('button', { name: 'Ask Waza', exact: true });
      await expect(launcher).toBeFocused();
      await expect(launcher).toHaveCSS('outline-style', 'solid');
      await page.screenshot({ path: testInfo.outputPath('launcher.png') });
    });
  }
}

test('input byte budget rejects excessive Unicode and YAML before contacting the service', async ({ page }) => {
  await setup(page);
  let calls = 0;
  page.on('request', (request) => { if (request.url().startsWith('https://waza-chat.example/')) calls++; });
  await page.locator('summary').filter({ hasText: 'Check pasted YAML' }).click();
  await page.getByLabel('YAML without credentials').fill('a'.repeat(8192));
  await send(page, 'é'.repeat(4096));
  await expect(page.getByRole('status')).toContainText('shorten');
  expect(calls).toBe(0);
});

test('expired session response never authorizes a model request', async ({ page }) => {
  await setup(page);
  let calls = 0;
  page.on('request', (request) => { if (request.url().endsWith('/api/chat')) calls++; });
  await page.route('https://waza-chat.example/api/session', (route) => route.fulfill({
    json: { version: 1, sessionToken: 'expired', expiresAt: Date.now() - 1000 },
  }));
  await send(page);
  await expect(page.getByRole('status')).toContainText('invalid session');
  expect(calls).toBe(0);
});

test('failed server-side challenge does not expose internals or call the model', async ({ page }) => {
  await setup(page);
  let calls = 0;
  page.on('request', (request) => { if (request.url().endsWith('/api/chat')) calls++; });
  await page.route('https://waza-chat.example/api/session', (route) => route.fulfill({
    status: 403, json: { version: 1, error: { code: 'CHALLENGE_FAILED', message: 'challenge-secret-canary' } },
  }));
  await send(page);
  await expect(page.getByRole('status')).toContainText('Bot verification failed');
  await expect(page.locator('[data-chat-dialog]')).not.toContainText('challenge-secret-canary');
  expect(calls).toBe(0);
});

test('oversized and malformed service bodies are not rendered', async ({ page }) => {
  await setup(page);
  await page.route('https://waza-chat.example/api/chat', (route) => route.fulfill({ contentType: 'application/json', body: 'x'.repeat(65537) }));
  await send(page);
  await expect(page.getByRole('status')).toContainText('oversized response');
  await expect(page.locator('[data-chat-messages] > li')).toHaveCount(0);
  await page.route('https://waza-chat.example/api/chat', (route) => route.fulfill({ contentType: 'application/json', body: '{broken' }));
  await send(page);
  await expect(page.getByRole('status')).toContainText('invalid response');
});

test('no-evidence answer is explicit and works in dark mode', async ({ page }) => {
  await page.emulateMedia({ colorScheme: 'dark' });
  await setup(page);
  await page.route('https://waza-chat.example/api/chat', (route) => route.fulfill({
    json: { ...validAnswer, answer: 'I could not find supporting documentation for that question.', citations: [] },
  }));
  await send(page);
  await expect(page.locator('[data-chat-messages]')).toContainText('could not find supporting documentation');
  await expect(page.locator('[data-chat-messages] a')).toHaveCount(0);
});

for (const yaml of ['  id: example\n  name: Example\n', 'description: |+\n  Keep blank lines.\n\n\n']) {
  test(`preserves significant YAML bytes: ${JSON.stringify(yaml)}`, async ({ page }) => {
    await setup(page);
    await page.locator('summary').filter({ hasText: 'Check pasted YAML' }).click();
    await page.getByLabel('YAML without credentials').fill(yaml);
    let sent = false;
    await page.route('https://waza-chat.example/api/chat', async (route) => {
      expect(route.request().postDataJSON().document).toEqual({ kind: 'eval', yaml });
      sent = true;
      await route.fulfill({ json: validAnswer });
    });
    await send(page);
    await expect(page.getByRole('status')).toContainText('Fixture answer ready');
    expect(sent).toBe(true);
  });
}

test('uses Unicode characters rather than UTF-16 units and rejects untrimmed excess', async ({ page }) => {
  await setup(page);
  const input = '😀'.repeat(3000);
  let calls = 0;
  await page.route('https://waza-chat.example/api/chat', (route) => {
    calls++;
    expect(route.request().postDataJSON().question).toBe(input);
    return route.fulfill({ json: validAnswer });
  });
  await send(page, input);
  await expect(page.getByRole('status')).toContainText('Fixture answer ready');
  await send(page, ' '.repeat(3000) + 'x');
  await expect(page.getByRole('status')).toContainText('3000 Unicode characters');
  expect(calls).toBe(1);
});

test('rejects YAML byte excess before creating a session', async ({ page }) => {
  await setup(page);
  let calls = 0;
  page.on('request', (request) => { if (request.url().includes('/api/')) calls++; });
  await page.locator('summary').filter({ hasText: 'Check pasted YAML' }).click();
  await page.getByLabel('YAML without credentials').fill('é'.repeat(4097));
  await expect(page.getByLabel('YAML without credentials')).toHaveValue('é'.repeat(4097));
  await send(page);
  await expect(page.getByRole('status')).toContainText('8192 UTF-8 bytes');
  expect(calls).toBe(0);
});

test('lazy initialization preserves early YAML input and form focus', async ({ page }) => {
  let release!: () => void;
  let loading = false;
  const delayed = new Promise<void>((resolve) => { release = resolve; });
  await page.route('**/src/scripts/chat.ts*', async (route) => {
    loading = true;
    await delayed;
    await route.continue();
  });
  await setup(page);
  await expect.poll(() => loading).toBe(true);
  await page.locator('summary').filter({ hasText: 'Check pasted YAML' }).click();
  const yaml = page.getByLabel('YAML without credentials');
  await yaml.fill('é'.repeat(4097));
  release();
  await expect(page.getByRole('status')).toHaveText('Ready for your question.');
  await expect(yaml).toBeFocused();
  await expect(yaml).toHaveValue('é'.repeat(4097));
  await send(page);
  await expect(page.getByRole('status')).toContainText('8192 UTF-8 bytes');
});

test('honors cooldown, prevents duplicate submits, and uses new IDs for manual submissions', async ({ page }) => {
  await page.clock.install();
  await setup(page);
  const ids: string[] = [];
  await page.route('https://waza-chat.example/api/chat', (route) => {
    ids.push(route.request().postDataJSON().requestId);
    return route.fulfill({
      status: 429,
      json: { version: 1, error: { code: 'QUOTA_EXCEEDED', message: 'private details', retryAfterSeconds: 10 } },
    });
  });
  await send(page);
  await expect(page.getByRole('status')).toContainText('Wait 10 seconds');
  await expect(page.getByRole('button', { name: 'Send', exact: true })).toBeDisabled();
  await page.locator('[data-chat-form]').evaluate((form: HTMLFormElement) => {
    form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
  });
  expect(ids).toHaveLength(1);
  await page.clock.fastForward(10001);
  await expect(page.getByRole('status')).toContainText('Wait complete');
  await send(page);
  await expect.poll(() => ids.length).toBe(2);
  expect(ids[0]).not.toBe(ids[1]);
  expect(ids.every((id) => /^[0-9a-f-]{36}$/.test(id))).toBe(true);
});

test('keeps only six complete messages, clears context, and never persists the transcript', async ({ page }) => {
  await setup(page);
  const contexts: unknown[][] = [];
  await page.route('https://waza-chat.example/api/chat', (route) => {
    contexts.push(route.request().postDataJSON().history);
    return route.fulfill({ json: validAnswer });
  });
  for (let turn = 0; turn < 5; turn++) {
    await send(page, `Question ${turn}`);
    await expect(page.getByRole('status')).toContainText('Fixture answer ready');
  }
  expect(contexts[4]).toHaveLength(6);
  expect(contexts[4][0]).toEqual({ role: 'user', content: 'Question 1' });
  await page.getByRole('button', { name: 'Clear conversation' }).click();
  await expect(page.locator('[data-chat-messages] > li')).toHaveCount(0);
  await send(page);
  await expect(page.getByRole('status')).toContainText('Fixture answer ready');
  expect(contexts[5]).toEqual([]);
  expect(await page.evaluate(() => sessionStorage.length)).toBe(0);
  await page.reload();
  await page.getByRole('button', { name: 'Ask Waza', exact: true }).click();
  await expect(page.locator('[data-chat-messages] > li')).toHaveCount(0);
});

test('pending request cannot be submitted twice and network failure stays explicit', async ({ page }) => {
  await setup(page);
  let calls = 0;
  let release: (() => void) | undefined;
  await page.route('https://waza-chat.example/api/chat', async (route) => {
    calls++;
    await new Promise<void>((resolve) => { release = resolve; });
    await route.abort('failed');
  });
  await send(page);
  await expect.poll(() => calls).toBe(1);
  await expect(page.getByRole('button', { name: 'Send', exact: true })).toBeDisabled();
  await page.locator('[data-chat-form]').evaluate((form: HTMLFormElement) => {
    form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
  });
  expect(calls).toBe(1);
  release?.();
  await expect(page.getByRole('status')).toContainText('network request failed');
  await expect(page.locator('[data-chat-messages] > li')).toHaveCount(0);
});

for (const token of ['x'.repeat(2049), '😀'.repeat(2048)]) {
  test(`rejects oversized challenge ${Array.from(token).length} characters before session exchange`, async ({ page }) => {
    await page.addInitScript((challengeToken) => {
      Object.assign(window, { turnstile: {
        render: (_element: HTMLElement, config: { callback: (token: string) => void }) => {
          config.callback(challengeToken);
          return 'test-widget';
        },
        reset: () => {},
      } });
    }, token);
    await setup(page, { noChallenge: true });
    let calls = 0;
    page.on('request', (request) => { if (request.url().includes('/api/')) calls++; });
    await send(page);
    await expect(page.getByRole('status')).toContainText('oversized challenge');
    expect(calls).toBe(0);
  });
}

test('a bare session authorization rejection never calls chat or retries', async ({ page }) => {
  await setup(page);
  let sessions = 0;
  let chats = 0;
  await page.route('https://waza-chat.example/api/session', (route) => {
    sessions++;
    return route.fulfill({ status: 401, body: 'Unauthorized' });
  });
  page.on('request', (request) => { if (request.url().endsWith('/api/chat')) chats++; });
  await send(page);
  await expect(page.getByRole('status')).toContainText('session has expired');
  expect(sessions).toBe(1);
  expect(chats).toBe(0);
});
