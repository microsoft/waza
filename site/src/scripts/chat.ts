import {
  answer, boundedHistory, bytes, json, metadata, object, requestBody, ServiceError, serviceError,
  type HistoryItem, type Metadata,
} from './chat-contract';
import { publicChatConfig } from './chat-public-config';

const fixtures = import.meta.env.DEV && import.meta.env.PUBLIC_CHAT_FIXTURES === 'true';
const privatePreview = import.meta.env.DEV && import.meta.env.PUBLIC_CHAT_PRIVATE_PREVIEW === 'true';
const publicChat = publicChatConfig(import.meta.env);
const api = publicChat?.api ?? 'https://waza-chat.example/api';
const maxVisibleAnswers = 6;
type Turnstile = {
  render(element: HTMLElement, options: Record<string, unknown>): string;
  reset(widget: string): void;
};
declare global {
  interface Window { turnstile?: Turnstile }
  interface ImportMeta {
    readonly env: {
      readonly DEV: boolean; readonly PUBLIC_CHAT_FIXTURES?: string;
      readonly PUBLIC_CHAT_PRIVATE_PREVIEW?: string;
      readonly PUBLIC_CHAT_API_URL?: string; readonly PUBLIC_CHAT_TURNSTILE_SITEKEY?: string;
    };
  }
}

let challengeScript: Promise<void> | undefined;
function loadChallenge() {
  if (window.turnstile) return Promise.resolve();
  if (!challengeScript) {
    challengeScript = new Promise<void>((resolve, reject) => {
      const script = document.createElement('script');
      const timeout = window.setTimeout(() => reject(new Error('Bot protection could not load. Please use documentation search.')), 15000);
      script.src = publicChat?.challengeScript ?? 'https://waza-chat.example/challenge.js';
      script.async = true;
      script.onload = () => { clearTimeout(timeout); resolve(); };
      script.onerror = () => { clearTimeout(timeout); reject(new Error('Bot protection could not load. Please use documentation search.')); };
      document.head.append(script);
    });
  }
  return challengeScript;
}

export async function initializeChat(root: HTMLElement) {
  const required = <T extends HTMLElement>(selector: string) => {
    const element = root.querySelector<T>(selector);
    if (!element) throw new Error('Chat component configuration is incomplete.');
    return element;
  };
  const dialog = required<HTMLDialogElement>('[data-chat-dialog]');
  const form = required<HTMLFormElement>('[data-chat-form]');
  const question = required<HTMLTextAreaElement>('#chat-question');
  const yaml = required<HTMLTextAreaElement>('#chat-yaml');
  const kind = required<HTMLSelectElement>('#chat-document-kind');
  const status = required('[data-chat-status]');
  const knowledge = required('[data-chat-knowledge]');
  const provenance = required('[data-chat-provenance]');
  const snapshot = required('[data-chat-snapshot]');
  const messages = required<HTMLOListElement>('[data-chat-messages]');
  const send = required<HTMLButtonElement>('[data-chat-send]');
  const cancel = required<HTMLButtonElement>('[data-chat-cancel]');
  const challenge = required('[data-chat-challenge]');
  const clear = required<HTMLButtonElement>('[data-chat-clear]');
  if (!fixtures && !privatePreview && (!publicChat || location.origin !== publicChat.origin)) {
    form.addEventListener('submit', (event) => event.preventDefault());
    question.disabled = yaml.disabled = kind.disabled = clear.disabled = true;
    status.textContent = 'Public chat is unavailable. Use documentation search or browse the CLI reference.';
    return;
  }
  let controller: AbortController | undefined;
  let session: { token: string; expires: number } | undefined;
  let challengeToken = '';
  let widget: string | undefined;
  let expected: Metadata | undefined;
  let history: HistoryItem[] = [];
  let snapshots: Metadata[] = [];
  let busy = false;
  let cooldownUntil = 0;
  let cooldownTimer: number | undefined;
  const requiresChallenge = !privatePreview && (fixtures || !!publicChat?.sitekey);
  const sessionValid = () => session && session.expires > Date.now() + 1000;
  const update = () => {
    send.disabled = busy || Date.now() < cooldownUntil || !expected || (requiresChallenge && !sessionValid() && !challengeToken);
    cancel.disabled = !busy;
    question.disabled = busy;
    yaml.disabled = busy;
    kind.disabled = busy;
    clear.disabled = busy;
  };
  const scheduleCooldown = () => {
    clearTimeout(cooldownTimer);
    const remaining = cooldownUntil - Date.now();
    if (remaining > 0) {
      cooldownTimer = window.setTimeout(scheduleCooldown, Math.min(remaining, 2147483647));
    } else {
      status.textContent = 'Wait complete. You may make a new submission; no request was automatically replayed.';
    }
    update();
  };
  const message = (label: string, content: string) => {
    const item = document.createElement('li');
    const heading = document.createElement('strong');
    heading.textContent = label;
    const text = document.createElement('pre');
    text.textContent = content;
    item.append(heading, text);
    messages.append(item);
    while (messages.children.length > maxVisibleAnswers * 2) messages.firstElementChild?.remove();
    item.scrollIntoView({ block: 'nearest' });
    return item;
  };
  cancel.addEventListener('click', () => controller?.abort());
  dialog.addEventListener('close', () => controller?.abort());
  clear.addEventListener('click', () => {
    history = [];
    snapshots = [];
    messages.replaceChildren();
    if (privatePreview) {
      knowledge.hidden = true;
      provenance.hidden = true;
    }
    question.value = '';
    yaml.value = '';
    status.textContent = Date.now() < cooldownUntil
      ? 'Conversation cleared. The service cooldown still applies.'
      : 'Conversation cleared from this page.';
    question.focus();
  });
  form.addEventListener('submit', async (event) => {
    event.preventDefault();
    if (busy || !expected || Date.now() < cooldownUntil) return;
    let body: string;
    try {
      body = requestBody(question.value, history, kind.value, yaml.value, crypto.randomUUID());
    } catch (error) {
      status.textContent = error instanceof Error ? error.message : 'Invalid input. Please check the question and YAML.';
      return;
    }
    const input = question.value.trim();
    busy = true;
    controller = new AbortController();
    const signal = controller.signal;
    const timeout = window.setTimeout(() => controller?.abort(), 45000);
    update();
    status.textContent = 'Checking the documentation...';
    try {
      if (!privatePreview && !sessionValid()) {
        if (requiresChallenge && !challengeToken) throw new Error('Complete bot protection before sending.');
        const token = challengeToken;
        challengeToken = '';
        if (Array.from(token).length > 2048 || bytes(JSON.stringify({ challengeToken: token })) > 4096) {
          throw new Error('Bot protection returned an oversized challenge. Please complete a new challenge.');
        }
        const response = await fetch(`${api}/session`, {
          method: 'POST', credentials: 'omit', cache: 'no-store', signal,
          headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ challengeToken: token }),
        });
        if (widget !== undefined) window.turnstile?.reset(widget);
        if (!response.ok) throw await serviceError(response);
        const value = await json(response);
        if (!object(value) || value.version !== 1 || typeof value.sessionToken !== 'string'
            || !value.sessionToken || value.sessionToken.length > 4096
            || typeof value.expiresAt !== 'number' || !Number.isFinite(value.expiresAt)
            || value.expiresAt <= Date.now()) throw new Error('The chat service returned an invalid session.');
        session = { token: value.sessionToken, expires: value.expiresAt };
      }
      const response = await fetch(privatePreview ? '/waza/__private-chat' : `${api}/chat`, {
        method: 'POST', credentials: privatePreview ? 'same-origin' : 'omit', cache: 'no-store', signal,
        headers: privatePreview ? { 'Content-Type': 'application/json' }
          : { 'Content-Type': 'application/json', Authorization: `Bearer ${session!.token}` },
        body,
      });
      if (!response.ok) {
        if (response.status === 401) session = undefined;
        throw await serviceError(response);
      }
      const value = await json(response);
      if (!answer(value)) throw new Error('The chat service returned an invalid answer.');
      if (!privatePreview && value.knowledge.fingerprint !== expected.fingerprint) {
        throw new Error('The assistant documentation needs updating. Please use documentation search.');
      }
      if (signal.aborted) throw new DOMException('Cancelled', 'AbortError');
      if (privatePreview) {
        const docs = expected;
        snapshots = [...snapshots, {
          commit: value.knowledge.commit, fingerprint: value.knowledge.fingerprint,
        }].slice(-maxVisibleAnswers);
        const mismatched = snapshots.some((source) => source.fingerprint !== docs.fingerprint);
        knowledge.textContent =
          mismatched
            ? 'Some answers may differ from the docs you are reading. Check the linked sources for current guidance.'
            : 'These answers use the same documentation snapshot as this page. Check the linked sources before applying suggestions.';
        snapshot.textContent = [
          `Local documentation build: ${docs.commit}.`,
          ...snapshots.map((source, index) => `Answer ${index + 1}: ${source.commit}. ${
            source.fingerprint === docs.fingerprint
              ? 'Documentation fingerprint matches this page.'
              : 'Documentation fingerprint differs; sources are not verified-current local content.'
          }`),
          'Matching fingerprints do not verify answer quality or service readiness.',
        ].join('\n');
        knowledge.hidden = false;
        provenance.hidden = false;
      }
      message('You', input);
      const item = message('Waza', value.answer);
      if (value.validation) {
        const result = document.createElement('pre');
        result.textContent = [
          value.validation.valid ? 'Schema valid (not proof of runtime correctness).' : 'Schema validation findings:',
          ...value.validation.errors,
        ].join('\n');
        item.append(result);
      }
      const sources = document.createElement('ul');
      for (const source of value.citations) {
        const row = document.createElement('li');
        const link = document.createElement('a');
        link.textContent = source.title;
        link.href = source.url;
        link.rel = 'noopener noreferrer';
        row.append(link);
        sources.append(row);
      }
      item.append(sources);
      const nextHistory: HistoryItem[] = [...history,
        { role: 'user', content: input }, { role: 'assistant', content: value.answer }];
      history = boundedHistory(nextHistory);
      question.value = '';
      yaml.value = '';
      status.textContent = privatePreview || !fixtures
        ? 'Answer ready. Check the linked sources before applying suggestions.'
        : 'Fixture answer ready (simulated, not live). Verify suggestions against the cited documentation.';
    } catch (error) {
      status.textContent = signal.aborted
        ? 'Request cancelled or timed out. Usage may have been consumed; provider cancellation or refund is not guaranteed. No automatic retry was made.'
        : error instanceof TypeError ? 'The network request failed. No automatic retry was made. Please use documentation search.'
        : error instanceof Error ? error.message : 'Chat failed. Please use documentation search.';
      if (error instanceof ServiceError && error.retryAfterSeconds) {
        cooldownUntil = Date.now() + error.retryAfterSeconds * 1000;
        status.textContent += ` Wait ${error.retryAfterSeconds} seconds before a new submission.`;
        scheduleCooldown();
      }
      if (!sessionValid() && widget !== undefined) window.turnstile?.reset(widget);
    } finally {
      clearTimeout(timeout);
      controller = undefined;
      busy = false;
      update();
      if (dialog.open) question.focus();
    }
  });
  if (!form.contains(document.activeElement)) question.focus();
  status.textContent = requiresChallenge ? 'Loading bot protection...' : 'Loading documentation metadata...';
  try {
    const response = await fetch(root.dataset.metaUrl!, {
      credentials: privatePreview ? 'same-origin' : 'omit', cache: 'no-store', signal: AbortSignal.timeout(15000),
    });
    if (!response.ok) {
      await response.body?.cancel();
      throw new Error('Documentation metadata is unavailable. Please use search.');
    }
    const value = await json(response);
    if (!object(value) || value.version !== 1 || !metadata(value)) throw new Error('Documentation metadata is invalid. Please use search.');
    expected = value;
    knowledge.textContent = 'Answers are checked against this page\'s documentation snapshot.';
    snapshot.textContent = `Local documentation build: ${value.commit}. A matching fingerprint does not verify answer quality or service readiness.`;
    knowledge.hidden = privatePreview;
    provenance.hidden = privatePreview;
    if (!requiresChallenge) {
      status.textContent = 'Ready for your question.';
      update();
      return;
    }
    await loadChallenge();
    if (!window.turnstile) throw new Error('Bot protection is unavailable. Please use search.');
    widget = window.turnstile.render(challenge, {
      sitekey: publicChat?.sitekey ?? 'test-public-sitekey', action: 'waza_chat', theme: 'auto',
      callback: (token: string) => {
        challengeToken = token;
        if (!busy) status.textContent = 'Ready for your question.';
        update();
      },
      'expired-callback': () => { challengeToken = ''; status.textContent = 'Bot verification expired. Please complete a new challenge.'; update(); },
      'error-callback': () => { challengeToken = ''; status.textContent = 'Bot verification failed. Please use documentation search.'; update(); },
    });
  } catch (error) {
    status.textContent = error instanceof Error ? error.message : 'Chat is unavailable. Please use search.';
  }
  update();
}
