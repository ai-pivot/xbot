import { useState, useEffect } from 'react'
import { formatRelativeTime } from '../utils'

/**
 * Hook that returns a relative time string and auto-refreshes every 30 seconds.
 * Falls back to absolute time for dates older than 7 days.
 */
export function useRelativeTime(ts: number | undefined): string {
  const [label, setLabel] = useState(() => ts ? formatRelativeTime(ts) : '')

  useEffect(() => {
    if (!ts) { setLabel(''); return }
    setLabel(formatRelativeTime(ts))

    const id = setInterval(() => {
      setLabel(formatRelativeTime(ts))
    }, 30_000)

    return () => clearInterval(id)
  }, [ts])

  return label
}
