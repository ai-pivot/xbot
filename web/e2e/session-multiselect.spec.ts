import { test, expect, type Page } from '@playwright/test'

async function mockSessions(page: Page) {
  let rows = ['Alpha', 'Beta', 'Gamma'].map((label, i) => ({ chat_id: `selection-${i}`, channel: 'web', label, preview: '', last_active: new Date().toISOString(), created_at: `2026-10-01T00:00:0${i}Z`, status: 'idle' }))
  const deletes: string[] = [], switches: string[] = []
  await page.addInitScript(() => { localStorage.setItem('xbot-locale', 'zh-CN') })
  await page.route('**/api/**', async route => {
    const path = new URL(route.request().url()).pathname
    const body = route.request().postDataJSON() ?? {}
    if (path.startsWith('/api/sse')) return route.fulfill({ contentType: 'text/event-stream', body: ': connected\n\n' })
    let data: unknown = {}
    if (path === '/api/session-tree') data = { sessions: rows, orphan_subagents: [], has_more: false }
    else if (path === '/api/history') data = { messages: [], last_seq: 0, active_progress: null }
    else if (path === '/api/session/status') data = { cwd: '/tmp' }
    else if (path === '/api/queue/list') data = { queued: [] }
    else if (path === '/api/rpc') {
      if (body.method === 'export_session') data = { messages: [{ role: 'user', content: 'batch export' }], model: 'mock' }
      else if (/^list_|plugin_list|plugin_status/.test(body.method || '')) data = []
      else if (body.method !== 'get_settings') data = null
    } else if (path.endsWith('/switch')) switches.push(body.chat_id || path)
    else if (path.endsWith('/delete')) {
      const id = decodeURIComponent(path.split('/')[3]); deletes.push(id)
      if (id === 'selection-1') return route.fulfill({ status: 500, json: { ok: false, error: 'failed' } })
      rows = rows.filter(row => row.chat_id !== id)
    }
    return route.fulfill({ json: { ok: true, data } })
  })
  await page.goto('/')
  return { deletes, switches }
}
for (const width of [1440, 390, 320]) test(`batch actions and search scope at ${width}px`, async ({ page }) => {
  await page.setViewportSize({ width, height: 844 })
  const { deletes } = await mockSessions(page)
  if (width < 768) await page.getByRole('button', { name: '会话', exact: true }).click()
  await page.getByRole('button', { name: '多选', exact: true }).click()
  await page.getByRole('checkbox', { name: 'Alpha', exact: true }).click()
  await page.getByRole('checkbox', { name: 'Gamma', exact: true }).click({ modifiers: ['Shift'] })
  await expect(page.getByRole('checkbox', { checked: true })).toHaveCount(3)
  await page.getByTestId('session-search-toggle').click()
  await page.getByRole('textbox', { name: '搜索', exact: true }).fill('Beta')
  await expect(page.getByRole('checkbox', { checked: true })).toHaveCount(0)
  await page.getByRole('button', { name: '全选', exact: true }).click()
  await expect(page.getByRole('checkbox', { checked: true })).toHaveCount(1)
  const bar = page.getByTestId('session-batch-actions')
  const bounds = await bar.boundingBox()
  expect(bounds).not.toBeNull(); expect(bounds!.x).toBeGreaterThanOrEqual(0); expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(width)
  await page.getByRole('button', { name: '删除', exact: true }).click()
  expect(deletes).toEqual([])
  await expect(page.getByRole('alertdialog')).toContainText('Beta')
  await page.getByRole('button', { name: '确认删除', exact: true }).click()
  await expect(page.getByTestId('session-batch-result')).toContainText('失败 1')
  await expect(page.getByRole('checkbox', { name: 'Beta', exact: true })).toBeChecked()
  expect(deletes).toEqual(['selection-1'])
})
test('one ZIP download with multiple selected sessions', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 }); await mockSessions(page)
  await page.getByRole('button', { name: '多选', exact: true }).click()
  await page.getByRole('button', { name: '全选', exact: true }).click()
  await page.getByRole('button', { name: '导出会话', exact: true }).click()
  await page.getByRole('combobox', { name: '导出格式', exact: true }).selectOption('codex')
  const wait = page.waitForEvent('download'); await page.getByRole('button', { name: '下载 ZIP', exact: true }).click()
  const download = await wait
  expect(download.suggestedFilename()).toBe('xbot-sessions.zip'); expect(await download.failure()).toBeNull()
  await expect(page.getByTestId('session-batch-result')).toContainText('成功 3')
  await expect(page.getByRole('checkbox', { checked: true })).toHaveCount(0)
})
