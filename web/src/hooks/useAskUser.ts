/**
 * useAskUser — reads the AskUser prompt from useSessionStore (not local state).
 *
 * The prompt is stored globally in useSessionStore keyed by "channel:chatID",
 * so it survives session switching. The SSE listener in useSessionStore
 * populates it; this hook just reads and provides respond/cancel actions.
 *
 * On SSE reconnect, the backend resends the pending ask_user message,
 * which repopulates the store — so page refresh works too.
 */
import { useCallback } from 'react'
import { toast } from 'sonner'

import { useSessionStore } from '@/hooks/useSessionStore'
import { useWSConnection } from '@/hooks/useWSConnection'

interface UseAskUserOptions {
  chatID: string | null
  channel?: string
}

export interface UseAskUserResult {
  prompt: import('@/types/agent').AskUserPrompt | null
  respond: (answers: Record<string, string>) => void
  cancel: () => void
}

export function useAskUser({ chatID, channel = 'web' }: UseAskUserOptions): UseAskUserResult {
  const ws = useWSConnection()
  const { askUserPrompts, clearAskUserPrompt } = useSessionStore()

  const key = `${channel}:${chatID ?? ''}`
  const prompt = chatID ? askUserPrompts.get(key) ?? null : null

  // [ASKDEBUG] 诊断：store 里有 pending ask 但本面板 miss —— key 匹配问题的
  // 直接证据（wantKey vs store 实际 keys 的对比暴露格式差异）。只在
  // askUserPrompts 非空（确实有 ask 到达过）时打印；DEV-only（CR#9: PR 描述
  // 承诺"正常流程静默"——生产不打）。
  if (!prompt && askUserPrompts.size > 0 && import.meta.env.DEV) {
    console.warn('[ASKDEBUG] panel miss (ask exists in store, key mismatch?)', {
      wantKey: key,
      wantChatID: chatID,
      wantChannel: channel,
      storeKeys: [...askUserPrompts.keys()],
    })
  }

  const respond = useCallback(
    (answers: Record<string, string>) => {
      void ws.send({ type: 'ask_user_response', channel, chat_id: chatID ?? undefined, answers, cancelled: false })
        .then(() => clearAskUserPrompt(channel, chatID ?? ''))
        .catch((error: unknown) => {
          toast.error(error instanceof Error ? error.message : 'response failed')
        })
    },
    [channel, chatID, ws, clearAskUserPrompt],
  )

  const cancel = useCallback(() => {
    void ws.send({ type: 'ask_user_response', channel, chat_id: chatID ?? undefined, answers: {}, cancelled: true })
      .then(() => clearAskUserPrompt(channel, chatID ?? ''))
      .catch((error: unknown) => {
        toast.error(error instanceof Error ? error.message : 'response failed')
      })
  }, [channel, chatID, ws, clearAskUserPrompt])

  return { prompt, respond, cancel }
}
