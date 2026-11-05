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
    // 白名单探活：`/api/settings` 在 api 层的 401 静默白名单里（见
    // `UNAUTHORIZED_SILENT_ENDPOINTS`）——未登录时 401 是正常语义，这里直接返回，
    // 不弹「登录已失效」。其它失败（含 403，403 已由 api 层上报）只记日志。
    if (err instanceof APIError && err.status === 401) return // not logged in
    console.warn('[userSettings] syncAndMigrateSettings failed:', err)
    return
  }

  // Filter synced keys (web:ui:*, web:session:*, web:workspace:*)
  const syncedSettings = Object.entries(serverSettings).filter(
    ([k]) => k in REVERSE_MAP,
  )

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
      } catch {
        // 允许吞：一次性迁移，下次加载会重试。401/403 的**可见提示**不依赖这里
        // （api 层抛错前已上报统一出口；401 属白名单探活语义）。
      }
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
  pendingSettings[serverKey] = value
  if (syncTimer) clearTimeout(syncTimer)
  syncTimer = setTimeout(async () => {
    syncTimer = null
    const batch = { ...pendingSettings }
    for (const k of Object.keys(pendingSettings)) delete pendingSettings[k]
    try {
      await postAPI('/api/settings', { settings: batch })
    } catch {
      // 允许吞：debounced 同步失败由下次写入重试。401/403 的可见提示已由 api 层
      // 统一出口负责（抛错前上报），此处吞掉不会让「登录失效」变成无声失败。
    }
  }, SYNC_DEBOUNCE_MS)
}

// ── Test helpers (exported for unit tests only) ─────────────────────────────

export const __SETTING_MAP = SETTING_MAP
export const __REVERSE_MAP = REVERSE_MAP
