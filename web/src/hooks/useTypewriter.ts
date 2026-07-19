/**
 * useTypewriter — adaptive typewriter hook mirroring TUI's algorithm.
 *
 * TUI algorithm (channel/cli/cli_animation.go advanceWriterCJK):
 *   - 50ms tick interval
 *   - Exponential catch-up: advance = gap / 3 per tick (min 1)
 *   - CJK awareness: CJK runes advance at half speed (skip every other tick)
 *   - Converges in ~log1.5(gap) ticks regardless of gap size
 *
 * Web enhancements:
 *   - Returns visibleText (substring of full text up to visible runes)
 *   - Returns isTyping (true when visible < target)
 *   - When isTyping, the container should apply a fade-in CSS class
 *
 * Synchronisation: when fullText changes (new stream chunk), useLayoutEffect
 * advances visibleText BEFORE paint so content and tools render in the same
 * frame. The interval then handles subsequent catch-up within the same chunk.
 */
import { useEffect, useLayoutEffect, useRef, useState } from 'react'

/** CJK range check — matches TUI isCJK (cli_animation.go:53). */
function isCJK(r: number): boolean {
  return (
    (r >= 0x1100 && r <= 0x11ff) || // Hangul Jamo
    (r >= 0x2e80 && r <= 0x9fff) || // CJK radicals + Han
    (r >= 0xa000 && r <= 0xa4ff) || // Yi
    (r >= 0xac00 && r <= 0xd7af) || // Hangul syllables
    (r >= 0xf900 && r <= 0xfaff) || // CJK compatibility ideographs
    (r >= 0xff00 && r <= 0xffef)    // CJK compatibility forms
  )
}

const TICK_MS = 50

export interface TypewriterState {
  /** The portion of text currently visible (rune-aware substring). */
  visibleText: string
  /** True when the typewriter hasn't caught up to the full text. */
  isTyping: boolean
}

export function useTypewriter(fullText: string): TypewriterState {
  const [state, setState] = useState<{ visibleText: string; isTyping: boolean }>({
    visibleText: '',
    isTyping: false,
  })
  const visibleRef = useRef(0)
  const skipFlipRef = useRef(false)
  const fullTextRef = useRef('')

  fullTextRef.current = fullText

  // Advance visible runes by the TUI exponential catch-up formula.
  // Returns the new visible count.
  const advanceVisible = (runes: string[], visible: number): number => {
    const gap = runes.length - visible
    if (gap <= 0) return visible

    const nextIsCJK = visible < runes.length && isCJK(runes[visible].codePointAt(0) ?? 0)
    const advance = Math.max(1, Math.floor(gap / 3))

    // CJK penalty: if next rune is CJK and we're at slow speed, skip
    // every other tick (effectively half speed for CJK)
    if (nextIsCJK && advance <= 3 && gap <= 20) {
      skipFlipRef.current = !skipFlipRef.current
      if (skipFlipRef.current) return visible // skip this advance
    }

    return Math.min(visible + advance, runes.length)
  }

  // On fullText change: synchronously advance BEFORE paint so content
  // and tools render in the same frame. Without this, tools (driven by
  // useSyncExternalStore) render immediately while content waits 50ms
  // for the next interval tick — causing "content jumps in after tools".
  useLayoutEffect(() => {
    if (!fullText) {
      if (visibleRef.current > 0) {
        visibleRef.current = 0
        setState({ visibleText: '', isTyping: false })
      }
      return
    }
    const runes = Array.from(fullText)
    // Reset if text shrank (new turn)
    if (runes.length < visibleRef.current) {
      visibleRef.current = runes.length
    }
    const gap = runes.length - visibleRef.current
    if (gap > 0) {
      const newVisible = advanceVisible(runes, visibleRef.current)
      visibleRef.current = newVisible
      setState({
        visibleText: runes.slice(0, newVisible).join(''),
        isTyping: newVisible < runes.length,
      })
    }
  }, [fullText])

  // Single interval for subsequent catch-up — created once.
  useEffect(() => {
    const tick = () => {
      const text = fullTextRef.current
      if (!text) return
      const runes = Array.from(text)
      const visible = visibleRef.current
      const gap = runes.length - visible
      if (gap <= 0) {
        setState((prev) => prev.isTyping ? { visibleText: text, isTyping: false } : prev)
        return
      }
      const newVisible = advanceVisible(runes, visible)
      if (newVisible !== visible) {
        visibleRef.current = newVisible
        setState({
          visibleText: runes.slice(0, newVisible).join(''),
          isTyping: newVisible < runes.length,
        })
      }
    }
    const interval = setInterval(tick, TICK_MS)
    return () => clearInterval(interval)
  }, [])

  return state
}
