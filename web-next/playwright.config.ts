import { defineConfig, devices } from "@playwright/test";

const externalBaseURL = process.env.MIDNIGHT_E2E_BASE_URL?.trim();
const baseURL = externalBaseURL || "http://127.0.0.1:4173";

export default defineConfig({
  testDir: "./e2e",
  testMatch: "**/*.e2e.ts",
  outputDir: "test-results/playwright",
  fullyParallel: false,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 1 : 0,
  workers: process.env.CI ? 1 : undefined,
  reporter: process.env.CI
    ? [
        ["line"],
        ["html", { open: "never", outputFolder: "playwright-report" }],
      ]
    : "line",
  expect: {
    timeout: 5_000,
  },
  use: {
    baseURL,
    serviceWorkers: "block",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"] },
    },
  ],
  webServer: externalBaseURL
    ? undefined
    : [{
        command: "go run ./cmd/midnight-server serve --storage=memory --listen 127.0.0.1:4180",
        cwd: "..",
        url: "http://127.0.0.1:4180/api/v1alpha/compat",
        env: { MIDNIGHT_API_BEARER_TOKEN: "" },
        reuseExistingServer: false,
        timeout: 300_000,
      }, {
        command: "npm run dev -- --host 127.0.0.1 --port 4173 --strictPort",
        url: baseURL,
        env: { MIDNIGHT_API_PROXY_TARGET: "http://127.0.0.1:4180" },
        reuseExistingServer: false,
        timeout: 120_000,
      }],
});
