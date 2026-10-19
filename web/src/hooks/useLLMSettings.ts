/**
 * useLLMSettings — full LLM config hook (Spec D).
 *
 * Manages:
 *   - Subscription CRUD (list, add, update, remove, set-default, enable/disable)
 *   - Model management (list entries, refresh, upsert, remove, per-model config)
 *   - User-level settings (thinking mode, max concurrency)
 *   - Tier config (vanguard / balance / swift) via generic settings RPC
 *
 * All RPC calls go through WSConnection.rpc → POST /api/rpc.
 * The backend resolves sender_id from auth context.
 */
import { useCallback, useEffect, useState, useSyncExternalStore } from 'react'
import { useWSConnection } from '@/hooks/useWSConnection'
import { llmSettingsStore } from './llmSettingsStore'

// External consumers refresh their session context after configuration edits.
const LLM_CONFIG_CHANGED = 'xbot:llm-config-changed'
const llmConfigBus = new EventTarget()

/** 订阅 LLM 配置变更（AgentPanel 另用它触发 sessionContext.refresh）。 */
export function subscribeLLMConfigChanged(listener: () => void): () => void {
  // ⚠️ add/remove 必须用**同一个** handler 实例，否则退订静默失效（监听器泄漏）。
  const handler = () => listener()
  llmConfigBus.addEventListener(LLM_CONFIG_CHANGED, handler)
  return () => llmConfigBus.removeEventListener(LLM_CONFIG_CHANGED, handler)
}
import {
  addSubscription as apiAddSubscription,
  updateSubscription as apiUpdateSubscription,
  removeSubscription as apiRemoveSubscription,
  setDefaultSubscription as apiSetDefaultSubscription,
  setSubscriptionEnabled as apiSetSubscriptionEnabled,
  refreshModelEntries as apiRefreshModelEntries,
  updatePerModelConfig as apiUpdatePerModelConfig,
  setModelEnabled as apiSetModelEnabled,
  removeModel as apiRemoveModel,
  upsertModel as apiUpsertModel,
  setUserThinkingMode as apiSetUserThinkingMode,
  setLLMConcurrency as apiSetLLMConcurrency,
  setSetting,
  isMaskedAPIKey,
} from '@/components/agent/api'
import type { Subscription, ModelEntry, PerModelConfig } from '@/types/shared'

export interface LLMSettingsData {
  subscriptions: Subscription[]
  modelEntries: ModelEntry[]
  thinkingMode: string
  llmConcurrency: number
  tierVanguard: string
  tierBalance: string
  tierSwift: string
}

export function useLLMSettings() {
  const conn = useWSConnection()
  const store = llmSettingsStore(conn)
  const { data, loading, error: loadError } = useSyncExternalStore(store.subscribe, store.getSnapshot)
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [refreshing, setRefreshing] = useState(false)
  const notifyConfigChanged = useCallback(() => {
    llmConfigBus.dispatchEvent(new Event(LLM_CONFIG_CHANGED))
  }, [])
  // Mounts and recent SSE edges reuse one batch. Explicit reload/mutations
  // bypass the cache and invalidate any older read in flight.
  const load = useCallback(() => {
    setError(null)
    return store.load(true)
  }, [store])
  const reload = useCallback(async () => {
    try { await load() } catch { /* The shared store exposes the error. */ }
  }, [load])
  useEffect(() => {
    setError(null)
    void store.load().catch(() => {})
  }, [store])

  // ── Subscription CRUD ──

  const addSubscription = useCallback(
    async (sub: {
      name: string
      provider: string
      base_url: string
      api_key: string
      model: string
      api_type?: string
    }): Promise<boolean> => {
      setSaving(true)
      try {
        await apiAddSubscription(conn, sub)
        await load()
        notifyConfigChanged()
        return true
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
        return false
      } finally {
        setSaving(false)
      }
    },
    [conn, load, notifyConfigChanged],
  )

  const updateSubscription = useCallback(
    async (
      id: string,
      sub: {
        name: string
        provider: string
        base_url: string
        api_key: string
        model: string
        max_output_tokens?: number
        thinking_mode?: string
        api_type?: string
      },
    ): Promise<boolean> => {
      setSaving(true)
      try {
        // If API key is masked (unchanged from server), send empty to preserve
        const apiKeyToSend = isMaskedAPIKey(sub.api_key) ? '' : sub.api_key
        // Merge with existing subscription to preserve fields not being edited
        const existing = data?.subscriptions.find((s) => s.id === id)
        await apiUpdateSubscription(conn, id, {
          ...sub,
          api_key: apiKeyToSend,
          max_output_tokens: sub.max_output_tokens ?? existing?.max_output_tokens ?? 0,
          thinking_mode: sub.thinking_mode ?? existing?.thinking_mode ?? '',
          api_type: sub.api_type ?? existing?.api_type ?? '',
        })
        await load()
        notifyConfigChanged()
        return true
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
        return false
      } finally {
        setSaving(false)
      }
    },
    [conn, load, data, notifyConfigChanged],
  )

  const removeSubscription = useCallback(
    async (id: string): Promise<boolean> => {
      setSaving(true)
      try {
        await apiRemoveSubscription(conn, id)
        await load()
        notifyConfigChanged()
        return true
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
        return false
      } finally {
        setSaving(false)
      }
    },
    [conn, load, notifyConfigChanged],
  )

  const setDefaultSubscription = useCallback(
    async (id: string): Promise<boolean> => {
      setSaving(true)
      try {
        await apiSetDefaultSubscription(conn, id)
        await load()
        notifyConfigChanged()
        return true
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
        return false
      } finally {
        setSaving(false)
      }
    },
    [conn, load, notifyConfigChanged],
  )

  const setSubscriptionEnabled = useCallback(
    async (subID: string, enabled: boolean): Promise<boolean> => {
      setSaving(true)
      try {
        await apiSetSubscriptionEnabled(conn, subID, enabled)
        await load()
        notifyConfigChanged()
        return true
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
        return false
      } finally {
        setSaving(false)
      }
    },
    [conn, load, notifyConfigChanged],
  )

  // ── Model Management ──

  const updatePerModelConfig = useCallback(
    async (subID: string, model: string, config: PerModelConfig): Promise<boolean> => {
      setSaving(true)
      try {
        await apiUpdatePerModelConfig(conn, subID, model, config)
        await load()
        notifyConfigChanged()
        return true
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
        return false
      } finally {
        setSaving(false)
      }
    },
    [conn, load, notifyConfigChanged],
  )

  const setModelEnabled = useCallback(
    async (subID: string, model: string, enabled: boolean): Promise<boolean> => {
      setSaving(true)
      try {
        await apiSetModelEnabled(conn, subID, model, enabled)
        await load()
        notifyConfigChanged()
        return true
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
        return false
      } finally {
        setSaving(false)
      }
    },
    [conn, load, notifyConfigChanged],
  )

  const removeModel = useCallback(
    async (subID: string, model: string): Promise<boolean> => {
      setSaving(true)
      try {
        await apiRemoveModel(conn, subID, model)
        await load()
        notifyConfigChanged()
        return true
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
        return false
      } finally {
        setSaving(false)
      }
    },
    [conn, load, notifyConfigChanged],
  )

  const upsertModel = useCallback(
    async (
      subID: string,
      model: string,
      maxContext = 0,
      maxOutput = 0,
      apiType = '',
    ): Promise<boolean> => {
      setSaving(true)
      try {
        await apiUpsertModel(conn, subID, model, maxContext, maxOutput, apiType)
        await load()
        notifyConfigChanged()
        return true
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
        return false
      } finally {
        setSaving(false)
      }
    },
    [conn, load, notifyConfigChanged],
  )

  const refreshModels = useCallback(async (): Promise<boolean> => {
    setRefreshing(true)
    try {
      await apiRefreshModelEntries(conn)
      await load()
      notifyConfigChanged()
      return true
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      return false
    } finally {
      setRefreshing(false)
    }
  }, [conn, load, notifyConfigChanged])

  // ── User-Level Settings ──

  const setThinkingMode = useCallback(
    async (mode: string): Promise<boolean> => {
      setSaving(true)
      try {
        await apiSetUserThinkingMode(conn, mode)
        await load()
        return true
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
        return false
      } finally {
        setSaving(false)
      }
    },
    [conn, load],
  )

  const setLLMConcurrency = useCallback(
    async (personal: number): Promise<boolean> => {
      setSaving(true)
      try {
        await apiSetLLMConcurrency(conn, personal)
        await load()
        return true
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
        return false
      } finally {
        setSaving(false)
      }
    },
    [conn, load],
  )

  // ── Tier Config ──

  const setTier = useCallback(
    async (tier: 'vanguard' | 'balance' | 'swift', value: string): Promise<boolean> => {
      setSaving(true)
      try {
        await setSetting(conn, 'cli', `tier_${tier}`, value)
        await load()
        return true
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
        return false
      } finally {
        setSaving(false)
      }
    },
    [conn, load],
  )

  return {
    data,
    loading,
    error: error ?? loadError,
    saving,
    refreshing,
    reload,
    // Subscription CRUD
    addSubscription,
    updateSubscription,
    removeSubscription,
    setDefaultSubscription,
    setSubscriptionEnabled,
    // Model management
    updatePerModelConfig,
    setModelEnabled,
    removeModel,
    upsertModel,
    refreshModels,
    // User-level settings
    setThinkingMode,
    setLLMConcurrency,
    // Tier config
    setTier,
  }
}
