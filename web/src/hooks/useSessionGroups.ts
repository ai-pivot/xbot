import { useCallback, useContext, useEffect, useSyncExternalStore } from 'react'
import { AuthContext } from '@/providers/AuthProvider'
import { postAPI } from '@/lib/api'
import type { SessionCustomGroup, SessionGroupAction, SessionGroupState, SessionGroupResponse, SessionSelector } from '@/lib/session-custom-groups'

const ENDPOINT = '/api/session-groups'
const CHANGED = 'xbot:session-groups-changed'
const emptyState = (): SessionGroupState => ({ revision: 0, groups: [], memberships: {} })
interface Snapshot { state: SessionGroupState; ready: boolean; loading: boolean; busy: boolean; error: Error | null }
let snapshot: Snapshot = { state: emptyState(), ready: false, loading: true, busy: false, error: null }
const listeners = new Set<() => void>()
const requests = new Map<AbortController, boolean>()
let consumers = 0
let owner: string | null | undefined
function resetSnapshot() {
  for (const controller of requests.keys()) controller.abort()
  requests.clear()
  emit({ state: emptyState(), ready: false, loading: true, busy: false, error: null })
}
const emit = (patch: Partial<Snapshot>) => {
  snapshot = { ...snapshot, ...patch }
  for (const listener of listeners) listener()
}
const getSnapshot = () => snapshot
const subscribe = (listener: () => void) => { listeners.add(listener); return () => { listeners.delete(listener) } }

async function request(body: SessionGroupAction | Record<string, never>, mutation = false) {
  if (!consumers || mutation && !snapshot.ready) return null
  const controller = new AbortController()
  requests.set(controller, mutation)
  emit(mutation ? { busy: true } : { loading: true })
  try {
    const result = await postAPI<SessionGroupResponse>(ENDPOINT, body, { signal: controller.signal })
    if (controller.signal.aborted) return null
    emit({
      state: !snapshot.ready || result.revision > snapshot.state.revision ? result : snapshot.state,
      ready: true, error: null,
    })
    return result
  } catch (cause) {
    if (!controller.signal.aborted) emit({ error: cause instanceof Error ? cause : new Error(String(cause)) })
    return null
  } finally {
    requests.delete(controller)
    if (!controller.signal.aborted) emit({
      loading: [...requests.values()].some((value) => !value),
      busy: [...requests.values()].some(Boolean),
    })
  }
}
async function refresh() { await request({}) }
async function mutate(action: SessionGroupAction) {
  // Structural typing permits SessionInfo here; only selectors belong on the wire.
  const body = 'sessions' in action && action.sessions
    ? { ...action, sessions: action.sessions.map(({ channel, chatID }) => ({ channel: channel || 'web', chatID })) }
    : action
  const result = await request(body, true)
  if (result) notifySessionGroupsChanged()
  return result
}

/** Notify the shared group reader after session creation/fork changed server membership. */
export function notifySessionGroupsChanged() {
  window.dispatchEvent(new Event(CHANGED))
}

/** Shared page snapshot, refreshed on mutations, visibility, focus and visible polling. */
export function useSessionGroups() {
  const auth = useContext(AuthContext)
  const identity = auth ? auth.user?.username ?? null : undefined
  const shared = useSyncExternalStore(subscribe, getSnapshot)
  const current = owner === identity ? shared : { ...shared, state: emptyState(), ready: false, loading: true, busy: false, error: null }
  useEffect(() => {
    if (owner !== identity) {
      resetSnapshot()
      owner = identity
      if (consumers > 0) void refresh()
    }
  }, [identity])
  useEffect(() => {
    consumers++
    if (consumers === 1) void refresh()
    const visibleRefresh = () => { if (document.visibilityState === 'visible') void refresh() }
    // Only the first consumer owns synchronization resources; the last tears them down.
    if (consumers === 1) startSync(visibleRefresh)
    return () => {
      consumers--
      if (consumers === 0) {
        stopSync?.()
        stopSync = undefined
        for (const controller of requests.keys()) controller.abort()
        requests.clear()
        snapshot = { state: emptyState(), ready: false, loading: true, busy: false, error: null }
      }
    }
  }, [])
  const addGroup = useCallback(async (
    action: 'create' | 'convert', name: string, defaultWorkDir: string, sessions?: SessionSelector[],
  ): Promise<SessionCustomGroup | null> => {
    const result = await mutate({ action, name, defaultWorkDir, ...(sessions ? { sessions } : {}) })
    return result?.groups.find((group) => group.id === result.createdGroupId) ?? null
  }, [])
  const createGroup = useCallback((name: string, defaultWorkDir: string, sessions?: SessionSelector[]) =>
    addGroup('create', name, defaultWorkDir, sessions), [addGroup])
  const convertGroup = useCallback((name: string, defaultWorkDir: string, sessions: SessionSelector[]) =>
    addGroup('convert', name, defaultWorkDir, sessions), [addGroup])
  const updateGroup = useCallback(async (id: string, name: string, defaultWorkDir: string) =>
    !!await mutate({ action: 'update', id, name, defaultWorkDir }), [])
  const deleteGroup = useCallback(async (id: string) => !!await mutate({ action: 'delete', id }), [])
  const moveSessions = useCallback(async (sessions: SessionSelector[], groupId: string | null) =>
    !!await mutate({ action: 'move', sessions, groupId }), [])
  const reorderGroups = useCallback(async (orderedIds: string[]) =>
    !!await mutate({ action: 'reorder', orderedIds }), [])
  // Background reconciliation must not disable controls on focus or polling.
  return { state: current.state, loading: current.loading && !current.ready, refreshing: current.loading && current.ready, busy: current.busy, error: current.error,
    refresh, createGroup, convertGroup, updateGroup, deleteGroup, moveSessions, reorderGroups }
}

let stopSync: (() => void) | undefined
function startSync(visibleRefresh: () => void) {
  const changed = () => { void refresh() }
  window.addEventListener(CHANGED, changed)
  window.addEventListener('focus', visibleRefresh)
  document.addEventListener('visibilitychange', visibleRefresh)
  const timer = window.setInterval(visibleRefresh, 15_000)
  stopSync = () => {
    window.clearInterval(timer)
    window.removeEventListener(CHANGED, changed)
    window.removeEventListener('focus', visibleRefresh)
    document.removeEventListener('visibilitychange', visibleRefresh)
  }
}
