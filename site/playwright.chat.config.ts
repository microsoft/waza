import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './e2e',
  testMatch: 'chat.spec.ts',
  forbidOnly: !!process.env.CI,
  workers: 1,
  reporter: 'list',
  use: { baseURL: 'http://localhost:4322/waza', trace: 'retain-on-failure' },
  projects: [{ name: 'chromium', use: { browserName: 'chromium' } }],
  webServer: {
    command: 'npm run dev -- --port 4322',
    port: 4322,
    reuseExistingServer: false,
    timeout: 120000,
    env: {
      PUBLIC_CHAT_ENABLED: 'true',
      PUBLIC_CHAT_FIXTURES: 'true',
    },
  },
});
