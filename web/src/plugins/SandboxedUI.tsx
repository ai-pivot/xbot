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

import React, { useCallback, useEffect, useRef, useState } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { flushSync } from 'react-dom'
import { transform } from 'sucrase'

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

// ─── Stable host slot ──────────────────────────────────────────
interface UISlot {
  current: React.ComponentType | null
  lastGood: React.ComponentType | null
}

/** Module-level STABLE host: same fiber across regenerations → state preserved. */
function UIHost({ slot, failed }: { slot: UISlot; failed: string | null }) {
  if (failed && !slot.lastGood) {
    return <div className="p-3 text-xs text-red-600 dark:text-red-400">⚠️ UI render error: {failed}</div>
  }
  const Current = slot.current || slot.lastGood
  if (!Current) return null
  // ⚠️ MUST use createElement(Current) (NOT call Current(props) directly).
  // The direct-call trick binds the generated component's hooks to THIS fiber, so
  // they're preserved across regenerations — but LLM streaming CHANGES the hook
  // count (partial code has fewer useState), which throws React #310
  // ("Rendered more hooks than during the previous render") → broken state.
  // createElement gives Current its OWN fiber: a STABLE Current (compiler-cache
  // hit on the final code) is reconciled (state preserved); a changing Current
  // (streaming) remounts harmlessly — NO #310.
  return React.createElement(Current, { 'data-sandboxed-ui-root': true } as Record<string, unknown>)
}

/** Catches render-time errors so they can't crash the host app.
 *  Streaming 期间不 surface 瞬时错误（macaron streaming 模式）：渲染失败时
 *  显示 lastGood（连续预览）或静默占位，绝不显示「Render error」；
 *  仅 final（committed）渲染失败才显示错误占位。 */
class UIErrorBoundary extends React.Component<
  { children?: React.ReactNode; fallback: React.ReactNode; streaming?: boolean; slot?: UISlot },
  { hasError: boolean }
> {
  constructor(props: { children?: React.ReactNode; fallback: React.ReactNode; streaming?: boolean; slot?: UISlot }) {
    super(props)
    this.state = { hasError: false }
  }
  static getDerivedStateFromError(): { hasError: boolean } {
    return { hasError: true }
  }
  override render() {
    if (!this.state.hasError) return this.props.children
    if (this.props.streaming) {
      // 流式：回退到 lastGood（连续预览不闪白），无 lastGood 则静默占位。
      const lastGood = this.props.slot?.lastGood
      if (lastGood) return React.createElement(lastGood, { 'data-sandboxed-ui-root': true } as Record<string, unknown>)
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
        try { clean = completePartialTsx(clean) } catch { /* best-effort */ }
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
      const wrapped = `
        const React = arguments[0];
        const { createElement, useState, useEffect, useMemo, useRef, useCallback,
                useContext, useReducer, useLayoutEffect, Fragment, forwardRef,
                useId, useSyncExternalStore, useTransition, useDeferredValue,
                useImperativeHandle, useDebugValue, memo, Children } = React;
        ${noImports}
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
      if (!isStreaming && !slotRef.current.lastGood) {
        setFailed(e instanceof Error ? e.message : 'compile failed')
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
  useEffect(() => {
    if (!hostRef.current) return
    if (!rootRef.current) rootRef.current = createRoot(hostRef.current)
    // ⚠️ flushSync 同步渲染子 root：行高度在一次 React 循环内确定，避免 createRoot
    // 异步渲染导致的"子 root 内容未渲染(高度≈0)→渲染后变高"二次变化。TanStack
    // Virtual 的 measureElement(ResizeObserver) 会反复捕获这个高度变化 → 每次虚拟化
    // 进出视口(genui 行 remount)都重新 createRoot → 0→实际 → 滚动跳变。同步渲染后
    // 行高立即稳定，measure 一次校正，无跳变。
    flushSync(() => {
      rootRef.current!.render(
        React.createElement(
          UIErrorBoundary,
          // streaming 期间不 surface 瞬时错误（macaron streaming 模式）：fallback
          // 依赖 streaming 分支，渲染失败回退 lastGood/占位，绝不显示「Render error」。
          {
            fallback: '⚠️ Render error — check the generated UI syntax',
            streaming,
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
      className={`sandboxed-ui w-full overflow-auto rounded-lg border border-slate-200 ${className ?? ''}`}
      style={{ minHeight: streaming ? 120 : undefined }}
      data-widget-id={widgetId}
    />
  )
}

// ─── Partial TSX completion (streaming only) ───────────────────
// macaron 式流式安全补全。核心语义：
//   - JSX 打开标签（有 '>'）是"完整结构" → 补 </tag> 闭合；
//   - 未完成的表达式 `{expr` / `fn(` / `[...` / 模板 `` ` `` —— 内核不确定会闭合，
//     补齐 `}`/`)` 往往生成非法代码（如 `{data.map(x =>}`）。因此回滚（截断）到该
//     分隔符的起点之前 —— 让代码停在"上一个语义完整点"，必然可编译可预览；
//   - 截断后剩余的函数体 `{` / return `(` / 完整 JSX 元素 → 补齐。
// 编译失败仍由 compileAndLoad 回退 lastGood（+ ErrorBoundary streaming 不 surface），
// 故流式任何瞬间都不会出现「Render error」（macaron streaming 模式）。
function completePartialTsx(source: string): string {
  let out = source
  // startIdx = 分隔符在 source 中的起点，用于表达式回滚截断。
  const stack: Array<{ kind: 'tag' | 'brace' | 'paren' | 'bracket' | 'tpl'; name: string; hadGt: boolean; startIdx: number }> = []
  let i = 0
  while (i < out.length) {
    const ch = out[i]
    const next = out[i + 1]
    if (out.startsWith('//', i)) { const nl = out.indexOf('\n', i); if (nl === -1) break; i = nl + 1; continue }
    if (out.startsWith('/*', i)) { const end = out.indexOf('*/', i + 2); if (end === -1) break; i = end + 2; continue }
    if (ch === '"' || ch === "'") {
      const q = ch
      let j = i + 1
      let closed = false
      while (j < out.length) { if (out[j] === '\\') { j += 2; continue } if (out[j] === q) { closed = true; j++; break } j++ }
      if (!closed) { out = out.slice(0, i) + q; break }
      i = j
      continue
    }
    if (ch === '`') {
      let j = i + 1
      let closed = false
      while (j < out.length) { if (out[j] === '\\') { j += 2; continue } if (out[j] === '$' && out[j + 1] === '{') { break } if (out[j] === '`') { closed = true; j++; break } j++ }
      if (!closed) { stack.push({ kind: 'tpl', name: '`', hadGt: false, startIdx: i }); break }
      i = j
      continue
    }
    if (ch === '{') { stack.push({ kind: 'brace', name: '{', hadGt: false, startIdx: i }); i++; continue }
    if (ch === '(') { stack.push({ kind: 'paren', name: '(', hadGt: false, startIdx: i }); i++; continue }
    if (ch === '[') { stack.push({ kind: 'bracket', name: '[', hadGt: false, startIdx: i }); i++; continue }
    if (ch === '}') { popTop(stack, 'brace'); i++; continue }
    if (ch === ')') { popTop(stack, 'paren'); i++; continue }
    if (ch === ']') { popTop(stack, 'bracket'); i++; continue }
    if (ch === '<' && /[a-zA-Z]/.test(next)) {
      let j = i + 1
      while (j < out.length && /[a-zA-Z0-9.-]/.test(out[j])) j++
      const name = out.slice(i + 1, j)
      const gt = out.indexOf('>', j)
      if (gt === -1) { out = out.slice(0, i); break } // mid-tag → 回滚到 '<' 前
      const isSelfClose = out[gt - 1] === '/'
      stack.push({ kind: 'tag', name, hadGt: !isSelfClose, startIdx: i })
      if (isSelfClose) stack.pop()
      i = gt + 1
      continue
    }
    if (ch === '<' && next === '/') {
      const gt = out.indexOf('>', i)
      if (gt !== -1) { popTag(stack, out.slice(i + 2, gt).trim()); i = gt + 1; continue }
    }
    i++
  }

  // 找最内层未闭合的"表达式类"分隔符（brace/paren/bracket/tpl）。若存在，回滚
  // （截断）到其起点 —— 该表达式可能未完成，补齐会非法。这使代码停在语义完整点。
  let cutIdx = -1
  for (let k = stack.length - 1; k >= 0; k--) {
    const s = stack[k]
    if (s.kind === 'brace' || s.kind === 'paren' || s.kind === 'bracket' || s.kind === 'tpl') {
      cutIdx = s.startIdx
      break
    }
  }
  if (cutIdx >= 0) {
    out = out.slice(0, cutIdx)
    // 截断后，栈中该分隔符及其后的项不再存在；其前的 JSX 完整标签仍保留。
    // 重建栈：只保留 startIdx < cutIdx 的项。
    const kept = stack.filter((s) => s.startIdx < cutIdx)
    const closers = buildClosers(kept)
    return out + closers
  }

  // 没有表达式未闭合 —— 只需补齐完整 JSX 标签。
  return out + buildClosers(stack)
}

function buildClosers(stack: Array<{ kind: 'tag' | 'brace' | 'paren' | 'bracket' | 'tpl'; name: string; hadGt: boolean; startIdx: number }>): string {
  const closers: string[] = []
  for (let k = stack.length - 1; k >= 0; k--) {
    const s = stack[k]
    if (s.kind === 'brace') closers.push('}')
    else if (s.kind === 'paren') closers.push(')')
    else if (s.kind === 'bracket') closers.push(']')
    else if (s.kind === 'tpl') closers.push('}')
    else if (s.kind === 'tag') {
      if (!s.hadGt) closers.push('>')
      closers.push(`</${s.name}>`)
    }
  }
  return closers.join('')
}

function popTop(stack: Array<{ kind: string }>, kind: string) {
  for (let k = stack.length - 1; k >= 0; k--) {
    if (stack[k].kind === kind) { stack.splice(k, 1); return }
  }
}
function popTag(stack: Array<{ kind: string; name: string }>, name: string) {
  for (let k = stack.length - 1; k >= 0; k--) {
    if (stack[k].kind === 'tag' && stack[k].name === name) { stack.splice(k, 1); return }
  }
}