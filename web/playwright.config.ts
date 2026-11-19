import { defineConfig } from "@playwright/test";

const port = Number(process.env.PLAYWRIGHT_PORT ?? 4173);

export default defineConfig({
  testDir: "./e2e",
  testIgnore: [
    ...(process.env.WAZA_REFRESH_DOCS_MEDIA === "1"
      ? []
      : ["**/screenshots.spec.ts", "**/release-screenshots.spec.ts"]),
    ...(process.env.WAZA_RELEASE_MEDIA_DIR ? [] : ["**/release-media.spec.ts"]),
  ],
  timeout: 30_000,
  retries: process.env.CI ? 1 : 0,
  workers: process.env.CI ? 2 : undefined,
  reporter: process.env.CI ? "github" : "list",

  use: {
    baseURL: `http://localhost:${port}`,
    screenshot: "only-on-failure",
    video: "off",
    trace: "retain-on-failure",
  },

  webServer: {
    command: `npm run preview -- --port ${port}`,
    port,
    reuseExistingServer: !process.env.CI,
  },

  projects: [
    {
      name: "chromium",
      use: { browserName: "chromium" },
    },
    {
      name: "firefox",
      use: { browserName: "firefox" },
    },
  ],
});
