import { defineConfig, devices } from '@playwright/test'

import { adminURL, stateFile } from './e2e/paths'

// Runs against a running dev stack (`make dev-up`); PULSE_E2E_URL overrides the admin URL.
export default defineConfig({
  testDir: './e2e',
  // Signs in an e2e administrator through the API (users, TOTP; ADR-0038) and shares the session with every test.
  globalSetup: './e2e/global-setup.ts',
  timeout: 30_000,
  fullyParallel: false,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
  use: {
    baseURL: adminURL,
    storageState: stateFile,
    trace: 'retain-on-failure',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
})
