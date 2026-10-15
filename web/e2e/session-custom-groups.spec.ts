/**
 * Named groups are metadata, not working directories. Exercise the real sidebar,
 * dialogs, menus and HTML drag gestures against a stateful operation API.
 *
 * Failure triage: inspect the attached operation/RPC log before changing waits.
 * - no operation: event bubbling, menu selection or drag target wiring;
 * - operation succeeds but stale DOM: revision adoption/shared group subscription;
 * - set_cwd on move/edit/delete: membership has incorrectly become a cwd mutation;
 * - only the 220px case fails: inspect the min-w-0 chain and scroll viewport,
 *   not merely document overflow (overflow-hidden can conceal the defect).
 * No evaluate() call changes application state; evaluate is geometry-only.
 */
import { test, expect, type Locator, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5199'
const NOW = new Date().toISOString()
const LONG_NAME = `Release planning ${'very-long-unbroken-name-'.repeat(12)}`

type Group = { id: string; name: string; defaultWorkDir: string }
type Selector = { channel: string; chatID: string }
type Operation = {
  action?: 'create' | 'convert' | 'update' | 'delete' | 'move' | 'reorder'
  id?: string
  name?: string
  defaultWorkDir?: string
  sessions?: Selector[]
  groupId?: string | null
  orderedIds?: string[]
}
type Session = { chat_id: string; channel: string; label: string; work_dir: string; last_active: string }

async function fixture(page: Page, groups: Group[] = [], memberships: Record<string, string> = {}) {
  const state = { revision: 1, groups: groups.map(group => ({ ...group })), memberships: { ...memberships } }
  const sessions: Session[] = [
    { chat_id: 'chat-a', channel: 'web', label: 'Alpha session', work_dir: '/projects/original', last_active: NOW },
    { chat_id: 'chat-b', channel: 'web', label: 'Beta session', work_dir: '/projects/original', last_active: NOW },
    { chat_id: 'chat-c', channel: 'web', label: LONG_NAME, work_dir: '/projects/other', last_active: NOW },
  ]
  const operations: Operation[] = []
  const rpc: { method: string; params?: { chat_id?: string; dir?: string } }[] = []
  const creates: { label: string; chatID: string }[] = []
  const errors: string[] = []
  page.on('pageerror', error => errors.push(error.message))
  page.on('console', message => { if (message.type() === 'error') errors.push(message.text()) })
  let nextID = 1
  let activeChat = 'chat-a'

  // Match the project-group fixture: connected transport, no unsolicited events.
  // Locale is fixed because browser and Node i18n instances need not agree.
  await page.addInitScript(() => {
    localStorage.setItem('xbot-locale', 'en')
    class MockEventSource {
      readyState = 1
      onopen: ((event: Event) => void) | null = null
      onerror: ((event: Event) => void) | null = null
      constructor(public url: string) { setTimeout(() => this.onopen?.(new Event('open')), 0) }
      addEventListener() {}
      removeEventListener() {}
      close() { this.readyState = 2 }
    }
    ;(window as unknown as { EventSource: typeof MockEventSource }).EventSource = MockEventSource
  })
  await page.route('**/api/settings', route => route.fulfill({ json: { ok: true, data: {} } }))
  await page.route('**/api/auth/config', route => route.fulfill({ json: { ok: true, data: { invite_only: false } } }))
  await page.route('**/api/auth/login', route => route.fulfill({ json: { ok: true, data: { user_id: 'test' } } }))
  await page.route('**/api/session-tree', route => route.fulfill({ json: { ok: true, data: {
    sessions: sessions.map(session => ({ ...session, is_current: session.chat_id === activeChat })),
    chats: sessions, orphan_subagents: [], has_more: false,
  } } }))
  await page.route('**/api/session-groups', async route => {
    expect(route.request().method()).toBe('POST')
    const operation = route.request().postDataJSON() as Operation
    operations.push(operation)
    // The real backend rejects unknown selector fields. Do not let full
    // SessionInfo objects pass here when production's strict decoder would fail.
    for (const selector of operation.sessions ?? []) {
      expect(Object.keys(selector).sort(), 'session selectors contain only channel/chatID').toEqual(['channel', 'chatID'])
      expect(typeof selector.channel).toBe('string')
      expect(typeof selector.chatID).toBe('string')
      expect(selector.channel.length).toBeGreaterThan(0)
      expect(selector.chatID.length).toBeGreaterThan(0)
    }
    let createdGroupId: string | undefined
    switch (operation.action) {
      case undefined: break // {} lists the authoritative snapshot; not a mutation.
      case 'create':
      case 'convert': {
        const id = `group-created-${nextID++}`
        createdGroupId = id
        state.groups.push({ id, name: operation.name!, defaultWorkDir: operation.defaultWorkDir! })
        for (const session of operation.sessions ?? []) state.memberships[`${session.channel}:${session.chatID}`] = id
        break
      }
      case 'update': {
        const group = state.groups.find(group => group.id === operation.id)
        expect(group, 'update must address an existing group').toBeDefined()
        Object.assign(group!, { name: operation.name, defaultWorkDir: operation.defaultWorkDir })
        break
      }
      case 'delete':
        state.groups = state.groups.filter(group => group.id !== operation.id)
        for (const [key, id] of Object.entries(state.memberships)) if (id === operation.id) delete state.memberships[key]
        break
      case 'move':
        for (const session of operation.sessions ?? []) {
          const key = `${session.channel}:${session.chatID}`
          if (operation.groupId === null) delete state.memberships[key]
          else {
            expect(state.groups.some(group => group.id === operation.groupId), 'move target exists').toBe(true)
            state.memberships[key] = operation.groupId!
          }
        }
        break
      case 'reorder': {
        expect([...(operation.orderedIds ?? [])].sort()).toEqual(state.groups.map(group => group.id).sort())
        state.groups = operation.orderedIds!.map(id => state.groups.find(group => group.id === id)!)
        break
      }
      default: throw new Error(`Unexpected session-group operation: ${JSON.stringify(operation)}`)
    }
    if (operation.action) state.revision++
    // Creation identity is operation-scoped, never retained in list snapshots.
    await route.fulfill({ json: { ok: true, data: { ...state, ...(createdGroupId ? { createdGroupId } : {}) } } })
  })
  await page.route('**/api/history**', route => route.fulfill({ json: { ok: true, data: {
    messages: [], chat_id: activeChat, last_seq: 0, active_progress: null,
  } } }))
  await page.route('**/api/chats/create', async route => {
    const { label } = route.request().postDataJSON() as { label: string }
    const chatID = `chat-created-${creates.length + 1}`
    creates.push({ label, chatID })
    sessions.push({ chat_id: chatID, channel: 'web', label, work_dir: '', last_active: NOW })
    await route.fulfill({ json: { ok: true, data: { chat_id: chatID } } })
  })
  await page.route('**/api/chats/*/switch**', async route => {
    activeChat = decodeURIComponent(new URL(route.request().url()).pathname.split('/')[3])
    await route.fulfill({ json: { ok: true, data: { chat_id: activeChat, channel: 'web' } } })
  })
  await page.route('**/api/session/status', route => route.fulfill({ json: { ok: true, data: {
    cwd: sessions.find(session => session.chat_id === activeChat)?.work_dir ?? '/', running: false,
  } } }))
  await page.route('**/api/sse**', route => route.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.route('**/api/rpc', async route => {
    const body = route.request().postDataJSON() as (typeof rpc)[number]
    rpc.push(body)
    if (body.method === 'set_cwd') {
      const session = sessions.find(session => session.chat_id === body.params?.chat_id)
      expect(session, 'set_cwd must address a fixture session').toBeDefined()
      session!.work_dir = body.params!.dir!
    }
    await route.fulfill({ json: { ok: true, data: body.method === 'set_cwd' ? { dir: body.params?.dir } : null } })
  })
  await page.route('**/api/queue/list', route => route.fulfill({ json: { ok: true, data: { items: [] } } }))
  return { state, sessions, operations, rpc, creates, errors }
}

type Fixture = Awaited<ReturnType<typeof fixture>>

async function login(page: Page, mobile = false) {
  await page.goto(`${BASE}/login`)
  await page.locator('input').first().fill('test')
  await page.locator('input[type="password"]').fill('test')
  await page.locator('button[type="submit"]').click()
  if (mobile) await page.getByRole('button', { name: 'Sessions', exact: true }).click()
  await expect(page.getByTestId('session-view-bar')).toBeVisible({ timeout: 20_000 })
  await expect(page.getByTestId('session-category-path')).toHaveAttribute('aria-pressed', 'true')
}

function section(page: Page, name: string) {
  return page.locator('section[data-testid^="session-group-"]').filter({ has: page.getByTestId('group-header').filter({ hasText: name }) })
}
function header(group: Locator) { return group.getByTestId('group-header') }
function row(page: Page, label: string) {
  return page.locator('div[role="button"]').filter({ has: page.locator('span[title]').filter({ hasText: label }) })
}
function cwdCalls(f: Fixture) { return f.rpc.filter(request => request.method === 'set_cwd') }
async function saveGroup(page: Page, name: string, dir: string) {
  const dialog = page.getByRole('dialog')
  await dialog.locator('#group-name').fill(name)
  await dialog.locator('#group-dir').fill(dir)
  await dialog.getByRole('button', { name: 'Save', exact: true }).click()
  await expect(dialog).not.toBeVisible()
}
async function moveMenu(page: Page, label: string, target: string) {
  await row(page, label).click({ button: 'right' })
  const submenu = page.getByRole('menuitem', { name: 'Move to group', exact: true })
  await submenu.focus()
  await page.keyboard.press('ArrowRight')
  await page.getByRole('menuitem', { name: target, exact: true }).click()
}

// Keep the operation trace on success as well; capture the actual test page on
// failure (the mobile test owns a separate context, not the default page fixture).
async function attachLog(f: Fixture, page: Page) {
  const info = test.info()
  if (info.status !== info.expectedStatus && !page.isClosed()) {
    await info.attach('group-sidebar-dom', { body: await page.locator('body').innerText(), contentType: 'text/plain' })
  }
  await info.attach('session-group-operations', {
    body: JSON.stringify({ operations: f.operations, rpc: f.rpc, creates: f.creates, snapshot: f.state, browserErrors: f.errors }, null, 2),
    contentType: 'application/json',
  })
}

test('create a named group, and keep header collapse, edit and + independent', async ({ page }) => {
  const f = await fixture(page)
  try {
    await login(page)
    await page.getByRole('button', { name: 'Create group', exact: true }).click()
    await saveGroup(page, 'Release team', '/projects/release-default')
    expect(f.operations).toContainEqual({ action: 'create', name: 'Release team', defaultWorkDir: '/projects/release-default' })
    const group = section(page, 'Release team')
    await expect(header(group)).toHaveAttribute('aria-expanded', 'true')
    await group.getByRole('button', { name: 'Edit group', exact: true }).click()
    await expect(page.locator('#group-name')).toHaveValue('Release team')
    await expect(page.locator('#group-dir')).toHaveValue('/projects/release-default')
    await page.getByRole('dialog').getByRole('button', { name: 'Cancel', exact: true }).click()
    await expect(header(group)).toHaveAttribute('aria-expanded', 'true')

    await header(group).click()
    await expect(header(group)).toHaveAttribute('aria-expanded', 'false')
    await group.getByRole('button', { name: /^New Session —/ }).click()
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByRole('textbox', { name: 'Work path', exact: true })).toHaveValue('/projects/release-default')
    await dialog.locator('#new-session-label').fill('Created inside Release')
    await dialog.getByRole('button', { name: 'Confirm', exact: true }).click()
    await expect(dialog).not.toBeVisible()
    // The + action must not toggle the folded header as a side effect.
    await expect(header(group)).toHaveAttribute('aria-expanded', 'false')
    await header(group).click()
    await expect(group.getByText('Created inside Release', { exact: true })).toBeVisible()
    expect(f.state.memberships[`web:${f.creates[0].chatID}`]).toBe(f.state.groups[0].id)
    expect(cwdCalls(f).map(call => call.params?.dir)).toEqual(['/projects/release-default'])
    await page.reload()
    await expect(section(page, 'Release team').getByText('Created inside Release', { exact: true })).toBeVisible()
  } finally { await attachLog(f, page) }
})

test('editing an automatic path group converts membership without changing any cwd', async ({ page }) => {
  const f = await fixture(page)
  try {
    await login(page)
    await section(page, 'original').getByRole('button', { name: 'Edit group', exact: true }).click()
    await expect(page.locator('#group-dir')).toHaveValue('/projects/original')
    await saveGroup(page, 'Independent team', '/projects/new-default')
    const group = section(page, 'Independent team')
    await expect(group.getByText('Alpha session', { exact: true })).toBeVisible()
    await expect(group.getByText('Beta session', { exact: true })).toBeVisible()
    expect(f.operations.find(operation => operation.action === 'convert')).toMatchObject({
      name: 'Independent team', defaultWorkDir: '/projects/new-default',
      sessions: expect.arrayContaining([{ channel: 'web', chatID: 'chat-a' }, { channel: 'web', chatID: 'chat-b' }]),
    })
    await group.getByRole('button', { name: 'Edit group', exact: true }).click()
    await saveGroup(page, 'Renamed team', '/projects/future-only')
    await expect(section(page, 'Renamed team').getByText('Alpha session', { exact: true })).toBeVisible()
    expect(f.operations.some(operation => operation.action === 'update')).toBe(true)
    expect(cwdCalls(f)).toEqual([])
    expect(f.sessions.slice(0, 2).map(session => session.work_dir)).toEqual(['/projects/original', '/projects/original'])
  } finally { await attachLog(f, page) }
})

test('context menu moves membership, automatic return and deletion never set cwd', async ({ page }) => {
  const f = await fixture(page, [{ id: 'team', name: 'Team bucket', defaultWorkDir: '/projects/team-default' }])
  try {
    await login(page)
    await moveMenu(page, 'Alpha session', 'Team bucket')
    await expect(section(page, 'Team bucket').getByText('Alpha session', { exact: true })).toBeVisible()
    await expect(section(page, 'original').getByText('Alpha session', { exact: true })).toHaveCount(0)
    await moveMenu(page, 'Alpha session', 'Automatic path group')
    await expect(section(page, 'original').getByText('Alpha session', { exact: true })).toBeVisible()
    await moveMenu(page, 'Alpha session', 'Team bucket')
    await section(page, 'Team bucket').getByRole('button', { name: 'Edit group', exact: true }).click()
    const dialog = page.getByRole('dialog')
    await dialog.getByRole('button', { name: 'Delete group', exact: true }).click()
    await dialog.getByRole('button', { name: 'Confirm', exact: true }).click()
    await expect(dialog).not.toBeVisible()
    await expect(section(page, 'Team bucket')).toHaveCount(0)
    await expect(section(page, 'original').getByText('Alpha session', { exact: true })).toBeVisible()
    expect(f.state.memberships).toEqual({})
    expect(cwdCalls(f)).toEqual([])
    expect(f.sessions[0].work_dir).toBe('/projects/original')
    await page.reload()
    await expect(section(page, 'original').getByText('Alpha session', { exact: true })).toBeVisible()
    await expect(section(page, 'Team bucket')).toHaveCount(0)
  } finally { await attachLog(f, page) }
})

test('global new session allows an optional group instead of inferring membership from cwd', async ({ page }) => {
  const f = await fixture(page, [{ id: 'team', name: 'Team bucket', defaultWorkDir: '/projects/team-default' }])
  try {
    await login(page)
    await page.locator('button[title="New Session"]').click()
    const dialog = page.getByRole('dialog')
    await dialog.locator('#new-session-label').fill('Explicit group choice')
    await dialog.locator('#new-session-group').selectOption('team')
    await expect(dialog.getByRole('textbox', { name: 'Work path', exact: true })).toHaveValue('/projects/team-default')
    await dialog.getByRole('textbox', { name: 'Work path', exact: true }).fill('/projects/independent')
    // Confirm must remain clickable even while PathPicker suggestions are open.
    await dialog.getByRole('button', { name: 'Confirm', exact: true }).click()
    await expect(dialog).not.toBeVisible()
    await expect(section(page, 'Team bucket').getByText('Explicit group choice', { exact: true })).toBeVisible()
    expect(f.state.memberships[`web:${f.creates[0].chatID}`]).toBe('team')
    expect(f.sessions.find(session => session.chat_id === f.creates[0].chatID)?.work_dir).toBe('/projects/independent')

    await page.locator('button[title="New Session"]').click()
    await expect(dialog.locator('#new-session-group')).toHaveValue('')
    await dialog.locator('#new-session-label').fill('Automatic despite same cwd')
    await dialog.getByRole('textbox', { name: 'Work path', exact: true }).fill('/projects/team-default')
    await dialog.getByRole('button', { name: 'Confirm', exact: true }).click()
    await expect(dialog).not.toBeVisible()
    await expect(section(page, 'team-default').getByText('Automatic despite same cwd', { exact: true })).toBeVisible()
    expect(f.state.memberships[`web:${f.creates[1].chatID}`]).toBeUndefined()
  } finally { await attachLog(f, page) }
})

test('real session drag changes membership and group drag persists order', async ({ page }) => {
  const f = await fixture(page, [
    { id: 'first', name: 'First team', defaultWorkDir: '' },
    { id: 'second', name: 'Second team', defaultWorkDir: '' },
  ])
  try {
    await login(page)
    // Drop on the header so this specifically exercises the group drop zone.
    await row(page, 'Alpha session').dragTo(header(section(page, 'Second team')))
    await expect(section(page, 'Second team').getByText('Alpha session', { exact: true })).toBeVisible()
    expect(f.state.memberships['web:chat-a']).toBe('second')
    await section(page, 'Second team').getByRole('button', { name: 'Reorder group', exact: true }).dragTo(header(section(page, 'First team')))
    await expect.poll(() => f.state.groups.map(group => group.id)).toEqual(['second', 'first'])
    const customHeaders = page.locator('section[data-testid^="session-group-custom:"]').getByTestId('group-header')
    await expect(customHeaders).toHaveText([/Second team/, /First team/])
    expect(cwdCalls(f)).toEqual([])
    await page.reload()
    await expect(customHeaders).toHaveText([/Second team/, /First team/])
  } finally { await attachLog(f, page) }
})

test('220px sidebar keeps long names within the viewport and + remains clickable', async ({ page }) => {
  const f = await fixture(page, [{ id: 'long', name: LONG_NAME, defaultWorkDir: '/projects/long-default' }])
  try {
    await login(page)
    const panel = page.locator('[data-panel-id="core.sessions"]')
    const separator = page.getByRole('separator', { name: 'Resize sessions sidebar', exact: true })
    const handle = await separator.boundingBox()
    expect(handle).not.toBeNull()
    await page.mouse.move(handle!.x + handle!.width / 2, handle!.y + 80)
    await page.mouse.down()
    await page.mouse.move(220, handle!.y + 80, { steps: 12 })
    await page.mouse.up()
    await expect.poll(async () => Math.round((await separator.locator('..').boundingBox())!.width)).toBe(220)
    const group = section(page, LONG_NAME)
    const plus = group.getByRole('button', { name: /^New Session —/ })
    await expect(plus).toBeInViewport()
    const viewport = panel.locator('[data-slot="scroll-area-viewport"]')
    await expect(viewport).toHaveCount(1)
    await expect.poll(() => viewport.evaluate(element => element.scrollWidth - element.clientWidth), {
      message: 'session scroll viewport must have no horizontal overflow at 220px sidebar width',
    }).toBe(0)
    const plusBox = await plus.boundingBox()
    const panelBox = await panel.boundingBox()
    expect(plusBox!.x + plusBox!.width).toBeLessThanOrEqual(panelBox!.x + panelBox!.width)
    await plus.click()
    await expect(page.getByRole('dialog').getByRole('textbox', { name: 'Work path', exact: true })).toHaveValue('/projects/long-default')
    await page.getByRole('dialog').getByRole('button', { name: 'Cancel', exact: true }).click()
    await expect(header(group)).toHaveAttribute('aria-expanded', 'true')
  } finally { await attachLog(f, page) }
})

test('mobile long-press menu can move a session without desktop hover', async ({ browser }) => {
  const context = await browser.newContext({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, locale: 'en-US' })
  const page = await context.newPage()
  const f = await fixture(page, [{ id: 'mobile', name: 'Mobile team', defaultWorkDir: '/projects/mobile' }])
  try {
    await login(page, true)
    // Wait for the authoritative groups to render and controls to leave loading
    // before measuring. Otherwise hydration/drawer layout can move the touch target.
    const mobileGroup = section(page, 'Mobile team')
    await expect(header(mobileGroup)).toBeVisible()
    await expect(mobileGroup.getByTestId('group-edit')).toBeEnabled()
    const sessionRow = row(page, 'Alpha session')
    await sessionRow.scrollIntoViewIfNeeded()
    await expect(sessionRow).toBeInViewport()
    let previousBox = ''
    let stableSamples = 0
    await expect.poll(async () => {
      const box = await sessionRow.boundingBox()
      const sample = box ? JSON.stringify([box.x, box.y, box.width, box.height]) : ''
      stableSamples = sample && sample === previousBox ? stableSamples + 1 : 0
      previousBox = sample
      return stableSamples
    }, { message: 'mobile session row must settle before native long-press coordinates are captured' }).toBeGreaterThanOrEqual(2)
    // Trial performs Playwright's visibility/stability/hit-target checks without
    // selecting the session or dispatching a touch gesture.
    await sessionRow.tap({ trial: true })
    await expect(sessionRow).toBeInViewport()
    const sessionBox = await sessionRow.boundingBox()
    expect(sessionBox).not.toBeNull()
    // Native browser input, not synthetic DOM dispatch or store mutation. Hold
    // until the real touch-context-menu timer opens the menu; no fixed sleep.
    const cdp = await context.newCDPSession(page)
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x: sessionBox!.x + 70, y: sessionBox!.y + sessionBox!.height / 2 }] })
    await expect(page.getByRole('menuitem', { name: 'Move to group', exact: true })).toBeVisible()
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] })
    await cdp.detach()
    await page.getByRole('menuitem', { name: 'Move to group', exact: true }).tap()
    await page.getByRole('menuitem', { name: 'Mobile team', exact: true }).tap()
    await expect(section(page, 'Mobile team').getByText('Alpha session', { exact: true })).toBeVisible()
    expect(f.state.memberships['web:chat-a']).toBe('mobile')
    expect(cwdCalls(f)).toEqual([])
  } finally {
    await attachLog(f, page)
    await context.close()
  }
})
