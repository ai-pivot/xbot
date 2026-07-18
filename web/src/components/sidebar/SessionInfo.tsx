/**
 * SessionInfo — session information panel with editable CWD.
 *
 * Displays session metadata (id, channel, work path, last active, message count)
 * and current LLM model. The work path is editable through the Web CWD API and
 * propagated to the CwdProvider so file browser/search follow it.
 */
import { useCallback, useEffect, useState } from 'react'
import { FolderOpen, Loader2 } from 'lucide-react'

import { useI18n } from '@/providers/i18n'
import { useWSConnection } from '@/hooks/useWSConnection'
import { useCwd } from '@/providers/CwdProvider'
import { useSessionStore } from '@/hooks/useSessionStore'
import { fetchCwd, fetchSessionSubscription, setCwd } from '@/components/agent/api'
import { sameSession } from '@/lib/session-grouping'
import type { TabManager } from '@/hooks/useTabManager'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Input } from '@/components/ui/input'
import { toast } from 'sonner'
import { parentSessionForFocusedAgent, sessionForFocusedAgent } from './session-scope'

interface SessionInfoProps {
  tabManager?: TabManager
}

interface AgentSessionDump {
  modelName?: string
  model_name?: string
  subscriptionID?: string
  subscription_id?: string
  maxContextTokens?: number
  max_context_tokens?: number
  promptTokens?: number
  prompt_tokens?: number
  completionTokens?: number
  completion_tokens?: number
}

export function SessionInfo({ tabManager }: SessionInfoProps) {
  const { t } = useI18n()
  const ws = useWSConnection()
  const { cwd } = useCwd()
  const session = useSessionStore()

  const focusedSession = sessionForFocusedAgent(tabManager, session.activeSession)
  const workspaceSession = parentSessionForFocusedAgent(tabManager, session.activeSession)
  const activeId = focusedSession?.chatID ?? null
  const activeChannel = focusedSession?.channel ?? 'web'
  const workspaceId = workspaceSession?.chatID ?? activeId
  const workspaceChannel = workspaceSession?.channel ?? activeChannel
  const current = focusedSession
    ? findSessionNode(session.sessions, focusedSession)
    : undefined

  const [model, setModel] = useState<string>('—')
  const [subscription, setSubscription] = useState<string>('—')
  const [tokenUsage, setTokenUsage] = useState<string>('—')
  const [contextLimit, setContextLimit] = useState<string>('—')
  const [displayCwd, setDisplayCwd] = useState<string | null>(cwd)
  const [editingCwd, setEditingCwd] = useState(false)
  const [cwdInput, setCwdInput] = useState('')
  const [cwdBusy, setCwdBusy] = useState(false)

  const refresh = useCallback(async () => {
    if (!focusedSession || !workspaceSession) return
    try {
      const [cwdResult, sub, settings, dump] = await Promise.all([
        fetchCwd(workspaceSession).catch((): { dir?: string } => ({})),
        fetchSessionSubscription(focusedSession).catch((): Record<string, string> => ({})),
        ws.rpc<Record<string, string>>('get_settings').catch((): Record<string, string> => ({})),
        focusedSession.channel === 'agent'
          ? ws.rpc<AgentSessionDump>('get_agent_session_dump_by_full_key', {
            full_key: focusedSession.chatID,
          }).catch((): AgentSessionDump => ({}))
          : Promise.resolve({} as AgentSessionDump),
      ])
      setDisplayCwd(cwdResult?.dir || null)
      setModel(dump?.modelName || dump?.model_name || sub?.model || settings?.model || '—')
      setSubscription(dump?.subscriptionID || dump?.subscription_id || sub?.subscription_id || sub?.subscription || '—')
      const promptTokens = numberValue(dump?.promptTokens ?? dump?.prompt_tokens)
      const completionTokens = numberValue(dump?.completionTokens ?? dump?.completion_tokens)
      setTokenUsage(promptTokens || completionTokens ? `${promptTokens + completionTokens}` : '—')
      const maxContext = numberValue(dump?.maxContextTokens ?? dump?.max_context_tokens)
      setContextLimit(maxContext ? `${maxContext}` : '—')
    } catch {
      /* ignore */
    }
  }, [focusedSession, workspaceSession, ws])

  useEffect(() => {
    void refresh()
  }, [refresh])

  useEffect(() => {
    if (!focusedSession || !session.activeSession || !sameSession(focusedSession, session.activeSession)) return
    setDisplayCwd(cwd)
  }, [cwd, focusedSession, session.activeSession])

  const applyCwd = useCallback(async () => {
    const path = cwdInput.trim()
    if (!workspaceId || !path || path === displayCwd) {
      setEditingCwd(false)
      return
    }
    setCwdBusy(true)
    try {
      await setCwd({ channel: workspaceChannel, chatID: workspaceId }, path)
      setDisplayCwd(path)
      if (session.activeSession && sameSession(session.activeSession, { channel: workspaceChannel, chatID: workspaceId })) {
        window.dispatchEvent(new CustomEvent('xbot:cwd-changed', { detail: path }))
      }
      toast.success(t('sidebar.cwdUpdated'))
    } catch {
      toast.error(t('sidebar.cwdUpdateFailed'))
    } finally {
      setCwdBusy(false)
      setEditingCwd(false)
    }
  }, [cwdInput, displayCwd, session.activeSession, t, workspaceChannel, workspaceId])

  return (
    <ScrollArea className="h-full">
      <div className="flex flex-col gap-5 px-3 py-3 text-sm">
        {/* Session info */}
        <section className="flex flex-col gap-2">
          <h3 className="text-xs font-semibold uppercase tracking-wide text-text-secondary">
            {t('sidebar.sessionInfo')}
          </h3>
          {activeId ? (
            <dl className="flex flex-col gap-1.5">
              <InfoRow label={t('sidebar.sessionId')} value={activeId} mono />
              <InfoRow
                label={t('sidebar.channel')}
                value={current?.channel ?? activeChannel}
              />
              {/* Editable work path */}
              <div className="flex items-baseline gap-2">
                <dt className="shrink-0 text-xs text-text-secondary">{t('sidebar.workPath')}</dt>
                {editingCwd ? (
                  <div className="flex min-w-0 flex-1 items-center gap-1">
                    <Input
                      value={cwdInput}
                      onChange={(e) => setCwdInput(e.target.value)}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter') void applyCwd()
                        if (e.key === 'Escape') setEditingCwd(false)
                      }}
                      className="h-6 flex-1 font-mono text-xs"
                      autoFocus
                    />
                    <button
                      type="button"
                      onClick={() => void applyCwd()}
                      disabled={cwdBusy}
                      className="flex size-5 shrink-0 items-center justify-center rounded-sm text-text-secondary hover:bg-bg-tertiary"
                    >
                      {cwdBusy ? <Loader2 className="size-3 animate-spin" /> : <FolderOpen className="size-3" />}
                    </button>
                  </div>
                ) : (
                  <dd
                    className="flex min-w-0 flex-1 cursor-pointer items-center gap-1 truncate font-mono text-xs text-text-primary hover:text-accent"
                    title={cwd ?? ''}
                    onClick={() => {
                      setCwdInput(displayCwd ?? '')
                      setEditingCwd(true)
                    }}
                  >
                    <span className="truncate">{displayCwd ?? '—'}</span>
                  </dd>
                )}
              </div>
              {current?.lastActive && (
                <InfoRow label={t('sidebar.lastActive')} value={current.lastActive} />
              )}
            </dl>
          ) : (
            <p className="text-xs text-text-muted">{t('sidebar.noActiveSession')}</p>
          )}
        </section>

        <div className="h-px bg-border" />

        {/* Model info (read-only) */}
        <section className="flex flex-col gap-2">
          <h3 className="text-xs font-semibold uppercase tracking-wide text-text-secondary">
            {t('sidebar.model')}
          </h3>
          <InfoRow label={t('sidebar.model')} value={model} mono />
          {focusedSession?.channel === 'agent' && subscription !== '—' && (
            <InfoRow label="Subscription" value={subscription} mono />
          )}
          {focusedSession?.channel === 'agent' && tokenUsage !== '—' && (
            <InfoRow label="Tokens" value={tokenUsage} mono />
          )}
          {focusedSession?.channel === 'agent' && contextLimit !== '—' && (
            <InfoRow label="Context" value={contextLimit} mono />
          )}
        </section>

        {!ws.connected && (
          <p className="text-xs text-text-muted">{t('sidebar.disconnectedHint')}</p>
        )}
      </div>
    </ScrollArea>
  )
}

function findSessionNode(sessions: ReturnType<typeof useSessionStore>['sessions'], selector: { channel: string; chatID: string }) {
  const visit = (nodes: typeof sessions): (typeof sessions)[number] | undefined => {
    for (const node of nodes) {
      if (sameSession(node, selector) || (selector.channel === 'agent' && (node.fullKey || node.agentChatID) === selector.chatID)) {
        return node
      }
      const found = visit(node.children || [])
      if (found) return found
    }
    return undefined
  }
  return visit(sessions)
}

function numberValue(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : 0
}

function InfoRow({
  label,
  value,
  mono,
}: {
  label: string
  value: string
  mono?: boolean
}) {
  return (
    <div className="flex items-baseline justify-between gap-2">
      <dt className="shrink-0 text-xs text-text-secondary">{label}</dt>
      <dd
        className={`truncate text-xs text-text-primary ${mono ? 'font-mono' : ''}`}
        title={value}
      >
        {value}
      </dd>
    </div>
  )
}
