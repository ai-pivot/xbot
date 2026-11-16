/**
 * StructuredWorkspace —— 结构化消息工作区：消息流 + 收件箱。
 *
 * 数据源：useSessionStore().activeSession（channel + chatID）。
 */
import { useMemo } from 'react'
import { useSearchParams } from 'react-router-dom'

import { useSessionStore } from '@/hooks/useSessionStore'
import { Inbox } from './Inbox'
import { StructuredMessageStream } from './StructuredMessageStream'
import { useStructuredMessages } from './useStructuredMessages'

export function StructuredWorkspace() {
  const { activeSession } = useSessionStore()
  // 支持深链/测试显式指定会话：/structured?channel=web&chat_id=chat_x
  const [params] = useSearchParams()
  const queryChannel = params.get('channel')
  const queryChatID = params.get('chat_id')
  const scope = useMemo(
    () => ({
      channel: queryChannel ?? activeSession?.channel ?? 'web',
      chat_id: queryChatID ?? activeSession?.chatID ?? '',
    }),
    [queryChannel, queryChatID, activeSession?.channel, activeSession?.chatID],
  )
  const state = useStructuredMessages(scope)

  if (!scope.chat_id) {
    return (
      <div data-testid="structured-no-session" className="p-4 text-sm opacity-60">
        请先选择一个会话。
      </div>
    )
  }

  return (
    <div data-testid="structured-workspace" className="flex h-full min-h-0 flex-col md:flex-row">
      <StructuredMessageStream
        messages={state.messages}
        loading={state.loading}
        error={state.error}
        hasMore={state.hasMore}
        onLoadMore={() => void state.loadMore()}
        onAnswer={state.answer}
        onDecide={state.decide}
      />
      <Inbox scope={scope} onAnswer={state.answer} onDecide={state.decide} />
    </div>
  )
}
