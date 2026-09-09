import { test, expect } from '@playwright/test'

/**
 * 回归测试：导航按钮方向正确（用户报告"上下按钮反了"——
 * 在会话最下方无法交互「上一条」但能交互「下一条」）。
 *
 * 语义：底部时「下一条」必须禁用（没有更靠后的 user 消息），
 * 「上一条」在存在更早 user 消息时可用。
 */
test('nav direction: Next disabled at bottom, Prev usable when history exists', async ({ page }) => {
  await page.goto('/')
  await page.fill('#login-username', 'e2e_tester')
  await page.fill('#login-password', 'e2e-pass-123')
  await page.click('button[type="submit"]')
  await page.waitForSelector('textarea, [contenteditable]', { timeout: 20_000 })
  await page.waitForTimeout(2500)

  // 打开一个有历史的会话
  const target = page.locator('text=install better').first()
  if (await target.count()) {
    await target.click().catch(() => undefined)
    await page.waitForTimeout(4000)
  }

  const scroller = page.locator('[data-message-list-content]').first().locator('xpath=..')
  await scroller.evaluate((el) => { el.scrollTop = el.scrollHeight })
  await page.waitForTimeout(1200)

  const next = page.locator('button[title="Next user message"]').first()
  const prev = page.locator('button[title="Previous user message"]').first()

  // 核心断言：底部时「下一条」必须禁用（旧代码恒 enabled = 方向反了）
  await expect(next).toBeDisabled()

  // 「上一条」：若历史中有更早的 user 消息则可用（列表短时两者皆禁用也正确，
  // 但绝不允许「下一条可用而 上一条禁用」的旧错误方向）
  const prevDisabled = await prev.isDisabled()
  const nextDisabled = await next.isDisabled()
  expect(nextDisabled, 'next must be disabled at bottom').toBe(true)
  if (!prevDisabled) {
    // 可用时必须真的能滚动
    const before = await scroller.evaluate((el) => el.scrollTop)
    await prev.click()
    await page.waitForTimeout(900)
    const after = await scroller.evaluate((el) => el.scrollTop)
    expect(after).not.toBe(before)
  }
})
