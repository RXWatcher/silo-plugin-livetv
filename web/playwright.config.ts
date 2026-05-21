// Playwright config for the livetv plugin's end-to-end harness.
//
// The harness boots:
//   * a Postgres container (or honors a pre-existing DATABASE_URL),
//   * the fixture HTTP server serving M3U + XMLTV + a stub HLS playlist,
//   * the livetv-e2e-server Go binary (chi router + SPA + auth-bypass).
//
// `globalSetup` publishes the base URL via process.env.E2E_BASE_URL; we use
// a thin `use.baseURL` indirection so each test runs against the dynamically-
// bound port without us reserving a fixed port in advance.
//
// Run with: `pnpm exec playwright test`.

import { defineConfig, devices } from '@playwright/test';

const baseURL = process.env.E2E_BASE_URL || 'http://127.0.0.1:4183';

export default defineConfig({
  testDir: './e2e',
  testIgnore: ['**/helpers/**', '**/fixtures/**'],
  timeout: 60_000,
  expect: { timeout: 10_000 },
  fullyParallel: false,
  workers: 1,
  reporter: [['list']],
  globalSetup: './e2e/helpers/globalSetup.ts',
  globalTeardown: './e2e/helpers/globalTeardown.ts',
  use: {
    baseURL,
    trace: 'on-first-retry',
    actionTimeout: 10_000,
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
});
