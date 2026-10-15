import { act, cleanup, renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createElement, type ReactNode } from 'react'
import { AuthContext, type AuthContextValue } from '@/providers/AuthProvider'
import { postAPI } from '@/lib/api'
import { useSessionGroups } from './useSessionGroups'
import type { SessionGroupState } from '@/lib/session-custom-groups'
vi.mock('@/lib/api', () => ({ postAPI: vi.fn() }))
const api = vi.mocked(postAPI)
const state = (revision: number): SessionGroupState => ({ revision, groups: [], memberships: {} })
const deferred = <T,>() => { let resolve!: (value: T) => void; const promise = new Promise<T>((r) => { resolve = r }); return { promise, resolve } }
beforeEach(() => { api.mockReset(); api.mockResolvedValue(state(1)) })
afterEach(() => { cleanup(); vi.useRealTimers() })

describe('useSessionGroups', () => {
  it('keeps ready interactions enabled during focus refresh and preserves error/data on refresh failure', async () => {
    const hook = renderHook(useSessionGroups)
    await waitFor(() => expect(hook.result.current.loading).toBe(false))
    const refreshResult = deferred<SessionGroupState>()
    api.mockReturnValueOnce(refreshResult.promise)
    act(() => { window.dispatchEvent(new Event('focus')) })
    expect(hook.result.current.loading).toBe(false)
    expect(hook.result.current.busy).toBe(false)
    expect(hook.result.current.refreshing).toBe(true)
    await act(async () => { refreshResult.resolve(state(3)) })
    expect(hook.result.current.refreshing).toBe(false)
    const failed = deferred<SessionGroupState>()
    api.mockImplementationOnce(() => failed.promise.then(() => { throw new Error('offline') }))
    act(() => { window.dispatchEvent(new Event('focus')) })
    expect(hook.result.current.loading).toBe(false)
    await act(async () => { failed.resolve(state(4)) })
    expect(hook.result.current.error?.message).toBe('offline')
    expect(hook.result.current.state.revision).toBe(3)
    expect(hook.result.current.loading).toBe(false)
  })
  it('serializes rich session objects to selectors for create, convert and move without cwd fields', async () => {
    const hook = renderHook(useSessionGroups)
    await waitFor(() => expect(hook.result.current.loading).toBe(false))
    const rich = { channel: 'cli', chatID: 'source', label: 'Private title', workDir: '/private', cwd: '/secret', children: [{ chatID: 'child' }], preview: 'private text' }
    const defaultChannel = { ...rich, channel: '', chatID: 'default' }
    const sessions = [rich, defaultChannel]
    const selectors = [{ channel: 'cli', chatID: 'source' }, { channel: 'web', chatID: 'default' }]
    await act(async () => {
      await hook.result.current.createGroup('Group', '/preset', sessions)
      await hook.result.current.convertGroup('Converted', '/preset', sessions)
      await hook.result.current.moveSessions(sessions, 'g')
      await hook.result.current.moveSessions(sessions, null)
    })
    const actions = api.mock.calls.map((call) => call[1]).filter((body) => Object.keys(body as object).length > 0)
    expect(actions).toEqual([
      { action: 'create', name: 'Group', defaultWorkDir: '/preset', sessions: selectors },
      { action: 'convert', name: 'Converted', defaultWorkDir: '/preset', sessions: selectors },
      { action: 'move', sessions: selectors, groupId: 'g' },
      { action: 'move', sessions: selectors, groupId: null },
    ])
    expect(rich.workDir).toBe('/private')
    expect(defaultChannel.channel).toBe('')
  })
  it('ignores an old account response even when its revision exceeds the new account', async () => {
    let username = 'old'
    const wrapper = ({ children }: { children: ReactNode }) => createElement(AuthContext.Provider, {
      value: { user: { username }, loading: false } as AuthContextValue,
    }, children)
    const old = deferred<SessionGroupState>()
    api.mockReturnValueOnce(old.promise)
    const hook = renderHook(useSessionGroups, { wrapper })
    const signal = api.mock.calls[0][2]!.signal!
    api.mockResolvedValueOnce(state(2))
    username = 'new'
    hook.rerender()
    await waitFor(() => expect(hook.result.current.state.revision).toBe(2))
    expect(signal.aborted).toBe(true)
    await act(async () => { old.resolve({ ...state(999), groups: [{ id: 'secret', name: 'Secret', defaultWorkDir: '/private' }] }) })
    expect(hook.result.current.state).toEqual(state(2))
    expect(hook.result.current.error).toBeNull()
  })

  it('keeps shared requests alive until the last consumer unmounts, then resets for remount', async () => {
    const load = deferred<SessionGroupState>()
    api.mockReturnValueOnce(load.promise)
    const a = renderHook(useSessionGroups), b = renderHook(useSessionGroups)
    const signal = api.mock.calls[0][2]!.signal!
    a.unmount()
    expect(signal.aborted).toBe(false)
    await act(async () => { load.resolve(state(12)) })
    expect(b.result.current.state.revision).toBe(12)
    const pending = deferred<SessionGroupState>()
    api.mockReturnValueOnce(pending.promise)
    act(() => { void b.result.current.refresh() })
    const lastSignal = api.mock.calls.at(-1)![2]!.signal!
    b.unmount()
    expect(lastSignal.aborted).toBe(true)
    api.mockResolvedValueOnce(state(1))
    const c = renderHook(useSessionGroups)
    await waitFor(() => expect(c.result.current.state.revision).toBe(1))
    await act(async () => { pending.resolve(state(100)) })
    expect(c.result.current.state.revision).toBe(1)
  })

  it('does not let an older list overwrite a successful mutation', async () => {
    const hook = renderHook(useSessionGroups)
    await waitFor(() => expect(hook.result.current.loading).toBe(false))
    const list = deferred<SessionGroupState>(), mutation = deferred<SessionGroupState>()
    api.mockReturnValueOnce(list.promise).mockReturnValueOnce(mutation.promise)
    let loaded!: Promise<void>, moved!: Promise<boolean>
    act(() => { loaded = hook.result.current.refresh(); moved = hook.result.current.moveSessions([{ channel: 'web', chatID: 'a' }], 'g') })
    const updated = { ...state(7), memberships: { 'web:a': 'g' } }
    api.mockResolvedValue(updated)
    await act(async () => { mutation.resolve(updated); expect(await moved).toBe(true) })
    await act(async () => { list.resolve(state(2)); await loaded })
    expect(hook.result.current.state).toEqual(updated)
  })

  it('does not let an older mutation response overwrite a newer list', async () => {
    const hook = renderHook(useSessionGroups)
    await waitFor(() => expect(hook.result.current.loading).toBe(false))
    const mutation = deferred<SessionGroupState>()
    api.mockReturnValueOnce(mutation.promise).mockResolvedValue(state(10))
    let changed!: Promise<boolean>
    act(() => { changed = hook.result.current.updateGroup('g', 'Edited', '') })
    await act(async () => { await hook.result.current.refresh() })
    await act(async () => { mutation.resolve(state(3)); expect(await changed).toBe(true) })
    expect(hook.result.current.state.revision).toBe(10)
  })

  it('keeps busy true until every concurrent mutation settles and never rolls back', async () => {
    const a = renderHook(useSessionGroups), b = renderHook(useSessionGroups)
    await waitFor(() => expect(a.result.current.loading).toBe(false))
    const first = deferred<SessionGroupState>(), second = deferred<SessionGroupState>()
    api.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise).mockResolvedValue(state(8))
    let one!: Promise<boolean>, two!: Promise<boolean>
    act(() => { one = a.result.current.deleteGroup('one'); two = b.result.current.deleteGroup('two') })
    await act(async () => { second.resolve(state(8)); expect(await two).toBe(true) })
    expect(a.result.current.busy).toBe(true)
    expect(b.result.current.state).toBe(a.result.current.state)
    await act(async () => { first.resolve(state(4)); expect(await one).toBe(true) })
    expect(b.result.current.busy).toBe(false)
    expect(b.result.current.state.revision).toBe(8)
  })

  it('returns createdGroupId despite server trimming and another new group with the same name', async () => {
    const hook = renderHook(useSessionGroups)
    await waitFor(() => expect(hook.result.current.loading).toBe(false))
    const other = { id: 'other', name: 'Team', defaultWorkDir: '/repo' }
    const created = { ...other, id: 'ours' }
    api.mockResolvedValue({ ...state(2), groups: [other, created], createdGroupId: 'ours' })
    await act(async () => { expect(await hook.result.current.createGroup('  Team  ', ' /repo ')).toEqual(created) })
    expect(api.mock.calls[1][1]).toEqual({ action: 'create', name: '  Team  ', defaultWorkDir: ' /repo ' })
  })

  it('has only one visible polling loop for multiple instances and resumes on visibility restoration', async () => {
    vi.useFakeTimers()
    const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden')
    const a = renderHook(useSessionGroups), b = renderHook(useSessionGroups)
    await act(async () => {})
    expect(api).toHaveBeenCalledTimes(1)
    await act(async () => { vi.advanceTimersByTime(45_000); document.dispatchEvent(new Event('visibilitychange')); window.dispatchEvent(new Event('focus')) })
    expect(api).toHaveBeenCalledTimes(1)
    visibility.mockReturnValue('visible')
    await act(async () => { document.dispatchEvent(new Event('visibilitychange')) })
    expect(api).toHaveBeenCalledTimes(2)
    a.unmount()
    await act(async () => { vi.advanceTimersByTime(15_000) })
    expect(api).toHaveBeenCalledTimes(3)
    b.unmount()
    await act(async () => { vi.advanceTimersByTime(15_000); document.dispatchEvent(new Event('visibilitychange')) })
    expect(api).toHaveBeenCalledTimes(3)
    visibility.mockRestore()
  })

  it('clears private state on account changes and accepts lower revisions from the new owner', async () => {
    let username = 'alice'
    const wrapper = ({ children }: { children: ReactNode }) => createElement(AuthContext.Provider, {
      value: { user: { username }, loading: false } as AuthContextValue,
    }, children)
    api.mockResolvedValueOnce({ ...state(50), groups: [{ id: 'private', name: 'Alice', defaultWorkDir: '/alice' }] })
    const hook = renderHook(useSessionGroups, { wrapper })
    await waitFor(() => expect(hook.result.current.state.revision).toBe(50))
    const load = deferred<SessionGroupState>()
    api.mockReturnValueOnce(load.promise)
    username = 'bob'
    hook.rerender()
    expect(hook.result.current.state.groups).toEqual([])
    await act(async () => { expect(await hook.result.current.deleteGroup('private')).toBe(false) })
    await act(async () => { load.resolve(state(1)) })
    expect(hook.result.current.state.revision).toBe(1)
  })
  it('blocks mutations before initial load and shares snapshot and pending across consumers', async () => {
    const load = deferred<SessionGroupState>()
    api.mockReturnValueOnce(load.promise)
    const a = renderHook(useSessionGroups), b = renderHook(useSessionGroups)
    await act(async () => { expect(await a.result.current.deleteGroup('g')).toBe(false) })
    expect(api).toHaveBeenCalledTimes(1)
    await act(async () => { load.resolve(state(3)) })
    expect(a.result.current.state).toBe(b.result.current.state)
    expect(b.result.current.state.revision).toBe(3)
    const mutation = deferred<SessionGroupState>()
    api.mockReturnValueOnce(mutation.promise)
    let done!: Promise<boolean>
    act(() => { done = a.result.current.moveSessions([{ channel: 'cli', chatID: 'x' }], null) })
    expect(b.result.current.busy).toBe(true)
    api.mockResolvedValue(state(4))
    await act(async () => { mutation.resolve(state(4)); expect(await done).toBe(true) })
    expect(b.result.current.state.revision).toBe(4)
    expect(api.mock.calls[1][1]).toEqual({ action: 'move', sessions: [{ channel: 'cli', chatID: 'x' }], groupId: null })
  })

  it('retains data on list errors and prevents out-of-order revision rollback', async () => {
    const hook = renderHook(useSessionGroups)
    await waitFor(() => expect(hook.result.current.loading).toBe(false))
    const old = deferred<SessionGroupState>(), fresh = deferred<SessionGroupState>()
    api.mockReturnValueOnce(old.promise).mockReturnValueOnce(fresh.promise)
    let p1!: Promise<void>, p2!: Promise<void>
    act(() => { p1 = hook.result.current.refresh(); p2 = hook.result.current.refresh() })
    await act(async () => { fresh.resolve(state(9)); await p2 })
    await act(async () => { old.resolve(state(2)); await p1 })
    expect(hook.result.current.state.revision).toBe(9)
    api.mockRejectedValueOnce(new Error('offline'))
    await act(async () => { await hook.result.current.refresh() })
    expect(hook.result.current.error?.message).toBe('offline')
    expect(hook.result.current.state.revision).toBe(9)
  })

  it('initial list failure does not authorize mutation; successful retry does', async () => {
    api.mockRejectedValueOnce(new Error('offline'))
    const hook = renderHook(useSessionGroups)
    await waitFor(() => expect(hook.result.current.error).not.toBeNull())
    await act(async () => { expect(await hook.result.current.deleteGroup('g')).toBe(false) })
    expect(api).toHaveBeenCalledTimes(1)
    await act(async () => { await hook.result.current.refresh() })
    api.mockRejectedValueOnce(new Error('denied'))
    await act(async () => { expect(await hook.result.current.deleteGroup('g')).toBe(false) })
    expect(hook.result.current.error?.message).toBe('denied')
  })

  it('sends exact mutation bodies and returns the newly created group', async () => {
    const hook = renderHook(useSessionGroups)
    await waitFor(() => expect(hook.result.current.loading).toBe(false))
    const group = { id: 'g', name: 'Team', defaultWorkDir: '/repo' }
    api.mockResolvedValue({ ...state(2), groups: [group], createdGroupId: group.id })
    await act(async () => { expect(await hook.result.current.createGroup('Team', '/repo', [])).toEqual(group) })
    expect(api).toHaveBeenCalledWith('/api/session-groups', { action: 'create', name: 'Team', defaultWorkDir: '/repo', sessions: [] }, expect.anything())
    await act(async () => {
      expect(await hook.result.current.updateGroup('g', 'Renamed', '/new')).toBe(true)
      expect(await hook.result.current.reorderGroups(['g'])).toBe(true)
      expect(await hook.result.current.deleteGroup('g')).toBe(true)
    })
    expect(api.mock.calls.map((call) => call[1])).toContainEqual({ action: 'update', id: 'g', name: 'Renamed', defaultWorkDir: '/new' })
    expect(api.mock.calls.map((call) => call[1])).toContainEqual({ action: 'reorder', orderedIds: ['g'] })
    const converted = { id: 'converted', name: 'Project', defaultWorkDir: '/project' }
    api.mockResolvedValue({ ...state(3), groups: [group, converted], createdGroupId: converted.id })
    await act(async () => {
      expect(await hook.result.current.convertGroup('Project', '/project', [{ channel: 'web', chatID: 'a' }])).toEqual(converted)
    })
    expect(api.mock.calls.map((call) => call[1])).toContainEqual({ action: 'convert', name: 'Project', defaultWorkDir: '/project', sessions: [{ channel: 'web', chatID: 'a' }] })
  })

  it('refreshes on focus/visibility and only polls when visible; last unmount aborts and removes timers', async () => {
    vi.useFakeTimers()
    const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
    const hook = renderHook(useSessionGroups)
    await act(async () => {})
    expect(api).toHaveBeenCalledTimes(1)
    await act(async () => { window.dispatchEvent(new Event('focus')); document.dispatchEvent(new Event('visibilitychange')) })
    expect(api).toHaveBeenCalledTimes(3)
    await act(async () => { vi.advanceTimersByTime(15_000) })
    expect(api).toHaveBeenCalledTimes(4)
    visibility.mockReturnValue('hidden')
    await act(async () => { vi.advanceTimersByTime(30_000); window.dispatchEvent(new Event('focus')) })
    expect(api).toHaveBeenCalledTimes(4)
    api.mockReturnValueOnce(new Promise(() => {}))
    act(() => { void hook.result.current.refresh() })
    const signal = api.mock.calls.at(-1)![2]!.signal!
    hook.unmount()
    expect(signal.aborted).toBe(true)
    act(() => { vi.advanceTimersByTime(30_000); window.dispatchEvent(new Event('focus')) })
    expect(api).toHaveBeenCalledTimes(5)
    visibility.mockRestore()
  })
})
