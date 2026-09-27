/**
 * TasksPanel — right-sidebar panel showing Cron tasks and background Shell tasks.
 *
 * Calls Web task REST APIs (via useTasks hook),
 * refreshing on session switch and every 30 seconds automatically.
 *
 * Icons: ⏰ cron task, ▶ running bg command, ✓ completed, ✗ failed.
 */
import { useEffect, useMemo, useState } from 'react'
import { AlarmClock, Bot, Check, Loader2, Play, Square, Trash2, X } from 'lucide-react'
import { useI18n } from '@/providers/i18n'
import { useWSConnection } from '@/hooks/useWSConnection'
import { useSessionStore } from '@/hooks/useSessionStore'
import { useTasks } from '@/hooks/useTasks'
import { openMobileAgent } from '@/lib/mobileNav'
import { pruneSubAgentForest } from '@/components/session/session-tree'
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
  const { cronTasks, bgTasks, loading, killBgTask, removeCronTask } = useTasks(ws, taskSession)
  const [expandedCronID, setExpandedCronID] = useState<string | null>(null)
  // SubAgents as a TREE (we support nesting — up to 5 levels deep): keep every
  // active node plus the ancestors that make its position readable. The previous
  // "flatten then filter" lost those ancestors, so a running sub-sub-agent
  // appeared as a flat row with no parent.
  const subAgentTree = useMemo(() => {
    const activeNode = findSessionNode(session.sessions, sessionForFocusedAgent(tabManager, session.activeSession))
    if (!activeNode?.children?.length) return []
    return pruneSubAgentForest(activeNode.children, isActiveSubAgent)
  }, [session.sessions, tabManager?.activeTabId, tabManager?.tabs, session.activeSession])

  const hasCron = cronTasks.length > 0
  const hasBg = bgTasks.length > 0
  const hasSubAgents = subAgentTree.length > 0
  const empty = !hasCron && !hasBg && !hasSubAgents && !loading
  const hasRunningSubAgent = useMemo(() => {
    const some = (nodes: SessionInfo[]): boolean =>
      nodes.some((n) => n.running === true || (n.children ? some(n.children) : false))
    return some(subAgentTree)
  }, [subAgentTree])

  useEffect(() => {
    if (!hasRunningSubAgent) return
    const timer = setInterval(() => {
      void session.refresh()
    }, 2_000)
    return () => clearInterval(timer)
  }, [hasRunningSubAgent, session])

  const openSubAgent = (agent: SessionInfo) => {
    // Mobile first: there is no dockview on the phone, so openTab would be a
    // no-op (user report: "手机端 task view 里 subagent 无法点开交互").
    // openMobileAgent returns false when no mobile shell is registered.
    const target = {
      subAgentRole: agent.role,
      subAgentInstance: agent.instance,
      parentChatID: agent.parentChatID,
      parentChannel: agent.parentChannel,
      agentChatID: agent.fullKey || agent.agentChatID,
    }
    if (openMobileAgent(target)) return
    tabManager?.openTab({
      type: 'agent',
      title: subAgentTitle(agent),
      icon: 'bot',
      closable: true,
      data: target,
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
    // 普通垂直滚动容器，不用 radix ScrollArea——radix Viewport 内部的
    // `display: table; min-width: 100%` wrapper 按内容 min-content 计宽，
    // nowrap 长消息（cron message 可达数百字符）会把 table 撑到面板外
    // （实测 320px 面板被撑到 4073px），气泡超出屏幕。min-w-0 flex-1
    // truncate 链在 table 布局测量中失效——纯垂直列表没有横向滚动需求。
    <div className="h-full overflow-y-auto overflow-x-hidden overscroll-contain">
      <div className="flex flex-col gap-4 px-3 py-3 text-sm">
        {/* Cron tasks */}
        <section className="flex flex-col gap-2">
          <h3 className="text-xs font-semibold uppercase tracking-wide text-text-secondary">
            {t('sidebar.tasksCron')}
          </h3>
          {hasCron ? (
            <div className="flex flex-col gap-1.5">
              {cronTasks.map((task) => {
                const expanded = expandedCronID === task.id
                const schedule = task.cronExpr
                  ? task.cronExpr
                  : task.everySeconds
                    ? `every ${task.everySeconds}s`
                    : task.at
                      ? task.at
                      : task.delaySeconds
                        ? `delay ${task.delaySeconds}s`
                        : ''
                return (
                  <div key={task.id} className="rounded-md bg-bg-tertiary px-2 py-1.5">
                    <div className="flex items-start gap-2">
                      <AlarmClock className="mt-0.5 size-3.5 shrink-0 text-text-secondary" />
                      {/* 可点开：没有这个交互时，cron 行既看不到详情也无法操作
                          （用户报告："电脑 cron 也无法点开看详情"）。 */}
                      <button
                        type="button"
                        data-testid="cron-row"
                        aria-expanded={expanded}
                        onClick={() => setExpandedCronID(expanded ? null : task.id)}
                        className="min-w-0 flex-1 text-left"
                      >
                        <p className="truncate text-xs text-text-primary">{task.message}</p>
                        <p className="mt-0.5 truncate text-xs text-text-muted">{schedule}</p>
                      </button>
                      {task.oneShot && (
                        <span className="shrink-0 text-xs text-text-muted">1×</span>
                      )}
                      <button
                        type="button"
                        data-testid="cron-delete"
                        aria-label={t('common.delete')}
                        title={t('common.delete')}
                        onClick={() => void removeCronTask(task.id)}
                        className="flex size-6 shrink-0 items-center justify-center rounded text-text-muted hover:bg-destructive/10 hover:text-destructive"
                      >
                        <Trash2 className="size-3.5" />
                      </button>
                    </div>
                    {expanded && (
                      <dl
                        data-testid="cron-details"
                        className="mt-2 space-y-1 border-t border-border/60 pt-2 text-[11px] text-text-muted"
                      >
                        <div className="flex gap-2">
                          <dt className="shrink-0">{t('sidebar.cronSchedule')}</dt>
                          <dd className="min-w-0 break-words text-text-secondary">{schedule || '—'}</dd>
                        </div>
                        <div className="flex gap-2">
                          <dt className="shrink-0">{t('sidebar.cronNext')}</dt>
                          <dd className="min-w-0 break-words text-text-secondary">{task.nextRun || '—'}</dd>
                        </div>
                        <div className="flex gap-2">
                          <dt className="shrink-0">{t('sidebar.cronTarget')}</dt>
                          <dd className="min-w-0 break-words font-mono text-text-secondary">
                            {[task.channel, task.chatID].filter(Boolean).join(':') || '—'}
                          </dd>
                        </div>
                        <div className="flex gap-2">
                          <dt className="shrink-0">ID</dt>
                          <dd className="min-w-0 break-all font-mono text-text-secondary">{task.id}</dd>
                        </div>
                      </dl>
                    )}
                  </div>
                )
              })}
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
              <SubAgentTreeRows nodes={subAgentTree} depth={0} onOpen={openSubAgent} />
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
    </div>
  )
}

/**
 * SubAgentTreeRows — SubAgents rendered as the TREE they really are.
 *
 * We support nesting (main → sub → sub-sub …, up to 5 levels), so a flat list
 * hides the structure that matters when reading progress: which worker a
 * grandchild belongs to. Depth is shown three ways — indentation, a connector
 * guide, and a child-count pill on parents — and every row stays clickable
 * (same target as before, so opening a session is unchanged).
 */
function SubAgentTreeRows({
  nodes,
  depth,
  onOpen,
}: {
  nodes: SessionInfo[]
  depth: number
  onOpen: (agent: SessionInfo) => void
}) {
  return (
    <>
      {nodes.map((agent) => {
        const children = agent.children || []
        const running = agent.running === true || agent.status === 'running'
        return (
          <div key={`${agent.channel}:${agent.chatID}`} className="flex flex-col gap-1.5">
            <div className="flex items-stretch gap-1.5" style={depth > 0 ? { paddingLeft: depth * 10 } : undefined}>
              {depth > 0 && (
                // Connector: makes the parent/child relation visible even when
                // the rows are far apart (long previews, scrolled panel).
                <span aria-hidden data-testid="subagent-guide" className="w-[2px] shrink-0 rounded-full bg-border" />
              )}
              <button
                type="button"
                data-testid="subagent-row"
                data-depth={depth}
                className={
                  'flex w-full min-w-0 items-start gap-2 rounded-md px-2 py-1.5 text-left hover:bg-bg-hover ' +
                  (depth === 0 ? 'bg-bg-tertiary' : 'bg-bg-tertiary/50')
                }
                onClick={() => onOpen(agent)}
              >
                <span className="relative mt-0.5 shrink-0">
                  <Bot className="size-3.5 text-text-secondary" />
                  {running && (
                    <span
                      aria-hidden
                      className="absolute -right-1 -top-0.5 size-1.5 animate-pulse rounded-full bg-status-running"
                    />
                  )}
                </span>
                <div className="min-w-0 flex-1">
                  <p className="flex min-w-0 items-center gap-1.5">
                    <span className="truncate text-xs text-text-primary">{subAgentTitle(agent)}</span>
                    {children.length > 0 && (
                      <span
                        data-testid="subagent-child-count"
                        className="shrink-0 rounded bg-bg-secondary px-1 text-[10px] leading-4 text-text-muted"
                      >
                        {children.length}
                      </span>
                    )}
                  </p>
                  <p className="mt-0.5 truncate text-xs text-text-muted">
                    {agent.preview || (agent.status === 'waiting_input' ? 'waiting' : agent.running ? 'running' : agent.historical ? 'history' : 'idle')}
                  </p>
                </div>
              </button>
            </div>
            {children.length > 0 && <SubAgentTreeRows nodes={children} depth={depth + 1} onOpen={onOpen} />}
          </div>
        )
      })}
    </>
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
