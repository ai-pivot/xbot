/**
 * AmbienceRoot — 氛围装饰层宿主渲染（Ambience Layer 方案）。
 *
 * 两个独立组件（注入宿主根容器，见 AppShell/MobileAppShell）：
 *   <AmbienceBackground />  放根容器第一个子元素——壁纸层（z:0，
 *                          pointer-events:none）+ 玻璃化 CSS 变量注入
 *                          （--app-bg/panel-bg alpha 化，
 *                          color-mix 半透明 → 面板透出壁纸，dsh 同款效果）。
 *   <AmbienceOverlays />    放根容器最后一个子元素——decoration（z:30，
 *                          粒子/氛围光，pointer-events:none）+ hud（z-[35]，
 *                          桌宠/挂件，可交互；draggable 由宿主提供拖拽 +
 *                          位置记忆 localStorage）。
 *
 * z 序预算：decoration/hud 位于 FloatingLayer(z-40)/Dialog(z-50) 之下——
 * 装饰绝不遮挡浮动面板与对话框。
 *
 * 玻璃化实现（CSS 变量覆盖法）：壁纸启用时在 <html> inline style 覆盖
 * --app-bg/--panel-bg 为 color-mix(alpha)——所有
 * 消费点（根容器/面板/卡片，tailwind bg-bg-* 经 @theme inline 链到这些
 * token）整体半透明化，壁纸大面积透出；禁用时 removeProperty 恢复。
 * 主题切换（.dark class 变化）经 effect deps 重算（先恢复再读新值）。
 * 壁纸柔焦用壁纸层 inline filter: blur（v1 简化：柔焦壁纸而非毛玻璃
 * 透壁，视觉等效且零散消费点改动）。
 */
import { useEffect, useMemo, useRef, useState } from 'react'

import { useSessionStore } from '@/hooks/useSessionStore'
import type { AmbienceProfile } from '@/plugin-api'
import {
  ambienceStore,
  DEFAULT_GLASS,
  loadOverlayPos,
  saveOverlayPos,
  useAmbience,
} from './store'
import { initUserWallpaperSync } from './api'
import type { OverlayEntry } from './store'

/** 合并全局 + 会话覆盖（glass 逐字段）。纯函数——store 与 hook 共用。 */
export function mergeProfile(
  global: AmbienceProfile,
  ov: Partial<AmbienceProfile> | undefined,
): AmbienceProfile {
  if (!ov) return global
  return {
    enabled: ov.enabled ?? global.enabled,
    wallpaper: ov.wallpaper !== undefined ? ov.wallpaper : global.wallpaper,
    glass: { ...global.glass, ...ov.glass },
  }
}

/** 当前生效 profile（React hook：snapshot 变化驱动重算）。 */
export function useActiveProfile(): AmbienceProfile {
  const snap = useAmbience()
  return useMemo(
    () => mergeProfile(snap.global, snap.activeChatID ? snap.sessionOverrides[snap.activeChatID] : undefined),
    [snap],
  )
}

// ─── 壁纸层 + 玻璃变量注入 ──────────────────────────────────────────────────

/** alpha 化一个 CSS 色（color-mix 半透明）。 */
function alphaColor(token: string, opacity: number): string {
  const pct = Math.round(Math.min(1, Math.max(0, opacity)) * 100)
  return `color-mix(in srgb, ${token} ${pct}%, transparent)`
}

const GLASS_TOKENS = ['--app-bg', '--panel-bg'] as const
/**
 * 全部覆盖 token（cleanup 全恢复用）。文字 token 保留清理项——tone 感知
 * 分支已删除（2026-08-29 用户反馈"自动颜色反转太奇怪"），但部署前
 * light 分支可能 set 过文字 inline 值，cleanup 需能清干净。
 * V3: 只覆盖 --app-bg / --panel-bg（内容区玻璃化）；--sidebar-bg / --input-bg /
 * --surface-bg / --tab-* 不覆盖 → UI chrome 始终不透明。
 */
const ALL_OVERRIDES = [
  ...GLASS_TOKENS,
  '--text-primary',
  '--text-secondary',
  '--text-muted',
  '--border',
  '--dv-group-view-background-color',
  '--dv-tabs-and-actions-container-background-color',
] as const

export function AmbienceBackground() {
  const snap = useAmbience()
  const profile = useActiveProfile()
  const session = useSessionStore()
  const chatID = session.activeSession?.chatID ?? null

  // 会话跟踪：activeSession 变化 → store（会话级 profile 合并源）。
  useEffect(() => {
    ambienceStore.setActiveSession(chatID)
  }, [chatID])

  // 用户上传壁纸预载（IndexedDB → store 内存缓存，幂等一次）。
  useEffect(() => {
    void initUserWallpaperSync()
  }, [])

  const wp = useMemo(
    () => (profile.enabled ? ambienceStore.resolveWallpaper(profile.wallpaper) : null),
    [profile.enabled, profile.wallpaper, snap.wallpapers],
  )
  /** 玻璃化 effect 的 deps 原始值（css 字符串）——wp 对象引用不稳定
   * （resolveWallpaper 每次新对象）曾让 effect 在每条 store emit 上重跑
   * （getComputedStyle ×3 强制样式重算 + setProperty --bg-* ×3 全站
   * CSS 变量失效 + cleanup removeProperty ×3 = 每事件两轮全站失效，
   * 2026-08-29 桌宠事件风暴根因——deps 只吃原始值）。 */
  const wpCss = wp?.css ?? ''

  const opacity = profile.glass.opacity ?? DEFAULT_GLASS.opacity
  const blur = profile.glass.blur ?? DEFAULT_GLASS.blur

  // 玻璃化：覆盖 --bg-* token 为 alpha 色（主题切换/theme deps 重算：
  // cleanup 先 removeProperty → getComputedStyle 读到的是新主题 CSS 段值）。
  // 所有壁纸统一走主题色 alpha 化——不做 tone 感知（原浅色壁纸白系玻璃 +
  // 深字自动切换已被用户要求移除：UI 主题色保持稳定，壁纸只是背景）。
  //
  // 2026-08-29 v2（Dockview/Sheet 玻璃效果统一修复）：
  // 单靠覆盖 --bg-* 变量不够——Dockview 内部 DOM 有硬编码背景（.dockview-theme-vs
  // 库自带的 #1e1e1e），Sheet overlay 有 bg-black/50 Tailwind 硬编码。两者都不走
  // --bg-* 变量链 → 玻璃效果对它们无效。修复：glass effect 同时覆盖 --dv-* 变量
  // （Dockview 面板内容区）+ 给 <html> 加 ambience-glass CSS 类（CSS 中用
  // !important 强制 Dockview 内部层 + Sheet overlay 使用变量/降低不透明度）。
  useEffect(() => {
    const root = document.documentElement
    if (!profile.enabled || !wpCss) {
      for (const t of ALL_OVERRIDES) root.style.removeProperty(t)
      root.classList.remove('ambience-glass')
      return
    }
    root.classList.add('ambience-glass')
    for (const t of ALL_OVERRIDES) root.style.removeProperty(t)
    // 引用式覆盖：color-mix 引用 var(--app-bg-src) / var(--panel-bg-src)
    // （CSS 段 :root/.dark 定义的主题原色）——主题切换时 src 经 CSS 级联自动重算，
    // glass 无需 JS 重新读色。
    // V3: 只覆盖内容区（--app-bg / --panel-bg）；UI chrome（--sidebar-bg /
    // --input-bg / --surface-bg / --tab-*）不覆盖 → 始终不透明。
    root.style.setProperty('--app-bg', alphaColor('var(--app-bg-src)', opacity))
    root.style.setProperty('--panel-bg', alphaColor('var(--panel-bg-src)', opacity))
    // Dockview --dv-* 变量同步覆盖（绕过库内部硬编码背景）。
    root.style.setProperty('--dv-group-view-background-color', alphaColor('var(--app-bg-src)', opacity))
    root.style.setProperty('--dv-tabs-and-actions-container-background-color', alphaColor('var(--sidebar-bg)', Math.min(0.95, opacity + 0.12)))
    return () => {
      for (const t of ALL_OVERRIDES) root.style.removeProperty(t)
      root.style.removeProperty('--dv-group-view-background-color')
      root.style.removeProperty('--dv-tabs-and-actions-container-background-color')
      root.classList.remove('ambience-glass')
    }
    // deps 全原始值：enabled/wallpaper/opacity/blur（profile 字段）、wpCss
    // （壁纸 css 字符串）。theme 不需要——主题切换由 CSS 级联自动重算
    // （src 变量变 → color-mix(var()) 引用链实时解析）。绝不放对象引用。
  }, [profile.enabled, profile.wallpaper, opacity, blur, wpCss])

  if (!profile.enabled || !wp) return null

  // 壁纸不透明度（glass.wallpaperOpacity，缺省 1 完全显示）——调低壁纸变淡
  // 露出底色（底层 bg-app-bg 透出 → 文字清晰）。
  const wallpaperOpacity = profile.glass.wallpaperOpacity ?? DEFAULT_GLASS.wallpaperOpacity

  return (
    <div className="pointer-events-none absolute inset-0 z-0 overflow-hidden" aria-hidden>
      <div
        className="absolute inset-0"
        style={{
          background: wp.css,
          backgroundPosition: wp.focus,
          opacity: wallpaperOpacity,
          // 柔焦壁纸（glass.blur）：scale 补偿 blur 边缘发白。
          filter: blur > 0 ? `blur(${blur}px)` : undefined,
          transform: blur > 0 ? 'scale(1.04)' : undefined,
        }}
      />
    </div>
  )
}

// ─── overlay 层（decoration z:30 / hud z:[35]）───────────────────────────────

export function AmbienceOverlays() {
  const snap = useAmbience()
  const decoration = snap.overlays.filter((o) => o.layer === 'decoration' && o.visible)
  const hud = snap.overlays.filter((o) => o.layer === 'hud' && o.visible)
  return (
    <>
      {decoration.length > 0 && (
        <div className="pointer-events-none absolute inset-0 z-30 overflow-hidden">
          {decoration.map((e) => (
            <e.component key={e.id} {...e.props} />
          ))}
        </div>
      )}
      {hud.length > 0 && (
        <div className="pointer-events-none absolute inset-0 z-[35] overflow-hidden">
          {hud.map((e) => (
            <HudEntry key={e.id} entry={e} />
          ))}
        </div>
      )}
    </>
  )
}

/** hud 单项：定位 + 拖拽（positionKey 位置记忆 localStorage）+ 交互透传。 */
function HudEntry({ entry }: { entry: OverlayEntry }) {
  const key = entry.options.positionKey ?? entry.id
  const [pos, setPos] = useState(() =>
    loadOverlayPos(key) ?? entry.options.position ?? { right: 24, bottom: 24 },
  )
  const [dragging, setDragging] = useState(false)
  const dragOffset = useRef<{ dx: number; dy: number } | null>(null)

  const onPointerDown = (e: React.PointerEvent) => {
    if (!entry.options.draggable) return
    const rect = (e.currentTarget as HTMLElement).getBoundingClientRect()
    dragOffset.current = { dx: e.clientX - rect.left, dy: e.clientY - rect.top }
    setDragging(true)
    ;(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId)
    e.preventDefault()
  }
  const onPointerMove = (e: React.PointerEvent) => {
    if (!dragOffset.current) return
    const parent = (e.currentTarget as HTMLElement).parentElement
    const pb = parent ? parent.getBoundingClientRect() : { left: 0, top: 0 }
    setPos({ left: e.clientX - pb.left - dragOffset.current.dx, top: e.clientY - pb.top - dragOffset.current.dy })
  }
  const onPointerUp = () => {
    if (!dragOffset.current) return
    dragOffset.current = null
    setDragging(false)
    saveOverlayPos(key, pos)
  }

  const style: React.CSSProperties = {
    position: 'absolute',
    pointerEvents: 'auto',
    touchAction: 'none',
    ...(dragging ? { left: pos.left, top: pos.top } : pos),
  }

  return (
    <div
      style={style}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={onPointerUp}
      onPointerCancel={onPointerUp}
      role={entry.options.draggable ? 'dialog' : undefined}
      aria-label={entry.options.draggable ? 'overlay' : undefined}
    >
      <entry.component {...entry.props} />
    </div>
  )
}
