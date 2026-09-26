/** A dismissible release notice beside the desktop connection status. */
import { useEffect, useState } from 'react'
import { ArrowUpCircle, X } from 'lucide-react'

import { useWSConnection } from '@/hooks/useWSConnection'
import { postAPI } from '@/lib/api'
import { useI18n } from '@/providers/i18n'

interface UpdateCheck {
  current: string
  latest: string
  hasUpdate: boolean
  skipped: boolean
}

const DISMISSED_VERSION_KEY = 'xbot:update-reminder:dismissed:v1'
const CHECK_INTERVAL_MS = 6 * 60 * 60 * 1000

function dismissedVersion(): string | null {
  try {
    return localStorage.getItem(DISMISSED_VERSION_KEY)
  } catch {
    return null
  }
}

export function UpdateReminder({ onOpenSettings }: { onOpenSettings: () => void }) {
  const { t } = useI18n()
  const ws = useWSConnection()
  const [connected, setConnected] = useState(ws.connected)
  const [update, setUpdate] = useState<UpdateCheck | null>(null)
  const [dismissed, setDismissed] = useState(dismissedVersion)

  // The WS context object is stable; subscribe to connection changes explicitly.
  useEffect(() => {
    const unsubscribe = ws.onConnectionChange((value) => {
      setConnected(value)
      if (!value) setUpdate(null)
    })
    setConnected(ws.connected)
    return unsubscribe
  }, [ws])

  useEffect(() => {
    if (!connected) return
    const controller = new AbortController()
    const check = async () => {
      try {
        const result = await postAPI<UpdateCheck>(
          '/api/rpc',
          { method: 'check_update', params: {} },
          { signal: controller.signal },
        )
        if (!controller.signal.aborted) setUpdate(result)
      } catch {
        // A failed check must not disturb chat. Retry at the next interval.
      }
    }
    void check()
    const timer = window.setInterval(() => void check(), CHECK_INTERVAL_MS)
    return () => {
      controller.abort()
      window.clearInterval(timer)
    }
  }, [connected])

  if (!connected || !update?.hasUpdate || update.skipped || !update.current || !update.latest || dismissed === update.latest) {
    return null
  }

  const label = t('layout.updateReminder', { current: update.current, latest: update.latest })
  const dismiss = () => {
    setDismissed(update.latest)
    try {
      localStorage.setItem(DISMISSED_VERSION_KEY, update.latest)
    } catch {
      // Still dismiss for this page when storage is unavailable.
    }
  }

  return (
    <div role="status" className="flex min-w-0 max-w-[min(24rem,45vw)] shrink items-center rounded-md border border-accent/30 bg-accent/10 text-accent">
      <button
        type="button"
        onClick={onOpenSettings}
        title={label}
        className="flex min-w-0 items-center gap-1.5 px-2 py-1 hover:bg-accent/10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/60"
      >
        <ArrowUpCircle className="size-3.5 shrink-0" aria-hidden="true" />
        <span className="truncate">{label}</span>
      </button>
      <button
        type="button"
        onClick={dismiss}
        aria-label={t('layout.dismissUpdateReminder')}
        className="shrink-0 rounded-r-md px-1.5 py-1 hover:bg-accent/10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/60"
      >
        <X className="size-3" aria-hidden="true" />
      </button>
    </div>
  )
}
