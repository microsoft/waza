export type HistoryItem = { role: 'user' | 'assistant'; content: string };
type Citation = { id: string; title: string; url: string };
export type Metadata = { commit: string; fingerprint: string };
export type ChatAnswer = {
  version: 1; answer: string; citations: Citation[]; knowledge: Metadata;
  validation?: { valid: boolean; errors: string[] };
};
export type DocumentKind = 'eval' | 'task' | 'config';

export const bytes = (text: string) => new TextEncoder().encode(text).length;
const characters = (text: string) => Array.from(text).length;
export const object = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null && !Array.isArray(value);
export const metadata = (value: unknown): value is Metadata => object(value)
  && typeof value.commit === 'string' && /^[a-f0-9]{40}$/.test(value.commit)
  && typeof value.fingerprint === 'string' && /^[a-f0-9]{64}$/.test(value.fingerprint);

function citation(value: unknown): value is Citation {
  if (!object(value) || typeof value.id !== 'string' || typeof value.title !== 'string'
      || typeof value.url !== 'string' || value.title.length > 300 || value.url.length > 2048) return false;
  try {
    const url = new URL(value.url);
    return url.origin === 'https://microsoft.github.io' && url.pathname.startsWith('/waza/')
      && !url.username && !url.password && !url.search;
  } catch { return false; }
}

export function answer(value: unknown): value is ChatAnswer {
  if (!object(value) || value.version !== 1 || typeof value.answer !== 'string'
      || !value.answer.trim() || value.answer.length > 16000 || !metadata(value.knowledge)
      || !Array.isArray(value.citations) || value.citations.length > 12 || !value.citations.every(citation)) return false;
  if (value.validation !== undefined) {
    const result = value.validation;
    if (!object(result) || typeof result.valid !== 'boolean' || !Array.isArray(result.errors)
        || result.errors.length > 30 || !result.errors.every((error) => typeof error === 'string' && error.length <= 1000)) return false;
  }
  return true;
}

export function requestBody(question: string, history: HistoryItem[], kind: string, yaml: string, requestId: string) {
  if (!question.trim()) throw new Error('Enter a question first.');
  if (characters(question) > 3000) throw new Error('Please shorten your question to 3000 Unicode characters or less.');
  if (!['eval', 'task', 'config'].includes(kind)) throw new Error('Choose a supported document type.');
  if (yaml.trim() && (characters(yaml) > 8192 || bytes(yaml) > 8192)) {
    throw new Error('Please shorten your YAML to 8192 characters and 8192 UTF-8 bytes or less.');
  }
  if (history.length > 6 || history.some((entry) => !['user', 'assistant'].includes(entry.role)
      || !characters(entry.content) || characters(entry.content) > 2000)) {
    throw new Error('Conversation context exceeds the service limits. Clear the conversation before sending.');
  }
  const body = {
    question: question.trim(), history, requestId,
    ...(yaml.trim() ? { document: { kind: kind as DocumentKind, yaml } } : {}),
  };
  if (bytes(body.question) + history.reduce((total, entry) => total + bytes(entry.content), 0)
      + bytes(body.document?.yaml ?? '') > 12288) {
    throw new Error('Please shorten your question or YAML, or clear the conversation: the total limit is 12288 UTF-8 bytes.');
  }
  const encoded = JSON.stringify(body);
  if (bytes(encoded) > 32768) throw new Error('The encoded request exceeds 32768 bytes. Please shorten the input.');
  return encoded;
}

// Keep a contiguous suffix of complete messages; never trim message content.
export function boundedHistory(history: HistoryItem[]): HistoryItem[] {
  const retained: HistoryItem[] = [];
  for (const entry of history.slice(-6).reverse()) {
    if (!characters(entry.content) || characters(entry.content) > 2000) break;
    retained.unshift(entry);
  }
  return retained;
}

export async function json(response: Response): Promise<unknown> {
  if (!/^application\/json(?:\s*;|$)/i.test(response.headers.get('content-type') ?? '')) {
    await response.body?.cancel();
    throw new Error('The chat service returned a non-JSON response. Please use documentation search.');
  }
  if (!response.body) throw new Error('The chat service returned an empty response.');
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let text = '';
  let size = 0;
  try {
    while (true) {
      const part = await reader.read();
      if (part.done) break;
      size += part.value.byteLength;
      if (size > 65536) throw new Error('The chat service returned an oversized response.');
      text += decoder.decode(part.value, { stream: true });
    }
    text += decoder.decode();
  } finally {
    await reader.cancel();
  }
  try { return JSON.parse(text); } catch { throw new Error('The chat service returned an invalid response.'); }
}

export class ServiceError extends Error {
  retryAfterSeconds?: number;
  constructor(message: string, retryAfterSeconds?: number) {
    super(message);
    this.retryAfterSeconds = retryAfterSeconds;
  }
}

const errors: Record<string, string> = {
  INVALID_REQUEST: 'The request was rejected. Check the question and YAML limits.',
  INVALID_SESSION: 'Your session has expired. Send again to start a new session.',
  CHALLENGE_FAILED: 'Bot verification failed. Complete a new challenge.',
  ORIGIN_DENIED: 'Chat is not available from this site. Please use documentation search.',
  QUOTA_EXCEEDED: 'The chat usage limit has been reached. Please use documentation search.',
  REQUEST_DUPLICATE: 'This request was already submitted. No automatic retry was made.',
  BUSY: 'The chat service is busy. Wait before making a new submission.',
  DISABLED: 'Public chat is unavailable. Please use documentation search.',
  CONFIGURATION_REQUIRED: 'Chat configuration is incomplete. Please use documentation search.',
  RUNTIME_UNVERIFIED: 'Chat runtime readiness is unverified. Please use documentation search.',
  CLIENT_ID_UNVERIFIED: 'Chat cannot verify this client. Please use documentation search.',
  KNOWLEDGE_UNAVAILABLE: 'Assistant documentation is unavailable. Please use documentation search.',
  STORAGE_UNAVAILABLE: 'Chat storage is unavailable. Please use documentation search.',
  MODEL_UNAVAILABLE: 'The model service is unavailable. Please use documentation search.',
  MODEL_TIMEOUT: 'The model service timed out. No automatic retry was made.',
  INVALID_OUTPUT: 'The model returned an invalid answer. Please use documentation search.',
};

export async function serviceError(response: Response): Promise<ServiceError> {
  const fallback: Record<number, string> = {
    401: errors.INVALID_SESSION,
    403: 'Bot verification or session creation failed. Please use documentation search.',
    413: 'Please shorten your question or YAML.',
    429: errors.QUOTA_EXCEEDED,
    503: errors.DISABLED,
    504: errors.MODEL_TIMEOUT,
  };
  let message = fallback[response.status] ?? 'The chat request failed. No automatic retry was made.';
  let retry: number | undefined;
  if (/^application\/json(?:\s*;|$)/i.test(response.headers.get('content-type') ?? '')) {
    const value = await json(response);
    if (!object(value) || value.version !== 1 || !object(value.error) || typeof value.error.code !== 'string'
        || typeof value.error.message !== 'string') throw new Error('The chat service returned an invalid error response.');
    message = errors[value.error.code] ?? message;
    const seconds = value.error.retryAfterSeconds;
    if (seconds !== undefined) {
      if (typeof seconds !== 'number' || !Number.isFinite(seconds) || seconds < 0 || seconds > Number.MAX_SAFE_INTEGER / 1000) {
        throw new Error('The chat service returned an invalid retry delay.');
      }
      retry = seconds;
    }
  } else {
    await response.body?.cancel();
  }
  return new ServiceError(message, retry);
}
