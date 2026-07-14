/**
 * AgentPanel — the Agent workspace panel.
 *
 * Wires the message + progress + ask-user hooks for one chat and composes the
 * message list, input, and ask-user surface.
 *
 * Spec C: Rewind is now inline-edit mode (no RewindDialog). The MessageList
 * carries editingMessageId state; user messages show a Pencil icon that
 * switches to an inline textarea on click.
 *
 * Chat identity:
 *   - The main Agent tab follows SessionStore.activeSession directly.
 *   - SubAgent tabs are fixed to their parent chat + role/instance params.
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import { toast } from 'sonner'

import { useAskUser } from '@/hooks/useAskUser'
import { useChatMessages, type Attachments } from '@/hooks/useChatMessages'
import { useCollapseLevel, useMergeTools } from '@/hooks/useCollapseLevel'
import { useProgressStream } from '@/hooks/useProgressStream'
import { useTodos } from '@/hooks/useTodos'
import { useActiveSSESubscription } from '@/hooks/useActiveSSESubscription'
import { useSessionContext } from '@/hooks/useSessionContext'
import { useLLMSettings } from '@/hooks/useLLMSettings'
import { rewindHistory } from '@/components/agent/api'

import { AskUserPanel } from '@/components/agent/AskUserPanel'
import { MessageInput } from '@/components/agent/MessageInput'
import { MessageList } from '@/components/agent/MessageList'
import { latestCompactBoundaryIndex } from '@/components/agent/MessageList'
import { ModelStatusBar } from '@/components/agent/ModelStatusBar'
import { useDockviewContext } from '@/workspace/types'
import type { PanelProps } from '@/workspace/panels/types'
import type { ChatMessage } from '@/types/shared'
import { useI18n } from '@/providers/i18n'

interface RewindHistoryResponse {
  draft?: string
  rewind_result?: {
    restored?: string[]
    created_del?: string[]
    skipped?: string[]
    errors?: string[]
  }
}

export function AgentPanel({ params }: PanelProps) {
  const ctx = useDockviewContext()
  const ws = ctx.ws
  const store = ctx.sessionStore
  const rightSidebar = ctx.rightSidebar
  const { t } = useI18n()
  const { level } = useCollapseLevel()
  const { mergeTools } = useMergeTools()
  const [draft, setDraft] = useState<string | undefined>(undefined)
  const [followResetToken, setFollowResetToken] = useState(0)
  const [editingMessageId, setEditingMessageId] = useState<string | null>(null)
  const wasSubscribedRef = useRef<boolean | null>(null)

  // Detect SubAgent mode: when the panel carries SubAgent params, we load
  // messages via get_session_messages RPC instead of get_history.
  const isSubAgent = !!((params.subAgentRole && params.parentChatID) || params.agentChatID)

  const activeSession = store.activeSession
  const chatID = params.agentChatID
    ? (params.agentChatID ?? null)
    : isSubAgent
      ? (params.parentChatID ?? null)
      : (activeSession?.chatID ?? null)
  const liveSubAgentChatID = !params.agentChatID && isSubAgent && params.subAgentRole && params.parentChatID
    ? `${params.parentChannel ?? 'web'}:${params.parentChatID}/${params.subAgentRole}${params.subAgentInstance ? `:${params.subAgentInstance}` : ''}`
    : null
  const progressChatID = params.agentChatID ?? liveSubAgentChatID ?? chatID
  const subscribeChatID = params.agentChatID ?? liveSubAgentChatID ?? chatID
  const messageChannel = params.agentChatID ? 'agent' : isSubAgent ? (params.parentChannel ?? 'web') : (activeSession?.channel ?? 'web')
  const progressChannel = params.agentChatID || liveSubAgentChatID ? 'agent' : messageChannel
  const shouldSubscribe = true // Panels always subscribe — SSE stays alive until panel closes
  const historyEnabled = params.agentChatID
    ? !!params.agentChatID
    : isSubAgent
      ? !!chatID
      : !!activeSession?.chatID

  useActiveSSESubscription({
    ws,
    chatID: subscribeChatID,
    channel: progressChannel,
    active: shouldSubscribe,
  })

  const chat = useChatMessages({
    chatID,
    channel: messageChannel,
    enabled: historyEnabled,
    ws,
    subAgentRole: params.subAgentRole,
    subAgentInstance: params.subAgentInstance,
    parentChatID: params.parentChatID,
    agentChatID: params.agentChatID,
    liveEventsEnabled: shouldSubscribe,
  })
  const reloadChat = chat.reload

  useEffect(() => {
    const wasSubscribed = wasSubscribedRef.current
    wasSubscribedRef.current = shouldSubscribe
    if (wasSubscribed === false && shouldSubscribe) void reloadChat()
  }, [reloadChat, shouldSubscribe])

  useEffect(() => {
    if (!isSubAgent) return
    return ws.onSession((ev) => {
      if (!ev.role) return
      if (params.subAgentRole && ev.role !== params.subAgentRole) return
      if ((params.subAgentInstance ?? '') && ev.instance !== params.subAgentInstance) return
      const parentID = ev.parent_id || ev.chat_id
      if (!params.agentChatID && params.parentChatID && parentID && parentID !== params.parentChatID) return
      void reloadChat()
    })
  }, [isSubAgent, params.agentChatID, params.parentChatID, params.subAgentInstance, params.subAgentRole, reloadChat, ws])

  const progress = useProgressStream({
    chatID: progressChatID,
    channel: progressChannel,
    initialProgress: chat.resolvedChatID === chatID ? chat.initialProgress : null,
    onAssistantComplete: (finalText, iterations) => {
      // Both main Agent and SubAgent panels append the final reply to the
      // message list. SubAgent panels previously had onAssistantComplete=undefined,
      // causing the final reply to never appear in the message list.
      chat.appendAssistant(finalText, iterations)
      void chat.reload()
    },
    ws,
    onHistoryCompacted: isSubAgent ? undefined : () => {
      chat.reload()
    },
    onSessionReset: isSubAgent ? undefined : () => {
      chat.clearMessages()
      void chat.reload()
    },
    disabled: false, // Always enabled — SSE subscription managed by useActiveSSESubscription
  })
  const progressSnapshot = progress.progressSnapshot
  const liveMessage = progress.liveMessage
  const isStreaming = progress.isStreaming
  const askUser = useAskUser({ chatID, channel: messageChannel })

  const todoState = useTodos(progressSnapshot.todos)
  // Busy while streaming (live or hydrated from a resumed session).
  const busy = isStreaming && !askUser.prompt

  // Session context info (model, maxContext) for ContextBar
  const sessionContext = useSessionContext(messageChannel, chatID)
  const promptTokens = progressSnapshot.tokenUsage?.promptTokens ?? 0
  const llmSettings = useLLMSettings()

  // Keep sendMessageRef before rewindTo so rewindTo can call sendMessage
  // (which increments followResetToken for scroll-follow behavior)
  const sendMessageRef = useRef(chat.sendMessage)
  sendMessageRef.current = chat.sendMessage

  const sendMessage = useCallback((content: string, attachments?: Attachments) => {
    setFollowResetToken((v) => v + 1)
    sendMessageRef.current(content, attachments)
  }, [])

  // Rewind via inline edit: rewind to the message's timestamp, then send
  // the edited content as a new message.
  const rewindTo = useCallback(async (editedContent: string, originalMessage: ChatMessage) => {
    if (!chatID || isSubAgent || !originalMessage.timestamp) return
    const cutoff = Date.parse(originalMessage.timestamp)
    if (!Number.isFinite(cutoff) || cutoff <= 0) return
    try {
      await rewindHistory<RewindHistoryResponse>({ channel: messageChannel, chatID }, cutoff)
      // Exit edit mode
      setEditingMessageId(null)
      // Send the edited content as a new message (sendMessage increments
      // followResetToken so the viewport scrolls to bottom for the response)
      sendMessage(editedContent)
      // Reload to reflect the rewound history
      void chat.reload()
      toast.success(t('agent.rewindComplete'))
    } catch (e) {
      // Keep edit mode active on error so user can retry or cancel
      toast.error(e instanceof Error ? e.message : t('agent.rewindFailed'))
    }
  }, [chatID, isSubAgent, messageChannel, chat, t, sendMessage])

  const rewindLatest = useCallback(() => {
    if (busy) return
    const candidates = rewindCandidates(chat.messages)
    if (candidates.length === 0) {
      toast.error(t('agent.noUserMessageToRewind'))
      return
    }
    // Enter edit mode for the latest rewindable user message
    const latest = candidates[candidates.length - 1]
    setEditingMessageId(latest.id)
  }, [busy, chat.messages, t])

  const handleStartEdit = useCallback((messageId: string) => {
    setEditingMessageId(messageId)
  }, [])

  const handleEndEdit = useCallback(() => {
    setEditingMessageId(null)
  }, [])

  return (
    <div className="flex h-full min-h-0 flex-col">
      <MessageList
        chatKey={`${messageChannel}:${chatID ?? ''}:${params.agentChatID ?? ''}:${params.subAgentRole ?? ''}:${params.subAgentInstance ?? ''}`}
        followResetToken={followResetToken}
        messages={chat.messages}
        liveMessage={liveMessage}
        liveProgress={liveMessage ? progressSnapshot : null}
        collapseLevel={level}
        mergeTools={mergeTools}
        loading={chat.loading}
        error={chat.error}
        onRewind={isSubAgent || busy ? undefined : rewindTo}
        editingMessageId={editingMessageId}
        onStartEdit={handleStartEdit}
        onEndEdit={handleEndEdit}
        footer={
          askUser.prompt && !isSubAgent ? (
            <AskUserPanel
              prompt={askUser.prompt}
              onRespond={askUser.respond}
              onCancel={askUser.cancel}
            />
          ) : null
        }
      />
      {!isSubAgent && (
        <MessageInput
          key={`${messageChannel}:${chatID ?? ''}`}
          busy={busy}
          onSend={sendMessage}
          onCancel={chat.cancel}
          onRewindLatest={rewindLatest}
          onOpenTasks={() => rightSidebar.openPanel('tasks')}
          onUpload={chat.upload}
          todoState={todoState.total > 0 ? todoState : null}
          model={sessionContext.model}
          maxContext={sessionContext.maxContext}
          promptTokens={promptTokens}
          draft={draft}
          onDraftConsumed={() => setDraft(undefined)}
          sessionKey={`${messageChannel}:${chatID ?? ''}`}
        />
      )}
      {!isSubAgent && chatID && (
        <ModelStatusBar
          channel={messageChannel}
          chatID={chatID}
          tokenUsage={progressSnapshot.tokenUsage ? {
            prompt: progressSnapshot.tokenUsage.promptTokens,
            completion: progressSnapshot.tokenUsage.completionTokens,
          } : null}
          thinkingMode={llmSettings.data.thinkingMode}
          preloadedSubID={sessionContext.subscriptionID || undefined}
          preloadedModel={sessionContext.model || undefined}
          preloadedSubs={llmSettings.data.subscriptions.length > 0 ? llmSettings.data.subscriptions : undefined}
        />
      )}
    </div>
  )
}

function rewindCandidates(messages: ChatMessage[]): ChatMessage[] {
  const boundary = latestCompactBoundaryIndex(messages)
  return messages.filter((m, i) => i > boundary && m.role === 'user' && !!m.timestamp && m.persisted === true)
}
