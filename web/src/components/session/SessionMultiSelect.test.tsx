import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, screen, waitFor } from '@testing-library/react'
import '@testing-library/jest-dom/vitest'
import { I18nProvider } from '@/providers/i18n'
import i18n from '@/i18n'
import { renderWithProviders } from '@/test-utils'
import { CoreSessionsPanel } from '@/components/panel/builtinPanels'
import { SessionSidebar } from './SessionSidebar'
import type { SessionStore } from '@/hooks/useSessionStore'
import type { SessionInfo } from '@/types/shared'

const mocks = vi.hoisted(() => ({ store: {} as SessionStore, archive: vi.fn() }))
vi.mock('@/hooks/useSessionStore', () => ({ useSessionStore: () => mocks.store }))
vi.mock('@/components/agent/api', async original => ({ ...await original<typeof import('@/components/agent/api')>(), downloadSessionsArchive: mocks.archive }))
vi.mock('@/components/ui/scroll-area', () => ({ ScrollArea: ({ children }: { children: React.ReactNode }) => <div>{children}</div> }))
vi.mock('@/components/ui/tooltip', () => ({ Tooltip: ({ children }: { children: React.ReactNode }) => <>{children}</>, TooltipTrigger: ({ children }: { children: React.ReactNode }) => <>{children}</>, TooltipContent: ({ children }: { children: React.ReactNode }) => <>{children}</> }))
const tabs = { tabs: [], openTab: vi.fn(), closeTab: vi.fn() }
const sessions: SessionInfo[] = ['Alpha', 'Beta', 'Gamma'].map((label, i) => ({
  label, chatID: `${i}`, channel: 'web', preview: '', lastActive: new Date().toISOString(),
  createdAt: `2026-10-01T00:00:0${i}Z`, status: 'idle', isCurrent: false,
}))
function setup(mobile = false) {
  const node = () => mobile ? <div role="dialog" aria-label="会话"><SessionSidebar tabManager={tabs as never} onSubAgentSelect={vi.fn()} /></div> : <CoreSessionsPanel ctx={{ tabManager: tabs } as never} />
  const result = renderWithProviders(node())
  return { update: () => result.rerender(<I18nProvider>{node()}</I18nProvider>) }
}
const click = (name: string) => fireEvent.click(screen.getByRole('button', { name }))
const row = (label: string) => screen.getByRole('checkbox', { name: new RegExp(label) })
const select = (label: string, shiftKey = false) => fireEvent.click(row(label), { shiftKey })
beforeEach(async () => {
  await i18n.changeLanguage('zh-CN')
  vi.clearAllMocks()
  Object.assign(mocks.store, {
    sessions: [...sessions], category: 'time', activeChannel: null, activeSession: null, starredIds: [], unreadIds: [], subAgents: [], collapsedGroups: new Set(), loading: false,
    hasMore: false, loadMore: vi.fn(), toggleGroupCollapsed: vi.fn(), setCategory: vi.fn(), setGroupsCollapsed: vi.fn(), setActiveChannel: vi.fn(),
    activateSession: vi.fn(), switchSession: vi.fn(), toggleStar: vi.fn(), markRead: vi.fn(), deleteSession: vi.fn().mockResolvedValue(true),
    renameSession: vi.fn(), forkSession: vi.fn(), createSession: vi.fn(), reorderSessions: vi.fn(),
  })
  mocks.archive.mockResolvedValue({ succeeded: ['web:0', 'web:1'], failed: [] })
})
for (const mobile of [false, true]) describe(mobile ? 'mobile selection' : 'desktop selection', () => {
  it('selects without navigating; all/clear/escape work', () => {
    setup(mobile); click('多选'); select('Alpha')
    expect(mocks.store.activateSession).not.toHaveBeenCalled()
    expect(tabs.openTab).not.toHaveBeenCalled()
    click('全选'); expect(screen.getAllByRole('checkbox', { checked: true })).toHaveLength(3)
    click('清空选择'); expect(screen.queryAllByRole('checkbox', { checked: true })).toHaveLength(0)
    fireEvent.keyDown(row('Alpha'), { key: 'Escape' }); expect(screen.queryByRole('checkbox')).toBeNull()
  })
  it('Shift follows displayed star order, and all respects search', () => {
    mocks.store.starredIds = ['web:1']
    setup(mobile); click('多选'); select('Beta'); select('Gamma', true)
    expect(screen.getAllByRole('checkbox', { checked: true })).toHaveLength(3)
    fireEvent.click(screen.getByTestId('session-search-toggle'))
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'Alpha' } })
    expect(row('Alpha')).not.toBeChecked()
    click('全选'); click('标为已读')
    expect(mocks.store.markRead).toHaveBeenCalledExactlyOnceWith('web:0')
  })
  it('sets stars explicitly for mixed selection', () => {
    mocks.store.starredIds = ['web:1']
    setup(mobile); click('多选'); click('全选'); click('标星')
    expect(vi.mocked(mocks.store.toggleStar).mock.calls).toEqual([['web:0'], ['web:2']])
  })
  it('confirms deletion, blocks duplicates and keeps failures selected', async () => {
    let resolve!: (value: boolean) => void
    mocks.store.deleteSession = vi.fn().mockImplementation(id => id === '0' ? new Promise<boolean>(r => { resolve = r }) : Promise.resolve(false))
    setup(mobile); click('多选'); select('Alpha'); select('Beta'); click('删除')
    expect(mocks.store.deleteSession).not.toHaveBeenCalled()
    expect(screen.getByRole('alertdialog')).toHaveTextContent('Alpha')
    const confirm = screen.getByRole('button', { name: '确认删除' })
    fireEvent.click(confirm); fireEvent.click(confirm); resolve(true)
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
    expect(mocks.store.deleteSession).toHaveBeenCalledTimes(2)
    expect(row('Beta')).toBeChecked(); expect(row('Alpha')).not.toBeChecked()
    expect(screen.getByTestId('session-batch-result')).toHaveTextContent('失败 1')
  })
  it('exports a snapshot with one format and preserves failures', async () => {
    mocks.archive.mockResolvedValue({ succeeded: ['web:0'], failed: ['web:1'] })
    setup(mobile); click('多选'); select('Alpha'); select('Beta'); click('导出会话')
    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'codex' } }); click('下载 ZIP')
    await waitFor(() => expect(mocks.archive).toHaveBeenCalled())
    expect(mocks.archive.mock.calls[0][0].map((s: SessionInfo) => s.chatID)).toEqual(['0', '1'])
    expect(mocks.archive.mock.calls[0][1]).toBe('codex'); expect(row('Beta')).toBeChecked()
  })
})
it('keyboard selection is scoped and ignores IME and dialog cancellation', () => {
  setup(); click('多选')
  fireEvent.keyDown(document.body, { key: 'a', ctrlKey: true }); expect(screen.queryAllByRole('checkbox', { checked: true })).toHaveLength(0)
  fireEvent.keyDown(row('Alpha'), { key: 'a', metaKey: true, isComposing: true }); expect(screen.queryAllByRole('checkbox', { checked: true })).toHaveLength(0)
  fireEvent.keyDown(row('Alpha'), { key: 'a', metaKey: true }); expect(screen.getAllByRole('checkbox', { checked: true })).toHaveLength(3)
  click('删除'); fireEvent.keyDown(screen.getByRole('button', { name: '取消' }), { key: 'Escape' })
  expect(screen.getAllByRole('checkbox', { checked: true })).toHaveLength(3)
})
it('channel changes clear selection; collapsed groups are excluded', () => {
  const view = setup(); click('多选'); select('Alpha')
  mocks.store.activeChannel = 'web'; view.update(); expect(screen.queryAllByRole('checkbox', { checked: true })).toHaveLength(0)
  mocks.store.collapsedGroups = new Set(['time:today']); view.update(); click('全选'); click('标为已读')
  expect(mocks.store.markRead).not.toHaveBeenCalled()
})
it('subagents cannot be selected or opened during multi-select', () => {
  const child = { ...sessions[0], chatID: 'child', channel: 'agent', label: 'ReviewChild', type: 'agent', parentChatID: '0', parentChannel: 'web' } as SessionInfo
  mocks.store.sessions = [{ ...sessions[0], children: [child] }]
  setup(); click('多选'); fireEvent.click(screen.getByText('ReviewChild'))
  expect(tabs.openTab).not.toHaveBeenCalled()
  click('全选'); expect(screen.getAllByRole('checkbox', { checked: true })).toHaveLength(1)
})
