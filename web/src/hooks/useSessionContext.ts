/**
 * useSessionContext — resolves session-level context info (Spec C §1.2).
 *
 * max_context resolution chain (mirrors TUI ResolveEffectiveMaxContext):
 *   1. getSessionSubscription(ws, channel, chatID) → (subID, model)
 *   2. listSubscriptions(ws) → find subscription by subID
 *   3. subscription.per_model_configs[model]?.max_context → if > 0, use it
 *   4. subscription.max_context → if > 0, use it
 *   5. Fallback: 200000 (config default)
 *
 * Also resolves the current model name for display.
 * Caches results per (channel, chatID) — re-fetches on session switch.
 */
import { useCallback, useEffect, useState } from 'react'
import { useWSConnection } from '@/hooks/useWSConnection'
import { getSessionSubscription, listSubscriptions } from '@/components/agent/api'
import type { Subscription } from '@/types/shared'

const DEFAULT_MAX_CONTEXT = 200000

export interface SessionContextInfo {
  /** Current model name (from session subscription). */
  model: string
  /** Current subscription ID. */
  subscriptionID: string
  /** Current subscription name. */
  subscriptionName: string
  /** Resolved max context tokens (resolution chain applied). */
  maxContext: number
  /** True while loading. */
  loading: boolean
  /** Error message if resolution failed. */
  error: string | null
}

const emptyInfo: SessionContextInfo = {
  model: '',
  subscriptionID: '',
  subscriptionName: '',
  maxContext: DEFAULT_MAX_CONTEXT,
  loading: true,
  error: null,
}

export function useSessionContext(channel: string, chatID: string | null): SessionContextInfo {
  const ws = useWSConnection()
  const [info, setInfo] = useState<SessionContextInfo>(emptyInfo)

  const load = useCallback(async () => {
    if (!chatID || !ws.connected) {
      setInfo({ ...emptyInfo, loading: false })
      return
    }
    setInfo((prev) => ({ ...prev, loading: true, error: null }))
    try {
      const [sessionSub, subs] = await Promise.all([
        getSessionSubscription(ws, channel, chatID),
        listSubscriptions(ws),
      ])

      const subID = sessionSub.subscription_id ?? ''
      const model = sessionSub.model ?? ''
      const subsList = Array.isArray(subs) ? subs : []
      const sub = subsList.find((s: Subscription) => s.id === subID)
      const subName = sub?.name ?? ''

      // max_context resolution chain
      let maxContext = DEFAULT_MAX_CONTEXT
      if (sub) {
        const perModel = sub.per_model_configs?.[model]
        if (perModel && perModel.max_context > 0) {
          maxContext = perModel.max_context
        } else if (sub.max_context > 0) {
          maxContext = sub.max_context
        }
      }

      setInfo({
        model,
        subscriptionID: subID,
        subscriptionName: subName,
        maxContext,
        loading: false,
        error: null,
      })
    } catch (e) {
      setInfo({
        model: '',
        subscriptionID: '',
        subscriptionName: '',
        maxContext: DEFAULT_MAX_CONTEXT,
        loading: false,
        error: e instanceof Error ? e.message : String(e),
      })
    }
  }, [ws, channel, chatID])

  useEffect(() => {
    void load()
  }, [load])

  return info
}

/** Format a token count with K/M suffix (Spec C §1.2 right-side info). */
export function formatTokenCount(n: number): string {
  if (n >= 1_000_000) {
    const v = n / 1_000_000
    return `${Number.isInteger(v) ? v : v.toFixed(1)}M`
  }
  if (n >= 1000) {
    const v = n / 1000
    return `${Number.isInteger(v) ? v : v.toFixed(1)}K`
  }
  return String(n)
}
