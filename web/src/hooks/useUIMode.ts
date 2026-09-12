/**
 * useUIMode — UI 外壳模式偏好（自动 / 桌面 / 移动端）。
 *
 * 单一权威源：AppShell 用 `effective === 'mobile'` 决定渲染手机外壳还是桌面
 * 外壳（`useIsMobile` 派生自这里，TerminalPanel / LLM 控制台等布局判断同样
 * 受益）。localStorage（`xbot-ui-mode`）是读路径（首帧即生效，无异步闪烁），
 * 服务端 `user_settings` 的 `web:ui:ui-mode` 负责跨设备同步（SETTING_MAP）。
 *
 *   mode      — 用户偏好：'auto' 跟随视口断点，或强制 'desktop' / 'mobile'
 *   effective — 实际生效的外壳（'auto' 时由视口断点解析）
 *
 * 同窗口多实例同步用 useSyncExternalStore（设置面板切换后所有消费者立即
 * 重渲染，无需刷新）；视口跨断点（auto 模式）由 matchMedia change 驱动。
 */
import { useCallback, useEffect, useState, useSyncExternalStore } from 'react'

import { syncSettingToServer, SETTINGS_SYNCED_EVENT } from '@/lib/userSettings'

export type UIMode = 'auto' | 'desktop' | 'mobile'
/** 解析后的实际外壳模式（'auto' 已按视口断点落定）。 */
export type EffectiveUIMode = 'desktop' | 'mobile'

export const UI_MODE_STORAGE_KEY = 'xbot-ui-mode'
export const DEFAULT_UI_MODE: UIMode = 'auto'

/** 手机外壳断点 —— 与 MobileAppShell 布局假设一致（原 useIsMobile 常量）。 */
const MOBILE_QUERY = '(max-width: 767px)'

export const UI_MODES: readonly UIMode[] = ['auto', 'desktop', 'mobile']

function isUIMode(value: unknown): value is UIMode {
  return typeof value === 'string' && (UI_MODES as readonly string[]).includes(value)
}

function matchesMobile(): boolean {
  if (typeof window === 'undefined') return false
  return window.matchMedia(MOBILE_QUERY).matches
}

function readStoredMode(): UIMode {
  try {
    const stored = localStorage.getItem(UI_MODE_STORAGE_KEY)
    if (isUIMode(stored)) return stored
  } catch {
    /* storage unavailable */
  }
  return DEFAULT_UI_MODE
}

// ── 同窗口多实例 + 跨窗口同步 ───────────────────────────────────────────────
const listeners = new Set<() => void>()

function notify() {
  listeners.forEach((listener) => listener())
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener)
  return () => listeners.delete(listener)
}

if (typeof window !== 'undefined') {
  window.addEventListener('storage', (event: StorageEvent) => {
    if (event.key === UI_MODE_STORAGE_KEY) notify()
  })
  // 服务端同步完成（跨设备值拉回 localStorage）后重新读取。
  window.addEventListener(SETTINGS_SYNCED_EVENT, notify)
}

export function useUIMode(): {
  mode: UIMode
  effective: EffectiveUIMode
  setMode: (mode: UIMode) => void
} {
  const mode = useSyncExternalStore(subscribe, readStoredMode, readStoredMode)

  // 'auto' 模式下视口跨越断点要立即切换外壳。
  const [autoMobile, setAutoMobile] = useState(matchesMobile)
  useEffect(() => {
    if (typeof window === 'undefined') return
    const media = window.matchMedia(MOBILE_QUERY)
    const onChange = () => setAutoMobile(media.matches)
    onChange()
    media.addEventListener('change', onChange)
    return () => media.removeEventListener('change', onChange)
  }, [])

  const setMode = useCallback((next: UIMode) => {
    try {
      localStorage.setItem(UI_MODE_STORAGE_KEY, next)
      syncSettingToServer(UI_MODE_STORAGE_KEY, next)
    } catch {
      /* storage unavailable */
    }
    notify()
  }, [])

  return {
    mode,
    effective: mode === 'auto' ? (autoMobile ? 'mobile' : 'desktop') : mode,
    setMode,
  }
}
