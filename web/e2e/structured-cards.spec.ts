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
    // 行头：HH:mm:ss + kind 徽标 + 发送者。
    // 徽标文案以 web/src/structured/StructuredCards.tsx 的 KIND_META 为准 ——
    // 81663b9f「去掉朝廷定制化文案」把 请旨→提问 / 奏折→汇报 / 批红→准·驳·改 之后，
    // 这里原先的 '请旨' 断言就成了过时口径（CI 实测 2026-10-22：Expected "请旨", Received "提问"）。
    await expect(card.getByTestId('row-time')).toHaveText(/^\d{2}:\d{2}:\d{2}$/)
    await expect(card.getByTestId('kind-badge')).toHaveText('提问')
    await expect(card.getByText('web-1')).toBeVisible()
    await page.screenshot({ path: 'e2e/artifacts/cards-ask-user.png' })

    await card.getByRole('radio').nth(2).check()
    await card.getByTestId('ask-user-submit').click()
    await expect(page.getByTestId('answer-card').filter({ hasText: 'q1:modify' }).first()).toBeVisible()
    await expect(card.getByText('已回答')).toBeVisible()
    await expect(card.getByRole('radio').first()).toBeDisabled()
  })

  test('report 卡片渲染 + 批红（决策）流程', async ({ page }) => {
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
    await expect(card.getByTestId('kind-badge')).toHaveText('汇报')
    await page.screenshot({ path: 'e2e/artifacts/cards-report.png' })

    await card.getByTestId('report-approve').click()
    // 决策卡的状态位渲染 verdict 的中文（准/驳/改），报告卡的状态位是「已决策」。
    await expect(page.getByTestId('decision-card').filter({ hasText: '准' }).last()).toBeVisible()
    await expect(card.getByText('已决策')).toBeVisible()
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

    // 2026-10-22 CI：原来逐条 await POST 1000 次，CI 上单次约 250ms ⇒ 累计 ~250s，
    // 撞上 test.setTimeout(240_000) 报「Target page, context or browser has been closed」。
    // 改为**分批并发**写入（每批 20 条 Promise.all）：数据量仍是 1000，下面的判据
    // （首屏 < 2s、stream-count ≥ 1000、时间戳格式、虚拟滚动）一条都没放宽。
    const WRITE_CONCURRENCY = 20
    type Posted = { i: number; res: import('@playwright/test').APIResponse }

    const buildOne = (i: number) => {
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
      return { kind, createdAt, text, payload }
    }

    for (let batchStart = 0; batchStart < total; batchStart += WRITE_CONCURRENCY) {
      const batch: Promise<Posted>[] = []
      const batchEnd = Math.min(batchStart + WRITE_CONCURRENCY, total)
      for (let i = batchStart; i < batchEnd; i++) {
        const { kind, createdAt, text, payload } = buildOne(i)
        batch.push(
          page.request
            .post('/api/message', {
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
            .then((res) => ({ i, res })),
        )
      }
      const results = await Promise.all(batch)
      for (const { i, res } of results) {
        expect(res.status(), `#${i} ${await res.text()}`).toBe(200)
      }
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

    // kind 混合分布：可见区里应有「事件」紧凑行 + 「汇报/提问」卡片。
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

  test('跨端同步：A 端批红/回答，B 端状态与收件箱立即跟上', async ({ browser }) => {
    // 两个独立浏览器上下文 = 两台设备（各自 cookie / SSE 连接）。
    const ctxA = await browser.newContext()
    const ctxB = await browser.newContext()
    const pageA = await ctxA.newPage()
    const pageB = await ctxB.newPage()
    try {
      await login(pageA)
      await login(pageB)
      const chatID = await newChat(pageA, 'xdev')
      const question = `跨端问题（${RUN}）`
      const summary = `跨端奏折（${RUN}）`
      await postStructured(pageA, chatID, {
        kind: 'ask_user',
        correlation_id: `cor_xdev_q_${RUN}`,
        idempotency_key: `xdq_${RUN}`,
        text: question,
        payload: {
          questions: [{ id: 'q1', question, options: [{ id: 'yes', label: '是' }] }],
          allow_cancel: true,
        },
      })
      await postStructured(pageA, chatID, {
        kind: 'report',
        correlation_id: `cor_xdev_r_${RUN}`,
        idempotency_key: `xdr_${RUN}`,
        text: summary,
        payload: {
          summary,
          state_ref: 'court/state/x.yaml',
          state_sha256: 'deadbeef',
          evidence: [{ path: 'court/evidence/x.log', kind: 'log' }],
        },
      })

      await gotoStructured(pageA, chatID)
      await gotoStructured(pageB, chatID)

      // B 端初始视图：一件待答 + 一张待决策。
      await expect(pageB.getByTestId('inbox-badge')).toHaveText('1')
      const cardB = pageB.getByTestId('report-card').filter({ hasText: summary }).first()
      await expect(cardB.getByText('待决策')).toBeVisible()

      // A 端批红 —— B 端不刷新、不重连，仅靠广播 + 消息流派生。
      const cardA = pageA.getByTestId('report-card').filter({ hasText: summary }).first()
      await cardA.getByTestId('report-approve').click()
      await expect(cardB.getByText('已决策')).toBeVisible({ timeout: 8000 })
      await expect(cardB.getByTestId('report-approve')).toHaveCount(0)

      // A 端回答 —— B 端收件箱徽标归零、问题卡变已回答。
      const askA = pageA.getByTestId('ask-user-card').filter({ hasText: question }).first()
      await askA.getByRole('radio').first().check()
      await askA.getByTestId('ask-user-submit').click()
      await expect(pageB.getByTestId('inbox-badge')).toHaveText('0', { timeout: 8000 })
      const askB = pageB.getByTestId('ask-user-card').filter({ hasText: question }).first()
      await expect(askB.getByText('已回答')).toBeVisible({ timeout: 8000 })

      await pageB.screenshot({ path: 'e2e/artifacts/cards-cross-device.png' })
    } finally {
      await ctxA.close()
      await ctxB.close()
    }
  })
})
