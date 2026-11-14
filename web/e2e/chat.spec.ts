import { test, expect } from '@playwright/test'

const username = process.env.E2E_USERNAME || 'admin'
const password = process.env.E2E_PASSWORD || 'admin'

/**
 * 真实后端下「输入框可用」不是页面打开的必然结果：
 *   · 页面会自动恢复上一次的会话，而 MessageInput 在 sessionLoading /
 *     switchSplash 期间是**不渲染**的（见 AgentPanel 的渲染条件）；
 *   · 会话上下文（chatID）没好之前发送，user_echo 无路可走，用户消息永远不渲染。
 * 所以每个用例开始前都显式落到一个「新会话 + 会话树已加载 + 编辑器可见」的确定状态，
 * 而不是靠「点进去大概就好了」——后者在并行/慢机器上就是 flaky 的来源。
 */
test.beforeEach(async ({ page }) => {
  await page.goto('/')

  const userInput = page.locator('#login-username')
  // 只认 tiptap 编辑器本体：全仓只有 MessageInput 用 useEditor，`.tiptap` 唯一。
  // 不能用 `[contenteditable="true"]` 泛匹配 —— 页面别处（隐藏面板）也可能有
  // contenteditable，DOM 序在前的那个不可见，`isVisible()` 就会误判成"编辑器没就绪"。
  const editor = page.locator('.tiptap')

  // 等「登录表单」或「编辑器」之一出现。这里**不能** catch 掉超时：吞掉之后
  // `userInput.isVisible()` 会判成 false（页面还在白屏），于是静默跳过登录，
  // 用例体随后在没有任何输入框的页面上等到 60s 超时 —— 历史 flaky 就是这么来的。
  await expect(userInput.or(editor).first()).toBeVisible({ timeout: 30_000 })
  if (await userInput.isVisible().catch(() => false)) {
    await page.fill('#login-username', username)
    await page.fill('#login-password', password)
    await page.click('button[type="submit"], button:has-text("登录"), button:has-text("Login")')
  }

  // 显式开一个新会话：历史环境里页面会恢复某个旧会话（甚至卡在切换态），
  // 新会话把「聊天上下文已绑定」变成确定前提。
  const newSession = page.getByRole('button', { name: /New Session|新建会话|新建/ }).first()
  if (await newSession.isVisible().catch(() => false)) {
    await newSession.click()
  }

  // 会话树加载完（Loading 指示器消失）才有稳定的 chatID 绑定。
  await expect(page.locator('text=Loading...')).toHaveCount(0, { timeout: 30_000 })
  // 交棒给用例体之前，编辑器必须已经就绪（新登录 / 已是登录态两条路径都过这里）。
  await expect(editor).toBeVisible({ timeout: 30_000 })
})

test.describe('Chat interaction', () => {
  test('should have visible editor/input area', async ({ page }) => {
    await expect(page.locator('.tiptap')).toBeVisible({ timeout: 10_000 })
  })

  test('should have a send button', async ({ page }) => {
    const sendBtn = page.locator('button[aria-label="发送"], button[aria-label="Send"], button.send-btn')
    // At least one send mechanism should exist
    await expect(sendBtn.or(page.locator('button:has-text("发送")'))).toBeVisible({ timeout: 10_000 })
  })

  test('should show user message after sending', async ({ page }) => {
    const composer = page.locator('.tiptap')
    await expect(composer).toBeVisible({ timeout: 30_000 })
    await composer.click()
    await page.keyboard.type('hello test')

    // Click send（输入到 React 状态生效之间可能有几帧延迟，先等按钮可用）。
    // 编辑器的可见性由 beforeEach 负责（显式 .tiptap + 30s 等待）。这里不再探测
    // textarea：composer 是 contenteditable，textarea 从未存在，旧的回退分支只会
    // 在 CI 负载下变成一次看不懂的 60s 超时。
    const sendBtn = page
      .locator('button[aria-label="发送"], button[aria-label="Send"], button.send-btn, button:has-text("发送")')
      .first()
    await expect(sendBtn).toBeEnabled({ timeout: 10_000 })
    await sendBtn.click()

    // Check for user message bubble (the newly sent one with our text)
    await expect(page.locator('[data-role="user"]', { hasText: 'hello test' }).first()).toBeVisible({ timeout: 15_000 })
  })
})
