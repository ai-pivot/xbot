/**
 * SandboxedUI — the core GENERIC capability for rendering free-form UI modules.
 *
 * This is plugin-agnostic: it compiles arbitrary TSX/JS and mounts it, with NO
 * knowledge of any specific tool (display_html/genui/…). UI plugins (e.g. the
 * xbot-genui plugin) call this with the code they want to render.
 *
 * Two modes (top-level branch, stable hook order):
 *   - code mode: compiles TSX/JS in-process with sucrase and mounts it INLINE
 *     (a <div> + separate React root) — NO iframe. Inline rendering removes the
 *     iframe/sandbox/contentDocument race that used to leave a blank 120px box,
 *     and lets arbitrary classes resolve against the host's compiled Tailwind.
 *   - src mode: loads an external trusted URL in a sandboxed iframe (isolation
 *     is unavoidable for cross-origin content).
 *
 * 0-injection: the compiled module receives ONLY React + hooks (the JS
 * environment) — no component library. The generated code uses standard
 * React/TSX + Tailwind (host dark mode applies automatically since we render
 * inline in the host document).
 *
 * State continuity (the "不流畅 / 卡顿" fix): compile produces a NEW component
 * function on every regeneration; rendering it via createElement would make
 * React treat it as a different type and REMOUNT the subtree (resetting
 * useState, rebuilding the DOM). Instead a module-level stable host calls the
 * current component *directly as a function*, so React keeps the host fiber and
 * the inner hooks/state/DOM survive regeneration (macaron's GeneratedComponentSlot
 * mechanism). Failures keep last-good, so the preview never blanks.
 */

import React, { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { flushSync } from 'react-dom'
import { transform } from 'sucrase'
import { normalizeGeneratedTsx } from 'partial-tsx'

// 暴露到 window 供独立 ESM 插件模块使用（无法 import 内部模块路径）。
// 独立插件（如 xbot-genui）通过 window.__xbot_ui__.SandboxedUI 复用这个通用
// 渲染原语，通过 window.React 获取 React（避免独立 bundle 重复打包 React）。
if (typeof window !== 'undefined') {
  const w = window as unknown as { __xbot_ui__?: unknown; React?: unknown }
  w.__xbot_ui__ = { SandboxedUI }
  w.React = React
}

// ─── Compilation cache ─────────────────────────────────────────
const compileCache = new Map<string, React.ComponentType>()
const CACHE_MAX = 8

// ─── GenUI code extraction (host-side, shared with AssistantMessage) ──────
// Priority: args.code (the raw LLM argument — never offloaded/prefix-polluted)
// → detail (pure TSX persisted by the backend, or a legacy Summary prefix).
// Mirrors the xbot-genui plugin's own extractor so the committed tool can be
// rendered by the stable genui slot (single-instance, never-rebuild).
export function genUICode(tool: { args?: string; detail?: string } | null | undefined): string {
  if (!tool) return ''
  const args = parseToolArgs(tool.args)
  if (typeof args?.code === 'string' && args.code.trim()) return stripGenUIPrefix(args.code)
  if (tool.detail) return stripGenUIPrefix(tool.detail)
  return ''
}

function parseToolArgs(args?: string): Record<string, unknown> | null {
  if (!args) return null
  try { return JSON.parse(args) as Record<string, unknown> } catch { return null }
}

function stripGenUIPrefix(code: string): string {
  if (!code) return ''
  const lines = code.split('\n')
  if (lines.length > 1) {
    const first = lines[0].trim()
    if (!/^(import|export|const|function|class|return|\/\/|\/\*|#|\{|\()/.test(first) && /export default|function App|<[A-Z]/.test(code)) {
      return lines.slice(1).join('\n').trimStart()
    }
  }
  return code
}

function codeHash(code: string): string {
  if (code.length <= 80) return code
  return `${code.length}:${code.slice(0, 32)}…${code.slice(-32)}`
}

// ─── Stable host slot (macaron GeneratedComponentSlot pattern) ──────
// 核心机制（参考 macaron-genui-demo/lib/partial-react/src/state.ts）：
// 一个稳定的 wrapper 组件，内部直接调用 currentRef.current(props)（函数组件），
// 而非 createElement(Current)。这样 React 把 hooks 绑在 wrapper fiber 上，
// 组件函数变化时不 remount → state/DOM 保留 → 流式平滑。
interface UISlot {
  current: React.ComponentType | null
  lastGood: React.ComponentType | null
}

/** Module-level STABLE host: same fiber across regenerations → state preserved.
 *  macaron GeneratedComponentSlot 方案：direct-call Current(props)，hooks 绑在
 *  UIHost 的稳定 fiber 上，Current 变化时不 remount → state/DOM 保留 → 流式平滑。
 *  返回值由 React 处理（包括 {} 等非法值 → ErrorBoundary 捕获 → streaming 返回 null）。 */
function UIHost({ slot, failed }: { slot: UISlot; failed: string | null }) {
  if (failed && !slot.lastGood) {
    return <div className="p-3 text-xs text-red-600 dark:text-red-400">⚠️ UI render error: {failed}</div>
  }
  const Current = slot.current || slot.lastGood
  if (!Current) return null
  // direct-call（macaron 方案）：hooks 绑在 UIHost fiber 上，Current 变化不 remount。
  // 返回值由 React 处理；非法值（如 {}）→ React 报错 → ErrorBoundary 捕获。
  if (typeof Current === 'function') {
    return (Current as (props: Record<string, unknown>) => React.ReactNode)({ 'data-sandboxed-ui-root': true }) as React.ReactNode
  }
  return React.createElement(Current, { 'data-sandboxed-ui-root': true } as Record<string, unknown>)
}

/** Catches render-time errors so they can't crash the host app.
 *  Streaming 期间不 surface 瞬时错误（macaron streaming 模式）：渲染失败时
 *  回退 lastGood（上次成功的组件，连续预览不闪白）。仅 final（committed）渲染
 *  失败才显示错误占位。resetKey prop 用于在下次编译成功时重置 hasError。 */
class UIErrorBoundary extends React.Component<
  { children?: React.ReactNode; fallback: React.ReactNode; streaming?: boolean; resetKey?: number; slot?: UISlot },
  { hasError: boolean }
> {
  constructor(props: { children?: React.ReactNode; fallback: React.ReactNode; streaming?: boolean; resetKey?: number; slot?: UISlot }) {
    super(props)
    this.state = { hasError: false }
  }
  // resetKey 变化（tick = 每次编译成功）时重置 hasError → 重新尝试渲染。
  override componentDidUpdate(prev: { resetKey?: number }) {
    if (prev.resetKey !== this.props.resetKey && this.state.hasError) {
      this.setState({ hasError: false })
    }
  }
  static getDerivedStateFromError(): { hasError: boolean } {
    return { hasError: true }
  }
  override render() {
    if (!this.state.hasError) return this.props.children
    // streaming 时回退 lastGood（上次成功组件）—— 连续预览不闪白。
    // lastGood 也可能出错（无限循环险）→ 此时返回 null（0高度），
    // 但 resetKey 会在下次编译成功时重置 hasError → 重新尝试。
    if (this.props.streaming) {
      const lastGood = this.props.slot?.lastGood
      if (lastGood) {
        return typeof lastGood === 'function'
          ? (lastGood as (props: Record<string, unknown>) => React.ReactNode)({ 'data-sandboxed-ui-root': true } as Record<string, unknown>)
          : React.createElement(lastGood, { 'data-sandboxed-ui-root': true } as Record<string, unknown>)
      }
      return null
    }
    return this.props.fallback
  }
}

// ─── Public API ────────────────────────────────────────────────

export interface SandboxedUIProps {
  /** Free-form TSX/JS source (code mode). Mutually exclusive with src. */
  code?: string
  /** External trusted URL (src mode). */
  src?: string
  widgetId?: string
  /** Fired on data-action clicks. */
  onAction?: (action: string, data: string) => void
  className?: string
  /** Streaming = the code may still be growing; compile is throttled + partial-completed. */
  streaming?: boolean
}

export function SandboxedUI(props: SandboxedUIProps) {
  if (props.src && !props.code) return <SourcedUI {...props} />
  return <CodeUI {...props} />
}

/** src mode: plain sandboxed iframe pointing at an external URL (cross-origin isolation). */
function SourcedUI({ src, widgetId, className }: SandboxedUIProps) {
  const iframeRef = useRef<HTMLIFrameElement>(null)
  const [height, setHeight] = useState(0)

  useEffect(() => {
    const iframe = iframeRef.current
    if (!iframe) return
    const doc = iframe.contentDocument
    if (!doc?.body) return
    const measure = () => {
      const h = doc.body.scrollHeight
      if (Math.abs(h - height) <= 2) return
      setHeight(h)
    }
    const ro = new ResizeObserver(measure)
    ro.observe(doc.body)
    measure()
    return () => ro.disconnect()
  }, [height])

  return (
    <iframe
      ref={iframeRef}
      src={src}
      className={`w-full rounded-lg border border-slate-200 ${className ?? ''}`}
      style={{ height: height > 0 ? `${height}px` : '320px', backgroundColor: '#fff' }}
      sandbox="allow-scripts allow-same-origin"
      title={`plugin-ui-${widgetId ?? 'widget'}`}
    />
  )
}

/** code mode: compile TSX + mount a separate React root INLINE (no iframe). */
function CodeUI({ code, widgetId, onAction, className, streaming = false }: SandboxedUIProps) {
  const hostRef = useRef<HTMLDivElement>(null)
  const rootRef = useRef<Root | null>(null)
  const slotRef = useRef<UISlot>({ current: null, lastGood: null })
  const [tick, setTick] = useState(0)
  const [failed, setFailed] = useState<string | null>(null)

  const codeRef = useRef(code)
  const timerRef = useRef<number | null>(null)
  const compileSeqRef = useRef(0)
  const lastRenderRef = useRef(0)

  // Compile TSX → JS → evaluate with React injected as parameter.
  const compileAndLoad = useCallback(async (tsx: string | undefined, seq: number, isStreaming: boolean) => {
    if (!tsx || tsx.trim().length < 10) return
    const hash = codeHash(tsx)
    const cached = compileCache.get(hash)
    if (cached) {
      if (seq !== compileSeqRef.current) return
      slotRef.current.current = cached
      slotRef.current.lastGood = cached
      setFailed(null)
      setTick((t) => t + 1)
      return
    }
    try {
      let clean = tsx.trim()
      if (clean.startsWith('```')) {
        clean = clean.replace(/^```(?:tsx|jsx|ts|js)?\s*\n?/i, '').replace(/\n?```\s*$/i, '')
      }
      if (isStreaming) {
        clean = normalizeGeneratedTsx(clean, { mode: 'streaming' })
      }
      const { code: js } = transform(clean, {
        transforms: ['typescript', 'jsx'],
        jsxRuntime: 'classic',
        production: true,
      })
      const noImports = js
        .replace(/^\s*import\s+.*$/gm, '')
        .replace(/^\s*export\s+default\s+/gm, '')
        .replace(/^\s*export\s+/gm, '')
      // 0-injection: only React + hooks (the JS environment). NO component library.
      // ⚠️ normalizeGeneratedTsx 在 streaming 模式下会追加 `export default App;`，
      // 但 new Function() 不支持 export 语句（"unexpected keyword export"）。
      // 在 wrapped 之前把所有 export 语句去掉（上面已做），但 normalizeGeneratedTsx
      // 追加的 export 在 noImports 之后才出现 —— 所以在 wrapped 模板里再清一次。
      const cleanNoImports = noImports.replace(/^\s*export\s+default\s+/gm, '').replace(/^\s*export\s+/gm, '')
      const wrapped = `
        const React = arguments[0];
        const { createElement, useState, useEffect, useMemo, useRef, useCallback,
                useContext, useReducer, useLayoutEffect, Fragment, forwardRef,
                useId, useSyncExternalStore, useTransition, useDeferredValue,
                useImperativeHandle, useDebugValue, memo, Children } = React;
        ${cleanNoImports}
        return typeof App !== 'undefined' ? App : null;
      `
      const fn = new Function(wrapped)
      const Comp = fn(React)
      if (seq !== compileSeqRef.current) return
      if (Comp && typeof Comp === 'function') {
        if (compileCache.size >= CACHE_MAX) {
          const firstKey = compileCache.keys().next().value
          if (firstKey) compileCache.delete(firstKey)
        }
        compileCache.set(hash, Comp)
        slotRef.current.current = Comp
        slotRef.current.lastGood = Comp
        setFailed(null)
        setTick((t) => t + 1)
      }
    } catch (e) {
      if (seq !== compileSeqRef.current) return
      // streaming 时编译失败不更新 slot.current（保持上次成功的组件），
      // 但也不 setFailed —— UIHost 会继续渲染 slot.current || slot.lastGood
      // （上次成功的组件），不会闪白。只有 final 失败且无 lastGood 才报错。
      if (!isStreaming && !slotRef.current.lastGood) {
        setFailed(e instanceof Error ? e.message : 'compile failed')
      }
      // ⚠️ streaming 编译失败时，确保 slot.current 仍指向 lastGood（上次成功的组件），
      // 而不是 null（否则 UIHost 渲染 null → 内容消失再出现）。
      if (isStreaming && slotRef.current.lastGood && !slotRef.current.current) {
        slotRef.current.current = slotRef.current.lastGood
        setTick((t) => t + 1)
      }
    }
  }, [])

  // Throttled scheduling (100ms during streaming, immediate otherwise).
  useEffect(() => {
    codeRef.current = code
    if (!streaming) {
      if (timerRef.current) { clearTimeout(timerRef.current); timerRef.current = null }
      compileAndLoad(code, ++compileSeqRef.current, false)
      return
    }
    if (timerRef.current) return
    const elapsed = Date.now() - lastRenderRef.current
    const delay = Math.max(0, 100 - elapsed)
    timerRef.current = window.setTimeout(() => {
      timerRef.current = null
      lastRenderRef.current = Date.now()
      compileAndLoad(codeRef.current, ++compileSeqRef.current, true)
    }, delay)
  }, [code, streaming, compileAndLoad])

  // Create root + render the STABLE host (sub-root).
  useLayoutEffect(() => {
    if (!hostRef.current) return
    if (!rootRef.current) rootRef.current = createRoot(hostRef.current)
    // ⚠️ useLayoutEffect（paint 前）+ flushSync 同步渲染子 root：行高度在**布局阶段**
    // 就确定，TanStack Virtual 的 measureElement(ResizeObserver) 一次得到稳定高度。
    // 若用 useEffect（paint 后），行 mount 时 hostRef 还是空 div(高度≈0)，子 root 渲染
    // 后才变高(≈560) → "0→实际"二次变化；虚拟化滚动频繁卷出/卷回 genui 行(remount)会
    // 反复触发 → 高度不断跳变（用户：只有 genui session 的 view 高度一直跳变）。
    flushSync(() => {
      rootRef.current!.render(
        React.createElement(
          UIErrorBoundary,
          {
            // ⚠️ 不用 key={boundary:${tick}} —— tick 每次编译成功都变（streaming 每
            // ~100ms），key 变 → React 整棵 ErrorBoundary remount → SandboxedUI 内容
            // 卸载 → host div 高度塌到 0 → ResumeObserver 重测 → totalSize 振荡
            // （"有内容→空白→有内容" + 滚动高度跳变，根因就在这）。
            // resetKey prop 已能在 componentDidUpdate 里重置 hasError（无 remount）：
            // ErrorBoundary 保持挂载，内容就地更新，高度平滑增长。
            fallback: '⚠️ Render error — check the generated UI syntax',
            streaming,
            resetKey: tick,
            slot: slotRef.current,
          },
          React.createElement(UIHost, { slot: slotRef.current, failed })
        )
      )
    })
  }, [tick, failed, streaming])

  useEffect(() => {
    return () => {
      rootRef.current?.unmount()
      rootRef.current = null
    }
  }, [])

  // Native click delegation for data-action. The sub-root's events do NOT bubble
  // to this element's React synthetic handler (separate root), so a native
  // capture listener on the container is required.
  useEffect(() => {
    const el = hostRef.current
    if (!el) return
    const handler = (e: MouseEvent) => {
      let node = e.target as HTMLElement | null
      while (node && node !== el) {
        const action = node.getAttribute?.('data-action')
        if (action) {
          // ⚠️ 不 stopPropagation：capture 阶段截断会杀掉 React 的 onClick。
          // 一个元素可同时有 data-action(回传 agent) + onClick(本地 state)，两者都应生效。
          const data: Record<string, string> = {}
          for (const attr of Array.from(node.attributes ?? [])) {
            if (attr.name.startsWith('data-') && attr.name !== 'data-action') data[attr.name.slice(5)] = attr.value
          }
          onAction?.(action, JSON.stringify(data))
          return
        }
        node = node.parentElement
      }
    }
    el.addEventListener('click', handler, true)
    return () => el.removeEventListener('click', handler, true)
  }, [onAction])

  if (!code || code.trim().length < 10) return null

  return (
    <div
      ref={hostRef}
      className={`sandboxed-ui w-full ${className ?? ''}`}
      data-widget-id={widgetId}
    />
  )
}

// ─── Partial TSX completion: now using partial-tsx (macaron's library) ──
// Replaced the hand-rolled completePartialTsx with the battle-tested
// normalizeGeneratedTsx from partial-tsx (macaron-genui-demo).
// It handles all edge cases: JSX tags, expressions, template literals,
// regex, comments, ASI, ternary, function declarations, etc.