// 临时本地配置（仅本次复跑用，跑完删除）：自建 vite dev server（15300）+ 复用
// 已经跑着的测试后端（18432）—— 避免与 worktree 里其他 agent 的 5199/8082 冲突。
import { defineConfig } from '@playwright/test'

export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  retries: 0,
  timeout: 120_000,
  expect: { timeout: 10_000 },
  use: {
    baseURL: 'http://127.0.0.1:15300',
    trace: 'off',
    screenshot: 'only-on-failure',
  },
  projects: [{ name: 'chromium', use: { browserName: 'chromium' } }],
  webServer: {
    command: 'E2E_BACKEND_URL=http://127.0.0.1:18432 npx vite --port 15300 --strictPort',
    port: 15300,
    reuseExistingServer: false,
    timeout: 90_000,
  },
})
