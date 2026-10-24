import { expect, test } from '@playwright/test'

import { answerRow, askRow, bootStructured, emitStructured } from './helpers/structured-mock'

/**
 * AskUser **跨客户端失效**回归（2026-09-16 用户报告）：
 *   「askuser 还是有时候走前端缓存，不该弹的时候弹出」
 *   「askuser 必须是会话级别的状态且后端保存，和 busy 互斥。并且多 channel 同步」
 *
 * 被钉死的契约：同一会话的多个客户端共享同一份 ask_user 状态 —— 一个客户端回答/取消后，
 * **另一个客户端无需刷新**就把那张卡片推进到终态（已回答 / 已取消），且失效是
 * per-request 的（新问题照常出现）、重复投递幂等。
 *
 * ⚠️ 2026-10-22 迁移：本 spec 原先断言"旧 AskUserPanel 立即收起"。`AskUserPanel`
 * 与其水合路径已删除（514ec7b3），交互面搬到 `/structured` 的 `AskUserCard`，
 * 失效语义由 `useStructuredMessages.deriveTargetStates`（answer/decision 行按
 * `reply_to` 投影回目标消息、只做 false→true 的单调 OR 合并）承载。所以断言从
 * "面板消失"改为"卡片进入终态"，语义等价且仍是同一批判别点：
 *
 *   ① 另一端回答 ⇒ 本端卡片变「已回答」（无刷新、无重连）
 *   ② 另一端取消 ⇒ 本端卡片变「已取消」
 *   ③ 重复投递 answer 行幂等，且**后续新问题**照常以待答态出现（失效不是永久静音）
 *
 * 判别力：把 `deriveTargetStates` 的 `cancelled`/`answered` 投影删掉 ⇒ ①② 必红
 * （卡片停在「等待处理」）。
 *
 * 真后端那条链路（A 端点击 → 服务端广播 → B 端跟上）由
 * `e2e/structured-cards.spec.ts`「跨端同步」覆盖；本文件用 mock 精确控制事件顺序与重放。
 */

test.describe('AskUser 跨客户端失效（结构化卡片）', () => {
  test('① 另一端应答 ⇒ 本端卡片立即变「已回答」（无需刷新）', async ({ browser }) => {
    const page = await bootStructured(browser, [askRow()])
    const card = page.getByTestId('ask-user-card').first()
    await expect(card).toBeVisible()
    await expect(card.getByText('等待处理')).toBeVisible()

    // 另一端回答 ⇒ 服务端把 answer 行广播到本端（本端没点过任何东西）。
    await emitStructured(page, answerRow(42))

    await expect(card.getByText('已回答')).toBeVisible({ timeout: 5_000 })
    await expect(card.getByText('该问题已关闭')).toBeVisible()
    await page.context().close()
  })

  test('② 另一端取消 ⇒ 本端卡片立即变「已取消」', async ({ browser }) => {
    const page = await bootStructured(browser, [askRow()])
    const card = page.getByTestId('ask-user-card').first()
    await expect(card.getByText('等待处理')).toBeVisible()

    await emitStructured(page, answerRow(42, { cancelled: true }))

    await expect(card.getByText('已取消')).toBeVisible({ timeout: 5_000 })
    await page.context().close()
  })

  test('③ 重复投递 answer 行幂等，且不影响后续新问题', async ({ browser }) => {
    const page = await bootStructured(browser, [askRow()])
    const card = page.getByTestId('ask-user-card').first()
    await expect(card.getByText('等待处理')).toBeVisible()

    // 同一条 answer 行重复投递（重连补齐 / 重播）不得改变终态、不得让卡片回退。
    await emitStructured(page, answerRow(42))
    await emitStructured(page, answerRow(42))
    await expect(card.getByText('已回答')).toBeVisible({ timeout: 5_000 })

    // 新的问题（新 message_id）仍必须以待答态弹出 —— 失效是 per-message，不是"永久静音"。
    const fresh = askRow({
      message_id: 100,
      correlation_id: 'req-2',
      payload: {
        questions: [{ id: 'q1', question: 'A brand new question?', options: [{ id: 'yes', label: 'yes' }] }],
      },
    })
    await emitStructured(page, fresh)
    await expect(
      page.getByTestId('ask-user-card').filter({ hasText: 'A brand new question?' }),
    ).toBeVisible({ timeout: 5_000 })
    await page.context().close()
  })
})
