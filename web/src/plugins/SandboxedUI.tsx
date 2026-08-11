/**
 * SandboxedUI — renders free-form plugin UI source in an isolated iframe.
 *
 * Generalized from GenUIBlock (display_html renderer):
 *   - code mode: compiles TSX/JS with sucrase, mounts a separate React root
 *     inside the iframe (allow-scripts allow-same-origin), so plugin code
 *     can never touch the parent page DOM.
 *   - src mode: loads an external trusted URL in the same sandboxed iframe.
 *
 * Interactions: `data-action="..."` elements bubble up → onAction(action, data)
 * which the parent routes to the web_ui_action RPC (→ plugin process).
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { transform } from 'sucrase'

export interface SandboxedUIProps {
  /** Free-form TSX/JS source (code mode). Mutually exclusive with src. */
  code?: string
  /** External trusted URL (custom mode). */
  src?: string
  widgetId?: string
  /** Fired on data-action clicks. */
  onAction?: (action: string, data: string) => void
  className?: string
}

/** Message types allowed across the iframe boundary (whitelist). */
type UIRequestMessage =
  | { type: 'ui_action'; action: string; data: string }
  | { type: 'ui_resize'; height: number }
  | { type: 'ui_wheel'; deltaY: number }

export function SandboxedUI({ code, src, widgetId, onAction, className }: SandboxedUIProps) {
  const iframeRef = useRef<HTMLIFrameElement>(null)
  const rootRef = useRef<Root | null>(null)
  const [height, setHeight] = useState(0)

  // ─── src mode: plain iframe load ───────────────────────────────
  if (src && !code) {
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

  // ─── code mode: compile + separate React root ──────────────────
  const [component, setComponent] = useState<React.ComponentType | null>(null)
  const compileSeqRef = useRef(0)

  useEffect(() => {
    if (!code || code.trim().length < 10) return
    const seq = ++compileSeqRef.current
    let cancelled = false
    ;(async () => {
      try {
        let clean = code.trim()
        if (clean.startsWith('```')) {
          clean = clean.replace(/^```(?:tsx|jsx|ts|js)?\s*\n?/i, '').replace(/\n?```\s*$/i, '')
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
        if (cancelled || seq !== compileSeqRef.current) return
        if (Comp && typeof Comp === 'function') setComponent(() => Comp)
      } catch {
        if (cancelled || seq !== compileSeqRef.current) return
      }
    })()
    return () => {
      cancelled = true
    }
  }, [code])

  // Initialize iframe document + React root.
  useEffect(() => {
    const iframe = iframeRef.current
    if (!iframe) return
    const doc = iframe.contentDocument
    if (!doc) return
    doc.open()
    doc.write(`<!DOCTYPE html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<style>html,body{margin:0;padding:0;background:#fff;overflow:hidden}body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif}*{box-sizing:border-box}</style>
</head><body></body></html>`)
    doc.close()
    if (!rootRef.current) {
      rootRef.current = createRoot(doc.body)
    }
    return () => {
      rootRef.current?.unmount()
      rootRef.current = null
    }
  }, [])

  // Render component + delegate data-action clicks.
  const handleClick = useCallback(
    (e: React.MouseEvent) => {
      let el = e.target as HTMLElement | null
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
          onAction?.(action, JSON.stringify(data))
          return
        }
        el = el.parentElement
      }
    },
    [onAction],
  )

  useEffect(() => {
    if (!rootRef.current || !component) return
    rootRef.current.render(
      React.createElement(
        component,
        { 'data-sandboxed-ui-root': true, onClick: handleClick } as Record<string, unknown>,
      ),
    )
  }, [component, handleClick])

  // Height measurement via ResizeObserver.
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
  }, [component, height])

  if (!code || code.trim().length < 10) return null
  return (
    <iframe
      ref={iframeRef}
      className={`w-full rounded-lg border border-slate-200 ${className ?? ''}`}
      style={{ height: height > 0 ? `${height}px` : '120px', backgroundColor: '#fff' }}
      sandbox="allow-scripts allow-same-origin"
      title={`plugin-ui-${widgetId ?? 'widget'}`}
    />
  )
}
