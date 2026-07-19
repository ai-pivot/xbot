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
 *     that fades the trailing edge from semi-transparent to full opacity
 */
import { useEffect, useRef, useState } from 'react'

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

  // Track the full text in a ref so the tick closure always sees the latest
  fullTextRef.current = fullText

  // Tick: advance visible runes using TUI exponential catch-up
  useEffect(() => {
    if (!fullText) {
      visibleRef.current = 0
      setState({ visibleText: '', isTyping: false })
      return
    }

    // If new text is shorter than what we've shown, reset
    const runes = Array.from(fullText)
    if (runes.length < visibleRef.current) {
      visibleRef.current = runes.length
    }

    const tick = () => {
      const text = fullTextRef.current
      if (!text) return
      const runes = Array.from(text)
      const target = runes.length
      const visible = visibleRef.current
      const gap = target - visible

      if (gap <= 0) {
        setState({ visibleText: text, isTyping: false })
        return
      }

      // Check if next rune is CJK
      const nextIsCJK = visible < runes.length && isCJK(runes[visible].codePointAt(0) ?? 0)

      // Exponential catch-up: advance 1/3 of remaining gap per tick
      let advance = Math.max(1, Math.floor(gap / 3))

      // CJK penalty: if next rune is CJK and we're at slow speed, skip
      // every other tick (effectively half speed for CJK)
      if (nextIsCJK && advance <= 3 && gap <= 20) {
        skipFlipRef.current = !skipFlipRef.current
        if (skipFlipRef.current) return // skip this tick
      }

      const newVisible = Math.min(visible + advance, target)
      visibleRef.current = newVisible
      setState({ visibleText: runes.slice(0, newVisible).join(''), isTyping: newVisible < target })
    }

    const interval = setInterval(tick, TICK_MS)
    return () => clearInterval(interval)
  }, [fullText])

  return state
}
