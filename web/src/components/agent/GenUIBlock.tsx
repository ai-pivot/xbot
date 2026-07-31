/**
 * GenUIBlock — renders LLM-generated TSX in an isolated iframe.
 *
 * Architecture:
 * 1. Compilation cache: Map<hash, ComponentType> — skip recompile if hash matches
 * 2. Throttled compilation: 100ms during streaming, immediate when not streaming
 * 3. Last-good retention: keep previous component on compile failure (no flash to empty)
 * 4. ResizeObserver for height: no setTimeout polling, instant height updates
 * 5. Separate React root in iframe via createRoot
 */
import React, { useState, useRef, useEffect, useCallback } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { transform } from 'sucrase'
import { useWSConnection } from '@/hooks/useWSConnection'

// ─── Compilation cache ─────────────────────────────────────────
// Module-level cache: key = code hash, value = compiled component.
// Survives component unmount/remount (e.g. scroll virtualization).
const compileCache = new Map<string, React.ComponentType>()
const CACHE_MAX = 8

function codeHash(code: string): string {
  // Fast hash: length + first 32 + last 32 chars. Good enough for
  // detecting incremental changes during streaming.
  if (code.length <= 80) return code
  return `${code.length}:${code.slice(0, 32)}…${code.slice(-32)}`
}

// ─── Error Boundary ────────────────────────────────────────────
// Catches render errors (e.g. invalid SVG attributes) so they don't
// crash the entire React tree. Shows fallback on error.
class GenUIErrorBoundary extends React.Component<
  { children?: React.ReactNode; fallback: React.ReactNode },
  { hasError: boolean }
> {
  constructor(props: { children?: React.ReactNode; fallback: React.ReactNode }) {
    super(props)
    this.state = { hasError: false }
  }
  override componentDidCatch() {
    this.setState({ hasError: true })
  }
  override render() {
    return this.state.hasError ? this.props.fallback : this.props.children
  }
}

// ─── Component ────────────────────────────────────────────────

interface GenUIBlockProps {
  code: string
  chatId?: string
  uiSource?: string
  streaming?: boolean
  onAction?: (action: string, data: string) => void
}

export function GenUIBlock({ code, chatId, uiSource, streaming = false, onAction }: GenUIBlockProps) {
  const ws = useWSConnection()
  const effectiveChatId = chatId || ws.chatID || ''
  const iframeRef = useRef<HTMLIFrameElement>(null)
  const [iframeHeight, setIframeHeight] = useState(0)
  const [component, setComponent] = useState<React.ComponentType | null>(null)
  const rootRef = useRef<Root | null>(null)

  // Throttle refs
  const codeRef = useRef(code)
  const timerRef = useRef<number | null>(null)
  const compileSeqRef = useRef(0)
  const lastRenderRef = useRef(0)

  // Forward wheel events from iframe to parent: iframe has overflow:hidden
  // so wheel events are swallowed. This lets the parent chat history scroll
  // when the user scrolls inside the GenUI iframe.
  useEffect(() => {
    const handler = (e: MessageEvent) => {
      if (e.data?.type !== 'genui_wheel') return
      window.scrollBy({ top: e.data.deltaY, behavior: 'auto' })
    }
    window.addEventListener('message', handler)
    return () => window.removeEventListener('message', handler)
  }, [])

  // Schedule compilation with throttle: max 100ms between renders during streaming.
  useEffect(() => {
    codeRef.current = code
    // Non-streaming (history or final): compile immediately
    if (!streaming) {
      if (timerRef.current) { clearTimeout(timerRef.current); timerRef.current = null }
      compileAndLoad(code, ++compileSeqRef.current, false)
      return
    }
    // Streaming: if timer already pending, just update codeRef (timer will pick it up).
    if (timerRef.current) return
    const elapsed = Date.now() - lastRenderRef.current
    const delay = Math.max(0, 100 - elapsed)
    timerRef.current = window.setTimeout(() => {
      timerRef.current = null
      lastRenderRef.current = Date.now()
      compileAndLoad(codeRef.current, ++compileSeqRef.current, true)
    }, delay)
  }, [code, streaming])

  // Compile TSX → JS → evaluate with React injected as parameter
  const compileAndLoad = useCallback(async (tsx: string, seq: number, isStreaming: boolean) => {
    if (!tsx || tsx.trim().length < 10) return

    const hash = codeHash(tsx)

    // Cache check (both streaming and non-streaming)
    const cached = compileCache.get(hash)
    if (cached) {
      if (seq !== compileSeqRef.current) return
      setComponent(() => cached)
      return
    }

    try {
      let clean = tsx.trim()
      // Strip markdown fences
      if (clean.startsWith('```')) {
        clean = clean.replace(/^```(?:tsx|jsx|ts|js)?\s*\n?/i, '').replace(/\n?```\s*$/i, '')
      }

      // Auto-close only during streaming
      if (isStreaming) {
        try { clean = autoClose(clean) } catch { /* best-effort */ }
      }

      // Extract <style> blocks (Sucrase misparses CSS braces in <style>)
      const styleBlocks: string[] = []
      clean = clean.replace(/<style>([\s\S]*?)<\/style>/g, (_, content) => {
        const m = content.match(/^\{`([\s\S]*?)`\}$/)
        styleBlocks.push(m ? m[1] : content)
        return `<style>{__STYLE_${styleBlocks.length - 1}__}</style>`
      })

      // Compile TSX → JS
      const { code: js } = transform(clean, {
        transforms: ['typescript', 'jsx'],
        jsxRuntime: 'classic',
        production: true,
      })

      // Restore style blocks
      let restored = js
      for (let i = 0; i < styleBlocks.length; i++) {
        restored = restored.replace(`__STYLE_${i}__`, '`' + styleBlocks[i].replace(/`/g, '\\`') + '`')
      }

      // Strip imports/exports
      const noImports = restored
        .replace(/^\s*import\s+.*$/gm, '')
        .replace(/^\s*export\s+default\s+/gm, '')
        .replace(/^\s*export\s+/gm, '')

      // Wrap with React injection
      const wrapped = `
        const React = arguments[0];
        const { createElement, useState, useEffect, useMemo, useRef, useCallback,
                useContext, useReducer, useLayoutEffect, Fragment, forwardRef,
                useId, useSyncExternalStore, useTransition, useDeferredValue } = React;
        ${noImports}
        return typeof App !== 'undefined' ? App : null;
      `

      const fn = new Function(wrapped)
      const Comp = fn(React)

      if (seq !== compileSeqRef.current) return

      if (Comp && typeof Comp === 'function') {
        // Cache the result
        if (compileCache.size >= CACHE_MAX) {
          const firstKey = compileCache.keys().next().value
          if (firstKey) compileCache.delete(firstKey)
        }
        compileCache.set(hash, Comp)
        setComponent(() => Comp)
      }
      // On failure: keep previous component (no flash to empty)
    } catch {
      if (seq !== compileSeqRef.current) return
    }
  }, []) // no deps — streaming is passed as parameter

  // Initialize iframe document + React root
  useEffect(() => {
    if (!iframeRef.current) return
    const doc = iframeRef.current.contentDocument
    if (!doc) return
    const twLink = document.querySelector('link[href*="/assets/index-"][href$=".css"]') as HTMLLinkElement | null
    const twHref = twLink?.href || ''
    doc.open()
    doc.write(`<!DOCTYPE html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
${twHref ? `<link rel="stylesheet" href="${twHref}">` : ''}
<style>html,body{margin:0;padding:0;background:#fff;overflow:hidden}body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif}*{box-sizing:border-box}</style>
</head><body></body></html>`)
    doc.close()

    // Add wheel event forwarding via JS (not inline <script> which breaks
    // React 19's createRoot under allow-scripts allow-same-origin sandbox).
    doc.addEventListener('wheel', (e: WheelEvent) => {
      let el = e.target as HTMLElement | null
      let isScrollable = false
      while (el && el !== doc.body && el !== doc.documentElement) {
        const style = doc.defaultView?.getComputedStyle(el)
        if (style && (
          (style.overflow === 'auto' || style.overflow === 'scroll' ||
           style.overflowY === 'auto' || style.overflowY === 'scroll') &&
          el.scrollHeight > el.clientHeight
        )) {
          isScrollable = true
          break
        }
        el = el.parentElement
      }
      if (!isScrollable) {
        window.scrollBy({ top: e.deltaY })
      }
    }, { passive: true })

    // Reuse existing root if body already has one (prevents React #299)
    if (!rootRef.current) {
      rootRef.current = createRoot(doc.body)
    }
    return () => {
      rootRef.current?.unmount()
      rootRef.current = null
    }
  }, [])

  // Height measurement via ResizeObserver
  const heightRef = useRef(0)
  useEffect(() => {
    const iframe = iframeRef.current
    if (!iframe) return
    const doc = iframe.contentDocument
    if (!doc?.body) return

    const measure = () => {
      const h = doc.body.scrollHeight
      // Only update if height changed by more than 2px (prevents scrollbar
      // oscillation: setting height → scrollbar appears → scrollHeight changes
      // → ResizeObserver fires → set height again → infinite loop)
      if (Math.abs(h - heightRef.current) <= 2) return
      if (streaming) {
        // During streaming: only grow, never shrink
        if (h > heightRef.current) {
          heightRef.current = h
          setIframeHeight(h)
        }
      } else {
        // Final/historical: set to actual height
        heightRef.current = h
        setIframeHeight(h)
      }
    }

    const ro = new ResizeObserver(measure)
    ro.observe(doc.body)
    measure()
    return () => ro.disconnect()
  }, [component, streaming])

  // data-action click delegation
  const handleClick = useCallback((e: React.MouseEvent) => {
    const target = e.target as HTMLElement
    let el: HTMLElement | null = target
    while (el && el !== e.currentTarget) {
      const action = el.getAttribute('data-action')
      if (action) {
        e.stopPropagation()
        const data: Record<string, string> = {}
        for (const attr of Array.from(el.attributes)) {
          if (attr.name.startsWith('data-') && attr.name !== 'data-action') {
            data[attr.name.slice(5)] = attr.value
          }
        }
        ws.rpc('genui_action', {
          chat_id: effectiveChatId,
          action,
          data: JSON.stringify(data),
          ui_source: uiSource,
        }).catch(() => {})
        onAction?.(action, JSON.stringify(data))
        return
      }
      el = el.parentElement
    }
  }, [ws, effectiveChatId, uiSource, onAction])

  // Render component into iframe root, wrapped in error boundary
  useEffect(() => {
    if (!rootRef.current) return
    if (component) {
      rootRef.current.render(
        React.createElement(GenUIErrorBoundary,
          { fallback: '⚠️ Render error — check SVG/HTML syntax' },
          React.createElement(component, { 'data-genui-root': true as const, onClick: handleClick } as Record<string, unknown>)
        )
      )
    }
  }, [component, handleClick])

  // Lazy: don't render iframe until code exists
  if (!code || code.trim().length < 10) return null

  return (
    <iframe
      ref={iframeRef}
      className="w-full rounded-xl border border-gray-200 dark:border-gray-700 overflow-hidden transition-[height] duration-150 ease-out"
      style={{ height: iframeHeight > 0 ? `${iframeHeight}px` : '120px', backgroundColor: '#fff' }}
      sandbox="allow-scripts allow-same-origin"
      title="GenUI Preview"
    />
  )
}

// ─── Auto-close (streaming only) ──────────────────────────────

function autoClose(source: string): string {
  let out = source
  const stack: string[] = []
  let i = 0
  while (i < out.length) {
    const ch = out[i]
    const next = out[i + 1]
    if (ch === '/' && next === '/') { i = out.indexOf('\n', i); if (i === -1) break; i++; continue }
    if (ch === '/' && next === '*') { i = out.indexOf('*/', i + 2); if (i === -1) break; i += 2; continue }
    if (ch === '"' || ch === "'") { const q = ch; i++; while (i < out.length) { if (out[i] === '\\') { i += 2; continue } if (out[i] === q) { i++; break } i++ } continue }
    if (ch === '`') { i++; while (i < out.length) { if (out[i] === '\\') { i += 2; continue } if (out[i] === '$' && out[i + 1] === '{') { stack.push('`'); i += 2; break } if (out[i] === '`') { i++; break } i++ } continue }
    if (ch === '{') { stack.push('{'); i++; continue }
    if (ch === '(') { stack.push('('); i++; continue }
    if (ch === '[') { stack.push('['); i++; continue }
    if (ch === '}') { popStack(stack, '{'); i++; continue }
    if (ch === ')') { popStack(stack, '('); i++; continue }
    if (ch === ']') { popStack(stack, '['); i++; continue }
    if (ch === '<' && isAlpha(out[i + 1])) {
      let j = i + 1
      while (j < out.length && (isAlnum(out[j]) || out[j] === '-' || out[j] === '.')) j++
      const tagName = out.slice(i + 1, j)
      const close = out.indexOf('>', j)
      if (close !== -1 && out[close - 1] === '/') { i = close + 1; continue }
      stack.push(`<${tagName}`)
      i = j
      continue
    }
    if (ch === '<' && next === '/') {
      const close = out.indexOf('>', i)
      if (close !== -1) {
        const tagContent = out.slice(i + 2, close).trim()
        const idx = stack.lastIndexOf(`<${tagContent}`)
        if (idx !== -1) stack.splice(idx)
        i = close + 1
        continue
      }
    }
    i++
  }
  const closers: string[] = []
  for (let k = stack.length - 1; k >= 0; k--) {
    const s = stack[k]
    if (s === '{') closers.push('}')
    else if (s === '(') closers.push(')')
    else if (s === '[') closers.push(']')
    else if (s === '`') closers.push('}')
    else if (s.startsWith('<')) closers.push(`</${s.slice(1)}>`)
  }
  if (closers.length > 0) out += closers.join('')
  return out
}

function popStack(stack: string[], expected: string) {
  for (let k = stack.length - 1; k >= 0; k--) {
    if (stack[k] === expected) { stack.splice(k); return }
    if (stack[k].startsWith('<')) { stack.splice(k); return }
  }
}

function isAlpha(ch: string | undefined): boolean {
  if (!ch) return false
  return /[a-zA-Z]/.test(ch)
}

function isAlnum(ch: string | undefined): boolean {
  if (!ch) return false
  return /[a-zA-Z0-9]/.test(ch)
}
