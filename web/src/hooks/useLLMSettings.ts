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
import { useCallback, useEffect, useState } from 'react'
import { useWSConnection } from '@/hooks/useWSConnection'

/**
 * Module-level event bus for thinking mode changes.
 * When any useLLMSettings instance calls setThinkingMode, all other
 * instances update their local state instantly — no API re-fetch needed.
 */
const thinkingModeBus = new EventTarget()
import {
  listSubscriptions,
  addSubscription as apiAddSubscription,
  updateSubscription as apiUpdateSubscription,
  removeSubscription as apiRemoveSubscription,
  setDefaultSubscription as apiSetDefaultSubscription,
  setSubscriptionEnabled as apiSetSubscriptionEnabled,
  listAllModelEntries,
  refreshModelEntries as apiRefreshModelEntries,
  updatePerModelConfig as apiUpdatePerModelConfig,
  setModelEnabled as apiSetModelEnabled,
  removeModel as apiRemoveModel,
  upsertModel as apiUpsertModel,
  getUserThinkingMode,
  setUserThinkingMode as apiSetUserThinkingMode,
  getLLMConcurrency,
  setLLMConcurrency as apiSetLLMConcurrency,
  getSettings,
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

const empty: LLMSettingsData = {
  subscriptions: [],
  modelEntries: [],
  thinkingMode: '',
  llmConcurrency: 0,
  tierVanguard: '',
  tierBalance: '',
  tierSwift: '',
}

export function useLLMSettings() {
  const conn = useWSConnection()
  const connected = conn.connected
  const [data, setData] = useState<LLMSettingsData>(empty)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [refreshing, setRefreshing] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      if (!connected) {
        setError('not_connected')
        setLoading(false)
        return
      }
      const [subs, entries, thinkingMode, concurrency, settings] = await Promise.all([
        listSubscriptions(conn),
        listAllModelEntries(conn),
        getUserThinkingMode(conn),
        getLLMConcurrency(conn),
        getSettings(conn, 'cli'),
      ])
      setData({
        subscriptions: Array.isArray(subs) ? subs : [],
        modelEntries: Array.isArray(entries) ? entries : [],
        thinkingMode: thinkingMode ?? '',
        llmConcurrency: concurrency ?? 0,
        tierVanguard: settings['tier_vanguard'] ?? '',
        tierBalance: settings['tier_balance'] ?? '',
        tierSwift: settings['tier_swift'] ?? '',
      })
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }, [conn, connected])

  useEffect(() => {
    void load()
  }, [load])

  // Sync thinking mode across all useLLMSettings instances (e.g. settings
  // dialog changes thinking mode → AgentPanel reflects it instantly).
  useEffect(() => {
    const handler = (e: Event) => {
      const mode = (e as CustomEvent<string>).detail
      setData((d) => ({ ...d, thinkingMode: mode }))
    }
    thinkingModeBus.addEventListener('thinking-mode-change', handler)
    return () => thinkingModeBus.removeEventListener('thinking-mode-change', handler)
  }, [])

  // ── Subscription CRUD ──

  const addSubscription = useCallback(
    async (sub: {
      name: string
      provider: string
      base_url: string
      api_key: string
      model: string
    }): Promise<boolean> => {
      setSaving(true)
      try {
        await apiAddSubscription(conn, sub)
        await load()
        return true
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
        return false
      } finally {
        setSaving(false)
      }
    },
    [conn, load, data],
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

  const removeSubscription = useCallback(
    async (id: string): Promise<boolean> => {
      setSaving(true)
      try {
        await apiRemoveSubscription(conn, id)
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

  const setDefaultSubscription = useCallback(
    async (id: string): Promise<boolean> => {
      setSaving(true)
      try {
        await apiSetDefaultSubscription(conn, id)
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

  const setSubscriptionEnabled = useCallback(
    async (subID: string, enabled: boolean): Promise<boolean> => {
      setSaving(true)
      try {
        await apiSetSubscriptionEnabled(conn, subID, enabled)
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

  // ── Model Management ──

  const updatePerModelConfig = useCallback(
    async (subID: string, model: string, config: PerModelConfig): Promise<boolean> => {
      setSaving(true)
      try {
        await apiUpdatePerModelConfig(conn, subID, model, config)
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

  const setModelEnabled = useCallback(
    async (subID: string, model: string, enabled: boolean): Promise<boolean> => {
      setSaving(true)
      try {
        await apiSetModelEnabled(conn, subID, model, enabled)
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

  const removeModel = useCallback(
    async (subID: string, model: string): Promise<boolean> => {
      setSaving(true)
      try {
        await apiRemoveModel(conn, subID, model)
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

  const refreshModels = useCallback(async (): Promise<boolean> => {
    setRefreshing(true)
    try {
      const entries = await apiRefreshModelEntries(conn)
      setData((d) => ({ ...d, modelEntries: Array.isArray(entries) ? entries : [] }))
      return true
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      return false
    } finally {
      setRefreshing(false)
    }
  }, [conn])

  // ── User-Level Settings ──

  const setThinkingMode = useCallback(
    async (mode: string): Promise<boolean> => {
      setSaving(true)
      try {
        await apiSetUserThinkingMode(conn, mode)
        setData((d) => ({ ...d, thinkingMode: mode }))
        // Broadcast to all other useLLMSettings instances for instant sync.
        thinkingModeBus.dispatchEvent(new CustomEvent('thinking-mode-change', { detail: mode }))
        return true
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
        return false
      } finally {
        setSaving(false)
      }
    },
    [conn],
  )

  const setLLMConcurrency = useCallback(
    async (personal: number): Promise<boolean> => {
      setSaving(true)
      try {
        await apiSetLLMConcurrency(conn, personal)
        setData((d) => ({ ...d, llmConcurrency: personal }))
        return true
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
        return false
      } finally {
        setSaving(false)
      }
    },
    [conn],
  )

  // ── Tier Config ──

  const setTier = useCallback(
    async (tier: 'vanguard' | 'balance' | 'swift', value: string): Promise<boolean> => {
      setSaving(true)
      try {
        await setSetting(conn, 'cli', `tier_${tier}`, value)
        setData((d) => ({
          ...d,
          tierVanguard: tier === 'vanguard' ? value : d.tierVanguard,
          tierBalance: tier === 'balance' ? value : d.tierBalance,
          tierSwift: tier === 'swift' ? value : d.tierSwift,
        }))
        return true
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
        return false
      } finally {
        setSaving(false)
      }
    },
    [conn],
  )

  return {
    data,
    loading,
    error,
    saving,
    refreshing,
    reload: load,
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
