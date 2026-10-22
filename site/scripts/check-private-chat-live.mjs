import { readFile } from 'node:fs/promises';
import { chromium } from '@playwright/test';

// Explicit opt-in only: this command makes one real, billable operator request.
let browser;
let stage = 'opening authenticated browser';
try {
  if (process.argv.length !== 3) throw new Error();
  const login = JSON.parse(await readFile(process.argv[2], 'utf8'));
  browser = await chromium.launch();
  const context = await browser.newContext({ httpCredentials: login });
  const page = await context.newPage();
  let calls = 0;
  let violation = false;
  page.on('request', (request) => {
    const url = new URL(request.url());
    if (url.hostname !== '127.0.0.1') violation = true;
    if (Object.keys(request.headers()).includes('x-functions-key')) violation = true;
    if (url.pathname === '/waza/__private-chat' && request.method() === 'POST') calls++;
  });
  await page.goto('http://127.0.0.1:4324/waza/quick-start/');
  stage = 'opening widget';
  await page.getByRole('button', { name: 'Ask Waza', exact: true }).click();
  await page.getByRole('button', { name: 'Send', exact: true }).waitFor();
  await page.getByLabel('Question or error message').fill('What is Waza, and what command runs an evaluation?');
  stage = 'sending one live request';
  const response = page.waitForResponse((value) =>
    value.url().endsWith('/waza/__private-chat') && value.request().method() === 'POST', { timeout: 50000 });
  await page.getByRole('button', { name: 'Send', exact: true }).click();
  const result = await response;
  stage = `checking adapter HTTP status ${result.status()}`;
  if (result.status() !== 200) throw new Error();
  const payload = await result.json();
  await page.getByRole('status').filter({ hasText: 'Live operator answer ready' }).waitFor({ timeout: 50000 });
  stage = 'checking rendered answer and citations';
  const messages = page.locator('[data-chat-messages]');
  const text = await messages.locator(':scope > li').last().locator('pre').first().textContent();
  const citations = await messages.locator('a').evaluateAll((links) => links.map((link) => link.href));
  stage = `checking exact rendered live answer, ${citations.length} citations, ${calls} local submissions, browser boundary ${violation ? 'failed' : 'passed'}`;
  if (typeof payload.answer !== 'string' || !payload.answer.trim() || text !== payload.answer
      || citations.length === 0 || JSON.stringify(citations) !== JSON.stringify(payload.citations.map((source) => source.url))
      || citations.some((value) => {
        const url = new URL(value);
        return url.origin !== 'https://microsoft.github.io' || !url.pathname.startsWith('/waza/');
      }) || violation || calls !== 1) throw new Error();
  const storage = await page.evaluate(() => ({
    local: Object.keys(localStorage).filter((key) => key !== 'starlight-theme'),
    session: sessionStorage.length,
  }));
  if (storage.local.length || storage.session) throw new Error();
  console.log('LIVE BROWSER VERIFIED: one real operator answer rendered with trusted documentation citations.');
  console.log(await page.locator('[data-chat-knowledge]').innerText());
  console.log(await page.locator('[data-chat-snapshot]').textContent());
  console.log(`Trusted citations rendered: ${citations.length}. No Function key or external service request observed in browser.`);
  console.log(`Literal "waza run" in model answer: ${/waza\s+run/i.test(text)}. This transport check does not attest answer quality.`);
  await context.close();
} catch {
  console.error(`Live browser check failed while ${stage}. No credentials or raw response were logged; inspect the authenticated widget.`);
  process.exitCode = 1;
} finally {
  await browser?.close();
}
