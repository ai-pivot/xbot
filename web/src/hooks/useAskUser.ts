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
