import { useEffect, useRef, useState } from 'react'
import { Maximize2, Minimize2 } from 'lucide-react'
import { useI18n } from '@/providers/i18n'

interface KeyboardLock {
  lock(codes: string[]): Promise<void>
  unlock(): void
}

// Lock physical keys (including modified combinations), but leave Escape to
// the browser. Do not preventDefault globally: editors and dialogs own routing.
const LOCK_CODES = [
  ...Array.from('ABCDEFGHIJKLMNOPQRSTUVWXYZ', key => `Key${key}`),
  ...Array.from('0123456789', key => `Digit${key}`),
  ...Array.from({ length: 12 }, (_, i) => `F${i + 1}`),
  'Tab', 'Enter', 'Space', 'Backspace', 'Delete', 'Insert', 'Home', 'End',
  'PageUp', 'PageDown', 'ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight',
  'Backquote', 'Minus', 'Equal', 'BracketLeft', 'BracketRight', 'Backslash',
  'Semicolon', 'Quote', 'Comma', 'Period', 'Slash',
]

export function ImmersiveModeButton() {
  const { t } = useI18n()
  const [active, setActive] = useState(false)
  const [busy, setBusy] = useState(false)
  const [notice, setNotice] = useState('')
  const owned = useRef(false)
  const mounted = useRef(false)
  const pending = useRef(false)
  const keyboard = (navigator as Navigator & { keyboard?: KeyboardLock }).keyboard
  const supported = typeof document.documentElement.requestFullscreen === 'function' && document.fullscreenEnabled !== false

  useEffect(() => {
    mounted.current = true
    const onChange = () => {
      const isActive = owned.current && document.fullscreenElement === document.documentElement
      setActive(isActive)
      if (!isActive && owned.current) {
        owned.current = false
        keyboard?.unlock()
        setNotice('')
      }
    }
    document.addEventListener('fullscreenchange', onChange)
    return () => {
      mounted.current = false
      document.removeEventListener('fullscreenchange', onChange)
      if (owned.current) {
        owned.current = false
        keyboard?.unlock()
        if (document.fullscreenElement === document.documentElement) void document.exitFullscreen().catch(() => {})
      }
    }
  }, [keyboard])

  async function toggle() {
    if (pending.current) return
    pending.current = true
    setBusy(true)
    setNotice('')
    try {
      if (owned.current && document.fullscreenElement === document.documentElement) {
        await document.exitFullscreen()
        return
      }
      owned.current = true
      // Invoke directly from the click: Fullscreen requires user activation.
      await document.documentElement.requestFullscreen()
      if (!mounted.current || !owned.current || document.fullscreenElement !== document.documentElement) {
        if (!mounted.current && document.fullscreenElement === document.documentElement) await document.exitFullscreen()
        return
      }
      setActive(true)
      if (typeof keyboard?.lock !== 'function') {
        setNotice('layout.immersiveNoLock')
        return
      }
      try {
        await keyboard.lock(LOCK_CODES)
        // Permission prompts can outlive fullscreen or the component.
        if (!mounted.current || !owned.current || document.fullscreenElement !== document.documentElement) keyboard.unlock()
      } catch {
        if (mounted.current && owned.current) setNotice('layout.immersiveNoLock')
      }
    } catch {
      if (!document.fullscreenElement) owned.current = false
      if (mounted.current) setNotice('layout.immersiveFailed')
    } finally {
      pending.current = false
      if (mounted.current) setBusy(false)
    }
  }

  const label = t(active ? 'layout.immersiveExit' : 'layout.immersiveEnter')
  return (
    <>
      {notice && <span role="status" className="max-w-64 truncate text-text-muted" title={t(notice)}>{t(notice)}</span>}
      <button
        type="button"
        aria-label={label}
        aria-pressed={active}
        title={supported ? `${label} · ${t('layout.immersiveHint')}` : t('layout.immersiveUnavailable')}
        disabled={!supported || busy}
        onClick={() => { void toggle() }}
        className="flex shrink-0 items-center rounded-md p-1.5 transition-spring hover:bg-bg-tertiary active:scale-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-app-accent/50 disabled:opacity-40"
        style={{ color: 'var(--text-secondary)' }}
      >
        {active ? <Minimize2 className="size-3.5" /> : <Maximize2 className="size-3.5" />}
      </button>
    </>
  )
}
