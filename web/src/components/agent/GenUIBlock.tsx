/**
 * GenUIBlock — renders LLM-generated TSX in an isolated iframe via React portal.
 *
 * Architecture:
 * 1. LLM generates TSX code (React component with hooks)
 * 2. Sucrase compiles TSX → JS in the browser (no WASM, no Babel)
 * 3. JS is loaded as a Blob URL module
 * 4. React component renders via createPortal into iframe's body
 * 5. iframe provides style isolation; Tailwind CSS loaded via same-origin <link>
 * 6. data-action clicks are delegated and sent back to agent via onAction
 *
 * Performance:
 * - Lazy: iframe only created when code is non-empty
 * - Debounced: code updates batched via rAF, max 1 compile/render per frame
 * - Height locked: iframe height only grows, never shrinks during streaming
 *   (prevents layout jitter). Final height measured after stream completes.
 */
import React, { useState, useRef, useEffect, useMemo, useCallback } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { transform } from 'sucrase'
import { useWSConnection } from '@/hooks/useWSConnection'

interface GenUIBlockProps {
  code: string
  chatId?: string
  uiSource?: string
  /** true during streaming — enables debouncing and height locking */
  streaming?: boolean
  onAction?: (action: string, data: string) => void
}

export function GenUIBlock({ code, chatId, uiSource, streaming = false, onAction }: GenUIBlockProps) {
  const ws = useWSConnection()
  const effectiveChatId = chatId || ws.chatID || ''
  const iframeRef = useRef<HTMLIFrameElement>(null)
  const [iframeBody, setIframeBody] = useState<HTMLElement | null>(null)
  const [iframeHeight, setIframeHeight] = useState(0)
  const [component, setComponent] = useState<React.ComponentType | null>(null)
  const rootRef = useRef<Root | null>(null)
  const [error, setError] = useState<string | null>(null)

  // Debounce code updates: batch via rAF so streaming tokens don't cause
  // excessive recompiles. Only compile once per frame.
  const codeRef = useRef(code)
  const rafRef = useRef<number | null>(null)
  const lastCompiledRef = useRef('')
  // Track the latest compile request to discard stale results (race condition:
  // a new compile starts before the previous import() resolves)
  const compileSeqRef = useRef(0)

  useEffect(() => {
    codeRef.current = code
    if (rafRef.current !== null) return
    rafRef.current = requestAnimationFrame(() => {
      rafRef.current = null
      const currentCode = codeRef.current
      if (currentCode === lastCompiledRef.current) return
      lastCompiledRef.current = currentCode
      compileAndLoad(currentCode, ++compileSeqRef.current)
    })
    return () => {
      if (rafRef.current !== null) {
        cancelAnimationFrame(rafRef.current)
        rafRef.current = null
      }
    }
  }, [code])

  // Compile TSX → JS → evaluate with React injected as parameter
  // No Blob URL, no esm.sh — uses new Function with host React instance.
  // This avoids CSP issues (no blob: or external script-src needed).
  const compileAndLoad = useCallback(async (tsx: string, seq: number) => {
    if (!tsx || tsx.trim().length < 10) {
      return
    }
    try {
      // Strip markdown fences
      let clean = tsx.trim()
      if (clean.startsWith('```')) {
        clean = clean.replace(/^```(?:tsx|jsx|ts|js)?\s*\n?/i, '').replace(/\n?```\s*$/i, '')
      }

      // Auto-close unterminated JSX/braces during streaming only.
      // For complete code (history or final), autoClose can add wrong
      // closers and break compilation. Only run when streaming.
      if (streaming) {
        try {
          clean = autoClose(clean)
        } catch {
          // autoClose is best-effort
        }
      }

      // Extract <style> blocks before compilation — Sucrase misparses CSS
      // braces inside <style> tags as JSX expression containers.
      // Replace the entire <style>...</style> with a <style> tag that has
      // its content stored in a variable, injected after compilation.
      const styleBlocks: string[] = []
      clean = clean.replace(/<style>([\s\S]*?)<\/style>/g, (_, content) => {
        // Check if content is a template literal expression {`...`}
        const tmplMatch = content.match(/^\{`([\s\S]*?)`\}$/)
        if (tmplMatch) {
          styleBlocks.push(tmplMatch[1])
        } else {
          styleBlocks.push(content)
        }
        return `<style>{__STYLE_${styleBlocks.length - 1}__}</style>`
      })

      // Compile TSX → JS with Sucrase (classic runtime → React.createElement)
      const { code: js } = transform(clean, {
        transforms: ['typescript', 'jsx'],
        jsxRuntime: 'classic',
        production: true,
      })

      // Restore style blocks: replace placeholder with template literal
      let restored = js
      for (let i = 0; i < styleBlocks.length; i++) {
        restored = restored.replace(
          `__STYLE_${i}__`,
          '`' + styleBlocks[i].replace(/`/g, '\\`') + '`'
        )
      }

      // Remove all import/export statements — React is injected as parameter.
      // `export default function App()` becomes `function App()` and we
      // return App from the wrapper function.
      const noImports = restored
        .replace(/^\s*import\s+.*$/gm, '')        // remove import lines
        .replace(/^\s*export\s+default\s+/gm, '')  // `export default function App` → `function App`
        .replace(/^\s*export\s+/gm, '')            // `export function` → `function`

      // Wrap in function: React + hooks passed as arguments
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

      console.log('[GenUI] compileAndLoad result:', { seq, hasComp: !!Comp, compType: typeof Comp, codeLen: tsx.length })

      if (Comp && typeof Comp === 'function') {
        setComponent(() => Comp)
        setError(null)
      } else {
        console.log('[GenUI] No App found — code may be incomplete')
      }
    } catch (e) {
      console.error('[GenUI] compileAndLoad error:', e instanceof Error ? e.message : e, 'codeLen:', tsx.length)
      if (seq !== compileSeqRef.current) return
    }
  }, [])

  // Initialize iframe document once, create React root inside iframe
  useEffect(() => {
    if (!iframeRef.current) return
    const iframe = iframeRef.current
    const doc = iframe.contentDocument
    if (!doc) return
    const twLink = document.querySelector('link[href*="/assets/index-"][href$=".css"]') as HTMLLinkElement | null
    const twHref = twLink?.href || ''
    doc.open()
    doc.write(`<!DOCTYPE html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
${twHref ? `<link rel="stylesheet" href="${twHref}">` : ''}
<style>html,body{margin:0;padding:0;background:#fff}body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif}*{box-sizing:border-box}</style>
</head><body></body></html>`)
    doc.close()
    // Create a separate React root inside the iframe.
    // This is required because React 19's createPortal from the host root
    // doesn't work reliably into a sandboxed iframe document.
    rootRef.current = createRoot(doc.body)
    console.log('[GenUI] iframe init done, root created:', !!rootRef.current, 'twHref:', twHref?.slice(0, 60))
    setIframeBody(doc.body)
    return () => {
      rootRef.current?.unmount()
      rootRef.current = null
    }
  }, [])

  // Measure height — but only grow, never shrink during streaming
  const heightRef = useRef(0)
  useEffect(() => {
    if (!iframeRef.current || !iframeBody) return
    const measure = () => {
      const doc = iframeRef.current?.contentDocument
      if (!doc?.body) return
      const h = Math.max(doc.body.scrollHeight, doc.documentElement.scrollHeight)
      if (h > heightRef.current) {
        heightRef.current = h
        setIframeHeight(h + 4)
      } else if (!streaming) {
        // Allow shrink when not streaming (final state)
        setIframeHeight(h + 4)
      }
    }
    measure()
    const t1 = setTimeout(measure, 50)
    const t2 = setTimeout(measure, 200)
    const t3 = setTimeout(measure, 500)
    return () => { clearTimeout(t1); clearTimeout(t2); clearTimeout(t3) }
  }, [component, iframeBody, streaming])

  // Handle data-action clicks via event delegation
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

  // Render component into iframe via separate React root (not createPortal)
  useEffect(() => {
    if (!rootRef.current) return
    console.log('[GenUI] render effect:', { hasComponent: !!component, hasRoot: !!rootRef.current })
    if (component) {
      try {
        rootRef.current.render(
          React.createElement(component, { 'data-genui-root': true, onClick: handleClick })
        )
        console.log('[GenUI] render succeeded')
      } catch (e) {
        console.error('[GenUI] render failed:', e instanceof Error ? e.message : e)
      }
    } else {
      rootRef.current.render(null)
    }
  }, [component, handleClick])

  // Don't render iframe until we have code (lazy)
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

/**
 * Auto-close unterminated JSX tags, braces, parens, and template literals
 * during streaming. This is a best-effort heuristic — the goal is to make
 * partial code compileable so the user sees a live preview, not to be a
 * perfect parser. The full code will arrive as the stream completes.
 */
function autoClose(source: string): string {
  let out = source
  const stack: string[] = []

  // Walk the string, tracking open/close of JSX tags, braces, parens, brackets
  // and template literals. Skip strings and comments.
  let i = 0
  while (i < out.length) {
    const ch = out[i]
    const next = out[i + 1]

    // Line comment
    if (ch === '/' && next === '/') {
      i = out.indexOf('\n', i)
      if (i === -1) break
      i++
      continue
    }
    // Block comment
    if (ch === '/' && next === '*') {
      i = out.indexOf('*/', i + 2)
      if (i === -1) break
      i += 2
      continue
    }
    // String literals (single, double)
    if (ch === '"' || ch === "'") {
      const q = ch
      i++
      while (i < out.length) {
        if (out[i] === '\\') { i += 2; continue }
        if (out[i] === q) { i++; break }
        i++
      }
      continue
    }
    // Template literal
    if (ch === '`') {
      i++
      while (i < out.length) {
        if (out[i] === '\\') { i += 2; continue }
        if (out[i] === '$' && out[i + 1] === '{') {
          stack.push('`')
          i += 2
          break
        }
        if (out[i] === '`') { i++; break }
        i++
      }
      continue
    }

    // Opening brackets
    if (ch === '{') { stack.push('{'); i++; continue }
    if (ch === '(') { stack.push('('); i++; continue }
    if (ch === '[') { stack.push('['); i++; continue }

    // Closing brackets
    if (ch === '}') { popStack(stack, '{'); i++; continue }
    if (ch === ')') { popStack(stack, '('); i++; continue }
    if (ch === ']') { popStack(stack, '['); i++; continue }

    // JSX self-closing tag: <Foo ... />
    if (ch === '<' && isAlpha(out[i + 1])) {
      // Find the tag name end
      let j = i + 1
      while (j < out.length && (isAlnum(out[j]) || out[j] === '-' || out[j] === '.')) j++
      const tagName = out.slice(i + 1, j)
      // Look ahead for self-closing />
      const close = out.indexOf('>', j)
      if (close !== -1 && out[close - 1] === '/') {
        i = close + 1
        continue
      }
      // It's an opening tag — push onto stack and skip attributes
      stack.push(`<${tagName}`)
      i = j
      continue
    }

    // JSX closing tag: </Foo>
    if (ch === '<' && next === '/') {
      const close = out.indexOf('>', i)
      if (close !== -1) {
        const tagContent = out.slice(i + 2, close).trim()
        // Pop until matching opening tag
        const idx = stack.lastIndexOf(`<${tagContent}`)
        if (idx !== -1) {
          stack.splice(idx)
        }
        i = close + 1
        continue
      }
    }

    i++
  }

  // Close remaining open constructs in reverse order
  const closers: string[] = []
  for (let k = stack.length - 1; k >= 0; k--) {
    const s = stack[k]
    if (s === '{') closers.push('}')
    else if (s === '(') closers.push(')')
    else if (s === '[') closers.push(']')
    else if (s === '`') closers.push('}')
    else if (s.startsWith('<')) {
      const tagName = s.slice(1)
      closers.push(`</${tagName}>`)
    }
  }

  if (closers.length > 0) {
    out += closers.join('')
  }

  return out
}

function isAlpha(ch: string | undefined): boolean {
  if (!ch) return false
  const c = ch.charCodeAt(0)
  return (c >= 65 && c <= 90) || (c >= 97 && c <= 122)
}

function isAlnum(ch: string | undefined): boolean {
  if (!ch) return false
  const c = ch.charCodeAt(0)
  return (c >= 65 && c <= 90) || (c >= 97 && c <= 122) || (c >= 48 && c <= 57)
}

function popStack(stack: string[], expected: string): void {
  // Pop matching opener, tolerating mismatches (best-effort)
  for (let k = stack.length - 1; k >= 0; k--) {
    if (stack[k] === expected) {
      stack.splice(k)
      return
    }
  }
}


// Minimal createElement import for the portal rendering
import { createElement } from 'react'
