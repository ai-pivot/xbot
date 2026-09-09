// Image lightbox for markdown images — module-level singleton (openLightbox)
// + a single <ImageLightboxHost /> mounted once at the app root.
//
// Why module-level state instead of per-MarkdownRenderer state: MarkdownRenderer
// is a memo'd component re-rendered on every typewriter tick (50ms); putting
// lightbox state inside it would break the memoization and re-parse the whole
// markdown tree on every open/close. The host renders via createPortal to
// document.body — zero coupling with the markdown tree.
//
// Esc / backdrop click closes; a download link opens the original URL.

import { useCallback, useEffect, useState, type KeyboardEvent as ReactKeyboardEvent } from 'react'
import { createPortal } from 'react-dom'

type LightboxImage = { src: string; alt: string }

type Listener = (img: LightboxImage | null) => void
let listener: Listener | null = null

/** Open the lightbox for an image (called from markdown img onClick). */
export function openLightbox(src: string, alt: string) {
  if (!src) return
  listener?.({ src, alt: alt || '' })
}

function closeLightbox() {
  listener?.(null)
}

/** Mount once at the app root (e.g. in App). Renders nothing until opened. */
export function ImageLightboxHost() {
  const [img, setImg] = useState<LightboxImage | null>(null)
  useEffect(() => {
    listener = setImg
    return () => {
      if (listener === setImg) listener = null
    }
  }, [])
  // Escape 关闭走容器 onKeyDown（不挂 window 全局监听——per-session 组件禁止
  // 全局 window 事件，见 eslint no-restricted-properties / AGENTS.md
  // SESSION-PANEL GLOBAL-STATE BAN）。
  const onKey = useCallback((e: ReactKeyboardEvent<HTMLDivElement>) => {
    if (e.key === 'Escape') closeLightbox()
  }, [])
  // 挂载即聚焦（容器 tabIndex=-1）——无需全局监听即可响应 Escape。
  const focusRef = useCallback((el: HTMLDivElement | null) => {
    el?.focus()
  }, [])
  useEffect(() => {
    if (!img) return
    const prevOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return () => {
      document.body.style.overflow = prevOverflow
    }
  }, [img])
  if (!img) return null
  return createPortal(
    <div
      ref={focusRef}
      role="dialog"
      aria-modal="true"
      tabIndex={-1}
      aria-label={img.alt || 'image viewer'}
      onKeyDown={onKey}
      onClick={() => closeLightbox()}
      className="fixed inset-0 z-[60] flex items-center justify-center bg-black/85 p-4 outline-none backdrop-blur-sm"
      style={{ cursor: 'zoom-out' }}
    >
      <div className="relative flex max-h-full max-w-full flex-col items-center gap-3" onClick={(e) => e.stopPropagation()}>
        <img
          src={img.src}
          alt={img.alt}
          className="max-h-[85vh] max-w-full rounded-lg object-contain shadow-2xl"
          style={{ cursor: 'default' }}
        />
        <div className="flex items-center gap-3 text-xs text-white/70">
          <span className="max-w-[60ch] truncate">{img.alt || 'image'}</span>
          <a
            href={img.src}
            target="_blank"
            rel="noopener noreferrer"
            className="rounded-md border border-white/20 px-2 py-1 text-white/80 transition-colors hover:bg-white/10"
            onClick={(e) => e.stopPropagation()}
          >
            Open original
          </a>
          <button
            type="button"
            aria-label="close"
            onClick={() => closeLightbox()}
            className="rounded-md border border-white/20 px-2 py-1 text-white/80 transition-colors hover:bg-white/10"
          >
            Esc
          </button>
        </div>
      </div>
    </div>,
    document.body,
  )
}
