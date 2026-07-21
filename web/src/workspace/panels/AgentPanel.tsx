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
import { flushSync } from 'react-dom'
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
import { ContextRing } from '@/components/agent/ContextRing'
import { MessageInput } from '@/components/agent/MessageInput'
import { MessageList } from '@/components/agent/MessageList'
import { latestCompactBoundaryIndex } from '@/components/agent/MessageList'
import { ModelSelector } from '@/components/agent/ModelSelector'
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
  const sessionContext = useSessionContext(messageChannel, isSubAgent ? null : chatID)
  // Ref to hold resetProgress so the onSession effect can call it without
  // depending on the progress object (which is defined later).
  const resetProgressRef = useRef<() => void>(() => {})

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
      // When the SubAgent session transitions to idle/done, reset the
      // progress store. SubAgent panels never receive `text` or `session(idle)`
      // events directly (those carry the parent's chatID), so the store
      // would stay in finalizing state forever without this reset.
      if (ev.action !== 'busy') {
        resetProgressRef.current()
        // Reload to fetch the SubAgent's final persisted messages. SubAgent
        // panels may never receive a `text` event (those carry the parent's
        // chatID), so onAssistantComplete won't fire — reload is the only way
        // to surface the final reply.
        void reloadChat()
      }
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
      // flushSync ensures setMessages flushes synchronously BEFORE store.reset()
      // clears the live streaming message. Without this, there's a frame where
      // liveMessage is null but the appended message hasn't rendered → flicker.
      flushSync(() => {
        chat.appendAssistant(finalText, iterations)
      })
      void chat.reload()
      void sessionContext.refresh()
    },
    ws,
    onHistoryCompacted: isSubAgent ? undefined : () => {
      void chat.reload()
      void sessionContext.refresh()
    },
    onSessionReset: isSubAgent ? undefined : () => {
      chat.clearMessages()
      void chat.reload()
      void sessionContext.refresh()
    },
    disabled: false, // Always enabled — SSE subscription managed by useActiveSSESubscription
  })
  // Wire resetProgress to the ref so the onSession effect can call it.
  resetProgressRef.current = progress.resetProgress
  const progressSnapshot = progress.progressSnapshot
  const liveMessage = progress.liveMessage
  const isStreaming = progress.isStreaming
  const askUser = useAskUser({ chatID, channel: messageChannel })

  const todoState = useTodos(progressSnapshot.todos)
  // Busy while streaming (live or hydrated from a resumed session) OR
  // backend reports processing (covers SSE reconnect gap).
  const busy = (isStreaming || chat.processing) && !askUser.prompt

  const llmSettings = useLLMSettings()
  const progressPromptTokens = progressSnapshot.tokenUsage?.promptTokens
  const progressTokenRef = useRef<{ key: string; promptTokens: number | null }>({
    key: '',
    promptTokens: null,
  })

  useEffect(() => {
    if (isSubAgent) return
    const key = chatID ? `${messageChannel}:${chatID}` : ''
    const exactPromptTokens = typeof progressPromptTokens === 'number' && progressPromptTokens > 0
      ? progressPromptTokens
      : null
    if (progressTokenRef.current.key !== key) {
      progressTokenRef.current = { key, promptTokens: null }
      return
    }
    if (exactPromptTokens === null || exactPromptTokens === progressTokenRef.current.promptTokens) return
    progressTokenRef.current.promptTokens = exactPromptTokens
    void sessionContext.refresh()
  }, [chatID, isSubAgent, messageChannel, progressPromptTokens, sessionContext.refresh])

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
      // Rewind is destructive: clear the visible/cache rows before reload so
      // an empty truncated history is not mistaken for a background refresh.
      chat.clearMessages()
      // Reload FIRST to fetch the truncated history from the server.
      // This must happen BEFORE sendMessage — otherwise sendMessage increments
      // messageMutationGenRef, the subsequent reload captures the incremented
      // value, requestHasMessageMutation() returns false, and the optimistic
      // message is silently wiped by the fresh history.
      await chat.reload()
      // Send the edited content as a new message (sendMessage increments
      // followResetToken so the viewport scrolls to bottom for the response)
      sendMessage(editedContent)
      toast.success(t('agent.rewindComplete'))
    } catch (e) {
      // Keep edit mode active when the rewind request fails.
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
          trailingControls={
            chatID ? (
              <>
                <ContextRing
                  available={sessionContext.available}
                  promptTokens={sessionContext.promptTokens}
                  maxContext={sessionContext.maxContext}
                  usagePercent={sessionContext.usagePercent}
                />
                <ModelSelector
                  channel={messageChannel}
                  chatID={chatID}
                  currentSubID={sessionContext.subscriptionID}
                  currentModel={sessionContext.model}
                  subscriptions={llmSettings.data.subscriptions}
                  modelEntries={llmSettings.data.modelEntries}
                  thinkingMode={llmSettings.data.thinkingMode}
                  busy={busy}
                  saving={llmSettings.saving}
                  onModelSelected={sessionContext.refresh}
                  onThinkingModeChange={llmSettings.setThinkingMode}
                />
              </>
            ) : null
          }
          draft={draft}
          onDraftConsumed={() => setDraft(undefined)}
          sessionKey={`${messageChannel}:${chatID ?? ''}`}
        />
      )}
    </div>
  )
}

function rewindCandidates(messages: ChatMessage[]): ChatMessage[] {
  const boundary = latestCompactBoundaryIndex(messages)
  return messages.filter((m, i) => i > boundary && m.role === 'user' && !!m.timestamp && m.persisted === true)
}
