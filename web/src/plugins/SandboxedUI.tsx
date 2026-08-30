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
  const w = window as unknown as { __xbot_ui__?: Record<string, unknown>; React?: unknown }
  w.__xbot_ui__ = { SandboxedUI, onGenuiError: null as ((error: string, code: string) => void) | null }
  w.React = React
}

// ─── Compilation cache ─────────────────────────────────────────
const compileCache = new Map<string, React.ComponentType>()
const CACHE_MAX = 8

// ─── DOM persistence pool (prevents createRoot remount during virtual scroll) ──
// Problem: TanStack Virtual unmounts/remounts rows during scroll. Each GenUI
// row's CodeUI calls createRoot on mount + root.unmount() on unmount → the
// entire GenUI React subtree (charts, code highlight, etc.) is rebuilt from
// scratch every time the row scrolls in/out of view. This costs 70-100ms per
// remount (trace profile confirmed), causing severe jank with multiple GenUI rows.
//
// Solution: when CodeUI unmounts, instead of root.unmount(), move the host div
// to a hidden pool container. The createRoot (and its entire React subtree) stays
// alive. On remount (scroll back into view), move the host div back — zero
// recompilation, zero re-render. The pool is keyed by codeHash and uses LRU eviction.
interface PoolEntry { root: Root; host: HTMLDivElement; inUse: boolean; lastUsed: number }
const rootPool = new Map<string, PoolEntry>()
const POOL_MAX = 20
let poolContainer: HTMLDivElement | null = null
function getPoolContainer(): HTMLDivElement {
  if (poolContainer) return poolContainer
  poolContainer = document.createElement('div')
  poolContainer.style.cssText = 'position:fixed;left:-9999px;top:-9999px;width:1px;height:1px;overflow:hidden;pointer-events:none;visibility:hidden'
  document.body.appendChild(poolContainer)
  return poolContainer
}
function evictOldestPoolEntry() {
  if (rootPool.size <= POOL_MAX) return
  let oldest: { key: string; entry: PoolEntry } | null = null
  for (const [key, entry] of rootPool) {
    if (entry.inUse) continue // never evict in-use entries
    if (!oldest || entry.lastUsed < oldest.entry.lastUsed) oldest = { key, entry }
  }
  if (oldest) {
    oldest.entry.root.unmount()
    oldest.entry.host.remove()
    rootPool.delete(oldest.key)
  }
}

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
    // streaming 时返回 null（0 高度），等下一个 chunk → resetKey 变化 → hasError 重置 → 重试。
    //
    // 不回退 lastGood：compileAndLoad 在成功路径（L278-279 / L343-344）把 slot.current 和
    // slot.lastGood 设为同一个组件引用；streaming 编译失败路径（L361-362）也不清空 current，
    // 而是 current = lastGood。因此 lastGood === current 恒成立 —— 回退 lastGood 等于重渲染
    // 同一个抛错组件 → error boundary 自身抛错 → React re-throw → window.onerror。
    // lastGood 也只是「上次编译出」的组件（非「上次渲染成功」），可能正是抛错的那个。
    if (this.props.streaming) {
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
  /** Fired when compilation fails (non-streaming, no lastGood). Use to collapse panels etc. */
  onError?: () => void
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
function CodeUI({ code, widgetId, onAction, className, streaming = false, onError }: SandboxedUIProps) {
  // containerRef: the React-rendered <div> that sits in the document.
  // hostRef: the div that createRoot renders into — may be pooled (moved to
  // hidden pool on unmount, moved back on remount) to avoid root teardown.
  const containerRef = useRef<HTMLDivElement>(null)
  const hostRef = useRef<HTMLDivElement | null>(null)
  const rootRef = useRef<Root | null>(null)
  const poolKeyRef = useRef<string>('')
  const slotRef = useRef<UISlot>({ current: null, lastGood: null })
  const [tick, setTick] = useState(0)
  const [failed, setFailed] = useState<string | null>(null)
  const onErrorRef = useRef(onError)
  onErrorRef.current = onError

  const codeRef = useRef(code)
  const timerRef = useRef<number | null>(null)
  const compileSeqRef = useRef(0)

  // Compile TSX → JS → evaluate with React injected as parameter.
  const compileAndLoad = useCallback(async (tsx: string | undefined, seq: number, isStreaming: boolean) => {
    if (!tsx || tsx.trim().length < 10) return
    const hash = codeHash(tsx)
    const cached = compileCache.get(hash)
    if (cached) {
      if (seq !== compileSeqRef.current) return
      // Same component already mounted → skip the tick (the streaming tick
      // chain re-runs this on an unchanged code tail every 100ms; a redundant
      // setTick would flushSync re-render the whole UI for nothing).
      if (slotRef.current.current === cached) return
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
        .replace(/\bexport\s+default\s+/g, '')
        .replace(/\bexport\s+(?!default\b)/g, '')
      // 0-injection: only React + hooks (the JS environment). NO component library.
      //
      // ── 安全模型（inline 编译的已知取舍）────────────────────────────
      // 这里用 new Function() 编译 LLM 生成的 TSX 并跑在宿主页面主线程：
      // `arguments[0]` 只是入口参数（React），函数体仍运行在全局作用域，可
      // 访问 window / document / localStorage / fetch 等全部宿主能力。这是
      // 「LLM 生成 UI」的已知攻击面，而非沙箱。
      //
      // 为什么不做 iframe 隔离：之前 code mode 用 iframe(sandbox) 有过
      // contentDocument 竞态导致内容空白（box 常量 120px）——iframe 隔离在
      // 本场景与「流式稳定渲染 + 复用宿主 Tailwind」互斥。inline 是刻意取舍，
      // 安全边界由上游控制（只有受信任的 internal tool / 插件能触发 genui，
      // 用户手动安装的插件本来就有宿主权限）。若未来引入「不受信任的任意
      // LLM 输出」入口，必须重新评估 iframe + postMessage 隔离，而不是继续
      // inline 执行。
      //
      // ── 编译：去掉 import 与 export ──────────────────────────────
      // ⚠️ normalizeGeneratedTsx 在 streaming 模式下会追加 `export default App;`，
      // 但 new Function() 不支持 export 语句（"unexpected keyword export"）。
      // 在 wrapped 之前把所有 export 语句去掉（上面已做），但 normalizeGeneratedTsx
      // 追加的 export 在 noImports 之后才出现 —— 所以在 wrapped 模板里再清一次。
      const cleanNoImports = noImports.replace(/\bexport\s+default\s+/g, '').replace(/\bexport\s+(?!default\b)/g, '')
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
        // Notify parent (GenUIPanel) to collapse — prevents virtual view
        // height calculation errors when the error panel is expanded.
        onErrorRef.current?.()
      }
      // ⚠️ streaming 编译失败时，确保 slot.current 仍指向 lastGood（上次成功的组件），
      // 而不是 null（否则 UIHost 渲染 null → 内容消失再出现）。
      if (isStreaming && slotRef.current.lastGood && !slotRef.current.current) {
        slotRef.current.current = slotRef.current.lastGood
        setTick((t) => t + 1)
      }
      // NOTE: Do NOT inject compilation errors back into the agent conversation.
      // The error is already shown in the UI ("⚠️ UI render error: ...").
      // Injecting messages causes infinite loops: panel renders from history →
      // fails → injects message → agent responds → re-renders → fails again.
    }
  }, [])

  // Fixed-cadence scheduling (streaming): compile the LATEST code, then wait
  // 100ms before the next pass. The timer re-arms AFTER compilation finishes —
  // never while it is in flight — so the cadence is always ≥100ms apart and
  // never queues up.
  //
  // ⚠️ History: the old elapsed-compensation throttle stamped lastRender
  // BEFORE compiling. Any compile pass slower than 100ms (large TSX: sucrase
  // transform + normalizeGeneratedTsx is hundreds of ms) made delay=0 on the
  // next chunk, so the throttle degenerated into per-chunk full recompiles
  // (~20/s) — main thread saturated, streaming visibly slow.
  useEffect(() => {
    codeRef.current = code
    if (!streaming) {
      if (timerRef.current != null) { clearTimeout(timerRef.current); timerRef.current = null }
      compileAndLoad(code, ++compileSeqRef.current, false)
      return
    }
    if (timerRef.current != null) return // chain already running; tick reads codeRef
    const tick = () => {
      timerRef.current = null
      compileAndLoad(codeRef.current, ++compileSeqRef.current, true)
      // Re-arm only AFTER compilation finished → slow compiles stretch the
      // cadence instead of piling up; fast compiles hold a steady 100ms beat.
      timerRef.current = window.setTimeout(tick, 100)
    }
    tick()
  }, [code, streaming, compileAndLoad])

  // Unmount cleanup: the tick chain re-arms itself on every pass, so a
  // dependency-scoped cleanup would kill it on every code change — clear it
  // once on unmount instead.
  useEffect(() => () => {
    if (timerRef.current != null) clearTimeout(timerRef.current)
  }, [])

  // ── DOM persistence pool: avoid createRoot remount during virtual scroll ──
  // On mount: acquire a pooled { root, host } by codeHash. If a pooled entry
  // exists and is idle (inUse=false), move its host div back into this
  // container — the createRoot subtree (charts, hooks, state) survives intact,
  // zero recompilation/re-render. On unmount: move host to hidden pool
  // container (NOT root.unmount). The subtree stays alive, just detached.
  useLayoutEffect(() => {
    const container = containerRef.current
    if (!container) return
    // Acquire from pool or create new
    const key = codeHash(codeRef.current || '')
    poolKeyRef.current = key
    let entry = rootPool.get(key)
    if (entry && !entry.inUse) {
      // Reuse pooled host + root — zero recompilation, zero re-render
      entry.inUse = true
      entry.lastUsed = Date.now()
      rootRef.current = entry.root
      hostRef.current = entry.host
      container.appendChild(entry.host)
    } else {
      // Create new host + root
      const host = document.createElement('div')
      host.className = `sandboxed-ui w-full ${className ?? ''}`
      host.dataset.widgetId = widgetId ?? ''
      container.appendChild(host)
      hostRef.current = host
      rootRef.current = createRoot(host)
      entry = { root: rootRef.current, host, inUse: true, lastUsed: Date.now() }
      rootPool.set(key, entry)
      evictOldestPoolEntry()
    }
    // ⚠️ flushSync (paint before layout) — ensures height is stable before
    // TanStack Virtual's measureElement (ResizeObserver) fires.
    flushSync(() => {
      rootRef.current!.render(
        React.createElement(
          UIErrorBoundary,
          {
            fallback: '⚠️ Render error — check the generated UI syntax',
            streaming,
            resetKey: tick,
            slot: slotRef.current,
          },
          React.createElement(UIHost, { slot: slotRef.current, failed })
        )
      )
    })
  }, []) // mount once — pool handles root persistence

  // Re-render when tick/failed/streaming change (root already exists from pool)
  useLayoutEffect(() => {
    const root = rootRef.current
    if (!root) return
    flushSync(() => {
      root.render(
        React.createElement(
          UIErrorBoundary,
          {
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

  // On unmount: move host to pool (NOT root.unmount) — the entire GenUI
  // React subtree (hooks, state, DOM, charts) survives. When this CodeUI
  // remounts (virtual scroll brings the row back), the pooled host is moved
  // back into the new container — instant, zero recompilation.
  useEffect(() => {
    return () => {
      const key = poolKeyRef.current
      const entry = rootPool.get(key)
      if (entry && entry.host === hostRef.current) {
        entry.inUse = false
        entry.lastUsed = Date.now()
        // Move host to hidden pool container — root stays alive, just detached
        getPoolContainer().appendChild(entry.host)
      } else {
        // host was replaced (code changed mid-life) — unmount normally
        rootRef.current?.unmount()
        rootPool.delete(key)
      }
      rootRef.current = null
      hostRef.current = null
    }
  }, [])

  // Native click delegation for data-action. The sub-root's events do NOT bubble
  // to this element's React synthetic handler (separate root), so a native
  // capture listener on the host div is required.
  useEffect(() => {
    const el = hostRef.current
    if (!el) return
    const handler = (e: MouseEvent) => {
      let node = e.target as HTMLElement | null
      while (node && node !== el) {
        const action = node.getAttribute?.('data-action')
        if (action) {
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

  // ── Escape-proof container (host-enforced, independent of generated code) ──
  // `contain: layout paint` guarantees, per CSS spec, that NO descendant can
  // escape this box, however the generated code positions itself:
  //   1. PAINT CLIPPING — every descendant's drawing is clipped to this box:
  //      position:fixed/absolute, negative offsets, oversized elements, all of it.
  //   2. CONTAINING BLOCK — this element becomes the positioning ancestor for
  //      fixed/absolute descendants, so a `fixed inset-0` modal anchors to the
  //      PANEL (a sane in-panel modal) instead of leaking over the whole row.
  //   3. STACKING CONTEXT — a descendant with z-index: 2147483647 can never
  //      paint above the host app chrome.
  // The className prop (max-h + overflow-auto from the renderer) is applied to
  // the HOST div (the createRoot container) — inner scrolling works there; the
  // containment layer here never scrolls and never lets anything out.
  return (
    <div
      ref={containerRef}
      className="w-full"
      style={{ contain: 'layout paint', overflow: 'hidden' }}
    />
  )
}

// ─── Partial TSX completion: now using partial-tsx (macaron's library) ──
// Replaced the hand-rolled completePartialTsx with the battle-tested
// normalizeGeneratedTsx from partial-tsx (macaron-genui-demo).
// It handles all edge cases: JSX tags, expressions, template literals,
// regex, comments, ASI, ternary, function declarations, etc.