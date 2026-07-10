/**
 * useLLMSettings — load + mutate the user-level LLM config over WS RPC
 * (Spec 7 §3.6).
 *
 * RPC contract (serverapp/rpc_table.go):
 *   list_models                  -> string[]
 *   get_default_model            -> string          (current user model)
 *   switch_model {model}         -> void            (user-level switch)
 *   get_user_max_context         -> int
 *   set_user_max_context {max_context:int}
 *   get_user_max_output_tokens   -> int
 *   set_user_max_output_tokens {max_tokens:int}
 *   get_user_thinking_mode       -> string          ("" = auto)
 *   set_user_thinking_mode {mode:string}
 *
 * Returns {data, loading, error, setters, saving}. Each setter returns a
 * Promise so the caller can await + toast on completion.
 */
import { useCallback, useEffect, useState } from 'react'
import { useWSConnection } from '@/hooks/useWSConnection'

export interface LLMSettings {
  models: string[]
  model: string
  maxContext: number
  maxOutputTokens: number
  thinkingMode: string
}

const empty: LLMSettings = {
  models: [],
  model: '',
  maxContext: 0,
  maxOutputTokens: 0,
  thinkingMode: '',
}

export function useLLMSettings() {
  const conn = useWSConnection()
  const [data, setData] = useState<LLMSettings>(empty)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      if (!conn.connected) {
        setError('not_connected')
        setLoading(false)
        return
      }
      const [models, model, maxContext, maxOutputTokens, thinkingMode] =
        await Promise.all([
          conn.rpc<string[]>('list_models'),
          conn.rpc<string>('get_default_model'),
          conn.rpc<number>('get_user_max_context'),
          conn.rpc<number>('get_user_max_output_tokens'),
          conn.rpc<string>('get_user_thinking_mode'),
        ])
      setData({
        models: Array.isArray(models) ? models : [],
        model: model ?? '',
        maxContext: maxContext ?? 0,
        maxOutputTokens: maxOutputTokens ?? 0,
        thinkingMode: thinkingMode ?? '',
      })
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }, [conn])

  useEffect(() => {
    void load()
  }, [load])

  const runSet = useCallback(
    async (fn: () => Promise<unknown>, patch: Partial<LLMSettings>) => {
      setSaving(true)
      try {
        await fn()
        setData((d) => ({ ...d, ...patch }))
        return true
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
        return false
      } finally {
        setSaving(false)
      }
    },
    [],
  )

  const setModel = useCallback(
    (model: string) =>
      runSet(() => conn.rpc('switch_model', { model }), { model }),
    [conn, runSet],
  )

  const setMaxContext = useCallback(
    (maxContext: number) =>
      runSet(() => conn.rpc('set_user_max_context', { max_context: maxContext }), {
        maxContext,
      }),
    [conn, runSet],
  )

  const setMaxOutputTokens = useCallback(
    (maxOutputTokens: number) =>
      runSet(
        () => conn.rpc('set_user_max_output_tokens', { max_tokens: maxOutputTokens }),
        { maxOutputTokens },
      ),
    [conn, runSet],
  )

  const setThinkingMode = useCallback(
    (thinkingMode: string) =>
      runSet(() => conn.rpc('set_user_thinking_mode', { mode: thinkingMode }), {
        thinkingMode,
      }),
    [conn, runSet],
  )

  return {
    data,
    loading,
    error,
    saving,
    reload: load,
    setModel,
    setMaxContext,
    setMaxOutputTokens,
    setThinkingMode,
  }
}
