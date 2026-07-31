/**
 * useKeyboardInset — tracks the mobile soft-keyboard height via the
 * VisualViewport API.
 *
 * Returns 0 when the keyboard is closed, or the keyboard's pixel height
 * when it's open. Used by the terminal accessory bar to float above
 * the keyboard.
 */
import { useEffect, useState } from 'react'

/** Minimum height delta to consider the keyboard "open" (avoids jitter). */
const THRESHOLD = 80

export function useKeyboardInset(): number {
  const [inset, setInset] = useState(0)

  useEffect(() => {
    const vv = window.visualViewport
    if (!vv) return

    const update = () => {
      const kb = window.innerHeight - vv.height - vv.offsetTop
      setInset(kb > THRESHOLD ? kb : 0)
    }

    update()
    vv.addEventListener('resize', update)
    vv.addEventListener('scroll', update)
    return () => {
      vv.removeEventListener('resize', update)
      vv.removeEventListener('scroll', update)
    }
  }, [])

  return inset
}
