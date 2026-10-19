import type { WSConnection } from '@/types/ws'
import type { LLMSettingsData } from './useLLMSettings'
import {
  listSubscriptions, listAllModelEntries, getUserThinkingMode,
  getLLMConcurrency, getSettings,
} from '@/components/agent/api'

const maxAgeMs = 30_000
const empty: LLMSettingsData = {
  subscriptions: [], modelEntries: [], thinkingMode: '', llmConcurrency: 0,
  tierVanguard: '', tierBalance: '', tierSwift: '',
}

// A provider creates a new connection on login/backend change. Never share
// account configuration across providers, tabs, or authenticated backends.
const stores = new WeakMap<WSConnection, ReturnType<typeof createStore>>()

function createStore(conn: WSConnection) {
  let snapshot = { data: empty, loading: true, error: null as string | null }
  let loadedAt: number | null = null
  let generation = 0
  let pending: Promise<void> | null = null
  const listeners = new Set<() => void>()
  let unsubscribe: (() => void) | undefined
  const publish = (next: typeof snapshot) => {
    snapshot = next
    listeners.forEach(listener => listener())
  }
  const load = (force = false): Promise<void> => {
    if (!force && pending) return pending
    if (!force && loadedAt !== null && Date.now() - loadedAt < maxAgeMs) return Promise.resolve()
    const current = ++generation
    publish({ ...snapshot, loading: true, error: null })
    pending = Promise.all([
      listSubscriptions(conn), listAllModelEntries(conn), getUserThinkingMode(conn),
      getLLMConcurrency(conn), getSettings(conn, 'cli'),
    ]).then(([subs, entries, thinkingMode, concurrency, settings]) => {
      // A mutation/reload may have started a newer read while this was pending.
      if (current !== generation) return
      loadedAt = Date.now()
      publish({ loading: false, error: null, data: {
        subscriptions: Array.isArray(subs) ? subs : [],
        modelEntries: Array.isArray(entries) ? entries : [],
        thinkingMode: thinkingMode ?? '', llmConcurrency: concurrency ?? 0,
        tierVanguard: settings['tier_vanguard'] ?? '',
        tierBalance: settings['tier_balance'] ?? '', tierSwift: settings['tier_swift'] ?? '',
      } })
    }).catch((error: unknown) => {
      if (current === generation) {
        loadedAt = null
        publish({ ...snapshot, loading: false, error: error instanceof Error ? error.message : String(error) })
      }
      throw error
    }).finally(() => { if (current === generation) pending = null })
    return pending
  }
  return {
    getSnapshot: () => snapshot,
    load,
    subscribe(listener: () => void) {
      listeners.add(listener)
      if (listeners.size === 1) {
        let wasConnected = conn.connected
        unsubscribe = conn.onConnectionChange(connected => {
          // The provider updates its connection getter in another listener.
          // Let all connection listeners finish before notifying subscribers
          // so they never observe a getter from the previous connection state.
          if (!wasConnected && connected) queueMicrotask(() => {
            if (listeners.size) void load().catch(() => {})
          })
          wasConnected = connected
        })
      }
      return () => {
        listeners.delete(listener)
        if (!listeners.size) { unsubscribe?.(); unsubscribe = undefined }
      }
    },
  }
}

export function llmSettingsStore(conn: WSConnection) {
  let store = stores.get(conn)
  if (!store) { store = createStore(conn); stores.set(conn, store) }
  return store
}
