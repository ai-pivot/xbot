import { test, expect, type Page } from '@playwright/test'

function annotationPayload(content: string) {
  const start = content.indexOf('<response-annotations>')
  const end = content.indexOf('</response-annotations>', start)
  expect(start).toBeGreaterThanOrEqual(0)
  return JSON.parse(content.slice(start + '<response-annotations>'.length, end))
}

const recording = process.env.XBOT_RECORD_ANNOTATIONS === '1'
test.use({ video: recording ? { mode: 'on', size: { width: 1120, height: 760 } } : 'off' })

async function setup(page: Page, busy = false) {
  await page.addInitScript(() => {
    localStorage.setItem('xbot-locale', 'zh-CN')
    const sources: EventTarget[] = []
    ;(window as unknown as { __annotationSources: EventTarget[] }).__annotationSources = sources
    // Keep this mocked session connected; an empty fulfilled SSE response disconnects immediately.
    class MockEventSource extends EventTarget {
      readyState = 1
      onopen: ((event: Event) => void) | null = null
      onerror: ((event: Event) => void) | null = null
      constructor(public url: string) {
        super()
        sources.push(this)
        setTimeout(() => { if (this.readyState === 1) this.onopen?.(new Event('open')) }, 0)
      }
      close() { this.readyState = 2 }
    }
    ;(window as unknown as { EventSource: typeof MockEventSource }).EventSource = MockEventSource
  })
  // Unspecified API requests must never reach the user's running backend.
  await page.route('**/api/**', (r) => r.fulfill({ status: 404, json: { ok: false, error: { code: 'unmocked', message: 'Not included in this fixture' } } }))
  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/session-tree', (r) => r.fulfill({ json: { ok: true, data: {
    sessions: [{ chat_id: 'annotation-test', channel: 'web', label: '批注验收', is_current: true, running: busy, status: busy ? 'running' : 'idle' }], chats: [], orphan_subagents: [],
  } } }))
  await page.route('**/api/history', (r) => r.fulfill({ json: { ok: true, data: {
    chat_id: r.request().postDataJSON().chat_id, last_seq: 2, active_progress: null,
    messages: [
      { id: 1, role: 'user', content: '待优化事项', turn_id: 7 },
      { id: 2, role: 'assistant', content: '', turn_id: 7, iterations: [
        { iteration: 1, reasoning: '不应引用的思考', content: '注释、评论功能的设计稿尚未交付。', tools: [{ name: 'Shell', status: 'done', detail: '不应引用的工具' }] },
        { iteration: 2, content: '第二段独立正文' },
      ] },
    ],
  } } }))
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp', running: busy } } }))
  await page.route('**/api/chats/*/switch', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/queue/list', (r) => r.fulfill({ json: { ok: true, data: { items: [] } } }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
}

async function openDesktopSession(page: Page) {
  await page.goto('/')
  await page.getByRole('button', { name: /批注验收/ }).click()
  await expect(page.locator('[data-agent-visible="1"]')).toHaveAttribute('data-agent-chat-id', 'annotation-test')
  await expect(page.getByText('第二段独立正文', { exact: true })).toBeVisible()
}

async function emitProgress(page: Page, seq: number, type: string, progress: Record<string, unknown>) {
  await page.evaluate(({ type, progress, seq }) => {
    const sources = (window as unknown as { __annotationSources: (EventTarget & { readyState: number })[] }).__annotationSources
    for (const source of sources) {
      if (source.readyState === 1) source.dispatchEvent(new MessageEvent(type, {
        data: JSON.stringify({ type, chat_id: 'annotation-test', seq, progress }),
      }))
    }
  }, { type, progress, seq })
}

async function add(page: Page, comment = '我们来把这个推进一下') {
  await selectReply(page)
  await page.getByTestId('annotation-selection-action').click()
  await page.getByRole('textbox', { name: '用户评论' }).fill(comment)
  await page.getByRole('button', { name: '确认批注', exact: true }).click()
  await expect(page.getByTestId('annotation-chip')).toContainText('1 条批注')
}

async function selectReply(page: Page, length?: number) {
  const body = page.getByText('注释、评论功能的设计稿尚未交付。', { exact: true })
  // Native selection blurs the composer before changing the document range.
  await body.click()
  await body.evaluate((el, end) => {
    const range = document.createRange()
    range.setStart(el.firstChild!, 0)
    range.setEnd(el.firstChild!, end ?? el.firstChild!.textContent!.length)
    const selection = document.getSelection()!
    selection.removeAllRanges()
    selection.addRange(range)
    document.dispatchEvent(new Event('selectionchange'))
  }, length)
  await expect(page.getByTestId('annotation-selection-action')).toBeVisible()
}

test('desktop: dragging selects text and shows a comment action without right-click', async ({ page }) => {
  await setup(page)
  await openDesktopSession(page)
  await expect(page.locator('.animate-spin')).toHaveCount(0)
  const body = page.getByText('注释、评论功能的设计稿尚未交付。', { exact: true })
  const points = await body.evaluate((el) => {
    const pointAt = (offset: number) => {
      const range = document.createRange()
      range.setStart(el.firstChild!, offset)
      range.collapse(true)
      const rect = range.getBoundingClientRect()
      return { x: rect.x, y: rect.y + rect.height / 2 }
    }
    return { start: pointAt(0), end: pointAt(7) }
  })
  await page.mouse.move(points.start.x, points.start.y)
  await page.mouse.down()
  await page.mouse.move(points.end.x, points.end.y, { steps: 10 })
  await expect(page.getByTestId('annotation-selection-action')).toHaveCount(0)
  await page.mouse.up()
  const action = page.getByTestId('annotation-selection-action')
  await expect(action).toBeVisible()
  const box = await action.boundingBox()
  expect(box!.x).toBeGreaterThanOrEqual(0)
  expect(box!.y).toBeGreaterThanOrEqual(0)
  expect(box!.x + box!.width).toBeLessThanOrEqual(1280)
  await page.screenshot({ path: '/tmp/xbot-selection-comment-desktop.png' })
  await expect(page.getByTestId('copy-menu')).toHaveCount(0)
  await action.click()
  await expect(page.getByRole('textbox', { name: '所选文本' })).toHaveValue('注释、评论功能')
  await expect(page.getByRole('textbox', { name: '用户评论' })).toBeFocused()
  await page.keyboard.press('Escape')
  await expect(action).toHaveCount(0)
  await body.click({ button: 'right' })
  await expect(page.getByTestId('copy-menu')).toContainText('复制该迭代正文')
  await expect(page.getByTestId('copy-menu')).not.toContainText('批注')
})

test.describe('comment keyboard flow', () => {
  test.use({ viewport: { width: 1120, height: 760 } })

  test('desktop: Enter confirms locally, Shift+Enter inserts a newline, and chrome stays minimal', async ({ page }) => {
    await setup(page)
    const sent: Record<string, unknown>[] = []
    await page.route('**/api/message', (r) => {
      sent.push(r.request().postDataJSON())
      return r.fulfill({ json: { ok: true, data: { turn_id: 8, queued: false } } })
    })
    await openDesktopSession(page)
    const pause = async () => { if (recording) await page.waitForTimeout(1200) }
    const composer = page.locator('.tiptap[contenteditable="true"]')
    await composer.fill('我们先把评论功能完善好。')
    await pause()
    const body = page.getByText('注释、评论功能的设计稿尚未交付。', { exact: true })
    const points = await body.evaluate((el) => {
      const range = document.createRange()
      range.selectNodeContents(el)
      const rect = range.getBoundingClientRect()
      return { x1: rect.left + 1, x2: rect.right - 1, y: rect.top + rect.height / 2 }
    })
    await page.mouse.move(points.x1, points.y)
    await page.mouse.down()
    await page.mouse.move(points.x2, points.y, { steps: 20 })
    await page.mouse.up()
    await expect(page.getByTestId('annotation-selection-action')).toBeVisible()
    await pause()
    await page.getByTestId('annotation-selection-action').click()
    const overlay = page.getByTestId('annotation-editor')
    await expect(page.getByRole('textbox', { name: '所选文本' })).toHaveValue('注释、评论功能的设计稿尚未交付。')
    await expect(overlay).not.toContainText(/第\s*7\s*轮|第\s*1\s*次迭代|\/6000|\/2000/)
    const comment = page.getByRole('textbox', { name: '用户评论' })
    await comment.press('Enter')
    await expect(overlay).toBeVisible()
    await expect(overlay.getByRole('alert')).toHaveCount(0)
    await comment.fill('先支持选文评论')
    await comment.dispatchEvent('keydown', { key: 'Enter', isComposing: true })
    await comment.dispatchEvent('keydown', { key: 'Enter', keyCode: 229 })
    await expect(overlay).toBeVisible()
    await comment.press('End')
    await comment.press('Shift+Enter')
    await comment.pressSequentially('输入框里已有的正文要保留。', { delay: recording ? 90 : 0 })
    await expect(comment).toHaveValue('先支持选文评论\n输入框里已有的正文要保留。')
    await pause()
    await page.screenshot({ path: '/tmp/xbot-comment-enter-desktop.png' })
    await comment.press('Enter')
    await expect(overlay).toHaveCount(0)
    await expect(page.getByTestId('annotation-chip')).toContainText('1 条批注')
    await expect(composer).toHaveText('我们先把评论功能完善好。')
    await expect(composer).toBeFocused()
    expect(sent).toHaveLength(0)
    await pause()
    await page.getByTestId('annotation-chip').click()
    const list = page.getByTestId('annotation-list')
    await expect(list).toContainText('先支持选文评论')
    await expect(list).not.toContainText(/第\s*7\s*轮|第\s*1\s*次迭代|\/6000|\/2000/)
    await pause()
    await page.getByRole('button', { name: '编辑批注' }).click()
    await comment.fill('请先实现选文评论，保留现有输入内容。')
    await pause()
    await comment.press('Enter')
    await expect(page.getByTestId('annotation-chip')).toContainText('1 条批注')
    expect(sent).toHaveLength(0)
    await pause()
    await page.getByRole('button', { name: '发送', exact: true }).click()
    await expect(page.getByTestId('annotation-chip')).toHaveCount(0)
    await expect(composer).toBeEmpty()
    expect(sent).toHaveLength(1)
    expect(sent[0].content).toContain('我们先把评论功能完善好。')
    expect(sent[0].content).toContain('请先实现选文评论，保留现有输入内容。')
    expect(sent[0].content).not.toMatch(/第\s*7\s*轮|第\s*1\s*次迭代/)
    expect(annotationPayload(sent[0].content as string)).toEqual([{
      text: '注释、评论功能的设计稿尚未交付。',
      annotation: '请先实现选文评论，保留现有输入内容。',
      source: { turnId: 7, iteration: 1, startOffset: 0, endOffset: '注释、评论功能的设计稿尚未交付。'.length, offsetBasis: 'dom-text', offsetEncoding: 'utf-16' },
    }])
    const bubble = page.getByTestId('user-bubble').filter({ hasText: '请先实现选文评论' })
    await expect(bubble).toContainText('注释、评论功能的设计稿尚未交付。')
    await expect(bubble).not.toContainText(/response-annotations|turnId|startOffset|Response annotations|My request/)
    await pause()
  })
})

test('desktop: preserve composer, preview/edit/delete, refresh and send once', async ({ page }) => {
  await setup(page)
  const sent: Record<string, unknown>[] = []
  await page.route('**/api/message', async (r) => {
    sent.push(r.request().postDataJSON())
    await r.fulfill({ json: { ok: true, data: { queued: true, message_id: 0 } } })
  })
  await openDesktopSession(page)
  const editor = page.locator('.tiptap[contenteditable="true"]')
  await editor.fill('已有正文')
  await add(page)
  await expect(editor).toHaveText('已有正文')
  await expect(editor).toBeFocused()
  expect(sent).toHaveLength(0)
  await page.getByTestId('annotation-chip').click()
  await expect(page.getByTestId('annotation-list')).toContainText('我们来把这个推进一下')
  await expect(page.getByTestId('annotation-list')).not.toContainText('不应引用')
  await page.getByRole('button', { name: '编辑批注' }).click()
  await page.getByRole('textbox', { name: '用户评论' }).fill('请先完成评论功能')
  await page.getByRole('button', { name: '确认批注', exact: true }).click()
  await page.reload()
  await expect(page.getByTestId('annotation-chip')).toContainText('1 条批注')
  await expect(editor).toHaveText('已有正文')
  await page.getByTestId('annotation-chip').click()
  await expect(page.getByTestId('annotation-list')).toContainText('请先完成评论功能')
  await expect(page.getByText('第二段独立正文', { exact: true })).toBeVisible()
  await expect(page.getByTestId('session-loading-screen')).toHaveCount(0)
  await expect(page.locator('.animate-spin')).toHaveCount(0)
  await page.screenshot({ path: '/tmp/xbot-annotations-desktop-verified.png' })
  await page.keyboard.press('Escape')
  await page.getByRole('button', { name: '发送', exact: true }).click()
  await expect(page.getByTestId('annotation-chip')).toHaveCount(0)
  expect(sent).toHaveLength(1)
  expect(sent[0].content).toContain('已有正文')
  expect(sent[0].content).toContain('请先完成评论功能')
  expect(annotationPayload(sent[0].content as string)[0].source).toMatchObject({ turnId: 7, iteration: 1, startOffset: 0, endOffset: '注释、评论功能的设计稿尚未交付。'.length })
  expect(sent[0].interrupt).not.toBe(true)
  await expect(editor).toBeEmpty()
})

test('desktop: locates a repeated Markdown excerpt and keeps XML feedback readable after history reload', async ({ page }) => {
  await setup(page)
  let submitted = ''
  const messages = () => [
    { id: 1, role: 'user', content: '待优化事项', turn_id: 7 },
    { id: 2, role: 'assistant', content: '', turn_id: 7, iterations: [
      { iteration: 3, content: '**重复** 前文 🙂 *重复* 尾文' },
      { iteration: 4, content: '第二段独立正文' },
    ] },
    ...(submitted ? [{ id: 3, role: 'user', content: submitted, turn_id: 8 }] : []),
  ]
  await page.route('**/api/history', (r) => r.fulfill({ json: { ok: true, data: { chat_id: 'annotation-test', last_seq: submitted ? 3 : 2, active_progress: null, messages: messages() } } }))
  await page.route('**/api/message', (r) => {
    submitted = r.request().postDataJSON().content
    return r.fulfill({ json: { ok: true, data: { turn_id: 8, queued: false } } })
  })
  await openDesktopSession(page)
  const body = page.locator('[data-annotation-body]').filter({ hasText: '重复 前文' })
  await body.click()
  await body.evaluate((el) => {
    const node = el.querySelector('em')!.firstChild!
    const selection = document.getSelection()!
    selection.setBaseAndExtent(node, 2, node, 0)
    document.dispatchEvent(new Event('selectionchange'))
  })
  await page.getByTestId('annotation-selection-action').click()
  await expect(page.getByRole('textbox', { name: '所选文本' })).toHaveValue('重复')
  await page.getByRole('textbox', { name: '用户评论' }).fill('这里只改第二次出现的内容。')
  await page.getByRole('textbox', { name: '用户评论' }).press('Enter')
  const composer = page.locator('.tiptap[contenteditable="true"]')
  await composer.fill('保留正文。')
  await page.getByRole('button', { name: '发送', exact: true }).click()
  expect(annotationPayload(submitted)).toEqual([{
    text: '重复', annotation: '这里只改第二次出现的内容。',
    source: { turnId: 7, iteration: 3, startOffset: 9, endOffset: 11, offsetBasis: 'dom-text', offsetEncoding: 'utf-16' },
  }])
  await page.reload()
  await expect(page.locator('[data-agent-visible="1"]')).toHaveAttribute('data-agent-chat-id', 'annotation-test')
  const bubble = page.getByTestId('user-bubble').filter({ hasText: '这里只改第二次出现的内容。' })
  await expect(bubble).toBeVisible()
  await expect(bubble).toContainText('保留正文。')
  await expect(bubble).toContainText('重复')
  await expect(bubble).not.toContainText(/response-annotations|turnId|startOffset|Response annotations|My request/)
  await expect(page.getByTestId('session-loading-screen')).toHaveCount(0)
  await expect(page.locator('.animate-spin')).toHaveCount(0)
  await page.screenshot({ path: '/tmp/xbot-annotations-xml-history.png' })
})

test('busy annotation-only send queues; rejected send retains the draft', async ({ page }) => {
  await setup(page, true)
  const attempts: string[] = []
  let reject = true
  await page.route('**/api/message', (r) => {
    attempts.push(r.request().postDataJSON().id)
    expect(r.request().postDataJSON().interrupt).not.toBe(true)
    return reject
      ? r.fulfill({ status: 400, json: { ok: false, error: '测试拒绝' } })
      : r.fulfill({ json: { ok: true, data: { queued: true, message_id: 0 } } })
  })
  await openDesktopSession(page)
  await add(page)
  await page.getByRole('button', { name: '排队发送/', exact: true }).click()
  await expect(page.getByTestId('annotation-chip')).toContainText('1 条批注')
  await expect(page.getByRole('button', { name: '排队发送/', exact: true })).toBeEnabled()
  // The existing transport retries rejected requests; all attempts must be one logical send.
  expect(attempts.length).toBeGreaterThan(0)
  expect(new Set(attempts).size).toBe(1)
  reject = false
  await page.getByRole('button', { name: '排队发送/', exact: true }).click()
  await expect(page.getByTestId('annotation-chip')).toHaveCount(0)
  expect(new Set(attempts).size).toBe(1)
})

for (const { selectionTarget, mobile } of [
  { selectionTarget: 'completed', mobile: false },
  { selectionTarget: 'live', mobile: false },
  { selectionTarget: 'live', mobile: true },
] as const) {
  test.describe(`${mobile ? 'mobile' : 'desktop'} ${selectionTarget} selection`, () => {
    test.use({ viewport: mobile ? { width: 390, height: 844 } : { width: 1280, height: 720 }, isMobile: mobile, hasTouch: mobile })
    test('live output continues through comment editing and queued submission', async ({ page }) => {
      await setup(page, true)
      const cancelled: string[] = []
      const sent: Record<string, unknown>[] = []
      const events: string[] = []
      page.on('pageerror', (e) => events.push(String(e)))
      page.on('request', (r) => {
        if (new URL(r.url()).pathname === '/api/cancel') cancelled.push(r.url())
        if (['/api/history', '/api/session/status', '/api/rpc'].includes(new URL(r.url()).pathname)) events.push(`${new URL(r.url()).pathname}: ${r.postData()}`)
      })
      await page.route('**/api/message', (r) => {
        sent.push(r.request().postDataJSON())
        return r.fulfill({ json: { ok: true, data: { queued: true, message_id: 0 } } })
      })
      if (mobile) {
        await page.goto('/')
        await expect(page.getByText('第二段独立正文', { exact: true })).toBeVisible()
      } else await openDesktopSession(page)
      let seq = 2
      const emit = (type: string, progress: Record<string, unknown>) => emitProgress(page, ++seq, type, progress)
      await emit('progress_structured', { phase: 'turn_started', turn_id: 8, seq: 1, turn_start: { trigger: 'user', content: '继续输出' } })
      let content = ''
      const advance = async (part: string) => {
        content += part
        await emit('stream_content', { turn_id: 8, iteration: 1, stream_content: content })
        try {
          await expect(page.getByText(content, { exact: true })).toBeVisible()
        } catch (error) {
          console.log('annotation stream evidence', events, await page.evaluate(() => ({
            diag: (window as unknown as { __xbotChatDiag?: { counts(): unknown; dump(): unknown } }).__xbotChatDiag?.dump(),
            text: document.querySelector('[data-agent-visible="1"]')?.textContent,
            sources: (window as unknown as { __annotationSources: { readyState?: number }[] }).__annotationSources.map(s => s.readyState),
          })))
          throw error
        }
      }
      await advance('持续输出第一段。')
      if (selectionTarget === 'completed') {
        await selectReply(page, 7)
      } else {
        const body = page.getByText(content, { exact: true })
        if (mobile) {
          await expect(body).toHaveCSS('user-select', 'text')
          await body.click()
          await body.evaluate((el) => {
            const range = document.createRange()
            range.setStart(el.firstChild!, 0)
            range.setEnd(el.firstChild!, 4)
            const selection = document.getSelection()!
            selection.removeAllRanges()
            selection.addRange(range)
            document.dispatchEvent(new Event('selectionchange'))
          })
        } else {
          const points = await body.evaluate((el) => {
            const pointAt = (offset: number) => {
              const range = document.createRange()
              range.setStart(el.firstChild!, offset)
              range.collapse(true)
              const rect = range.getBoundingClientRect()
              return { x: rect.x, y: rect.y + rect.height / 2 }
            }
            return { start: pointAt(0), end: pointAt(4) }
          })
          await page.mouse.move(points.start.x, points.start.y)
          await page.mouse.down()
          await page.mouse.move(points.end.x, points.end.y, { steps: 8 })
          await page.mouse.up()
        }
        await expect(page.getByTestId('annotation-selection-action')).toBeVisible()
      }
      await advance('选文后仍在输出。')
      await page.getByTestId('annotation-selection-action').click()
      await expect(page.getByRole('textbox', { name: '所选文本' })).toHaveValue(selectionTarget === 'live' ? '持续输出' : '注释、评论功能')
      await page.getByRole('textbox', { name: '用户评论' }).fill('不要打断当前输出')
      await advance('评论编辑中仍在输出。')
      if (selectionTarget === 'live') await page.screenshot({ path: `/tmp/xbot-live-comment-${mobile ? 'mobile' : 'desktop'}.png` })
      await page.getByRole('button', { name: '确认批注', exact: true }).click()
      await expect(page.getByTestId('annotation-chip')).toBeVisible()
      await advance('确认后仍在输出。')
      expect(sent).toHaveLength(0)
      await page.getByTestId('annotation-chip').click()
      await expect(page.getByTestId('annotation-list')).toBeVisible()
      await advance('预览中仍在输出。')
      await page.keyboard.press('Escape')
      await page.getByRole('button', { name: '排队发送/', exact: true }).click()
      await expect(page.getByTestId('annotation-chip')).toHaveCount(0)
      await advance('排队发送后仍在输出。')
      expect(sent).toHaveLength(1)
      expect(sent[0].interrupt).not.toBe(true)
      expect(annotationPayload(sent[0].content as string)[0].source).toMatchObject({ turnId: selectionTarget === 'live' ? 8 : 7, iteration: 1, startOffset: 0, endOffset: selectionTarget === 'live' ? 4 : 7 })
      expect(cancelled).toEqual([])
    })
  })
}

test.describe('long live reply', () => {
  test.use({ viewport: { width: 1280, height: 960 } })

  test('selecting 109 through 124 preserves the comment while counting continues to 1000', async ({ page }) => {
    await setup(page, true)
    const cancelled: string[] = []
    page.on('request', (request) => {
      if (new URL(request.url()).pathname === '/api/cancel') cancelled.push(request.url())
    })
    await openDesktopSession(page)
    let seq = 2
    await emitProgress(page, ++seq, 'progress_structured', { phase: 'turn_started', turn_id: 8, seq: 1, turn_start: { trigger: 'user', content: '数下1到1000' } })
    const body = page.locator('[data-annotation-live]')
    const advance = async (last: number) => {
      const numbers = Array.from({ length: last }, (_, index) => String(index + 1))
      const lines = Array.from({ length: Math.ceil(last / 20) }, (_, index) => numbers.slice(index * 20, (index + 1) * 20).join(' '))
      await emitProgress(page, ++seq, 'stream_content', { turn_id: 8, iteration: 1, stream_content: lines.join('\n\n') })
      await expect(body.locator('p').last()).toHaveText(lines.at(-1)!)
    }
    await advance(153)
    const points = await body.evaluate((el) => {
      const startNode = el.querySelectorAll('p')[5].firstChild!
      const endNode = el.querySelectorAll('p')[6].firstChild!
      const startOffset = startNode.textContent!.indexOf('109')
      const endOffset = endNode.textContent!.indexOf('124') + 3
      const pointAt = (node: Node, offset: number) => {
        const range = document.createRange()
        range.setStart(node, offset)
        range.collapse(true)
        const rect = range.getBoundingClientRect()
        return { x: rect.x, y: rect.y + rect.height / 2 }
      }
      return { start: pointAt(startNode, startOffset), end: pointAt(endNode, endOffset) }
    })
    await page.mouse.move(points.start.x, points.start.y)
    await page.mouse.down()
    await page.mouse.move(points.end.x, points.end.y, { steps: 10 })
    await page.mouse.up()
    const quote = Array.from({ length: 16 }, (_, index) => String(index + 109)).join(' ')
    expect((await page.evaluate(() => document.getSelection()!.toString())).replace(/\s+/g, ' ')).toBe(quote)
    await expect(page.getByTestId('annotation-selection-action')).toBeVisible()
    await advance(160)
    await page.screenshot({ path: '/tmp/xbot-live-numeric-selection.png' })
    await page.getByTestId('annotation-selection-action').click()
    const capturedQuote = await page.getByRole('textbox', { name: '所选文本' }).inputValue()
    expect(capturedQuote.replace(/\s+/g, ' ')).toBe(quote)
    await page.getByRole('textbox', { name: '用户评论' }).fill('数的还可以')
    for (const last of [240, 480, 760, 1000]) await advance(last)
    await expect(page.getByRole('textbox', { name: '所选文本' })).toHaveValue(capturedQuote)
    await page.getByRole('button', { name: '确认批注', exact: true }).click()
    await expect(page.getByTestId('annotation-chip')).toContainText('1 条批注')
    await expect(body.locator('p').last()).toContainText('1000')
    expect(cancelled).toEqual([])
  })
})

test('mobile: native text selection, keyboard inset and item menu', async ({ browser }) => {
  const context = await browser.newContext({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true })
  const page = await context.newPage()
  await setup(page)
  await page.goto('/')
  const body = page.getByText('注释、评论功能的设计稿尚未交付。', { exact: true })
  await expect(body).toHaveCSS('user-select', 'text')
  await body.evaluate(async (el) => {
    el.dispatchEvent(new PointerEvent('pointerdown', { pointerType: 'touch', bubbles: true, clientX: 50, clientY: 200 }))
    await new Promise((resolve) => setTimeout(resolve, 550))
    el.dispatchEvent(new PointerEvent('pointerup', { pointerType: 'touch', bubbles: true }))
  })
  await expect(page.getByTestId('copy-sheet')).toHaveCount(0)
  await selectReply(page, 7)
  const nativeMenuAllowed = await body.evaluate((el) => el.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true })))
  expect(nativeMenuAllowed).toBe(true)
  await expect(page.getByTestId('annotation-selection-action')).toBeVisible()
  const actionBox = await page.getByTestId('annotation-selection-action').boundingBox()
  expect(actionBox!.width).toBe(44)
  expect(actionBox!.x + actionBox!.width).toBeLessThanOrEqual(390)
  await page.screenshot({ path: '/tmp/xbot-selection-comment-mobile.png' })
  await page.getByTestId('annotation-selection-action').click()
  const overlay = page.getByTestId('annotation-editor')
  await expect(overlay).toBeVisible()
  await expect(overlay).not.toContainText(/第\s*7\s*轮|第\s*1\s*次迭代|\/6000|\/2000/)
  const quote = page.getByRole('textbox', { name: '所选文本' })
  await expect(quote).toHaveValue('注释、评论功能')
  await page.getByRole('textbox', { name: '用户评论' }).fill('先支持标注评论')
  await page.evaluate(() => {
    // Old WebViews/iOS shrink only the visual viewport while the layout stays tall.
    Object.defineProperties(window.visualViewport!, {
      height: { configurable: true, value: window.innerHeight - 300 },
      offsetTop: { configurable: true, value: 0 },
    })
    window.visualViewport!.dispatchEvent(new Event('resize'))
  })
  await expect(overlay).toHaveCSS('bottom', '300px')
  const box = await overlay.boundingBox()
  expect(box!.x).toBeGreaterThanOrEqual(0)
  expect(box!.width).toBeLessThanOrEqual(390)
  expect(box!.y).toBeGreaterThanOrEqual(0)
  expect(box!.y + box!.height).toBeLessThanOrEqual(545)
  const confirm = await page.getByRole('button', { name: '确认批注', exact: true }).boundingBox()
  expect(confirm!.y + confirm!.height).toBeLessThanOrEqual(545)
  await page.evaluate(() => {
    delete (window.visualViewport as unknown as Record<string, unknown>).height
    delete (window.visualViewport as unknown as Record<string, unknown>).offsetTop
    window.visualViewport!.dispatchEvent(new Event('resize'))
  })
  await expect(overlay).toHaveCSS('bottom', '0px')
  await page.screenshot({ path: '/tmp/xbot-annotations-mobile-verified.png' })
  await page.getByRole('textbox', { name: '用户评论' }).press('Enter')
  await page.getByTestId('annotation-chip').click()
  await expect(page.getByTestId('annotation-list')).not.toContainText(/第\s*7\s*轮|第\s*1\s*次迭代|\/6000|\/2000/)
  await page.getByRole('button', { name: '批注操作' }).click()
  await page.getByRole('menuitem', { name: '删除批注' }).click()
  await expect(page.getByTestId('annotation-chip')).toHaveCount(0)
  await context.close()
})

test('switching sessions keeps annotation drafts separate and restores them on return', async ({ page }) => {
  await setup(page)
  await page.route('**/api/session-tree', (r) => r.fulfill({ json: { ok: true, data: {
    sessions: ['annotation-test', 'another-session'].map((id) => ({ chat_id: id, channel: 'web', label: id === 'annotation-test' ? '批注验收' : '另一会话', running: false, status: 'idle' })),
    chats: [], orphan_subagents: [],
  } } }))
  await page.goto('/')
  await page.getByRole('button', { name: /批注验收/ }).click()
  await add(page)
  await page.getByRole('button', { name: /另一会话/ }).click()
  await expect(page.locator('[data-agent-visible="1"]')).toHaveAttribute('data-agent-chat-id', 'another-session')
  await expect(page.locator('[data-agent-visible="1"] [data-testid="annotation-chip"]')).toHaveCount(0)
  await page.getByRole('button', { name: /批注验收/ }).click()
  await expect(page.locator('[data-agent-visible="1"] [data-testid="annotation-chip"]')).toContainText('1 条批注')
})

test('selected excerpt + attachment survive comment insertion and one accepted send', async ({ page }) => {
  await setup(page)
  await page.route('**/api/files/upload', (r) => r.fulfill({ json: { ok: true, data: { upload_key: 'uploads/comment-notes.txt', name: 'notes.txt', size: 5, mime: 'text/plain' } } }))
  const sent: Record<string, unknown>[] = []
  await page.route('**/api/message', (r) => {
    sent.push(r.request().postDataJSON())
    return r.fulfill({ json: { ok: true, data: { turn_id: 8, queued: false } } })
  })
  await openDesktopSession(page)
  const editor = page.locator('.tiptap[contenteditable="true"]')
  await editor.fill('原有正文')
  await editor.press('ControlOrMeta+A')
  await editor.press('ControlOrMeta+B')
  await editor.press('ArrowRight')
  await expect(editor.locator('strong')).toHaveText('原有正文')
  await page.locator('input[type="file"]').setInputFiles({ name: 'notes.txt', mimeType: 'text/plain', buffer: Buffer.from('notes') })
  await expect(editor.locator('a')).toContainText('notes.txt')
  const before = await editor.innerHTML()
  await selectReply(page, 7)
  await page.getByTestId('annotation-selection-action').click()
  await expect(page.getByRole('textbox', { name: '所选文本' })).toHaveValue('注释、评论功能')
  await page.getByRole('textbox', { name: '用户评论' }).fill('请实现这一项')
  await page.getByRole('button', { name: '确认批注', exact: true }).click()
  expect(await editor.innerHTML()).toBe(before)
  expect(sent).toHaveLength(0)
  await page.getByRole('button', { name: '发送', exact: true }).click()
  await expect(page.getByTestId('annotation-chip')).toHaveCount(0)
  expect(sent).toHaveLength(1)
  expect(sent[0].upload_keys).toEqual(['uploads/comment-notes.txt'])
  expect(annotationPayload(sent[0].content as string)[0]).toEqual({
    text: '注释、评论功能', annotation: '请实现这一项',
    source: { turnId: 7, iteration: 1, startOffset: 0, endOffset: 7, offsetBasis: 'dom-text', offsetEncoding: 'utf-16' },
  })
  expect(sent[0].content).not.toContain('尚未交付')
  expect(sent[0].content).toContain('请实现这一项')
  expect(sent[0].content).toContain('/api/files/download?key=uploads%2Fcomment-notes.txt')
})

test('cross-iteration selection is ineligible; reasoning and tools retain only their copy menus', async ({ page }) => {
  await setup(page)
  await openDesktopSession(page)
  await page.getByRole('button', { name: '思考 7 字' }).click()
  await page.getByText('不应引用的思考', { exact: true }).click({ button: 'right' })
  await expect(page.getByTestId('copy-menu')).not.toContainText('批注这段内容')
  await page.keyboard.press('Escape')
  await page.locator('[data-copy-target="tools"]').click({ button: 'right' })
  await expect(page.getByTestId('copy-menu')).not.toContainText('批注这段内容')
  await page.keyboard.press('Escape')
  await page.locator('[data-agent-visible="1"]').evaluate((el) => {
    const bodies = el.querySelectorAll('[data-annotation-body] p')
    const range = document.createRange()
    range.setStart(bodies[0].firstChild!, 0)
    range.setEnd(bodies[1].firstChild!, 3)
    const selection = window.getSelection()!
    selection.removeAllRanges()
    selection.addRange(range)
    document.dispatchEvent(new Event('selectionchange'))
  })
  await expect(page.getByTestId('annotation-selection-action')).toHaveCount(0)
  expect(await page.getByTestId('msg-actions').count()).toBe(0)
})
