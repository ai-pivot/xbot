/**
 * MermaidDiagram — lazily renders a Mermaid diagram to SVG.
 *
 * The `mermaid` package is ~1MB and ships a sizable JS bundle, so it is
 * dynamically imported only when the first diagram is encountered — it never
 * lands in the initial page-load bundle. Once loaded, the module is cached at
 * module scope so subsequent diagrams render without re-importing.
 *
 * Rendering is **async** (mermaid returns a Promise), so the component shows a
 * minimal placeholder while the SVG is being generated, then swaps it in. If
 * the source is invalid the component degrades gracefully: it shows the raw
 * source in a code block (never a blank box or a thrown error), matching the
 * CLI's fallback behaviour of preserving the original source.
 *
 * Theme: mermaid is re-initialised whenever the app theme (dark/light) or the
 * accent colour changes, so diagrams follow the user's theme at render time.
 *
 * Streaming integration: the PARENT decides when to mount this component.
 * During active streaming the parent shows a plain source block instead (see
 * MarkdownRenderer CodeBlock). This component is only mounted when the source
 * is settled, so it renders once — no per-tick re-render storm.
 */
import { memo, useCallback, useEffect, useRef, useState, useSyncExternalStore } from 'react'
import { createPortal } from 'react-dom'
import { Check, Copy, Maximize2, X } from 'lucide-react'

import { useTheme } from '@/hooks/useTheme'
import { useIsTouch } from '@/hooks/useIsMobile'
import { cn } from '@/lib/utils'

export interface MermaidDiagramProps {
  /** Raw mermaid source (without the surrounding ```mermaid fence). */
  source: string
}

// Type of the mermaid default export — resolved at compile time via
// `typeof import(...)`, no runtime import emitted.  This avoids the
// `import type { Mermaid }` pitfall where `typeof Mermaid` is illegal
// (type-only binding used in a value position, TS2693).
type MermaidAPI = (typeof import('mermaid'))['default']

// ── Module-scope mermaid singleton ──────────────────────────────────────────
// The dynamic import is cached so all diagrams share one load. Mirrors the
// highlight.js lazy-load pattern in highlight.ts (useSyncExternalStore).
let mermaidModule: MermaidAPI | null = null
let loadPromise: Promise<MermaidAPI> | null = null
let loadError: unknown = null
const readyListeners = new Set<() => void>()

/** Kick off the dynamic import of mermaid (fire-and-forget, cached).
 *  On failure: clears loadPromise (so callers can retry), records the error,
 *  and notifies subscribers — the component then falls back to source display
 *  instead of spinning "Rendering diagram…" forever. */
export function ensureMermaidLoaded(): void {
  if (mermaidModule || loadPromise) return
  loadPromise = import('mermaid').then((m) => {
    mermaidModule = m.default
    readyListeners.forEach((fn) => fn())
    return mermaidModule
  }).catch((e) => {
    loadPromise = null // allow retry on next call
    loadError = e
    readyListeners.forEach((fn) => fn())
    throw e
  })
}

function subscribeReady(fn: () => void): () => void {
  readyListeners.add(fn)
  return () => { readyListeners.delete(fn) }
}

/** Snapshot: null = still loading, true = ready, false = load failed. */
type LoadStatus = 'loading' | 'ready' | 'failed'

function getReadySnapshot(): LoadStatus {
  if (mermaidModule) return 'ready'
  if (loadError) return 'failed'
  return 'loading'
}

/** Re-render hook: returns the current load status. */
function useMermaidStatus(): LoadStatus {
  return useSyncExternalStore(subscribeReady, getReadySnapshot, () => 'loading')
}

// Monotonic id counter so each diagram gets a unique render target.
let idSeq = 0

export const MermaidDiagram = memo(function MermaidDiagram({ source }: MermaidDiagramProps) {
  const { theme, accentColor } = useTheme()
  const isDark = theme === 'dark'
  const status = useMermaidStatus()
  const [svg, setSvg] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  // Lazy id assignment — only on first mount, not every render (avoids
  // incrementing the module-level counter as a render-phase side effect).
  const idRef = useRef<string | null>(null)
  if (idRef.current === null) idRef.current = `mmd-${idSeq++}`

  // Monotonic render sequence — guards against stale async renders: if
  // source/theme changes while a render is in flight, the old result is
  // discarded. On unmount, the ref is set to Infinity so any in-flight
  // promise becomes permanently stale.
  const renderSeq = useRef(0)

  const [fullscreen, setFullscreen] = useState(false)

  // Esc closes the fullscreen overlay; also block body scroll while open.
  useEffect(() => {
    if (!fullscreen) return
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') setFullscreen(false) }
    document.addEventListener('keydown', onKey)
    document.body.style.overflow = 'hidden'
    return () => {
      document.removeEventListener('keydown', onKey)
      document.body.style.overflow = ''
    }
  }, [fullscreen])

  // Kick off the dynamic import on first mount (no-op if already loading).
  useEffect(() => { ensureMermaidLoaded() }, [])

  // Unmount: invalidate all in-flight renders so no stale setSvg/setError
  // fires on an unmounted component.
  useEffect(() => () => { renderSeq.current = Infinity }, [])

  const render = useCallback(async () => {
    const seq = ++renderSeq.current
    try {
      const mer = mermaidModule
      if (!mer) return // not loaded yet — will retry when status flips
      // Re-initialise on every render so theme + accent are always current.
      mer.initialize({
        startOnLoad: false,
        securityLevel: 'strict',
        theme: isDark ? 'dark' : 'default',
        themeVariables: isDark
          ? {
              primaryColor: accentColor,
              primaryTextColor: '#e5e7eb',
              primaryBorderColor: accentColor,
              lineColor: '#9ca3af',
              secondaryColor: '#374151',
              tertiaryColor: '#1f2937',
              background: 'transparent',
              mainBkg: '#1f2937',
              nodeBorder: accentColor,
              clusterBkg: '#1f2937',
              clusterBorder: '#4b5563',
            }
          : {
              primaryColor: accentColor,
              primaryTextColor: '#1f2937',
              primaryBorderColor: accentColor,
              lineColor: '#4b5563',
              background: 'transparent',
            },
      })
      const { svg: rendered } = await mer.render(idRef.current!, source)
      if (seq !== renderSeq.current) return // stale render — discard
      // Mermaid injects an inline `style="max-width: <px>"` that clamps small
      // diagrams to their natural width, preventing them from filling the
      // container. Strip it so the SVG scales responsively via CSS (w-full).
      const responsive = rendered.replace(/style="[^"]*max-width:[^"]*"/g, 'style=""')
      setSvg(responsive)
      setError(null)
    } catch (e) {
      if (seq !== renderSeq.current) return // stale render — discard
      // mermaid.render throws on invalid syntax. Clean up any leftover error
      // overlay mermaid may have injected into the DOM, then fall back.
      document.querySelectorAll(`#${idRef.current}`).forEach((el) => el.remove())
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [source, isDark, accentColor])

  useEffect(() => {
    if (status !== 'ready') return
    void render()
  }, [render, status])

  // Load failed (network error / missing chunk) — fall back to source
  // display instead of spinning "Rendering diagram…" forever.
  if (status === 'failed') {
    return <MermaidSourceBlock source={source} />
  }

  // Loading placeholder — shown before mermaid is available or while rendering.
  if (!svg && !error) {
    return (
      <div
        className="flex items-center justify-center rounded-md py-8 text-sm"
        style={{ border: '1px solid var(--md-code-border)', backgroundColor: 'var(--md-code-bg)', color: 'var(--md-code-lang-text)' }}
      >
        Rendering diagram…
      </div>
    )
  }

  // Error fallback — show the raw source so the user can diagnose the syntax.
  if (error) {
    return <MermaidSourceBlock source={source} />
  }

  return (
    <>
      <div
        className="mermaid-container group/code relative my-2 flex justify-center overflow-x-auto rounded-md"
        style={{ border: '1px solid var(--md-code-border)', backgroundColor: 'var(--md-code-bg)' }}
      >
        <CopyButton getText={() => source} />
        <MaximizeButton onClick={() => setFullscreen(true)} />
        {/* dangerouslySetInnerHTML: mermaid.render returns SVG we generated
            ourselves with securityLevel: 'strict' (no inline scripts/handlers).
            The inline max-width is stripped above so the SVG is fully responsive:
            small diagrams expand to fill the container, large diagrams scroll. */}
        <div
          className="w-full max-w-full p-3 [&>svg]:w-full [&>svg]:h-auto [&>svg]:max-w-none"
          dangerouslySetInnerHTML={{ __html: svg ?? '' }}
        />
      </div>
      {fullscreen && (
        <FullscreenOverlay svg={svg ?? ''} onClose={() => setFullscreen(false)} />
      )}
    </>
  )
})

/**
 * Plain source-code display for mermaid blocks — used during streaming (source
 * is incomplete, rendering would fail) and as the error fallback. Mirrors the
 * CodeBlock visual style so the transition from source → diagram is seamless.
 */
export function MermaidSourceBlock({ source }: { source: string }) {
  return (
    <div
      className="group/code relative my-2 overflow-hidden rounded-md"
      style={{ border: '1px solid var(--md-code-border)', backgroundColor: 'var(--md-code-bg)' }}
    >
      <span
        className="absolute left-3 top-2 z-10 select-none font-mono text-[11px] uppercase"
        style={{ color: 'var(--md-code-lang-text)' }}
      >
        mermaid
      </span>
      <CopyButton getText={() => source} />
      <pre className="overflow-x-auto whitespace-pre p-3 pt-7 text-[13px] leading-relaxed">
        <code className="font-mono" style={{ color: 'var(--md-code-text, var(--md-code-lang-text))' }}>
          {source}
        </code>
      </pre>
    </div>
  )
}

/** Copy button shared with code blocks — self-contained to avoid a circular
 *  import with MarkdownRenderer. */
function CopyButton({ getText }: { getText: () => string }) {
  const [copied, setCopied] = useState(false)
  const isTouch = useIsTouch()
  const onClick = useCallback(async () => {
    try {
      await navigator.clipboard.writeText(getText())
      setCopied(true)
      setTimeout(() => setCopied(false), 1200)
    } catch {
      /* clipboard unavailable — ignore */
    }
  }, [getText])

  return (
    <button
      type="button"
      aria-label="Copy code"
      onClick={onClick}
      className={cn(
        'absolute right-2 top-2 z-10 flex size-7 items-center justify-center rounded-md transition-opacity hover:text-text-primary focus-visible:opacity-100 focus-visible:outline-none',
        isTouch ? 'opacity-60' : 'opacity-0 group-hover/code:opacity-100',
      )}
      style={{
        backgroundColor: 'color-mix(in srgb, var(--md-code-bg) 80%, var(--md-code-border))',
        color: 'var(--md-code-lang-text)',
      }}
    >
      {copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
    </button>
  )
}

/** Maximize button — opens the diagram in a fullscreen overlay. */
function MaximizeButton({ onClick }: { onClick: () => void }) {
  const isTouch = useIsTouch()
  return (
    <button
      type="button"
      aria-label="Fullscreen diagram"
      onClick={onClick}
      className={cn(
        'absolute right-10 top-2 z-10 flex size-7 items-center justify-center rounded-md transition-opacity hover:text-text-primary focus-visible:opacity-100 focus-visible:outline-none',
        isTouch ? 'opacity-60' : 'opacity-0 group-hover/code:opacity-100',
      )}
      style={{
        backgroundColor: 'color-mix(in srgb, var(--md-code-bg) 80%, var(--md-code-border))',
        color: 'var(--md-code-lang-text)',
      }}
    >
      <Maximize2 className="size-3.5" />
    </button>
  )
}

/**
 * Fullscreen overlay — renders the SVG via a portal at document.body so it
 * escapes any ancestor overflow/transform. The SVG is scrollable (both axes)
 * and centered; click backdrop or Esc to close.
 */
function FullscreenOverlay({ svg, onClose }: { svg: string; onClose: () => void }) {
  const overlayRef = useRef<HTMLDivElement>(null)
  const onBackdropClick = useCallback((e: React.MouseEvent) => {
    if (e.target === overlayRef.current) onClose()
  }, [onClose])

  return createPortal(
    <div
      ref={overlayRef}
      onClick={onBackdropClick}
      role="dialog"
      aria-modal="true"
      aria-label="Fullscreen diagram"
      className="fixed inset-0 z-[9999] flex items-center justify-center p-4 sm:p-8"
      style={{ backgroundColor: 'rgba(0, 0, 0, 0.8)' }}
    >
      <button
        type="button"
        aria-label="Close fullscreen"
        onClick={onClose}
        className="absolute right-4 top-4 z-10 flex size-9 items-center justify-center rounded-full transition-colors hover:bg-white/10"
        style={{ color: '#fff' }}
      >
        <X className="size-5" />
      </button>
      <div
        className="flex max-h-full max-w-full items-center justify-center overflow-auto rounded-lg p-4"
        style={{ backgroundColor: 'var(--md-code-bg, #1e1e1e)' }}
      >
        <div dangerouslySetInnerHTML={{ __html: svg }} />
      </div>
    </div>,
    document.body,
  )
}
