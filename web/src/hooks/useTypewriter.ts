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

// TAIL controls how far behind the producer the typewriter is allowed to lag.
// gap/3 alone reaches a steady state where gap stabilises at ~3× the per-tick
// production rate, so every tick advances by the SAME large distance (stutter).
// Instead we cap the GAP (not the step): whenever the backlog exceeds TAIL, we
// jump straight to "TAIL chars behind" in one bounded step, then reveal those
// last TAIL chars at a fixed small per-tick rate. This guarantees:
//   - the typer always catches up (no fixed step cap that can fall behind)
//   - the per-tick reveal is small and constant (no big/equal-distance jumps)
const TAIL = 12
// CHAR_PER_TICK is the fixed reveal rate within the tail region.
const CHAR_PER_TICK = 3

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

  // Cache the runes array — Array.from(fullText) is O(n) and was previously
  // called on every 50ms tick. For long reasoning (10K+ chars), this allocated
  // a 10K-element array 20 times/second. Now we only rebuild when fullText changes.
  const runesRef = useRef<string[]>([])
  const runesTextRef = useRef('')

  fullTextRef.current = fullText

  // Advance visible runes using a tail-bounded reveal:
  //   - Backlog > TAIL  →  jump to `target - TAIL` in one step (catch up fast,
  //     no fixed per-tick cap that could fall behind a fast producer).
  //   - Backlog ≤ TAIL  →  reveal at a fixed small per-tick rate (smooth).
  // This keeps the steady-state gap pinned at ≤ TAIL instead of drifting to
  // ~3×production-rate (the old gap/3 invariant that caused equal-distance
  // jumps / stutter). Returns the new visible count.
  const advanceVisible = (runes: string[], visible: number): number => {
    const target = runes.length
    const gap = target - visible
    if (gap <= 0) return visible

    // Large backlog: collapse it to TAIL in a single bounded step. This is the
    // ONLY "big jump" the user ever sees, and it reflects genuinely catching up
    // on a burst rather than repeatedly jumping by a steady large distance.
    if (gap > TAIL) {
      return target - TAIL
    }

    // Tail region: reveal TAIL chars at a fixed small rate.
    const nextIsCJK = visible < runes.length && isCJK(runes[visible].codePointAt(0) ?? 0)
    const advance = CHAR_PER_TICK

    // CJK penalty: if next rune is CJK, advance every other tick for slower,
    // more natural CJK typing feel.
    if (nextIsCJK) {
      skipFlipRef.current = !skipFlipRef.current
      if (skipFlipRef.current) return visible // skip this tick
    }

    return Math.min(visible + advance, target)
  }

  // ── Reset on empty / shrink (new turn) ──
  // Synchronous reset so the next render shows empty content immediately,
  // without waiting for the 50ms interval tick.
  useLayoutEffect(() => {
    if (!fullText) {
      runesRef.current = []
      runesTextRef.current = ''
      if (visibleRef.current !== 0) {
        visibleRef.current = 0
        setState({ visibleChars: 0, isTyping: false })
      }
      return
    }
    // Rebuild runes cache only when text changes
    if (runesTextRef.current !== fullText) {
      runesRef.current = Array.from(fullText)
      runesTextRef.current = fullText
    }
    if (runesRef.current.length < visibleRef.current) {
      visibleRef.current = 0
      setState({ visibleChars: 0, isTyping: false })
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
      // Use cached runes — only rebuild when fullText changed
      if (runesTextRef.current !== text) {
        runesRef.current = Array.from(text)
        runesTextRef.current = text
      }
      const runes = runesRef.current
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
