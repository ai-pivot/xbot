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
  const { askUserPrompts, clearAskUserPrompt, markAskUserAnswered, hydrateAskUserPrompt } = useSessionStore()

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
      // ⛔ **点击即清**（用户 2026-10-16 亲报严重 bug「提交/cancel 后必须立刻消失，现在消失了还会
      //   出现一瞬间」）：旧实现把清除挂在 `ws.send().then()` 之后 ⇒ 往返期间面板仍在，且该窗口内
      //   服务端重播的 pending 问题（或行驱动 hydrate）会把**同一个** prompt 写回 ⇒ 闪回。
      //   ⇒ 先清 + 记下刚答的 requestId（抑制回填），再发消息；失败才把问题还回来（撤销抑制）。
      const requestId = prompt ? prompt.requestId : ''
      clearAskUserPrompt(channel, chatID ?? '')
      markAskUserAnswered(channel, chatID ?? '', requestId)
      void ws.send({ type: 'ask_user_response', channel, chat_id: chatID ?? undefined, answers, cancelled: false })
        .catch((error: unknown) => {
          // ⛔ 失败必须把问题**放回面板**（P1 2026-10-18 审查）：旧实现只弹 toast + 撤销抑制，
          //   而 prompt 在点击那一刻已被清掉、`hydrate` 只在会话切换/行对账时跑 ⇒ 服务端仍
          //   在等这条 `ask_user_response`，用户却再也看不到面板（会话卡 waiting_input）。
          //   撤销抑制（允许回填）+ 显式 hydrate（立即回填）——两者缺一不可：
          //   前者防"仍被闪回防线挡住"，后者不等下一次对账。
          markAskUserAnswered(channel, chatID ?? '', '')
          if (prompt) hydrateAskUserPrompt(channel, chatID ?? '', prompt)
          toast.error(error instanceof Error ? error.message : 'response failed')
        })
    },
    [channel, chatID, ws, clearAskUserPrompt, markAskUserAnswered, hydrateAskUserPrompt, prompt],
  )

  const cancel = useCallback(() => {
    const requestId = prompt ? prompt.requestId : ''
    clearAskUserPrompt(channel, chatID ?? '')
    markAskUserAnswered(channel, chatID ?? '', requestId)
    void ws.send({ type: 'ask_user_response', channel, chat_id: chatID ?? undefined, answers: {}, cancelled: true })
      .catch((error: unknown) => {
        markAskUserAnswered(channel, chatID ?? '', '')
        if (prompt) hydrateAskUserPrompt(channel, chatID ?? '', prompt)
        toast.error(error instanceof Error ? error.message : 'response failed')
      })
  }, [channel, chatID, ws, clearAskUserPrompt, markAskUserAnswered, hydrateAskUserPrompt, prompt])

  return { prompt, respond, cancel }
}
