interface PublicChatEnvironment {
  readonly DEV: boolean;
  readonly PUBLIC_CHAT_API_URL?: string;
  readonly PUBLIC_CHAT_TURNSTILE_SITEKEY?: string;
}

const publicApi = 'https://func-chat-fskwox3ttikuc.azurewebsites.net/api';

export function parsePublicChatConfig(api: string | undefined, sitekey?: string) {
  if (!api) throw new Error('Configure the public chat API URL.');
  let url: URL;
  try {
    url = new URL(api);
  } catch {
    throw new Error('The public chat API URL is invalid.');
  }
  if (url.href.replace(/\/$/, '') !== publicApi || url.username || url.password || api.includes('@')) {
    throw new Error('Public chat must use the configured HTTPS API without credentials, query parameters or fragments.');
  }
  if (sitekey && !/^[a-zA-Z0-9_-]+$/.test(sitekey)) {
    throw new Error('The public Turnstile sitekey is invalid.');
  }
  return {
    api: publicApi,
    sitekey: sitekey || undefined,
    origin: 'https://microsoft.github.io',
    challengeScript: 'https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit',
  };
}

export function publicChatConfig(env: PublicChatEnvironment) {
  if (env.DEV || (!env.PUBLIC_CHAT_API_URL && !env.PUBLIC_CHAT_TURNSTILE_SITEKEY)) return undefined;
  return parsePublicChatConfig(env.PUBLIC_CHAT_API_URL, env.PUBLIC_CHAT_TURNSTILE_SITEKEY);
}
