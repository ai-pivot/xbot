/**
 * AgentPanel — the Agent workspace panel.
 *
 * Wires the message + progress + ask-user hooks for one chat and composes the
 * message list, input, and ask-user surface.
 *
 * Chat identity:
 *   - The main Agent tab follows SessionStore.activeSession directly.
 *   - SubAgent tabs are fixed to their parent chat + role/instance params.
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import { toast } from 'sonner'
import { RotateCcw } from 'lucide-react'

import { useAskUser } from '@/hooks/useAskUser'
import { useChatMessages } from '@/hooks/useChatMessages'
import { useCollapseLevel } from '@/hooks/useCollapseLevel'
import { useProgressStream } from '@/hooks/useProgressStream'
import { useTodos } from '@/hooks/useTodos'
import { rewindHistory } from '@/components/agent/api'

import { AskUserPanel } from '@/components/agent/AskUserPanel'
import { MessageInput } from '@/components/agent/MessageInput'
import { MessageList } from '@/components/agent/MessageList'
import { latestCompactBoundaryIndex } from '@/components/agent/MessageList'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { useDockviewContext } from '@/workspace/types'
import type { PanelProps } from '@/workspace/panels/types'
import type { ChatMessage } from '@/types/shared'

interface RewindHistoryResponse {
  draft?: string
  rewind_result?: {
    restored?: string[]
    created_del?: string[]
    skipped?: string[]
    errors?: string[]
  }
}

type RewindResult = NonNullable<RewindHistoryResponse['rewind_result']>

export function AgentPanel({ params }: PanelProps) {
  const ctx = useDockviewContext()
  const ws = ctx.ws
  const store = ctx.sessionStore
  const rightSidebar = ctx.rightSidebar
  const { level } = useCollapseLevel()
  const [draft, setDraft] = useState<string | undefined>(undefined)
  const [rewindResult, setRewindResult] = useState<RewindResult | null>(null)
  const [rewindOpen, setRewindOpen] = useState(false)
  const [followResetToken, setFollowResetToken] = useState(0)
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
  const shouldSubscribe = params.active !== false
  const historyEnabled = params.agentChatID
    ? !!params.agentChatID
    : isSubAgent
      ? !!chatID
      : !!activeSession?.chatID

  useEffect(() => {
    if (!shouldSubscribe) return
    if (!subscribeChatID) return
    ws.subscribe(subscribeChatID)
  }, [ws, subscribeChatID, shouldSubscribe])

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
    initialProgress: chat.initialProgress,
    onAssistantComplete: isSubAgent ? undefined : (finalText, iterations) => {
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
    disabled: !shouldSubscribe,
  })
  const progressSnapshot = progress.progressSnapshot
  const liveMessage = progress.liveMessage
  const isStreaming = progress.isStreaming

  const todoState = useTodos(progressSnapshot.todos)
  // Busy while streaming (live or hydrated from a resumed session).
  const busy = isStreaming

  const askUser = useAskUser({ chatID, channel: messageChannel, ws })
  const rewindTo = useCallback(async (message: ChatMessage) => {
    if (!chatID || isSubAgent || !message.timestamp) return
    const cutoff = Date.parse(message.timestamp)
    if (!Number.isFinite(cutoff) || cutoff <= 0) return
    try {
      const result = await rewindHistory<RewindHistoryResponse>({ channel: messageChannel, chatID }, cutoff)
      await chat.reload()
      setDraft(result?.draft ?? message.content)
      const rw = result?.rewind_result
      setRewindResult(rw ?? null)
      if (rw) {
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
    } catch (e) {
      toast.error(e instanceof Error ? e.message : 'Rewind failed')
    }
  }, [chat, messageChannel, chatID, isSubAgent, ws])
  const rewindLatest = useCallback(() => {
    if (busy) return
    const candidates = rewindCandidates(chat.messages)
    if (candidates.length === 0) {
      toast.error('No user message to rewind')
      return
    }
    setRewindOpen(true)
  }, [busy, chat.messages])

  const sendMessage = useCallback((content: string, attachments?: Parameters<typeof chat.sendMessage>[1]) => {
    setRewindResult(null)
    setFollowResetToken((v) => v + 1)
    chat.sendMessage(content, attachments)
  }, [chat])

  return (
    <div className="flex h-full min-h-0 flex-col">
      <MessageList
        chatKey={`${messageChannel}:${chatID ?? ''}:${params.agentChatID ?? ''}:${params.subAgentRole ?? ''}:${params.subAgentInstance ?? ''}`}
        followResetToken={followResetToken}
        messages={chat.messages}
        liveMessage={liveMessage}
        liveProgress={liveMessage ? progressSnapshot : null}
        collapseLevel={level}
        loading={chat.loading}
        error={chat.error}
        onRewind={isSubAgent || busy ? undefined : rewindTo}
      />
      {askUser.prompt && !isSubAgent && (
        <AskUserPanel
          prompt={askUser.prompt}
          onRespond={askUser.respond}
          onCancel={askUser.cancel}
        />
      )}
      {rewindResult && !isSubAgent && (
        <RewindResultBlock result={rewindResult} onDismiss={() => setRewindResult(null)} />
      )}
      {!isSubAgent && (
        <RewindDialog
          open={rewindOpen}
          messages={chat.messages}
          onOpenChange={setRewindOpen}
          onSelect={(message) => {
            setRewindOpen(false)
            void rewindTo(message)
          }}
        />
      )}
      {!isSubAgent && (
        <MessageInput
          busy={busy}
          onSend={sendMessage}
          onCancel={chat.cancel}
          onRewindLatest={rewindLatest}
          onOpenTasks={() => rightSidebar.openPanel('tasks')}
          onUpload={chat.upload}
          todoState={todoState.total > 0 ? todoState : null}
          draft={draft}
          onDraftConsumed={() => setDraft(undefined)}
        />
      )}
    </div>
  )
}

function rewindCandidates(messages: ChatMessage[]): ChatMessage[] {
  const boundary = latestCompactBoundaryIndex(messages)
  return messages.filter((m, i) => i > boundary && m.role === 'user' && !!m.timestamp && m.persisted === true)
}

function RewindDialog({
  open,
  messages,
  onOpenChange,
  onSelect,
}: {
  open: boolean
  messages: ChatMessage[]
  onOpenChange: (open: boolean) => void
  onSelect: (message: ChatMessage) => void
}) {
  const candidates = rewindCandidates(messages)
  const newestFirst = [...candidates].reverse()
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-xl">
        <DialogHeader>
          <DialogTitle>Rewind</DialogTitle>
        </DialogHeader>
        <div className="max-h-96 overflow-auto">
          {newestFirst.map((message, index) => (
            <button
              key={message.id}
              type="button"
              className="flex w-full items-start gap-2 rounded-md px-2 py-2 text-left hover:bg-bg-tertiary"
              onClick={() => onSelect(message)}
            >
              <RotateCcw className="mt-0.5 size-4 shrink-0 text-text-secondary" />
              <div className="min-w-0 flex-1">
                <div className="text-xs font-medium text-text-secondary">#{index + 1}</div>
                <div className="mt-0.5 line-clamp-2 text-sm text-text-primary">{previewUserMessage(message.content)}</div>
              </div>
            </button>
          ))}
          {newestFirst.length === 0 && (
            <div className="px-2 py-6 text-center text-sm text-text-muted">No user message to rewind</div>
          )}
        </div>
      </DialogContent>
    </Dialog>
  )
}

function previewUserMessage(content: string): string {
  const first = content.split('\n')[0]?.trim() || content.trim()
  return first.length > 80 ? `${first.slice(0, 77)}...` : first
}

function RewindResultBlock({ result, onDismiss }: { result: RewindResult; onDismiss: () => void }) {
  const restored = result.restored ?? []
  const deleted = result.created_del ?? []
  const skipped = result.skipped ?? []
  const errors = result.errors ?? []
  const rows = [
    ['restored', restored],
    ['deleted', deleted],
    ['skipped', skipped],
    ['errors', errors],
  ] as const
  return (
    <div className="border-t border-border bg-bg-primary px-3 py-2">
      <div className="rounded-md border border-border bg-bg-secondary px-3 py-2 text-xs text-text-secondary">
        <div className="flex items-center justify-between gap-2">
          <span className="font-medium text-text-primary">Rewind complete</span>
          <button type="button" className="text-text-muted hover:text-text-primary" onClick={onDismiss}>
            Dismiss
          </button>
        </div>
        <div className="mt-1 flex flex-wrap gap-x-3 gap-y-1">
          {rows.map(([label, items]) => (
            <span key={label}>{label}: {items.length}</span>
          ))}
        </div>
        {rows.some(([, items]) => items.length > 0) && (
          <details className="mt-1">
            <summary className="cursor-pointer text-text-muted">Files</summary>
            <div className="mt-1 max-h-24 overflow-auto whitespace-pre-wrap font-mono text-[11px]">
              {rows.flatMap(([label, items]) => items.map((item) => `${label}: ${item}`)).join('\n')}
            </div>
          </details>
        )}
      </div>
    </div>
  )
}
