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
import { Loader2 } from 'lucide-react'
import { toast } from 'sonner'

import { useAskUser } from '@/hooks/useAskUser'
import { useChatMessages, type Attachments } from '@/hooks/useChatMessages'
import { sameSession } from "@/lib/session-grouping"
import { useCollapseLevel, useMergeTools } from '@/hooks/useCollapseLevel'
import { useAgentChatState } from '@/chat/useAgentChatState'
import { useTodos } from '@/hooks/useTodos'
import { useActiveSSESubscription } from '@/hooks/useActiveSSESubscription'
import { useSessionContext } from '@/hooks/useSessionContext'
import { useLLMSettings } from '@/hooks/useLLMSettings'
import { rewindHistory, fetchHistory } from '@/components/agent/api'
import { resolveUserMessageDBIDFromHistMsgs } from '@/components/agent/rewind'

import { AskUserPanel } from '@/components/agent/AskUserPanel'
import { ContextRing } from '@/components/agent/ContextRing'
import { MessageInput } from '@/components/agent/MessageInput'
import { MessageList } from '@/components/agent/MessageList'
import { latestCompactBoundaryIndex } from '@/components/agent/MessageList'
import { ModelSelector } from '@/components/agent/ModelSelector'
import { useDockviewContext } from '@/workspace/types'
import { DebugToolbar } from '@/workspace/panels/DebugToolbar'
import { useDeveloperMode } from '@/hooks/useDeveloperMode'
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
  const { enabled: devMode } = useDeveloperMode()
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
    onSendSuccess: () => {
      // Optimistically mark the session as running so the UI enters busy
      // immediately — don't wait for the SSE session(busy) event which may
      // arrive late or get lost.
      if (chatID) {
        const selector = { channel: messageChannel, chatID }
        store.setStatus(selector, 'running')
      }
    },
    onCancelSuccess: () => {
      // Optimistically mark the session as idle so the UI exits busy
      // immediately — don't wait for the SSE session(idle) event.
      if (chatID) {
        const selector = { channel: messageChannel, chatID }
        store.setStatus(selector, 'idle')
      }
    },
  })
  const reloadChat = chat.reload
  const sessionContext = useSessionContext(messageChannel, isSubAgent ? null : chatID)

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
        resetAgentChatRef.current()
        // Reload to fetch the SubAgent's final persisted messages. SubAgent
        // panels may never receive a `text` event (those carry the parent's
        // chatID), so onAssistantComplete won't fire — reload is the only way
        // to surface the final reply.
        void reloadChat()
      }
    })
  }, [isSubAgent, params.agentChatID, params.parentChatID, params.subAgentInstance, params.subAgentRole, reloadChat, ws])

  // ── M4：新状态机（web/src/chat/）作为唯一渲染数据源 ──
  // 全部 SSE 事件 → normalizeEvent → reduce；DB 历史 → history_replaced。
  // 旧 useProgressStream（1742 行）+ MessageStore（622 行）双轨协调已移除。
  const agentChat = useAgentChatState({
    progressChatID,
    ws,
    historyMessages: chat.messages,
    historyReady: chat.historyReady,
    // 属主门控：resolvedChatID（fetch 成功才更新）≠ 当前 chatID 时跳过
    // history dispatch —— 切会话窗口期旧会话 messages 不得灌入新 store。
    historyOwner: chat.resolvedChatID,
    historyChatID: chatID,
    initialProgress: chat.resolvedChatID === chatID ? chat.initialProgress : null,
    resetKey: `${messageChannel}:${chatID ?? ''}:${params.agentChatID ?? ''}:${params.subAgentRole ?? ''}:${params.subAgentInstance ?? ''}`,
  })
  // SubAgent idle/done 时重置（SubAgent 面板收不到 text/session(idle)）。
  const resetAgentChatRef = useRef(agentChat.reset)
  resetAgentChatRef.current = agentChat.reset
  const progressSnapshot = agentChat.liveProgress
  // liveMessage comes from useProgressStream's live store — its visibility is
  // governed by the store's own hydration/reset lifecycle (initialProgress →
  // historyProgressToLive → store.replace, SSE-driven updates, reset on
  // turn_end/session-idle). It must NOT be gated on useChatMessages' loading:
  //
  // CRITICAL: a turn mid-stream can trigger resync_required (SSE ring-buffer
  // overflow with high-frequency reasoning events) / replay_gap(force) →
  // useChatMessages setLoading(true) + reload(). The reload BLANKS messages
  // (`setMessages([])` in the no-cache path) and sets loading=true. If we hid
  // liveMessage on `chat.loading` (or even `loading && messages.length === 0`),
  // the ENTIRE live turn (with all its already-rendered iterations) vanished
  // from the DOM for the ~1s reload duration — rows collapsed from 65 to 0
  // (user report + [RENDER_LOSS_ROWS] rowsLen:0). The live store stays
  // authoritative during reload; buildMessageRows merges the live row into the
  // refreshed committed rows when history lands. NEVER gate live on loading.
  // 方案 A：live 行由 store.toRows() 输出（liveMessage=null），渲染永不 gate。
  const askUser = useAskUser({ chatID, channel: messageChannel })

  const todoState = useTodos(progressSnapshot.todos)
  // Busy state: sessionStore.running is the primary source (same source the
  // sidebar uses — SSE session(busy)/session(idle) events). BUT after a page
  // refresh, SSE does NOT replay session(busy) for an in-flight turn, so
  // running stays false even though active_progress says the agent is
  // mid-turn (first-iteration thinking with no content yet = no liveMessage).
  // Fall back to the hydrated progressSnapshot.streaming (set true by
  // historyProgressToLive and by any stream/structured event while phase !=
  // done) so the "思考中…" placeholder still renders on refresh.
  const currentSession = store.sessions.find((s) => sameSession(s, activeSession))
  const busy = ((currentSession?.running ?? false) ||
    (progressSnapshot.streaming && progressSnapshot.phase !== 'done' && progressSnapshot.phase !== 'frozen')) &&
    !askUser.prompt

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

  // Rewind via inline edit: rewind to the message's DB id, then send
  // the edited content as a new message.
  const rewindTo = useCallback(async (editedContent: string, originalMessage: ChatMessage) => {
    if (!chatID || isSubAgent) return
    // User messages rendered from user_echo SSE carry persisted=true but no
    // dbID — the DB id is assigned when the agent loop persists the message,
    // AFTER the echo is sent at queue-admission time. Resolve the id from a
    // direct history API call — bypass chat.reload() which can return null
    // due to requestIsSuperseded() race conditions when SSE events fire
    // during the await.
    try {
      // User messages rendered from user_echo SSE carry persisted=true but no
      // dbID — the DB id is assigned when the agent loop persists the message,
      // AFTER the echo is sent at queue-admission time. Resolve the id from a
      // direct history API call — bypass chat.reload() which can return null
      // due to requestIsSuperseded() race conditions when SSE events fire
      // during the await.
      let dbID = originalMessage.dbID
      if (!dbID) {
        const data = await fetchHistory(ws, { channel: messageChannel, chatID }, { limit: 100 })
        dbID = resolveUserMessageDBIDFromHistMsgs(data.messages ?? [], originalMessage)
      }
      if (!dbID) {
        toast.error(t('agent.rewindUnavailable'))
        return
      }
      await rewindHistory<RewindHistoryResponse>({ channel: messageChannel, chatID }, dbID)
      // Exit edit mode
      setEditingMessageId(null)
      // Rewind is destructive: clear the visible/cache rows before reload so
      // an empty truncated history is not mistaken for a background refresh.
      chat.clearMessages()
      // Rewind MUST also reset the state machine — otherwise the pre-rewind
      // turn's live residue is committed on the next turn_started,
      // re-rendering the rewind-deleted assistant below the new turn.
      resetAgentChatRef.current()
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
  }, [chatID, isSubAgent, messageChannel, chat, ws, t, sendMessage])

  const rewindLatest = useCallback(() => {
    if (busy) return
    const candidates = rewindCandidates(agentChat.messages)
    if (candidates.length === 0) {
      toast.error(t('agent.noUserMessageToRewind'))
      return
    }
    // Enter edit mode for the latest rewindable user message
    const latest = candidates[candidates.length - 1]
    setEditingMessageId(latest.id)
  }, [busy, agentChat.messages, t])

  const handleStartEdit = useCallback((messageId: string) => {
    setEditingMessageId(messageId)
  }, [])

  const handleEndEdit = useCallback(() => {
    setEditingMessageId(null)
  }, [])

  return (
    <div className="flex h-full min-h-0 flex-col">
      {!ws.connected && !isSubAgent && (
        <div className="flex items-center gap-2 border-b border-border/50 bg-amber-500/10 px-3 py-1.5 text-xs text-amber-600 dark:text-amber-400">
          <Loader2 className="size-3 animate-spin" />
          <span>{t('agent.reconnecting') || 'Reconnecting…'}</span>
        </div>
      )}
      {!isSubAgent && devMode && (
        <DebugToolbar
          ws={ws}
          getStateSnapshot={() => ({
            meta: {
              channel: messageChannel,
              chatID: progressChatID ?? null,
              isSubAgent: Boolean(isSubAgent),
              capturedAt: Date.now(),
            },
            // New state-machine snapshot (liveProgress + derived rows).
            chatState: { liveProgress: progressSnapshot, messages: agentChat.messages, busy },
            // Committed message rows as currently rendered — the other half of
            // "100% frontend reconstruction": the turn-vanish symptom is a
            // rendering-state divergence, so the baseline must include the
            // committed list, not just the live store.
            chat: {
              messages: chat.messages,
              resolvedChatID: chat.resolvedChatID,
            },
            session: { busy },
          })}
        />
      )}
      <MessageList
        chatKey={`${messageChannel}:${chatID ?? ''}:${params.agentChatID ?? ''}:${params.subAgentRole ?? ''}:${params.subAgentInstance ?? ''}`}
        followResetToken={followResetToken}
        messages={agentChat.messages}
        liveProgress={progressSnapshot}
        busy={busy}
        collapseLevel={level}
        mergeTools={mergeTools}
        loading={chat.loading}
        loadingMore={chat.loadingMore}
        hasMore={chat.hasMore}
        onLoadMore={chat.loadMore}
        error={chat.error}
        onRewind={isSubAgent || busy ? undefined : rewindTo}
        editingMessageId={editingMessageId}
        onStartEdit={handleStartEdit}
        onEndEdit={handleEndEdit}
        footer={
          askUser.prompt && !isSubAgent ? (
            <AskUserPanel
              prompt={askUser.prompt}
              onRespond={(answers) => {
                askUser.respond(answers)
                // Deterministic: the backend persists the answer as a user
                // message; reload history so it renders with its authoritative
                // turn_id (NO optimistic rendering).
                void chat.reload()
              }}
              onCancel={askUser.cancel}
            />
          ) : null
        }
      />
      {!isSubAgent && (
        <MessageInput
          key={`${messageChannel}:${chatID ?? ''}`}
          busy={busy}
          cancelling={chat.cancelling}
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
