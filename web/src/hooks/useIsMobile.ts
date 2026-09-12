/**
 * UI 外壳/设备能力谓词。
 *
 * useIsMobile —— 手机外壳/布局是否生效。单一权威源是 useUIMode（设置 →
 * 外观 → UI 模式）：'auto' 跟随视口断点，'desktop' / 'mobile' 为用户强制。
 * 本 hook 只是它的布局语义派生（AppShell 外壳切换、TerminalPanel /
 * LLM 控制台的布局分支共用）。
 *
 * useIsTouch —— 设备是否无 hover 能力（触屏）。这是【设备能力】而非布局
 * 模式，不受 UI 模式偏好影响：强制手机外壳的桌面上 hover 依然可用。
 */
import { useEffect, useState } from 'react'

import { useUIMode } from './useUIMode'

/** Detects touch-only devices (no hover capability) via pointer media queries. */
const TOUCH_QUERY = '(hover: none) and (pointer: coarse)'

export function useIsMobile(): boolean {
  return useUIMode().effective === 'mobile'
}

/**
 * Detects touch devices (no hover capability).
 * On such devices, `group-hover:opacity-*` classes never trigger because
 * there is no hover state. Use this to make hidden-on-hover buttons always
 * visible on touch devices.
 */
export function useIsTouch(): boolean {
  const [touch, setTouch] = useState(() => {
    if (typeof window === 'undefined') return false
    return window.matchMedia(TOUCH_QUERY).matches
  })

  useEffect(() => {
    const media = window.matchMedia(TOUCH_QUERY)
    const onChange = () => setTouch(media.matches)
    onChange()
    media.addEventListener('change', onChange)
    return () => media.removeEventListener('change', onChange)
  }, [])

  return touch
}
