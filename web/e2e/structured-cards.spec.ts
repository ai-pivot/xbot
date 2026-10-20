/**
 * 结构化消息 + 卡片交互 E2E（真实后端 /api/message /api/answer /api/inbox）。
 *
 * 前置：E2E_BACKEND_URL 指向本 worktree 的后端（默认 8082）。
 * 每个用例创建独立 chat（避免历史数据串味），通过
 * `/structured?channel=web&chat_id=<id>` 深链指定会话。
 * 产物：e2e/artifacts/cards-*.png（ask_user / report / inbox / 1000 条）。
 */
import { expect, test, type Page } from '@playwright/test'

const RUN = Date.now().toString(36)

async function login(page: Page) {
  const res = await page.request.post('/api/auth/login', {
    data: { username: 'admin', password: 'admin' },
  })
  if (!res.ok()) {
    await page.request.post('/api/auth/register', {
      data: { username: 'admin', password: 'admin' },
    })
    const retry = await page.request.post('/api/auth/login', {
      data: { username: 'admin', password: 'admin' },
    })
    expect(retry.ok()).toBeTruthy()
  }
}

async function newChat(page: Page, suffix: string): Promise<string> {
  const res = await page.request.post('/api/chats/create', {
    data: { label: `e2e-${suffix}-${RUN}` },
  })
  expect(res.status(), await res.text()).toBe(200)
  const body = (await res.json()) as { data: { chat_id: string } }
  expect(body.data.chat_id).toBeTruthy()
  return body.data.chat_id
}

async function postStructured(page: Page, chatID: string, body: Record<string, unknown>) {
  const res = await page.request.post('/api/message', {
    data: { channel: 'web', chat_id: chatID, ...body },
    timeout: 30_000,
  })
  expect(res.status(), await res.text()).toBe(200)
  return (await res.json()) as { data: { message_id: number } }
}

async function gotoStructured(page: Page, chatID: string) {
  await page.goto(`/structured?channel=web&chat_id=${encodeURIComponent(chatID)}`)
  await expect(page.getByTestId('structured-workspace')).toBeVisible()
}

test.describe.configure({ mode: 'serial' })

test.describe('结构化消息卡片', () => {
  test.use({ viewport: { width: 1500, height: 940 } })
  test.setTimeout(240_000)

  test('ask_user 卡片渲染 + 回答流程', async ({ page }) => {
    await login(page)
    const chatID = await newChat(page, 'ask')
    const question = `是否按计划今夜扩容？（${RUN}）`
    const corr = `cor_ask_${RUN}`
    await postStructured(page, chatID, {
      kind: 'ask_user',
      correlation_id: corr,
      idempotency_key: `q_${RUN}`,
      text: `户部请旨：${question}`,
      payload: {
        questions: [
          {
            id: 'q1',
            question,
            options: [
              { id: 'approve', label: '准', description: '按 8 节点扩容' },
              { id: 'reject', label: '驳', description: '暂缓' },
              { id: 'modify', label: '改', description: '先扩 4 节点' },
            ],
            allow_other: true,
          },
        ],
        allow_cancel: true,
      },
    })

    await gotoStructured(page, chatID)
    const card = page.getByTestId('ask-user-card').filter({ hasText: question }).first()
    await expect(card).toBeVisible()
    await page.screenshot({ path: 'e2e/artifacts/cards-ask-user.png' })

    await card.getByRole('radio').nth(2).check()
    await card.getByTestId('ask-user-submit').click()
    await expect(page.getByTestId('answer-card').filter({ hasText: 'q1:modify' }).first()).toBeVisible()
    await expect(card.getByText('已回答')).toBeVisible()
    await expect(card.getByRole('radio').first()).toBeDisabled()
  })

  test('report 卡片渲染 + 批红流程', async ({ page }) => {
    await login(page)
    const chatID = await newChat(page, 'report')
    const summary = `PPU 扩容已完成 8 节点（${RUN}）`
    const corr = `cor_report_${RUN}`
    await postStructured(page, chatID, {
      kind: 'report',
      correlation_id: corr,
      idempotency_key: `r_${RUN}`,
      text: summary,
      payload: {
        report_id: `rep_${RUN}`,
        department: 'gongbu',
        summary,
        state_ref: 'court/state/departments/gongbu.yaml',
        state_sha256: 'deadbeef',
        evidence: [
          { path: 'court/evidence/gongbu-ppu.log', kind: 'log' },
          { path: 'court/reports/2026-10-19/gongbu.yaml', kind: 'file' },
        ],
      },
    })

    await gotoStructured(page, chatID)
    const card = page.getByTestId('report-card').filter({ hasText: summary }).first()
    await expect(card).toBeVisible()
    await expect(card.getByText('court/evidence/gongbu-ppu.log')).toBeVisible()
    await page.screenshot({ path: 'e2e/artifacts/cards-report.png' })

    await card.getByTestId('report-approve').click()
    await expect(page.getByTestId('decision-card').filter({ hasText: '批红' }).last()).toBeVisible()
    await expect(card.getByText('已批红')).toBeVisible()
    await expect(card.getByTestId('report-approve')).toHaveCount(0)
  })

  test('收件箱：未读徽标 + 展开回答', async ({ page }) => {
    await login(page)
    const chatID = await newChat(page, 'inbox')
    const question = `收件箱待答问题（${RUN}）`
    await postStructured(page, chatID, {
      kind: 'ask_user',
      correlation_id: `cor_inbox_${RUN}`,
      idempotency_key: `i_${RUN}`,
      text: question,
      payload: {
        questions: [{ id: 'q1', question, options: [{ id: 'yes', label: '是' }], allow_other: true }],
      },
    })

    await gotoStructured(page, chatID)
    const inbox = page.getByTestId('inbox')
    await expect(inbox).toBeVisible()
    await expect(page.getByTestId('inbox-badge')).toHaveText('1')
    const item = inbox.getByTestId('inbox-item').filter({ hasText: question }).first()
    await expect(item).toBeVisible()
    await item.click()
    await expect(inbox.getByTestId('ask-user-card').filter({ hasText: question })).toBeVisible()
    await page.screenshot({ path: 'e2e/artifacts/cards-inbox.png' })
  })

  test('1000 条消息虚拟列表：首屏 < 2s', async ({ page }) => {
    await login(page)
    const chatID = await newChat(page, 'bulk')
    // 用 kind=event（不触发 turn）批量灌入；idempotency_key 保证可重复执行。
    const base = 1000
    for (let i = 0; i < base; i++) {
      const res = await page.request.post('/api/message', {
        data: {
          channel: 'web',
          chat_id: chatID,
          kind: 'event',
          idempotency_key: `bulk_${RUN}_${i}`,
          text: `事件 ${RUN} #${i}`,
          payload: { event_type: 'state.changed', severity: 'info', index: i },
        },
        timeout: 30_000,
      })
      expect(res.status(), await res.text()).toBe(200)
    }

    const started = Date.now()
    await page.goto(`/structured?channel=web&chat_id=${encodeURIComponent(chatID)}`)
    await expect(page.getByTestId('structured-workspace')).toBeVisible()
    await expect
      .poll(async () => {
        const text = (await page.getByTestId('stream-count').textContent()) ?? ''
        return Number.parseInt(text, 10) || 0
      })
      .toBeGreaterThanOrEqual(1000)
    const elapsed = Date.now() - started
    expect(elapsed, `首屏渲染 ${elapsed}ms，应 < 2000ms`).toBeLessThan(2000)
    await expect(page.getByText('虚拟滚动')).toBeVisible()
    await page.screenshot({ path: 'e2e/artifacts/cards-1000.png' })
    // eslint-disable-next-line no-console
    console.log(`[perf] 1000 条消息首屏渲染 ${elapsed}ms`)
  })
})
