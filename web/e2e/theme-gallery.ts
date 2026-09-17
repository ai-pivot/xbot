import { chromium, type Browser, type Page } from '@playwright/test'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'

/**
 * 主题画廊截图器（自动化，非断言用例）：为**每个内置 Markdown 主题**截一张
 * 统一构图的应用截图，供主 agent 用多模态能力逐张评审（对比度/层次/配色/未来感），
 * 并作为改主题后的**回归对照**。
 *
 * 运行：
 *   cd web && THEME_SHOT_DIR=/tmp/theme-shots npx tsx e2e/theme-gallery.spec.ts
 *   （或经 playwright 跑：见文件末尾 runAll()）
 * 输出：<THEME_SHOT_DIR>/<themeId>.png（统一 1440x900 视口，非 fullPage）
 *
 * 为什么单独写成脚本而非 *.spec.ts：它是"产出物生成器"，不做断言；放进 CI 只会
 * 增加跑时。评审流程 = 本脚本产图 → 主 agent view_image → 改 CSS → 再产图对比。
 */

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'
const OUT = process.env.THEME_SHOT_DIR || '/tmp/theme-shots'

/** 与 web/src/types/markdown-theme.ts 的 MARKDOWN_THEMES 保持一致（顺序同 UI）。 */
export const THEMES: Array<{ id: string; mode: 'dark' | 'light' }> = [
  { id: 'vscode-dark', mode: 'dark' },
  { id: 'github-dark', mode: 'dark' },
  { id: 'github-light', mode: 'light' },
  { id: 'solarized-light', mode: 'light' },
  { id: 'one-light', mode: 'light' },
  { id: 'quiet-light', mode: 'light' },
  { id: 'dracula', mode: 'dark' },
  { id: 'one-dark', mode: 'dark' },
  { id: 'monokai', mode: 'dark' },
  { id: 'tokyo-night', mode: 'dark' },
  { id: 'nord', mode: 'dark' },
  { id: 'solarized-dark', mode: 'dark' },
  { id: 'night-wolf-gray', mode: 'dark' },
  { id: 'night-wolf-blue', mode: 'dark' },
  { id: 'tui-midnight', mode: 'dark' },
  { id: 'tui-ocean', mode: 'dark' },
  { id: 'tui-forest', mode: 'dark' },
  { id: 'tui-sunset', mode: 'dark' },
  { id: 'tui-rose', mode: 'dark' },
  { id: 'tui-mono', mode: 'dark' },
  { id: 'tui-catppuccin', mode: 'dark' },
  { id: 'xbot-aurora', mode: 'dark' },
  { id: 'xbot-nebula', mode: 'dark' },
]

/** 富内容：一次性覆盖标题/强调/链接/列表/引用/代码块/表格/行内码 —— 主题的绝大多数变量。 */
const RICH = [
  '# 主题评审样例 Theme Sample',
  '',
  '正文与 **加粗**、*斜体*、`inline code`、以及 [链接](https://github.com/ai-pivot/xbot)。',
  '',
  '- 列表项一：普通文本',
  '- 列表项二：带 `code` 与 **强调**',
  '',
  '> 引用块：用于看 blockquote 边框/底色与主体文字的对比。',
  '',
  '```ts',
  'const greet = (who: string): string => `hi ${who}`',
  '// 注释颜色 / 关键字 / 字符串 / 数字 都要能在背景上读清',
  'export const N = 42',
  '```',
  '',
  '| 列 A | 列 B |',
  '| --- | --- |',
  '| 单元格 1 | 单元格 2 |',
].join('\n')

async function shoot(browser: Browser, themeId: string): Promise<void> {
  const page: Page = await browser.newPage({
    viewport: { width: 1440, height: 900 },
    deviceScaleFactor: 2,
  })

  await page.addInitScript((id: string) => {
    // ThemeProvider 挂载时从 localStorage 读取（providers/theme.tsx:42）。
    localStorage.setItem('xbot-md-theme', id)
    const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
    ;(window as unknown as { __sseListeners: typeof listeners }).__sseListeners = listeners
    class MockEventSource {
      readyState = 1
      onopen: ((ev: Event) => void) | null = null
      onerror: ((ev: Event) => void) | null = null
      constructor(public url: string) {
        setTimeout(() => this.onopen?.(new Event('open')), 0)
      }
      addEventListener(type: string, h: (ev: MessageEvent) => void) {
        ;(listeners[type] ||= new Set()).add(h)
      }
      removeEventListener(type: string, h: (ev: MessageEvent) => void) {
        listeners[type]?.delete(h)
      }
      close() {
        for (const k of Object.keys(listeners)) listeners[k].clear()
      }
    }
    ;(window as unknown as { EventSource: typeof MockEventSource }).EventSource = MockEventSource
  }, themeId)

  const now = new Date().toISOString()
  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'demo' } } }))
  await page.route('**/api/session-tree', (r) =>
    r.fulfill({
      json: {
        ok: true,
        data: {
          sessions: [
            { chat_id: 'chat-1', channel: 'web', label: '主题评审样例', last_active: now },
            { chat_id: 'chat-2', channel: 'web', label: '另一个会话', last_active: now },
          ],
          chats: [],
          orphan_subagents: [],
        },
      },
    }),
  )
  await page.route('**/api/history**', (r) =>
    r.fulfill({
      json: {
        ok: true,
        data: {
          chat_id: 'chat-1',
          last_seq: 4,
          active_progress: null,
          messages: [
            { id: 1, role: 'user', content: '帮我看一下这个主题的观感：**层次**、对比度、以及代码块可读性。', seq: 1, turn_id: 101, timestamp: now },
            { id: 2, role: 'assistant', content: RICH, seq: 2, turn_id: 101, timestamp: now },
          ],
        },
      },
    }),
  )
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/home/smith/src/xbot' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
  await page.route('**/api/queue/list', (r) => r.fulfill({ json: { ok: true, data: { items: [] } } }))

  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('demo')
  await page.locator('input[type="password"]').fill('demo')
  await page.locator('button[type="submit"]').click()
  await page.waitForFunction(() => document.body.textContent?.includes('主题评审样例'), { timeout: 20_000 })
  await page.waitForTimeout(600) // 字体/高亮/布局稳定
  await page.screenshot({ path: join(OUT, `${themeId}.png`) })
  await page.close()
}

export async function runAll(): Promise<void> {
  mkdirSync(OUT, { recursive: true })
  const browser = await chromium.launch()
  for (const t of THEMES) {
    process.stdout.write(`· ${t.id}\n`)
    await shoot(browser, t.id)
  }
  await browser.close()
  process.stdout.write(`done → ${OUT}\n`)
}

if (process.argv[1]?.includes('theme-gallery')) {
  void runAll()
}
