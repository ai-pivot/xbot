/**
 * useDebounce — debounce a value by `delay` ms.
 *
 * Returns a debounced snapshot that updates `delay` ms after the last change
 * to `value`. Used by PathPicker (300ms) and FileSearch (200ms).
 */
import { useEffect, useState } from 'react'

export function useDebounce<T>(value: T, delay: number): T {
  const [debounced, setDebounced] = useState(value)
  useEffect(() => {
    const id = setTimeout(() => setDebounced(value), delay)
    return () => clearTimeout(id)
  }, [value, delay])
  return debounced
}
