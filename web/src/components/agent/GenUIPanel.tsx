/**
 * GenUIPanel — a fancy "top-level panel" wrapper for UI results declared with
 * UIDecl.Surface (kind="panel"). Renders a header bar (summary title + collapse +
 * fullscreen buttons) around the content, DEFAULT-OPEN (not auto-folded), with
 * manual collapse (animated) and fullscreen (portal overlay).
 *
 * This is the generic rendering of the "special summary / top-level element"
 * capability — any UI tool can declare Surface=panel and get this treatment,
 * not just display_html (metadata-driven, plugin-declared).
 */
import { memo, useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { ChevronDown, Maximize2, X } from 'lucide-react'

import { AnimatedCollapse } from '@/components/ui/animated-collapse'
import { SandboxedUI } from '@/plugins/SandboxedUI'
import { cn } from '@/lib/utils'

export interface GenUIPanelProps {
  /** Panel title (falls back to the tool summary). */
  title?: string
  /** Manual collapse (header shows a chevron toggle). */
  collapsible?: boolean
  /** Fullscreen (header shows a maximize button). */
  fullscreen?: boolean
  /** Start open (not auto-folded). Default true. */
  defaultOpen?: boolean
  /** Force collapsed (e.g. GenUI render error — prevents virtual view height miscalculation). */
  forceCollapsed?: boolean
  /** Shown in header when forceCollapsed (e.g. "⚠️ 渲染失败"). */
  errorHint?: string
  children: ReactNode
}

export const GenUIPanel = memo(function GenUIPanel({
  title,
  collapsible = true,
  fullscreen = true,
  defaultOpen = true,
  forceCollapsed = false,
  errorHint,
  children,
}: GenUIPanelProps) {
  const [open, setOpen] = useState(defaultOpen)
  const [full, setFull] = useState(false)
  const effectiveOpen = forceCollapsed ? false : open

  // Esc closes fullscreen + block body scroll while open.
  useEffect(() => {
    if (!full) return
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') setFull(false) }
    document.addEventListener('keydown', onKey)
    document.body.style.overflow = 'hidden'
    return () => {
      document.removeEventListener('keydown', onKey)
      document.body.style.overflow = ''
    }
  }, [full])

  const showHeader = collapsible || fullscreen || title
  // Without any header there's no top-level chrome — just render content inline.
  if (!showHeader) return <>{children}</>

  return (
    <div className="genui-panel overflow-hidden rounded-xl border border-slate-200 bg-white dark:border-slate-700 dark:bg-slate-900">
      <div className="flex items-center gap-1.5 border-b border-slate-100 bg-slate-50/70 px-3 py-1.5 dark:border-slate-800 dark:bg-slate-800/50">
        <span className="min-w-0 flex-1 truncate text-xs font-medium text-slate-600 dark:text-slate-300">
          {errorHint || title || '生成的界面'}
        </span>
        {collapsible && !forceCollapsed && (
          <button
            type="button"
            onClick={() => setOpen((v) => !v)}
            aria-expanded={effectiveOpen}
            aria-label={effectiveOpen ? '折叠' : '展开'}
            className="flex h-6 w-6 items-center justify-center rounded-md text-slate-400 transition hover:bg-slate-200/70 hover:text-slate-700 dark:hover:bg-slate-700 dark:hover:text-slate-200"
          >
            <ChevronDown className={cn('size-3.5 transition-transform duration-200', !effectiveOpen && '-rotate-90')} />
          </button>
        )}
        {fullscreen && !forceCollapsed && (
          <button
            type="button"
            onClick={() => setFull(true)}
            aria-label="全屏"
            className="flex h-6 w-6 items-center justify-center rounded-md text-slate-400 transition hover:bg-slate-200/70 hover:text-slate-700 dark:hover:bg-slate-700 dark:hover:text-slate-200"
          >
            <Maximize2 className="size-3.5" />
          </button>
        )}
      </div>

      <AnimatedCollapse open={effectiveOpen} lazy={false} unmountOnClose={false}>
        <div className="max-h-[70vh] overflow-auto">{children}</div>
      </AnimatedCollapse>

      {full && (
        <FullscreenOverlay onClose={() => setFull(false)}>
          <div className="h-full w-full overflow-auto">{children}</div>
        </FullscreenOverlay>
      )}
    </div>
  )
})

/** Fullscreen overlay — portal at document.body so it escapes ancestor overflow/transform. */
function FullscreenOverlay({ children, onClose }: { children: ReactNode; onClose: () => void }) {
  const overlayRef = useRef<HTMLDivElement>(null)
  const onBackdrop = useCallback((e: React.MouseEvent) => {
    if (e.target === overlayRef.current) onClose()
  }, [onClose])

  return createPortal(
    <div
      ref={overlayRef}
      role="dialog"
      aria-modal="true"
      className="fixed inset-0 z-[9999] flex items-center justify-center bg-black/70 p-3 sm:p-6"
      style={{ paddingTop: 'max(env(safe-area-inset-top), 0.75rem)', paddingBottom: 'max(env(safe-area-inset-bottom), 0.75rem)' }}
      onClick={onBackdrop}
    >
      <div className="flex max-h-full max-w-full flex-col overflow-hidden rounded-xl bg-white shadow-2xl dark:bg-slate-900">
        <div className="flex shrink-0 items-center justify-end border-b border-slate-200 px-3 py-1.5 dark:border-slate-700">
          <button
            type="button"
            aria-label="关闭全屏"
            onClick={onClose}
            className="flex h-8 w-8 items-center justify-center rounded-md text-slate-400 transition hover:bg-slate-200/70 hover:text-slate-700 dark:hover:bg-slate-700 dark:hover:text-slate-200"
          >
            <X className="size-4" />
          </button>
        </div>
        <div className="min-h-0 flex-1 overflow-auto">{children}</div>
      </div>
    </div>,
    document.body,
  )
}

/**
 * GenUICollapsiblePanel — wraps GenUIPanel + SandboxedUI with error state.
 * When SandboxedUI fails to compile, the panel is force-collapsed to prevent
 * virtual view height miscalculation (the error panel would expand but the
 * virtual list assumes collapsed height). Error state resets when code changes.
 */
export function GenUICollapsiblePanel({ code, streaming, title }: { code: string; streaming?: boolean; title?: string }) {
  const [error, setError] = useState(false)
  useEffect(() => { setError(false) }, [code])
  return (
    <GenUIPanel
      title={title}
      collapsible
      fullscreen
      defaultOpen
      forceCollapsed={error}
      errorHint={error ? '⚠️ 渲染失败' : undefined}
    >
      <SandboxedUI code={code} streaming={streaming} onError={() => setError(true)} />
    </GenUIPanel>
  )
}