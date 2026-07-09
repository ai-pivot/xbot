/**
 * TasksPanel — right-sidebar panel showing Cron tasks and background Shell tasks.
 *
 * Calls Web task REST APIs (via useTasks hook),
 * refreshing on session switch and every 30 seconds automatically.
 *
 * Icons: ⏰ cron task, ▶ running bg command, ✓ completed, ✗ failed.
 */
import { useEffect, useMemo } from 'react'
import { AlarmClock, Bot, Check, Loader2, Play, Square, X } from 'lucide-react'
import { useI18n } from '@/providers/i18n'
import { useWSConnection } from '@/hooks/useWSConnection'
import { useSessionStore } from '@/hooks/useSessionStore'
import { useTasks } from '@/hooks/useTasks'
import { ScrollArea } from '@/components/ui/scroll-area'
import { flattenSubAgentTree } from '@/components/session/session-tree'
import { parseAgentChatID } from '@/lib/session-grouping'
import type { TabManager } from '@/hooks/useTabManager'
import type { SessionInfo, SessionSelector } from '@/types/shared'
import { sessionForFocusedAgent } from './session-scope'

interface TasksPanelProps {
  tabManager?: TabManager
}

export function TasksPanel({ tabManager }: TasksPanelProps) {
  const { t } = useI18n()
  const ws = useWSConnection()
  const session = useSessionStore()
  const taskSession = useMemo(
    () => sessionForTaskRPC(tabManager, session.activeSession),
    [tabManager?.activeTabId, tabManager?.tabs, session.activeSession],
  )
  const { cronTasks, bgTasks, loading, killBgTask } = useTasks(ws, taskSession)
  const subAgents = useMemo(() => {
    const activeNode = findSessionNode(session.sessions, sessionForFocusedAgent(tabManager, session.activeSession))
    if (activeNode?.children?.length) return flattenSubAgentTree([activeNode]).filter(isActiveSubAgent)
    return []
  }, [session.sessions, tabManager?.activeTabId, tabManager?.tabs, session.activeSession])

  const hasCron = cronTasks.length > 0
  const hasBg = bgTasks.length > 0
  const hasSubAgents = subAgents.length > 0
  const empty = !hasCron && !hasBg && !hasSubAgents && !loading
  const hasRunningSubAgent = subAgents.some((agent) => agent.running)

  useEffect(() => {
    if (!hasRunningSubAgent) return
    const timer = setInterval(() => {
      void session.refresh()
    }, 2_000)
    return () => clearInterval(timer)
  }, [hasRunningSubAgent, session])

  const openSubAgent = (agent: (typeof subAgents)[number]) => {
    tabManager?.openTab({
      type: 'agent',
      title: subAgentTitle(agent),
      icon: 'bot',
      closable: true,
      data: {
        subAgentRole: agent.role,
        subAgentInstance: agent.instance,
        parentChatID: agent.parentChatID,
        parentChannel: agent.parentChannel,
        agentChatID: agent.fullKey || agent.agentChatID,
      },
    })
  }

  const openBgTask = (task: (typeof bgTasks)[number]) => {
    if (!taskSession) return
    tabManager?.openTab({
      type: 'background',
      title: task.command || task.id,
      icon: 'background',
      closable: true,
      data: {
        taskID: task.id,
        command: task.command,
        taskChannel: taskSession.channel,
        taskChatID: taskSession.chatID,
      },
    })
  }

  return (
    <ScrollArea className="h-full">
      <div className="flex flex-col gap-4 px-3 py-3 text-sm">
        {/* Cron tasks */}
        <section className="flex flex-col gap-2">
          <h3 className="text-xs font-semibold uppercase tracking-wide text-text-secondary">
            {t('sidebar.tasksCron')}
          </h3>
          {hasCron ? (
            <div className="flex flex-col gap-1.5">
              {cronTasks.map((task) => (
                <div
                  key={task.id}
                  className="flex items-start gap-2 rounded-md bg-bg-tertiary px-2 py-1.5"
                >
                  <AlarmClock className="mt-0.5 size-3.5 shrink-0 text-text-secondary" />
                  <div className="min-w-0 flex-1">
                    <p className="truncate text-xs text-text-primary">{task.message}</p>
                    <p className="mt-0.5 text-xs text-text-muted">
                      {task.cronExpr
                        ? task.cronExpr
                        : task.everySeconds
                          ? `every ${task.everySeconds}s`
                          : task.at
                            ? task.at
                            : task.delaySeconds
                              ? `delay ${task.delaySeconds}s`
                              : ''}
                    </p>
                  </div>
                  {task.oneShot && (
                    <span className="shrink-0 text-xs text-text-muted">1×</span>
                  )}
                </div>
              ))}
            </div>
          ) : (
            <p className="text-xs text-text-muted">—</p>
          )}
        </section>

        {(hasCron || hasSubAgents) && hasBg && <div className="h-px bg-border" />}

        {/* SubAgents */}
        <section className="flex flex-col gap-2">
          <h3 className="text-xs font-semibold uppercase tracking-wide text-text-secondary">
            SubAgents
          </h3>
          {hasSubAgents ? (
            <div className="flex flex-col gap-1.5">
              {subAgents.map((agent) => (
                <button
                  type="button"
                  key={`${agent.channel}:${agent.chatID}`}
                  className="flex w-full items-start gap-2 rounded-md bg-bg-tertiary px-2 py-1.5 text-left hover:bg-bg-hover"
                  onClick={() => openSubAgent(agent)}
                >
                  <Bot className="mt-0.5 size-3.5 shrink-0 text-text-secondary" />
                  <div className="min-w-0 flex-1">
                    <p className="truncate text-xs text-text-primary">{subAgentTitle(agent)}</p>
                    <p className="mt-0.5 truncate text-xs text-text-muted">
                      {agent.preview || (agent.running ? 'running' : agent.historical ? 'history' : 'idle')}
                    </p>
                  </div>
                </button>
              ))}
            </div>
          ) : (
            <p className="text-xs text-text-muted">—</p>
          )}
        </section>

        {/* Background tasks */}
        <section className="flex flex-col gap-2">
          <h3 className="text-xs font-semibold uppercase tracking-wide text-text-secondary">
            {t('sidebar.tasksBg')}
          </h3>
          {hasBg ? (
            <div className="flex flex-col gap-1.5">
              {bgTasks.map((task) => {
                const running = task.status === 'running' || task.status === 'started'
                return (
                <div
                  key={task.id}
                  className="rounded-md bg-bg-tertiary px-2 py-1.5"
                >
                  <div className="flex items-start gap-2">
                    <BgTaskIcon task={task} />
                    <button
                      type="button"
                      className="min-w-0 flex-1 text-left"
                      onClick={() => openBgTask(task)}
                    >
                      <p className="truncate text-xs text-text-primary">{task.command}</p>
                      {task.error ? (
                        <p className="mt-0.5 truncate text-xs text-status-error">{task.error}</p>
                      ) : (
                        <p className="mt-0.5 text-xs text-text-muted">
                          {task.status === 'done'
                            ? `exit ${task.exitCode}`
                            : task.status}
                        </p>
                      )}
                    </button>
                    {running && (
                      <button
                        type="button"
                        aria-label="kill background task"
                        className="rounded p-0.5 text-text-muted hover:text-status-error"
                        onClick={() => void killBgTask(task.id)}
                      >
                        <Square className="size-3.5" />
                      </button>
                    )}
                  </div>
                </div>
              )})}
            </div>
          ) : (
            <p className="text-xs text-text-muted">—</p>
          )}
        </section>

        {loading && (
          <div className="flex items-center justify-center py-2">
            <Loader2 className="size-4 animate-spin text-text-muted" />
          </div>
        )}

        {empty && (
          <p className="text-xs text-text-muted">{t('sidebar.tasksEmpty')}</p>
        )}

        {!ws.connected && (
          <p className="text-xs text-text-muted">{t('sidebar.disconnectedHint')}</p>
        )}
      </div>
    </ScrollArea>
  )
}

function BgTaskIcon({ task }: { task: { status: string; exitCode: number; error?: string } }) {
  const { status } = task
  if (status === 'running' || status === 'started') {
    return <Play className="mt-0.5 size-3.5 shrink-0 text-status-running" />
  }
  if (task.error || (status === 'done' && task.exitCode !== 0)) {
    return <X className="mt-0.5 size-3.5 shrink-0 text-status-error" />
  }
  if (status === 'done' || status === 'finished') {
    return <Check className="mt-0.5 size-3.5 shrink-0 text-status-done" />
  }
  if (status === 'error' || status === 'failed' || status === 'killed') {
    return <X className="mt-0.5 size-3.5 shrink-0 text-status-error" />
  }
  return <Play className="mt-0.5 size-3.5 shrink-0 text-text-muted" />
}

function isActiveSubAgent(agent: SessionInfo): boolean {
  return agent.running === true || agent.status === 'running' || agent.status === 'waiting_input' || agent.status === 'pending'
}

function sessionForTaskRPC(tabManager: TabManager | undefined, fallback: SessionSelector | null): SessionSelector | null {
  return sessionForFocusedAgent(tabManager, fallback)
}

function subAgentTitle(agent: SessionInfo): string {
  if (agent.role) return agent.instance ? `${agent.role}/${agent.instance}` : agent.role
  const raw = (agent.label || '').trim()
  if (raw && raw !== 'default' && raw !== '默认会话') return agent.label
  const parsed = parseAgentChatID(agent.fullKey || agent.agentChatID || agent.chatID)
  if (parsed?.role) return parsed.instance ? `${parsed.role}/${parsed.instance}` : parsed.role
  return agent.agentChatID || agent.fullKey || agent.chatID || 'SubAgent'
}

function findSessionNode(sessions: SessionInfo[], selector: SessionSelector | null): SessionInfo | null {
  if (!selector) return null
  const visit = (nodes: SessionInfo[]): SessionInfo | null => {
    for (const node of nodes) {
      const nodeAgentID = node.fullKey || node.agentChatID || node.chatID
      const matches =
        (node.channel === selector.channel && node.chatID === selector.chatID) ||
        (selector.channel === 'agent' && nodeAgentID === selector.chatID)
      if (matches) return node
      const found = visit(node.children || [])
      if (found) return found
    }
    return null
  }
  return visit(sessions)
}
