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
  // 资源竞争彻底修复：测试文件串行（fullyParallel: false）——E2E 测试共享同一
  // dev server + 后端，并发文件必然竞争 CPU/内存/EventSource 资源导致 flaky
  // （慢硬件/CI 上尤其严重）。retries=2（本地+CI）为偶发超时兜底（垃圾硬件）。
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries: 2,
  timeout: 60_000,
  expect: {
    timeout: 10_000,
  },
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
    timeout: 60_000,
  },
})
