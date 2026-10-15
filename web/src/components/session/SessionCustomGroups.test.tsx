import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import '@testing-library/jest-dom'
import { renderWithProviders } from '@/test-utils'
import { SessionList } from './SessionList'
import { NewSessionDialog } from './NewSessionDialog'
import { SessionGroupDialog } from './SessionGroupDialog'
import type { SessionInfo } from '@/types/shared'

const api = vi.hoisted(() => ({
  state: { revision: 1, groups: [{ id: 'one', name: 'Team', defaultWorkDir: '/team' }, { id: 'two', name: 'Empty', defaultWorkDir: '' }], memberships: { 'cli:same': 'one' } as Record<string, string> },
  loading: false, busy: false, error: null as Error | null,
  refresh: vi.fn(), createGroup: vi.fn(), convertGroup: vi.fn(), updateGroup: vi.fn(), deleteGroup: vi.fn(), moveSessions: vi.fn(), reorderGroups: vi.fn(),
}))
vi.mock('@/hooks/useSessionGroups', () => ({ useSessionGroups: () => api }))
vi.mock('@/components/session/PathPicker', () => ({ PathPicker: ({ value, onChange }: { value: string; onChange: (v: string) => void }) => <input aria-label="directory" value={value} onChange={event => onChange(event.target.value)} /> }))
vi.mock('@/lib/recent-workdirs', () => ({ loadRecentWorkDirs: () => ['/recent'], removeRecentWorkDir: () => [] }))
vi.mock('@/components/ui/scroll-area', () => ({ ScrollArea: ({ children }: { children: React.ReactNode }) => <div>{children}</div> }))
const cli: SessionInfo = { chatID: 'same', channel: 'cli', label: 'CLI chat', workDir: '/cli', preview: '', lastActive: '', status: 'idle', isCurrent: false }
const web: SessionInfo = { ...cli, channel: 'web', label: 'Web chat', workDir: '/web' }
const listProps = { sessions: [cli, web], sortedSessions: [cli, web], groups: [], category: 'path' as const, collapsedGroups: new Set<string>(), onToggleGroup: vi.fn(), starredIds: [], unreadIds: [], activeSession: null, search: '', subAgents: [], onSelect: vi.fn(), onToggleStar: vi.fn(), onRename: vi.fn(), onDelete: vi.fn(), onReorder: vi.fn() }
beforeEach(() => { api.state.memberships = { 'cli:same': 'one' }; vi.clearAllMocks(); api.loading = false; api.busy = false; api.error = null; api.moveSessions.mockResolvedValue(true); api.reorderGroups.mockResolvedValue(true) })
function transfer(data: Record<string, string>) { return { getData: (type: string) => data[type] ?? '', setData: vi.fn(), effectAllowed: 'move' } }
describe('custom session group UI', () => {
  it('path row reorder only writes the same group and channel, leaving other groups untouched', () => {
    const rows = ['a1', 'a2', 'b1', 'b2'].map(chatID => ({ ...web, chatID, label: chatID }))
    const grouped = ['a1', 'a2', 'b1', 'b2'].map(chatID => ({ ...web, chatID, label: chatID, workDir: chatID.startsWith('a') ? '/a' : '/b' }))
    // Cover explicit membership and automatic paths independently.
    api.state.memberships = { ...api.state.memberships, 'web:a1': 'one', 'web:a2': 'one', 'web:b1': 'two', 'web:b2': 'two' }
    const { rerender } = renderWithProviders(<SessionList {...listProps} sessions={rows} sortedSessions={rows} />)
    const drag = () => {
      fireEvent.dragStart(screen.getByText('a1').closest('[data-session-key]')!, { dataTransfer: transfer({}) })
      fireEvent.drop(screen.getByText('a2').closest('[data-session-key]')!, { dataTransfer: transfer({}) })
    }
    drag()
    expect(listProps.onReorder).toHaveBeenLastCalledWith('web', ['a2', 'a1'])
    api.state.memberships = { 'cli:same': 'one' }
    rerender(<SessionList {...listProps} sessions={grouped} sortedSessions={grouped} />)
    drag()
    expect(listProps.onReorder).toHaveBeenLastCalledWith('web', ['a2', 'a1'])
  })
  it.each(['status', 'time'] as const)('keeps legacy channel-wide row reorder in %s view', category => {
    const rows = ['a1', 'a2', 'b1', 'b2'].map(chatID => ({ ...web, chatID, label: chatID }))
    renderWithProviders(<SessionList {...listProps} category={category} sessions={rows} sortedSessions={rows} groups={[{ key: category === 'status' ? 'idle' : 'today', sessions: rows }]} />)
    fireEvent.dragStart(screen.getByText('a1').closest('[data-session-key]')!, { dataTransfer: transfer({}) })
    fireEvent.drop(screen.getByText('a2').closest('[data-session-key]')!, { dataTransfer: transfer({}) })
    expect(listProps.onReorder).toHaveBeenCalledWith('web', ['a2', 'a1', 'b1', 'b2'])
  })
  it('keeps empty custom groups, membership wins and same chatID in another channel stays automatic', () => {
    renderWithProviders(<SessionList {...listProps} />)
    expect(within(screen.getByTestId('session-group-custom:one')).getByText('CLI chat')).toBeInTheDocument()
    expect(screen.getByTestId('session-group-custom:two')).toBeInTheDocument()
    expect(within(screen.getByTestId('session-group-/web')).getByText('Web chat')).toBeInTheDocument()
    expect(screen.queryByTestId('session-group-/cli')).not.toBeInTheDocument()
  })
  it('moves full selectors via group drop and automatic drop without old reorder', async () => {
    renderWithProviders(<SessionList {...listProps} />)
    const dataTransfer = transfer({ 'application/x-xbot-session': JSON.stringify({ channel: 'web', chatID: 'same' }) })
    fireEvent.drop(screen.getByTestId('session-group-custom:two'), { dataTransfer })
    await waitFor(() => expect(api.moveSessions).toHaveBeenCalledWith([{ channel: 'web', chatID: 'same' }], 'two'))
    fireEvent.drop(screen.getByTestId('automatic-group-drop'), { dataTransfer })
    await waitFor(() => expect(api.moveSessions).toHaveBeenLastCalledWith([{ channel: 'web', chatID: 'same' }], null))
    expect(listProps.onReorder).not.toHaveBeenCalled()
  })
  it('rejects dropping a grouped session into another automatic path, but keeps same-group row reorder channel scoped', async () => {
    const other: SessionInfo = { ...web, chatID: 'other', label: 'Other web' }
    renderWithProviders(<SessionList {...listProps} sessions={[cli, web, other]} sortedSessions={[cli, web, other]} />)
    fireEvent.drop(screen.getByTestId('session-group-/web'), { dataTransfer: transfer({ 'application/x-xbot-session': JSON.stringify({ channel: 'cli', chatID: 'same' }) }) })
    expect(api.moveSessions).not.toHaveBeenCalled()
    const row = screen.getByText('Web chat').closest('[data-session-key]')!
    const target = screen.getByText('Other web').closest('[data-session-key]')!
    fireEvent.dragStart(row, { dataTransfer: transfer({}) })
    fireEvent.drop(target, { dataTransfer: transfer({}) })
    expect(listProps.onReorder).toHaveBeenCalledWith('web', ['other', 'same'])
  })
  it('group drag changes only custom group order', async () => {
    renderWithProviders(<SessionList {...listProps} />)
    fireEvent.drop(screen.getByTestId('session-group-custom:two'), { dataTransfer: transfer({ 'application/x-xbot-group': 'one' }) })
    await waitFor(() => expect(api.reorderGroups).toHaveBeenCalledWith(['two', 'one']))
    expect(api.moveSessions).not.toHaveBeenCalled()
  })
  it('group drag dropped on a session row bubbles to the owning group', async () => {
    renderWithProviders(<SessionList {...listProps} />)
    fireEvent.drop(screen.getByText('CLI chat').closest('[data-session-key]')!, { dataTransfer: transfer({ 'application/x-xbot-group': 'two' }) })
    await waitFor(() => expect(api.reorderGroups).toHaveBeenCalledWith(['two', 'one']))
    expect(api.moveSessions).not.toHaveBeenCalled()
  })
  it('preserves synthetic parents with live children but does not include them in conversion', async () => {
    api.convertGroup.mockResolvedValue(api.state.groups[0])
    const child: SessionInfo = { ...cli, chatID: 'web:parent/review:1', channel: 'agent', type: 'agent', label: 'Live child', parentChatID: 'parent', parentChannel: 'web', running: true, status: 'running' }
    const parent: SessionInfo = { ...web, chatID: 'parent', label: 'Synthetic parent', synthetic: true, children: [child] }
    renderWithProviders(<SessionList {...listProps} sessions={[parent]} sortedSessions={[parent]} />)
    expect(screen.getByText('Synthetic parent')).toBeInTheDocument()
    expect(screen.getByText('Live child')).toBeInTheDocument()
    fireEvent.click(within(screen.getByTestId('session-group-/web')).getByTestId('group-edit'))
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(api.convertGroup).toHaveBeenCalledWith('web', '/web', []))
  })
  it('global new keeps recent default; selecting a group presets directory, empty directory falls back to recent', async () => {
    const onCreate = vi.fn().mockResolvedValue('new')
    renderWithProviders(<NewSessionDialog open onOpenChange={vi.fn()} onCreate={onCreate} />)
    expect(screen.getByLabelText('directory')).toHaveValue('/recent')
    fireEvent.change(screen.getByTestId('new-session-group'), { target: { value: 'one' } })
    expect(screen.getByLabelText('directory')).toHaveValue('/team')
    fireEvent.change(screen.getByTestId('new-session-group'), { target: { value: 'two' } })
    expect(screen.getByLabelText('directory')).toHaveValue('/recent')
    fireEvent.click(screen.getByRole('button', { name: 'Confirm' }))
    await waitFor(() => expect(onCreate).toHaveBeenCalledWith(undefined, '/recent', 'two'))
  })
  it('convert uses only the supplied visible selectors and delete requires confirmation', async () => {
    api.convertGroup.mockResolvedValue(api.state.groups[0])
    const close = vi.fn()
    const { rerender } = renderWithProviders(<SessionGroupDialog target={{ path: '/web', sessions: [web] }} onClose={close} />)
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(api.convertGroup).toHaveBeenCalledWith('web', '/web', [{ channel: web.channel, chatID: web.chatID }]))
    expect(api.createGroup).not.toHaveBeenCalled()
    rerender(<SessionGroupDialog target={{ group: api.state.groups[0] }} onClose={close} />)
    fireEvent.click(screen.getByRole('button', { name: 'Delete group' }))
    expect(api.deleteGroup).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Confirm' }))
    await waitFor(() => expect(api.deleteGroup).toHaveBeenCalledWith('one'))
  })
  it('loading disables group actions and creation; error offers inline retry', () => {
    api.loading = true
    const { rerender } = renderWithProviders(<NewSessionDialog open onOpenChange={vi.fn()} onCreate={vi.fn()} />)
    expect(screen.getByTestId('new-session-group')).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Confirm' })).toBeDisabled()
    api.loading = false; api.error = new Error('offline')
    rerender(<NewSessionDialog open onOpenChange={vi.fn()} onCreate={vi.fn()} />)
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(api.refresh).toHaveBeenCalled()
  })
})
