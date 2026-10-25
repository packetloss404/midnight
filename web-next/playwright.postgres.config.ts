import { defineConfig, devices } from "@playwright/test";
import { randomUUID } from "node:crypto";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

process.env.MIDNIGHT_PG_E2E_STATE_PATH ||= resolve(
  dirname(fileURLToPath(import.meta.url)), "..", ".midnight", "browser-e2e", randomUUID(), "state.json",
);

export default defineConfig({
  testDir: "./e2e-postgres",
  testMatch: "**/*.e2e.ts",
  outputDir: "test-results/postgres",
  fullyParallel: false,
  forbidOnly: Boolean(process.env.CI),
  // First-run administrator enrollment is intentionally one-shot.
  retries: 0,
  workers: 1,
  reporter: "line",
  timeout: 120_000,
  expect: { timeout: 10_000 },
  use: {
    baseURL: "http://127.0.0.1:4183",
    actionTimeout: 15_000,
    serviceWorkers: "block",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  projects: [{ name: "postgres-chromium", use: { ...devices["Desktop Chrome"] } }],
  webServer: [{
    command: "node scripts/start-postgres-e2e.mjs",
    url: "http://127.0.0.1:4182/api/v1alpha/compat",
    timeout: 300_000,
    reuseExistingServer: false,
    gracefulShutdown: { signal: "SIGTERM", timeout: 15_000 },
    env: { MIDNIGHT_PG_E2E_STATE_PATH: process.env.MIDNIGHT_PG_E2E_STATE_PATH },
  }, {
    command: "npm run dev -- --host 127.0.0.1 --port 4183 --strictPort",
    url: "http://127.0.0.1:4183",
    timeout: 120_000,
    reuseExistingServer: false,
    env: {
      MIDNIGHT_API_PROXY_TARGET: "http://127.0.0.1:4182",
      VITE_MIDNIGHT_TOKEN: "",
      VITE_AGENT_MOCK: "",
    },
  }],
});
