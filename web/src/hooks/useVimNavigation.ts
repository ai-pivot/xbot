import { useEffect, useRef, useCallback } from 'react'

export interface VimNavigationConfig {
  /** Container element to query messages from. Defaults to document. */
  containerSelector?: string
  /** Selector for individual messages. */
  messageSelector?: string
  /** Custom key bindings to override defaults. */
  keyBindings?: Partial<VimKeyBindings>
  /** Whether vim navigation is enabled */
  enabled?: boolean
}

export interface VimKeyBindings {
  scrollDown: string      // j
  scrollUp: string        // k
  goToTop: string         // gg (special: two consecutive g presses)
  goToBottom: string      // G
  halfPageUp: string      // Ctrl+u
  halfPageDown: string    // Ctrl+d
}

const DEFAULT_KEY_BINDINGS: VimKeyBindings = {
  scrollDown: 'j',
  scrollUp: 'k',
  goToTop: 'g',
  goToBottom: 'G',
  halfPageUp: 'u',
  halfPageDown: 'd',
}

/**
 * Returns true if the currently focused element is an input field where
 * keyboard shortcuts should not fire.
 */
function isInputFocused(): boolean {
  const el = document.activeElement
  if (!el) return false
  const tag = (el as HTMLElement).tagName
  if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return true
  if ((el as HTMLElement).isContentEditable) return true
  return false
}

/**
 * Vim-style keyboard navigation hook.
 * Supports j/k scrolling, gg/G jump, Ctrl+u/d half-page scroll.
 * Only active when no input element is focused.
 */
export function useVimNavigation(config: VimNavigationConfig = {}) {
  const {
    messageSelector = '[data-message-index]',
    enabled = true,
    keyBindings: customBindings,
  } = config

  const bindings = { ...DEFAULT_KEY_BINDINGS, ...customBindings }
  const lastGPressRef = useRef<number>(0)

  const getMessages = useCallback((): HTMLElement[] => {
    const container = document
    return Array.from(container.querySelectorAll<HTMLElement>(messageSelector))
  }, [messageSelector])

  const scrollByMessage = useCallback((direction: 'down' | 'up') => {
    const messages = getMessages()
    if (messages.length === 0) {
      // Fallback: scroll by half viewport
      window.scrollBy({ top: direction === 'down' ? window.innerHeight / 3 : -window.innerHeight / 3, behavior: 'smooth' })
      return
    }

    const viewTop = window.scrollY

    if (direction === 'down') {
      // Find first message whose bottom is below the viewport center
      const center = viewTop + window.innerHeight * 0.5
      const target = messages.find(m => {
        const rect = m.getBoundingClientRect()
        return rect.top + window.scrollY > center
      })
      if (target) {
        target.scrollIntoView({ behavior: 'smooth', block: 'start' })
      } else {
        window.scrollBy({ top: window.innerHeight / 3, behavior: 'smooth' })
      }
    } else {
      // Find last message whose top is above the viewport center
      const center = viewTop + window.innerHeight * 0.5
      let target: HTMLElement | undefined
      for (let i = messages.length - 1; i >= 0; i--) {
        const rect = messages[i].getBoundingClientRect()
        if (rect.top + window.scrollY < center) {
          target = messages[i]
          break
        }
      }
      if (target) {
        target.scrollIntoView({ behavior: 'smooth', block: 'start' })
      } else {
        window.scrollBy({ top: -window.innerHeight / 3, behavior: 'smooth' })
      }
    }
  }, [getMessages])

  const scrollToTop = useCallback(() => {
    window.scrollTo({ top: 0, behavior: 'smooth' })
  }, [])

  const scrollToBottom = useCallback(() => {
    window.scrollTo({ top: document.documentElement.scrollHeight, behavior: 'smooth' })
  }, [])

  const halfPageScroll = useCallback((direction: 'up' | 'down') => {
    const halfPage = window.innerHeight / 2
    window.scrollBy({
      top: direction === 'down' ? halfPage : -halfPage,
      behavior: 'smooth',
    })
  }, [])

  useEffect(() => {
    if (!enabled) return

    const handleKeyDown = (e: KeyboardEvent) => {
      if (isInputFocused()) return

      const now = Date.now()

      // Ctrl+u: half page up
      if (e.ctrlKey && e.key === bindings.halfPageUp) {
        e.preventDefault()
        halfPageScroll('up')
        return
      }

      // Ctrl+d: half page down
      if (e.ctrlKey && e.key === bindings.halfPageDown) {
        e.preventDefault()
        halfPageScroll('down')
        return
      }

      // Ignore keys with modifiers
      if (e.ctrlKey || e.metaKey || e.altKey) return

      switch (e.key) {
        case bindings.scrollDown: {
          e.preventDefault()
          scrollByMessage('down')
          break
        }
        case bindings.scrollUp: {
          e.preventDefault()
          scrollByMessage('up')
          break
        }
        case bindings.goToTop: {
          // Double-g: jump to top (gg)
          if (now - lastGPressRef.current < 500) {
            e.preventDefault()
            scrollToTop()
            lastGPressRef.current = 0
          } else {
            lastGPressRef.current = now
          }
          break
        }
        case bindings.goToBottom: {
          e.preventDefault()
          scrollToBottom()
          break
        }
      }
    }

    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [enabled, bindings, scrollByMessage, scrollToTop, scrollToBottom, halfPageScroll])
}
