/**
 * useTypewriter — adaptive typewriter hook mirroring TUI's algorithm.
 *
 * TUI algorithm (channel/cli/cli_animation.go advanceWriterCJK):
 *   - 50ms tick interval
 *   - Exponential catch-up: advance = gap / 3 per tick (min 1)
 *   - CJK awareness: CJK runes advance at half speed (skip every other tick)
 *   - Converges in ~log1.5(gap) ticks regardless of gap size
 *
 * Web adaptation:
 *   - Returns only visibleChars; the rendered Markdown DOM is clipped in place
 *   - Returns isTyping (true when visible < target)
 *   - The typewriter never reparses Markdown on timer ticks
 *
 * Synchronisation: a 50ms setInterval drives ALL catch-up — no
 * useLayoutEffect advance. The previous useLayoutEffect that advanced gap/3
 * on every SSE chunk caused "sawtooth" jumps (big jump on chunk, then slow
 * catch-up). The 50ms interval alone produces smooth, continuous motion.
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
  /** Number of visible Unicode code points. The renderer clips its existing DOM to this count. */
  visibleChars: number
  /** True when the typewriter hasn't caught up to the full text. */
  isTyping: boolean
}

export function useTypewriter(fullText: string): TypewriterState {
  const [state, setState] = useState<{ visibleChars: number; isTyping: boolean }>({
    visibleChars: 0,
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

  // ── Reset on empty / shrink (new turn) ──
  // Synchronous reset so the next render shows empty content immediately,
  // without waiting for the 50ms interval tick.
  useLayoutEffect(() => {
    if (!fullText) {
      if (visibleRef.current !== 0) {
        visibleRef.current = 0
        setState({ visibleChars: 0, isTyping: false })
      }
      return
    }
    const runes = Array.from(fullText)
    if (runes.length < visibleRef.current) {
      visibleRef.current = 0
      setState({ visibleText: '', visibleChars: 0, isTyping: false })
    }
  }, [fullText])

  // Single interval for ALL catch-up — created once.
  // No useLayoutEffect advance: that caused "sawtooth" jumps where each
  // SSE chunk ate gap/3 instantly, then the interval slowly caught up.
  // The 50ms interval delay is imperceptible (< human perception threshold
  // of ~100ms) and produces smooth, continuous typewriter motion.
  useEffect(() => {
    const tick = () => {
      const text = fullTextRef.current
      if (!text) return
      const runes = Array.from(text)
      const visible = visibleRef.current
      const gap = runes.length - visible
      if (gap <= 0) {
        setState((prev) => prev.isTyping ? { visibleChars: runes.length, isTyping: false } : prev)
        return
      }
      const newVisible = advanceVisible(runes, visible)
      if (newVisible !== visible) {
        visibleRef.current = newVisible
        setState({
          visibleChars: newVisible,
          isTyping: newVisible < runes.length,
        })
      }
    }
    const interval = setInterval(tick, TICK_MS)
    return () => clearInterval(interval)
  }, [])

  return state
}
