import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './e2e',
  testMatch: 'chat-unavailable.spec.ts',
  forbidOnly: !!process.env.CI,
  workers: 1,
  reporter: 'list',
  use: { baseURL: 'http://localhost:4323/waza' },
  projects: [{ name: 'chromium', use: { browserName: 'chromium' } }],
  webServer: {
    command: 'npm run build && npm run preview -- --port 4323',
    port: 4323,
    reuseExistingServer: false,
    timeout: 120000,
    env: {
      PUBLIC_CHAT_ENABLED: 'true',
      PUBLIC_CHAT_FIXTURES: 'true',
      PUBLIC_CHAT_PRIVATE_PREVIEW: 'true',
      PUBLIC_CHAT_API_URL: 'https://func-chat-fskwox3ttikuc.azurewebsites.net/api',
      PUBLIC_CHAT_TURNSTILE_SITEKEY: '',
    },
  },
});
