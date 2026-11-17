/**
 * userSettings — server-synced UI preferences.
 *
 * localStorage is the read path (existing code reads from it directly).
 * The server (user_settings table) is the authoritative source, synced
 * on app load. Writes go to localStorage (immediate) + server (debounced).
 *
 * Server keys use the `web:ui:<key>` namespace to avoid collisions with
 * agent-level settings (max_context_tokens, thinking_mode, etc.).
 *
 * Flow:
 *   1. App load → syncAndMigrateSettings() fetches web:ui:* from server,
 *      writes to localStorage (server authoritative), dispatches sync event.
 *      If server has no web:ui:* yet, pushes localStorage values (migration).
 *   2. User writes → existing code writes localStorage → syncSettingToServer()
 *      debounces a PUT to the server (500ms, batched).
 *   3. Components listen for SETTINGS_SYNCED_EVENT → re-read from localStorage.
 */
import { postAPI, APIError } from '@/lib/api'

/** localStorage key → server key mapping */
const SETTING_MAP: Record<string, string> = {
  // UI preferences (visual / interaction)
  'xbot-md-theme': 'web:ui:md-theme',
  'xbot-accent': 'web:ui:accent',
  'xbot-locale': 'web:ui:locale',
  'xbot-send-key-mode': 'web:ui:send-key-mode',
  'xbot-session-shortcuts': 'web:ui:session-shortcuts',
  'xbot-session-shortcuts-windows': 'web:ui:session-shortcuts-windows',
  'xbot-code-word-wrap': 'web:ui:code-word-wrap',
  'xbot:leftSidebarWidth': 'web:ui:left-sidebar-width',
  // UI 外壳模式（auto / desktop / mobile）——跨设备同步
  'xbot-ui-mode': 'web:ui:ui-mode',
  // Workspace layout (user-authored, VSCode-style dock layout overrides)
  'xbot:layout:overrides': 'web:ui:layout-overrides',
  // Layout ordering (slot → ordered item ids; widths/heights/collapse stay frontend-only)
  'xbot:layout:order': 'web:ui:layout-order',
  // Panel layout (v4「一切皆面板」：per-panel mode/x/y/w/h/collapsed + dockOrder)
  'xbot:panel-layout': 'web:ui:panel-layout',
  // Panel layout v2（v5 停靠引擎：Record<panelId, {loc, collapsed}>，zone/segment/order/xywh）
  'xbot:panel-layout-v2': 'web:ui:panel-layout-v2',
  // Session data (user-authored)
  'xbot-starred': 'web:session:starred',
  'xbot:session-category': 'web:session:category',
  // Workspace data (user-authored)
  'xbot:recent-workdirs:v1': 'web:workspace:recent-workdirs',
}

/** server key → localStorage key (reverse mapping) */
const REVERSE_MAP: Record<string, string> = Object.fromEntries(
  Object.entries(SETTING_MAP).map(([ls, srv]) => [srv, ls]),
)

/** Custom event dispatched after server→localStorage sync */
export const SETTINGS_SYNCED_EVENT = 'xbot:settings-synced'

/**
 * True once the server→localStorage sync has finished (success or failure).
 *
 * Why this gate exists: component defaults (e.g. ThemeProvider's mdTheme
 * fallback 'vscode-dark') run their init effects BEFORE
 * syncAndMigrateSettings() resolves. Without the gate they called
 * syncSettingToServer() with those defaults, which overwrote the user's real
 * server-side value — that is exactly how the operator's markdown theme was
 * silently reset from `xbot-aurora` to `vscode-dark` (2026-11-17 09:34:37).
 * Writes are only allowed after the server value has been loaded, and they
 * must come from a user action, not from initial state.
 */
let serverSynced = false

// ── Server → localStorage (on app load) ──────────────────────────────────────

/**
 * Fetch all settings from the server. If the server has `web:ui:*` keys,
 * write them to localStorage (server is authoritative) and dispatch
 * SETTINGS_SYNCED_EVENT so components re-read.
 *
 * If the server has NO `web:ui:*` keys yet (first time on any browser),
 * push all existing localStorage values to the server (one-time migration).
 *
 * Silently no-ops when not logged in (401) or server unavailable.
 */
export async function syncAndMigrateSettings(): Promise<void> {
  let serverSettings: Record<string, string>
  try {
    const data = await postAPI<{ settings: Record<string, string> }>('/api/settings')
    serverSettings = data.settings ?? {}
  } catch (err) {
    if (err instanceof APIError && err.status === 401) {
      // not logged in yet — keep the gate closed; nothing must be pushed.
      return
    }
    console.warn('[userSettings] syncAndMigrateSettings failed:', err)
    // Server unreachable: opening the gate would let defaults overwrite the
    // real settings on the next debounce, so leave it closed.
    return
  }

  // Filter synced keys (web:ui:*, web:session:*, web:workspace:*)
  const syncedSettings = Object.entries(serverSettings).filter(
    ([k]) => k in REVERSE_MAP,
  )

  serverSynced = true
  if (syncedSettings.length > 0) {
    // Server has synced settings → write to localStorage (server authoritative)
    const changedKeys: string[] = []
    for (const [serverKey, value] of syncedSettings) {
      const lsKey = REVERSE_MAP[serverKey]
      if (!lsKey) continue
      try {
        localStorage.setItem(lsKey, value)
        changedKeys.push(lsKey)
      } catch { /* ignore */ }
    }
    if (changedKeys.length > 0) {
      window.dispatchEvent(
        new CustomEvent(SETTINGS_SYNCED_EVENT, { detail: { keys: changedKeys } }),
      )
    }
  } else {
    // Server has no synced settings → migrate from localStorage
    const batch: Record<string, string> = {}
    for (const [lsKey, serverKey] of Object.entries(SETTING_MAP)) {
      try {
        const value = localStorage.getItem(lsKey)
        if (value !== null) batch[serverKey] = value
      } catch { /* ignore */ }
    }
    if (Object.keys(batch).length > 0) {
      try {
        await postAPI('/api/settings', { settings: batch })
      } catch { /* ignore — will retry next load */ }
    }
  }
}

// ── localStorage → server (on write, debounced) ────────────────────────────

const SYNC_DEBOUNCE_MS = 500
let syncTimer: ReturnType<typeof setTimeout> | null = null
const pendingSettings: Record<string, string> = {}

/**
 * Queue a setting for debounced sync to the server.
 * Called by preference writers after writing to localStorage.
 *
 * Multiple writes within the debounce window are batched into a single
 * PUT request. If the request fails, the values are lost from the pending
 * queue — the next write will retry.
 */
export function syncSettingToServer(lsKey: string, value: string): void {
  const serverKey = SETTING_MAP[lsKey]
  if (!serverKey) return // not a synced setting
  if (!serverSynced) return // server value not loaded yet — never push defaults
  pendingSettings[serverKey] = value
  if (syncTimer) clearTimeout(syncTimer)
  syncTimer = setTimeout(async () => {
    syncTimer = null
    const batch = { ...pendingSettings }
    for (const k of Object.keys(pendingSettings)) delete pendingSettings[k]
    try {
      await postAPI('/api/settings', { settings: batch })
    } catch { /* silent — next write will retry */ }
  }, SYNC_DEBOUNCE_MS)
}

// ── Test helpers (exported for unit tests only) ─────────────────────────────

export const __isServerSynced = () => serverSynced
export const __resetServerSynced = () => { serverSynced = false }
export const __markServerSynced = () => { serverSynced = true }
export const __SETTING_MAP = SETTING_MAP
export const __REVERSE_MAP = REVERSE_MAP
