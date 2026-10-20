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
    // 行头：HH:mm:ss + 「请旨」徽标 + 发送者。
    await expect(card.getByTestId('row-time')).toHaveText(/^\d{2}:\d{2}:\d{2}$/)
    await expect(card.getByTestId('kind-badge')).toHaveText('请旨')
    await expect(card.getByText('web-1')).toBeVisible()
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
    // 次要文字（证据路径）≥13px，深色主题可读。
    const evidenceFont = await card
      .getByText('court/evidence/gongbu-ppu.log')
      .evaluate((el) => Number.parseFloat(getComputedStyle(el).fontSize))
    expect(evidenceFont, `证据字号 ${evidenceFont}px，应 ≥ 13px`).toBeGreaterThanOrEqual(13)
    await expect(card.getByTestId('kind-badge')).toHaveText('奏折')
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
    // 收件箱条目必须有相对时间（刚刚 / N 分钟前 / N 小时前）。
    await expect(item.getByText(/刚刚|分钟前|小时前|天前|月.*日/)).toBeVisible()
    await item.click()
    await expect(inbox.getByTestId('ask-user-card').filter({ hasText: question })).toBeVisible()
    await page.screenshot({ path: 'e2e/artifacts/cards-inbox.png' })
  })

  test('1000 条消息虚拟列表：首屏 < 2s，且行有区分度', async ({ page }) => {
    await login(page)
    const chatID = await newChat(page, 'bulk')
    // 演示数据要能肉眼区分：递增时间戳 + 序号 + kind 混合（event/report/ask_user）。
    // 只用非投递 kind（event/report/ask_user），避免 1000 条消息各起一个 agent turn。
    const total = 1000
    const base = Date.parse('2026-10-20T00:00:00Z')
    for (let i = 0; i < total; i++) {
      const kind = i % 12 === 0 ? 'report' : i % 12 === 6 ? 'ask_user' : 'event'
      const createdAt = new Date(base + i * 1000).toISOString()
      const text =
        kind === 'event'
          ? `#${i} 状态事件 · ${i % 3 === 0 ? '告警' : '心跳'}${i % 5 === 0 ? ' · 节点 b300-5 已回收' : ''}`
          : kind === 'report'
            ? `#${i} 第 ${i} 份奏折：批量状态汇总`
            : `#${i} 第 ${i} 次请旨：是否继续扩容？`
      const payload =
        kind === 'report'
          ? {
              report_id: `rep_bulk_${i}`,
              department: 'gongbu',
              summary: `#${i} 第 ${i} 份奏折：批量状态汇总`,
              evidence: [{ path: `court/evidence/bulk-${i}.log`, kind: 'log' }],
            }
          : kind === 'ask_user'
            ? {
                questions: [
                  {
                    id: 'q1',
                    question: `#${i} 第 ${i} 次请旨：是否继续扩容？`,
                    options: [{ id: 'yes', label: '准' }],
                    allow_other: true,
                  },
                ],
              }
            : { event_type: 'state.changed', severity: i % 3 === 0 ? 'warn' : 'info', index: i }
      const res = await page.request.post('/api/message', {
        data: {
          channel: 'web',
          chat_id: chatID,
          kind,
          idempotency_key: `bulk_${RUN}_${i}`,
          created_at: createdAt,
          text,
          payload,
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

    // 每行必须有 HH:mm:ss（zh-CN 24h，不能出现 AM/PM）。
    const timeText = (await page.getByTestId('row-time').last().textContent()) ?? ''
    expect(timeText, `时间戳应形如 03:04:05，实际 ${timeText}`).toMatch(/^\d{2}:\d{2}:\d{2}$/)
    expect(timeText).not.toMatch(/AM|PM/)

    // kind 混合分布：可见区里应有「事件」紧凑行 + 「奏折/请旨」卡片。
    const badgeCount = await page.getByTestId('kind-badge').count()
    expect(badgeCount).toBeGreaterThan(0)
    await expect(page.getByTestId('kind-badge').filter({ hasText: '事件' }).first()).toBeVisible()
    const cardCount =
      (await page.getByTestId('report-card').count()) +
      (await page.getByTestId('ask-user-card').count())
    expect(cardCount, '演示数据必须有非 event 的卡片').toBeGreaterThan(0)

    // 紧凑 event 行 32–36px（不再是被撑开的灰框 + 大空隙）。
    const rowHeight = await page
      .getByTestId('event-row')
      .last()
      .evaluate((el) => el.getBoundingClientRect().height)
    expect(rowHeight, `event 行高 ${rowHeight}px，应在 32–36px`).toBeGreaterThanOrEqual(32)
    expect(rowHeight, `event 行高 ${rowHeight}px，应在 32–36px`).toBeLessThanOrEqual(36)

    // 消息流列 max-width 880px 居中，卡片不横跨全宽。
    const columnWidth = await page.evaluate(() => {
      const scroller = document.querySelector('[data-testid="stream-scroll"]')
      const column = scroller?.firstElementChild as HTMLElement | null
      return column?.getBoundingClientRect().width ?? 0
    })
    expect(columnWidth, `消息列宽 ${columnWidth}px，应 ≤ 880px`).toBeGreaterThan(0)
    expect(columnWidth).toBeLessThanOrEqual(880)

    await page.screenshot({ path: 'e2e/artifacts/cards-1000.png' })
    // eslint-disable-next-line no-console
    console.log(`[perf] 1000 条消息首屏渲染 ${elapsed}ms`)
  })
})
