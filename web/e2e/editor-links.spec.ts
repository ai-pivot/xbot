import { test, expect, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'

/**
 * E2E: tiptap composer link UX + file paste/drop upload + selection toolbar.
 *
 * Fully mocked backend (page.route) — no real server required. Verifies:
 *  1. Typed URLs auto-link and render visually distinct (accent color)
 *  2. Bare domains like file.tar.gz stay plain text (no false-positive autolink)
 *  3. Text typed after a link is NOT absorbed into the link (inclusive=false)
 *  4. Ctrl/Cmd+K link editor: add → apply → remove via the selection toolbar
 *  5. Pasted image files upload as attachment chips (any file type accepted)
 *  6. The selection toolbar stays inside the viewport on mobile widths
 */

interface SSEMockState {
  __sseListeners: Record<string, Set<(ev: MessageEvent) => void>>
}

async function setupMock(page: Page) {
  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', (r) => r.fulfill({
    json: { ok: true, data: {
      sessions: [{ chat_id: 'chat-1', channel: 'web', label: 'Test', last_active: new Date().toISOString() }],
      chats: [], orphan_subagents: [],
    } },
  }))
  await page.route('**/api/history', (r) => r.fulfill({
    json: { ok: true, data: { messages: [], chat_id: 'chat-1', last_seq: 0, active_progress: null } },
  }))
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/tmp' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', (r) => r.fulfill({ json: { ok: true, data: null } }))
}

async function loginAndOpenEditor(page: Page) {
  await page.addInitScript(() => {
    const listeners: Record<string, Set<(ev: MessageEvent) => void>> = {}
    const w = window as unknown as SSEMockState
    w.__sseListeners = listeners
    class M { readyState=1; onopen:((e:Event)=>void)|null=null; onerror:((e:Event)=>void)|null=null; constructor(public url:string){setTimeout(()=>this.onopen?.(new Event('open')),0)} addEventListener(t:string,h:(e:MessageEvent)=>void){if(!listeners[t])listeners[t]=new Set();listeners[t].add(h)} removeEventListener(){} close(){} }
    ;(window as unknown as { EventSource: typeof M }).EventSource = M
  })
  await setupMock(page)
  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  const editor = page.locator('.ProseMirror.xbot-editor')
  await expect(editor).toBeVisible({ timeout: 15_000 })
  return editor
}

test.describe('tiptap composer: links, toolbar, file paste', () => {
  test('typed URL auto-links and renders visually distinct (accent color)', async ({ browser }) => {
    const page = await browser.newPage()
    const editor = await loginAndOpenEditor(page)

    await editor.click()
    await page.keyboard.type('see https://example.com now')
    // Autolink fires on the whitespace-terminated word — immediate, but allow a tick
    const link = editor.locator('a[href="https://example.com"]')
    await expect(link).toHaveCount(1)
    await expect(link).toHaveText('https://example.com')

    // Visual distinction: link color must differ from the editor's text color
    const linkColor = await link.evaluate((el) => getComputedStyle(el).color)
    const textColor = await editor.evaluate((el) => getComputedStyle(el).color)
    expect(linkColor, `link color ${linkColor} must differ from text color ${textColor}`).not.toBe(textColor)

    await page.close()
  })

  test('bare domains like file.tar.gz stay plain text (no false-positive autolink)', async ({ browser }) => {
    const page = await browser.newPage()
    const editor = await loginAndOpenEditor(page)

    await editor.click()
    await page.keyboard.type('archive file.tar.gz here ')
    await expect(editor.locator('a')).toHaveCount(0)
    await expect(editor).toContainText('file.tar.gz')

    await page.close()
  })

  test('text typed after a link is NOT absorbed into the link (inclusive=false)', async ({ browser }) => {
    const page = await browser.newPage()
    const editor = await loginAndOpenEditor(page)

    await editor.click()
    await page.keyboard.type('https://example.com ')
    await expect(editor.locator('a')).toHaveCount(1)
    await page.keyboard.type('tail')
    // The link text must be exactly the URL — 'tail' is plain text outside it
    await expect(editor.locator('a')).toHaveText('https://example.com')
    await expect(editor).toContainText('https://example.com tail')

    await page.close()
  })

  test('Ctrl+K opens the link editor; apply and remove via the selection toolbar', async ({ browser }) => {
    const page = await browser.newPage()
    const editor = await loginAndOpenEditor(page)

    await editor.click()
    await page.keyboard.type('hello world')
    // Caret to the line start (Home), then Ctrl+K — deterministic: the word at
    // the line start is "hello" (a bare position-click can land in the blank
    // area right of short text and select the wrong word).
    await page.keyboard.press('Home')
    await page.keyboard.press('Control+k')
    const input = page.getByTestId('st-link-input')
    await expect(input).toBeVisible({ timeout: 5_000 })
    await input.fill('example.com')
    await input.press('Enter')
    await expect(editor.locator('a[href="https://example.com"]')).toHaveText('hello')

    // …and it can be removed again: applyLink preserves the "hello" selection
    // and refocuses the editor → the toolbar is live in bar mode right away.
    const linkBtn = page.getByTestId('st-link')
    await expect(linkBtn).toBeVisible({ timeout: 5_000 })
    await linkBtn.click()
    const removeBtn = page.getByTestId('st-link-remove')
    await expect(removeBtn).toBeVisible({ timeout: 5_000 })
    await removeBtn.click()
    await expect(editor.locator('a')).toHaveCount(0)
    await expect(editor).toContainText('hello world')

    await page.close()
  })

  test('pasted image uploads as an attachment chip (any file type)', async ({ browser }) => {
    const page = await browser.newPage()
    const editor = await loginAndOpenEditor(page)

    let uploadHits = 0
    await page.route('**/api/files/upload', (r) => {
      uploadHits += 1
      r.fulfill({ json: { ok: true, data: { upload_key: 'uploads/e2e/shot.png', name: 'shot.png', size: 3, mime: 'image/png' } } })
    })

    await editor.click()
    await page.evaluate(() => {
      const dt = new DataTransfer()
      dt.items.add(new File([new Uint8Array([1, 2, 3])], 'shot.png', { type: 'image/png' }))
      const el = document.querySelector('.ProseMirror.xbot-editor')
      if (!el) throw new Error('editor not found')
      el.dispatchEvent(new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true }))
    })

    // The attachment chip with the file name appears
    await expect(page.getByText('shot.png').first()).toBeVisible({ timeout: 5_000 })
    expect(uploadHits).toBe(1)
    // The pasted image must NOT insert content into the editor itself
    await expect(editor).not.toContainText('shot.png')

    await page.close()
  })

  test('selection toolbar stays within the viewport on mobile widths', async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: 375, height: 812 } })
    const page = await ctx.newPage()
    const editor = await loginAndOpenEditor(page)

    await editor.click()
    await page.keyboard.type('selectme word')
    await editor.getByText('selectme word').dblclick({ position: { x: 14, y: 6 } })

    const toolbar = page.getByTestId('selection-toolbar')
    await expect(toolbar).toBeVisible({ timeout: 5_000 })
    const box = await toolbar.boundingBox()
    expect(box).not.toBeNull()
    if (box) {
      expect(box.x).toBeGreaterThanOrEqual(0)
      expect(box.x + box.width).toBeLessThanOrEqual(375 + 1) // 1px tolerance for rounding
    }

    await ctx.close()
  })
})
