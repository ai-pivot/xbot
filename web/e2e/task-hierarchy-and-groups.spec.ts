/**
 * Visual acceptance for two features (screenshots are the deliverable here, the
 * assertions only guard that the frame being captured is the one intended):
 *
 *  ① Task panel renders the SubAgent HIERARCHY (we support nesting) — depth
 *     indentation + connectors + child counts, not a flat list.
 *  ② Sessions sidebar gains a 群组 (Groups) tab: which agents share a group, and
 *     editing that (add / remove member, create / delete group).
 *
 * Shots land in web/test-results/shots/ (gitignored) for human review.
 */
import { test, expect, type Page } from '@playwright/test'
import { mkdirSync } from 'node:fs'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'
const CHAT = 'chat-1'
const SHOTS = 'test-results/shots'
mkdirSync(SHOTS, { recursive: true })

/** A three-level SubAgent chain under the main session: review → fix → verify. */
function agent(role: string, instance: string, parentKey: string, opts: { running: boolean; children?: unknown[] }) {
  return {
    chat_id: `${parentKey}/${role}:${instance}`,
    channel: 'agent',
    label: 'default',
    last_active: new Date().toISOString(),
    preview: opts.running ? `working on ${role}` : '',
    status: opts.running ? 'running' : 'idle',
    running: opts.running,
    is_current: false,
    type: 'agent',
    role,
    instance,
    parent_channel: 'web',
    parent_chat_id: parentKey,
    agent_chat_id: `${parentKey}/${role}:${instance}`,
    children: opts.children ?? [],
  }
}

// The chain hangs off the session whose history the fixture serves (chat-1), so
// the active session owns the SubAgents the panel must draw.
const verify = agent('verify', '1', 'chat-1/review:1/fix:1', { running: false })
const fix = agent('fix', '1', 'chat-1/review:1', { running: true, children: [verify] })
const review = agent('review', '1', 'chat-1', { running: false, children: [fix] })

const SESSIONS = [
  {
    chat_id: 'chat-1',
    channel: 'web',
    label: 'Agent-main',
    work_dir: '/repo',
    last_active: new Date().toISOString(),
    preview: '',
    is_current: true,
    type: 'main',
    running: true,
    status: 'running',
    children: [review],
  },
  {
    chat_id: 'chat-2',
    channel: 'web',
    label: '前端重构',
    work_dir: '/repo',
    last_active: new Date(Date.now() - 3600_000).toISOString(),
    preview: 'assistant: done',
    is_current: false,
    type: 'main',
    children: [],
  },
]

const GROUPS = [
  // Members are session keys — the address the messaging pipeline routes on.
  // (A SubAgent inherits its parent's key, so an agent-key is never a member.)
  { id: 'dev-team', members: [
    { session_key: 'web:chat-1', name: 'Agent-main' },
    { session_key: 'web:chat-2', name: '前端重构' },
  ] },
  { id: 'release-crew', members: [
    // The session this member points at no longer exists: the UI flags it so the
    // user can clean it up, instead of hiding it.
    { session_key: 'web:ghost-chat', name: '已删除的会话' },
  ] },
]

async function boot(page: Page): Promise<void> {
  const now = new Date().toISOString()
  await page.route('**/api/settings', (r) => r.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', (r) => r.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', (r) => r.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', (r) =>
    r.fulfill({ json: { ok: true, data: { sessions: SESSIONS, chats: SESSIONS, orphan_subagents: [] } } }),
  )
  await page.route('**/api/history', (r) =>
    r.fulfill({
      json: {
        ok: true,
        data: {
          messages: [
            { role: 'user', content: '请并行推进这三件事', seq: 1, timestamp: now },
            { role: 'assistant', content: '收到，已派发子代理', seq: 2, timestamp: now },
          ],
          chat_id: CHAT,
          last_seq: 2,
          active_progress: null,
        },
      },
    }),
  )
  await page.route('**/api/session/status', (r) => r.fulfill({ json: { ok: true, data: { cwd: '/repo' } } }))
  await page.route('**/api/sse**', (r) => r.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/cron/list', (r) => r.fulfill({ json: { ok: true, data: { tasks: [] } } }))
  await page.route('**/api/tasks/list', (r) => r.fulfill({ json: { ok: true, data: { background_tasks: [] } } }))
  await page.route('**/api/rpc', (r) => {
    const method = (r.request().postDataJSON() as { method?: string }).method
    switch (method) {
      case 'peer_group_list':
        return r.fulfill({ json: { ok: true, data: { groups: GROUPS } } })
      case 'peer_group_join':
      case 'peer_group_leave':
      case 'peer_group_create':
      case 'peer_group_delete':
        return r.fulfill({ json: { ok: true, data: { groups: GROUPS, changed: true } } })
      case 'blackboard_list':
        return r.fulfill({ json: { ok: true, data: { board: 'web:chat-1', entries: [] } } })
      case 'blackboard_boards':
        return r.fulfill({ json: { ok: true, data: { boards: [] } } })
      default:
        return r.fulfill({ json: { ok: true, data: null } })
    }
  })

  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  await page.waitForFunction(() => document.body.textContent?.includes('已派发子代理'), { timeout: 15_000 })
}

test.describe('visual acceptance', () => {
  test('desktop: Task panel shows the SubAgent hierarchy', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await boot(page)
    await page.locator('[data-activity-item="core.tasks"]').click()

    const rows = page.getByTestId('subagent-row')
    await expect(rows).toHaveCount(3)
    // The chain review → fix → verify must carry its depth (a flat list would be
    // 0,0,0). Note review is idle: it survives only because the tree keeps the
    // ancestors of an active node.
    expect(await rows.evaluateAll((els) => els.map((e) => e.getAttribute('data-depth')))).toEqual(['0', '1', '2'])
    await expect(page.getByTestId('subagent-guide')).toHaveCount(2)

    await page.screenshot({ path: `${SHOTS}/task-hierarchy-desktop.png`, fullPage: false })
  })

  test('desktop: sessions sidebar 群组 tab lists groups and their agents', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await boot(page)
    await page.locator('[data-testid="session-category-group"]').click()

    await expect(page.getByTestId('group-card')).toHaveCount(2)
    await expect(page.getByTestId('group-member')).toHaveCount(3)
    // A member whose session is gone is flagged, not hidden.
    await expect(page.locator('[data-testid="group-member"][data-stale="true"]')).toHaveCount(1)

    await page.screenshot({ path: `${SHOTS}/groups-desktop.png`, fullPage: false })
  })

  test('mobile: Task panel keeps the hierarchy', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 })
    await boot(page)

    // Mobile reaches panels through the top-bar tools button, not a rail.
    await page.getByRole('button', { name: /tools|工具/i }).click()
    const tasksTab = page.getByRole('tab', { name: /tasks|任务/i })
    await tasksTab.click()
    // The highlighted tab must be the one whose panel is rendered (the strip is
    // the only way to tell which panel you are looking at on a phone).
    await expect(tasksTab).toHaveAttribute('aria-selected', 'true')

    const rows = page.getByTestId('subagent-row')
    await expect(rows).toHaveCount(3)
    expect(await rows.evaluateAll((els) => els.map((e) => e.getAttribute('data-depth')))).toEqual(['0', '1', '2'])

    await page.screenshot({ path: `${SHOTS}/task-hierarchy-mobile.png` })
  })

  test('mobile: sessions drawer 群组 tab', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 })
    await boot(page)

    // The drawer holds the same session list + view bar as the desktop panel.
    await page.getByRole('button', { name: /会话|Sessions/ }).first().click()
    await page.locator('[data-testid="session-category-group"]').click()

    await expect(page.getByTestId('group-card')).toHaveCount(2)
    await expect(page.locator('[data-testid="group-member"][data-stale="true"]')).toHaveCount(1)

    await page.screenshot({ path: `${SHOTS}/groups-mobile.png` })
  })

  test('desktop: the member picker offers the agents that can be added', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await boot(page)
    await page.locator('[data-testid="session-category-group"]').click()

    // dev-team already holds both sessions, so open the picker on release-crew —
    // the group whose only member is the deleted session.
    const card = page.getByTestId('group-card').filter({ hasText: 'release-crew' })
    await card.getByTestId('group-add-member').click()

    await expect(card.getByTestId('group-candidate')).toHaveCount(2)
    await page.screenshot({ path: `${SHOTS}/groups-picker-desktop.png`, fullPage: false })
  })
})
