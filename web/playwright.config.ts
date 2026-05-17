import { defineConfig } from '@playwright/test'

/**
 * Playwright E2E test configuration for xbot Web UI.
 *
 * Requirements:
 * - A running backend (default: http://127.0.0.1:8082)
 * - The frontend dev server will be auto-started on port 5199
 *
 * Environment variables:
 * - E2E_BASE_URL: Override base URL (default: http://127.0.0.1:5199)
 * - E2E_USERNAME: Login username (default: admin)
 * - E2E_PASSWORD: Login password (default: admin)
 */
export default defineConfig({
  testDir: './e2e',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  timeout: 30_000,
  use: {
    baseURL: process.env.E2E_BASE_URL || 'http://127.0.0.1:5199',
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
  },
  projects: [
    { name: 'chromium', use: { browserName: 'chromium' } },
  ],
  webServer: {
    command: 'npm run dev -- --port 5199',
    port: 5199,
    reuseExistingServer: !process.env.CI,
    timeout: 30_000,
  },
})
