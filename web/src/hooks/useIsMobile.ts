import { useEffect, useState } from 'react'

const MOBILE_QUERY = '(max-width: 767px)'

/** Detects touch-only devices (no hover capability) via pointer media queries. */
const TOUCH_QUERY = '(hover: none) and (pointer: coarse)'

export function useIsMobile(): boolean {
  const [mobile, setMobile] = useState(() => {
    if (typeof window === 'undefined') return false
    return window.matchMedia(MOBILE_QUERY).matches
  })

  useEffect(() => {
    const media = window.matchMedia(MOBILE_QUERY)
    const onChange = () => setMobile(media.matches)
    onChange()
    media.addEventListener('change', onChange)
    return () => media.removeEventListener('change', onChange)
  }, [])

  return mobile
}

/**
 * Detects touch devices (no hover capability).
 * On such devices, `group-hover:opacity-*` classes never trigger because
 * there is no hover state. Use this to make hidden-on-hover buttons always
 * visible on touch devices.
 */
export function useIsTouch(): boolean {
  const [touch, setTouch] = useState(() => {
    if (typeof window === 'undefined') return false
    return window.matchMedia(TOUCH_QUERY).matches
  })

  useEffect(() => {
    const media = window.matchMedia(TOUCH_QUERY)
    const onChange = () => setTouch(media.matches)
    onChange()
    media.addEventListener('change', onChange)
    return () => media.removeEventListener('change', onChange)
  }, [])

  return touch
}
