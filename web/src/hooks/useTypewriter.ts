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
 * catch-up). The 50ms interval alone produces smooth, continuous typewriter
 * motion.
 *
 * Performance: ALL useTypewriter instances share a SINGLE setInterval timer
 * (sharedTimer). This ensures React batches their setState calls into one
 * render per tick — critical when LiveIteration uses two instances (text +
 * reasoning). Without sharing, two independent 50ms timers fire at different
 * event-loop ticks, causing two separate renders per 50ms cycle (trace
 * analysis: 343 React renders in 13s, 5 >100ms each with 82-94ms
 * UpdateLayoutTree).
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

// ── Shared timer: all useTypewriter instances tick on the SAME setInterval ──
// This guarantees their setState calls land in the same event-loop tick,
// so React batches them into a single render (instead of N separate renders).
const subscribers = new Set<() => void>()
let sharedTimer: ReturnType<typeof setInterval> | null = null

function ensureTimer() {
  if (sharedTimer) return
  sharedTimer = setInterval(() => {
    for (const fn of subscribers) {
      try { fn() } catch (err) { console.error('typewriter tick failed', err) }
    }
  }, TICK_MS)
}

function subscribe(fn: () => void): () => void {
  subscribers.add(fn)
  ensureTimer()
  return () => {
    subscribers.delete(fn)
    if (subscribers.size === 0 && sharedTimer) {
      clearInterval(sharedTimer)
      sharedTimer = null
    }
  }
}

export interface TypewriterState {
  /** Number of visible Unicode code points. The rendered Markdown DOM is clipped to this count. */
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

  // Subscribe to the shared timer — all instances tick on the same interval,
  // so React batches their setState calls into one render per tick.
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
    return subscribe(tick)
  }, [])

  return state
}
