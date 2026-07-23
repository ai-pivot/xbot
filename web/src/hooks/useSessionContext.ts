/**
 * Read the backend's authoritative context snapshot for one existing session.
 * The last successful snapshot survives refreshes and connection interruptions;
 * switching sessions immediately returns an unknown snapshot.
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import { getContextUsage } from '@/components/agent/api'
import { useWSConnection } from '@/hooks/useWSConnection'

export interface SessionContextInfo {
  available: boolean
  promptTokens: number
  completionTokens: number
  maxContext: number
  usagePercent: number | null
  model: string
  subscriptionID: string
  subscriptionName: string
  loading: boolean
  error: string | null
  refresh: () => Promise<void>
}

type SessionContextState = Omit<SessionContextInfo, 'refresh'> & { key: string }

function emptyInfo(key: string, loading = false): SessionContextState {
  return {
    key,
    available: false,
    promptTokens: 0,
    completionTokens: 0,
    maxContext: 0,
    usagePercent: null,
    model: '',
    subscriptionID: '',
    subscriptionName: '',
    loading,
    error: null,
  }
}

function finiteNonNegative(value: number): number {
  return Number.isFinite(value) && value >= 0 ? value : 0
}

export function useSessionContext(channel: string, chatID: string | null): SessionContextInfo {
  const ws = useWSConnection()
  const connected = ws.connected
  const key = chatID ? `${channel}:${chatID}` : ''
  const [info, setInfo] = useState<SessionContextState>(() => emptyInfo(key, Boolean(chatID)))
  const loadSeq = useRef(0)

  const load = useCallback(async () => {
    const seq = ++loadSeq.current
    if (!chatID) {
      setInfo(emptyInfo('', false))
      return
    }
    if (!connected) {
      setInfo((previous) => previous.key === key
        ? { ...previous, loading: false }
        : emptyInfo(key, false))
      return
    }

    setInfo((previous) => previous.key === key
      ? { ...previous, loading: true, error: null }
      : emptyInfo(key, true))

    try {
      const snapshot = await getContextUsage(ws, channel, chatID)
      if (seq !== loadSeq.current) return

      const promptTokens = finiteNonNegative(snapshot.prompt_tokens)
      const completionTokens = finiteNonNegative(snapshot.completion_tokens)
      const maxContext = finiteNonNegative(snapshot.max_context_tokens)
      const usagePercent = typeof snapshot.usage_percent === 'number' && Number.isFinite(snapshot.usage_percent)
        ? snapshot.usage_percent
        : null
      const available = snapshot.available && promptTokens > 0 && maxContext > 0 && usagePercent !== null

      setInfo({
        key,
        available,
        promptTokens: available ? promptTokens : 0,
        completionTokens: available ? completionTokens : 0,
        maxContext,
        usagePercent: available ? usagePercent : null,
        model: snapshot.model ?? '',
        subscriptionID: snapshot.subscription_id ?? '',
        subscriptionName: snapshot.subscription_name ?? '',
        loading: false,
        error: null,
      })
    } catch (error) {
      if (seq !== loadSeq.current) return
      setInfo((previous) => ({
        ...(previous.key === key ? previous : emptyInfo(key)),
        loading: false,
        error: error instanceof Error ? error.message : String(error),
      }))
    }
  }, [ws, channel, chatID, key, connected])

  useEffect(() => {
    void load()
    return () => {
      loadSeq.current += 1
    }
  }, [load])

  const visibleInfo = info.key === key ? info : emptyInfo(key, Boolean(chatID && ws.connected))
  const { key: _key, ...result } = visibleInfo
  return { ...result, refresh: load }
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
