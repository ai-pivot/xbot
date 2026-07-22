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
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
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
import { ModelSelector } from '@/components/agent/ModelSelector'
import { useDockviewContext } from '@/workspace/types'
import type { PanelProps } from '@/workspace/panels/types'
import type { ChatMessage } from '@/types/shared'

interface RewindHistoryResponse {
  history_rewound?: boolean
  files_rewound?: boolean
  checkpoint_error?: string
  rewind_result?: {
    restored?: string[]
    created_del?: string[]
    skipped?: string[]
    errors?: string[]
  }
}

interface HistoryReloadBarrier {
  key: string
  sessionKey: string
  generation: number
  promise: Promise<boolean>
}

interface SessionGeneration {
  key: string
  generation: number
}

interface LocalRewindOperation {
  operationID: number
  historyID: number
  eventSeen: boolean
  eventReloadPromise: Promise<boolean> | null
  completed: boolean
}

function historyReloadBarrierKey(channel: string, chatID: string | null, historyID?: number): string {
  return JSON.stringify([channel, chatID, historyID ?? null])
}

export function AgentPanel({ params }: PanelProps) {
  const ctx = useDockviewContext()
  const ws = ctx.ws
  const store = ctx.sessionStore
  const clearAskUserPrompt = store.clearAskUserPrompt
  const rightSidebar = ctx.rightSidebar
  const { level } = useCollapseLevel()
  const { mergeTools } = useMergeTools()
  const [drafts, setDrafts] = useState<Map<string, string>>(() => new Map())
  const [followResetToken, setFollowResetToken] = useState(0)
  const [editingMessages, setEditingMessages] = useState<Map<string, string>>(() => new Map())
  const wasSubscribedRef = useRef<boolean | null>(null)
  const mountedRef = useRef(true)
  const rewindInFlightRef = useRef(new Map<string, number>())
  const localRewindOperationRef = useRef(new Map<string, LocalRewindOperation>())
  const [rewindPendingKeys, setRewindPendingKeys] = useState<Set<string>>(() => new Set())
  const rewindOperationSeqRef = useRef(0)
  const historyReloadBarrierRef = useRef<HistoryReloadBarrier | null>(null)

  // Agent children use their canonical (channel="agent", full interactive key)
  // session identity for history, progress, and rewind.
  const isSubAgent = !!((params.subAgentRole && params.parentChatID) || params.agentChatID)

  const activeSession = store.activeSession
  const chatID = params.agentChatID ? (params.agentChatID ?? null) : isSubAgent ? (params.parentChatID ?? null) : (activeSession?.chatID ?? null)
  const liveSubAgentChatID =
    !params.agentChatID && isSubAgent && params.subAgentRole && params.parentChatID
      ? `${params.parentChannel ?? 'web'}:${params.parentChatID}/${params.subAgentRole}${params.subAgentInstance ? `:${params.subAgentInstance}` : ''}`
      : null
  const progressChatID = params.agentChatID ?? liveSubAgentChatID ?? chatID
  const subscribeChatID = params.agentChatID ?? liveSubAgentChatID ?? chatID
  const messageChannel = params.agentChatID ? 'agent' : isSubAgent ? (params.parentChannel ?? 'web') : (activeSession?.channel ?? 'web')
  const progressChannel = params.agentChatID || liveSubAgentChatID ? 'agent' : messageChannel
  const historyChatID = isSubAgent ? progressChatID : chatID
  const historyChannel = isSubAgent ? progressChannel : messageChannel
  const shouldSubscribe = true // Panels always subscribe — SSE stays alive until panel closes
  const historyEnabled = !!historyChatID
  const historySessionKey = historyReloadBarrierKey(historyChannel, historyChatID)
  const sessionGenerationRef = useRef<SessionGeneration>({ key: historySessionKey, generation: 0 })

  useLayoutEffect(() => {
    const current = sessionGenerationRef.current
    if (current.key === historySessionKey) return
    sessionGenerationRef.current = {
      key: historySessionKey,
      generation: current.generation + 1,
    }
    historyReloadBarrierRef.current = null
  }, [historySessionKey])

  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
      historyReloadBarrierRef.current = null
    }
  }, [])

  const setSessionDraft = useCallback((sessionKey: string, content?: string) => {
    if (!mountedRef.current) return
    setDrafts((current) => {
      const existing = current.get(sessionKey)
      if (content === undefined ? existing === undefined : existing === content) return current
      const next = new Map(current)
      if (content === undefined) next.delete(sessionKey)
      else next.set(sessionKey, content)
      return next
    })
  }, [])

  const setSessionEditing = useCallback((sessionKey: string, messageID?: string) => {
    if (!mountedRef.current) return
    setEditingMessages((current) => {
      const existing = current.get(sessionKey)
      if (messageID === undefined ? existing === undefined : existing === messageID) return current
      const next = new Map(current)
      if (messageID === undefined) next.delete(sessionKey)
      else next.set(sessionKey, messageID)
      return next
    })
  }, [])

  const isCurrentGeneration = useCallback((target: SessionGeneration) => {
    const current = sessionGenerationRef.current
    return mountedRef.current && current.key === target.key && current.generation === target.generation
  }, [])

  const handleSendError = useCallback(
    (content: string) => {
      setSessionDraft(historySessionKey, content)
    },
    [historySessionKey, setSessionDraft],
  )

  useActiveSSESubscription({
    ws,
    chatID: subscribeChatID,
    channel: progressChannel,
    active: shouldSubscribe,
  })

  const chat = useChatMessages({
    chatID: historyChatID,
    channel: historyChannel,
    enabled: historyEnabled,
    ws,
    liveEventsEnabled: shouldSubscribe,
    onSendError: handleSendError,
  })
  const reloadChat = chat.reload
  const sessionContext = useSessionContext(messageChannel, isSubAgent ? null : chatID)
  // Avoid a duplicate SubAgent lifecycle reload while the final-message reload
  // for the same turn is already in flight.
  const reloadForTurnRef = useRef(false)
  // Ref to hold resetProgress so the onSession effect can call it without
  // depending on the progress object (which is defined later).
  const resetProgressRef = useRef<() => void>(() => {})

  const reloadAfterHistoryRewind = useCallback(
    (historyID?: number, target = sessionGenerationRef.current) => {
      if (!historyChatID || target.key !== historySessionKey || !isCurrentGeneration(target)) {
        return Promise.resolve(false)
      }
      const key = historyReloadBarrierKey(historyChannel, historyChatID, historyID)
      const current = historyReloadBarrierRef.current
      if (current?.key === key && current.sessionKey === target.key && current.generation === target.generation) {
        return current.promise
      }

      chat.clearMessages()
      const barrier: HistoryReloadBarrier = {
        key,
        sessionKey: target.key,
        generation: target.generation,
        promise: Promise.resolve(false),
      }
      historyReloadBarrierRef.current = barrier
      barrier.promise = chat.reload()
        .then(
          (loaded) => loaded && isCurrentGeneration(target) && historyReloadBarrierRef.current === barrier,
          () => false,
        )
        .finally(() => {
          if (historyReloadBarrierRef.current === barrier) historyReloadBarrierRef.current = null
        })
      return barrier.promise
    },
    [chat.clearMessages, chat.reload, historyChannel, historyChatID, historySessionKey, isCurrentGeneration],
  )

  const handleHistoryRewound = useCallback((historyID?: number) => {
    const target = sessionGenerationRef.current
    const localOperation = localRewindOperationRef.current.get(target.key)
    if (localOperation && localOperation.historyID === historyID) {
      localOperation.eventSeen = true
      if (localOperation.completed) {
        localRewindOperationRef.current.delete(target.key)
        void sessionContext.refresh()
        return
      }
      setSessionEditing(target.key)
      clearAskUserPrompt(historyChannel, historyChatID ?? '')
      localOperation.eventReloadPromise = reloadAfterHistoryRewind(historyID, target)
      void localOperation.eventReloadPromise
      void sessionContext.refresh()
      return
    }

    const operationID = ++rewindOperationSeqRef.current
    rewindInFlightRef.current.set(target.key, operationID)
    setRewindPendingKeys((prev) => {
      const next = new Set(prev)
      next.add(target.key)
      return next
    })
    setSessionEditing(target.key)
    clearAskUserPrompt(historyChannel, historyChatID ?? '')
    void reloadAfterHistoryRewind(historyID, target).finally(() => {
      if (operationID === undefined || rewindInFlightRef.current.get(target.key) !== operationID) return
      rewindInFlightRef.current.delete(target.key)
      setRewindPendingKeys((prev) => {
        if (!prev.has(target.key)) return prev
        const next = new Set(prev)
        next.delete(target.key)
        return next
      })
    })
    void sessionContext.refresh()
  }, [clearAskUserPrompt, historyChannel, historyChatID, reloadAfterHistoryRewind, sessionContext.refresh, setSessionEditing])

  useEffect(() => {
    const wasSubscribed = wasSubscribedRef.current
    wasSubscribedRef.current = shouldSubscribe
    if (wasSubscribed === false && shouldSubscribe) void reloadChat()
  }, [reloadChat, shouldSubscribe])

  useEffect(() => {
    if (!isSubAgent || !historyChatID) return
    return ws.onSession((ev) => {
      if (ev.action !== 'subagent_started' && ev.action !== 'subagent_stopped') return
      if (ev.session_key !== historyChatID) return
      if (ev.action === 'subagent_stopped') resetProgressRef.current()
      if (reloadForTurnRef.current) return
      void reloadChat()
    })
  }, [historyChatID, isSubAgent, reloadChat, ws])

  const progress = useProgressStream({
    chatID: progressChatID,
    channel: progressChannel,
    initialProgress: chat.resolvedChatID === historyChatID
      ? (chat.initialProgress ?? (chat.processing ? { phase: 'processing' } : null))
      : null,
    onAssistantComplete: (finalText, iterations, eventSeq) => {
      // Both main Agent and SubAgent panels append the final reply to the
      // message list. SubAgent panels previously had onAssistantComplete=undefined,
      // causing the final reply to never appear in the message list.
      // flushSync ensures setMessages flushes synchronously BEFORE store.reset()
      // clears the live streaming message. Without this, there's a frame where
      // liveMessage is null but the appended message hasn't rendered → flicker.
      flushSync(() => {
        chat.appendAssistant(finalText, iterations, eventSeq)
      })
      reloadForTurnRef.current = true
      void chat.reload().finally(() => {
        reloadForTurnRef.current = false
      })
      void sessionContext.refresh()
    },
    ws,
    onHistoryCompacted: () => {
      void chat.reload()
      void sessionContext.refresh()
    },
    onSessionReset: () => {
      chat.clearMessages()
      void chat.reload()
      void sessionContext.refresh()
    },
    onHistoryRewound: handleHistoryRewound,
    disabled: false, // Always enabled — SSE subscription managed by useActiveSSESubscription
  })
  // Wire resetProgress to the ref so the onSession effect can call it.
  resetProgressRef.current = progress.resetProgress
  const progressSnapshot = progress.progressSnapshot
  const liveMessage = progress.liveMessage
  const isStreaming = progress.isStreaming
  const askUser = useAskUser({ chatID, channel: messageChannel })

  const todoState = useTodos(progressSnapshot.todos)
  // Busy while streaming (live or hydrated from a resumed session).
  const agentBusy = (chat.processing || isStreaming) && (isSubAgent || !askUser.prompt)
  const rewindPending = rewindPendingKeys.has(historySessionKey)
  const busy = agentBusy || rewindPending

  const llmSettings = useLLMSettings()
  const progressPromptTokens = progressSnapshot.tokenUsage?.promptTokens
  const progressTokenRef = useRef<{ key: string; promptTokens: number | null }>({
    key: '',
    promptTokens: null,
  })

  useEffect(() => {
    if (isSubAgent) return
    const key = chatID ? `${messageChannel}:${chatID}` : ''
    const exactPromptTokens = typeof progressPromptTokens === 'number' && progressPromptTokens > 0 ? progressPromptTokens : null
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
    if (rewindInFlightRef.current.has(historySessionKey)) return
    setFollowResetToken((v) => v + 1)
    sendMessageRef.current(content, attachments)
  }, [historySessionKey])

  const respondToAskUser = useCallback((answers: Record<string, string>) => {
    if (rewindInFlightRef.current.has(historySessionKey)) return
    askUser.respond(answers)
  }, [askUser.respond, historySessionKey])

  const cancelAskUser = useCallback(() => {
    if (rewindInFlightRef.current.has(historySessionKey)) return
    askUser.cancel()
  }, [askUser.cancel, historySessionKey])

  const rewindTo = useCallback(
    async (editedContent: string, message: ChatMessage) => {
      const historyID = message.historyID
      const target = sessionGenerationRef.current
      if (
        !historyChatID ||
        target.key !== historySessionKey ||
        rewindInFlightRef.current.has(target.key) ||
        typeof historyID !== 'number' ||
        historyID <= 0
      ) return
      const operationID = ++rewindOperationSeqRef.current
      rewindInFlightRef.current.set(target.key, operationID)
      const localOperation: LocalRewindOperation = {
        operationID,
        historyID,
        eventSeen: false,
        eventReloadPromise: null,
        completed: false,
      }
      localRewindOperationRef.current.set(target.key, localOperation)
      setRewindPendingKeys((prev) => {
        const next = new Set(prev)
        next.add(target.key)
        return next
      })

      const restoreDraft = () => {
        setSessionEditing(target.key)
        setSessionDraft(target.key, editedContent)
      }

      let replacementSent = false
      try {
        const result = await rewindHistory<RewindHistoryResponse>({ channel: historyChannel, chatID: historyChatID }, historyID)
        if (result?.history_rewound !== true) {
          restoreDraft()
          if (isCurrentGeneration(target)) toast.error('Rewind did not change history')
          return
        }
        clearAskUserPrompt(historyChannel, historyChatID)
        if (!isCurrentGeneration(target) || rewindInFlightRef.current.get(target.key) !== operationID) {
          restoreDraft()
          return
        }
        const reloadPromise = localOperation.eventReloadPromise ?? reloadAfterHistoryRewind(historyID, target)
        if (!(await reloadPromise)) {
          restoreDraft()
          if (isCurrentGeneration(target)) toast.error('History rewound, but history reload failed')
          return
        }
        if (!isCurrentGeneration(target) || rewindInFlightRef.current.get(target.key) !== operationID) {
          restoreDraft()
          return
        }
        const rw = result?.rewind_result
        const checkpointFailed = result?.files_rewound === false || !!result?.checkpoint_error
        if (checkpointFailed) {
          toast.warning('History rewound; files were not fully restored', {
            description: result?.checkpoint_error ?? 'File checkpoint restore reported errors',
          })
        } else if (rw) {
          const restored = rw.restored?.length ?? 0
          const deleted = rw.created_del?.length ?? 0
          const skipped = rw.skipped?.length ?? 0
          const errors = rw.errors?.length ?? 0
          const details = [`restored ${restored}`, `deleted ${deleted}`, `skipped ${skipped}`]
          if (errors > 0) details.push(`errors ${errors}`)
          toast(errors > 0 ? 'Rewind completed with errors' : 'Rewind complete', {
            description: details.join(' · '),
          })
        } else {
          toast.success('Rewind complete')
        }
        setSessionEditing(target.key)
        setSessionDraft(target.key)
        setFollowResetToken((v) => v + 1)
        sendMessageRef.current(editedContent, undefined)
        replacementSent = true
      } catch (e) {
        restoreDraft()
        if (isCurrentGeneration(target)) toast.error(e instanceof Error ? e.message : 'Rewind failed')
      } finally {
        if (localOperation.eventReloadPromise) {
          await localOperation.eventReloadPromise
        }
        if (localRewindOperationRef.current.get(target.key) === localOperation) {
          if (replacementSent && !localOperation.eventSeen) {
            localOperation.completed = true
          } else {
            localRewindOperationRef.current.delete(target.key)
          }
        }
        if (rewindInFlightRef.current.get(target.key) === operationID) {
          rewindInFlightRef.current.delete(target.key)
          setRewindPendingKeys((prev) => {
            if (!prev.has(target.key)) return prev
            const next = new Set(prev)
            next.delete(target.key)
            return next
          })
        }
      }
    },
    [clearAskUserPrompt, historyChannel, historyChatID, historySessionKey, isCurrentGeneration, reloadAfterHistoryRewind, setSessionDraft, setSessionEditing],
  )

  const rewindLatest = useCallback(() => {
    if (busy) return
    const candidates = rewindCandidates(chat.messages)
    if (candidates.length === 0) {
      toast.error('No user message to rewind')
      return
    }
    // Enter edit mode for the latest rewindable user message
    const latest = candidates[candidates.length - 1]
    setSessionEditing(historySessionKey, latest.id)
  }, [busy, chat.messages, historySessionKey, setSessionEditing])

  const handleStartEdit = useCallback((messageId: string) => {
    setSessionEditing(historySessionKey, messageId)
  }, [historySessionKey, setSessionEditing])

  const handleEndEdit = useCallback(() => {
    setSessionEditing(historySessionKey)
  }, [historySessionKey, setSessionEditing])

  const draft = drafts.get(historySessionKey)
  const editingMessageId = editingMessages.get(historySessionKey) ?? null

  return (
    <div className="flex h-full min-h-0 flex-col">
      <MessageList
        chatKey={`${historyChannel}:${historyChatID ?? ''}`}
        followResetToken={followResetToken}
        messages={chat.messages}
        liveMessage={liveMessage}
        liveProgress={liveMessage ? progressSnapshot : null}
        collapseLevel={level}
        mergeTools={mergeTools}
        loading={chat.loading}
        error={chat.error}
        onRewind={busy ? undefined : rewindTo}
        editingMessageId={editingMessageId}
        onStartEdit={handleStartEdit}
        onEndEdit={handleEndEdit}
        footer={askUser.prompt && !isSubAgent ? (
          <AskUserPanel
            prompt={askUser.prompt}
            onRespond={respondToAskUser}
            onCancel={cancelAskUser}
            disabled={rewindPending}
          />
        ) : null}
      />
      <MessageInput
        key={`${historyChannel}:${historyChatID ?? ''}`}
        busy={agentBusy}
        disabled={rewindPending}
        onSend={sendMessage}
        onCancel={chat.cancel}
        onRewindLatest={rewindLatest}
        onOpenTasks={() => rightSidebar.openPanel('tasks')}
        onUpload={isSubAgent ? undefined : chat.upload}
        todoState={todoState.total > 0 ? todoState : null}
        trailingControls={
          !isSubAgent && chatID ? (
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
        onDraftConsumed={() => setSessionDraft(historySessionKey)}
        sessionKey={`${historyChannel}:${historyChatID ?? ''}`}
      />
    </div>
  )
}

function rewindCandidates(messages: ChatMessage[]): ChatMessage[] {
  return messages.filter(
    (m) => m.role === 'user' && !m.displayOnly && m.recordType === 'message' && typeof m.historyID === 'number' && m.historyID > 0 && m.persisted === true,
  )
}
