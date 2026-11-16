/**
 * TasksPanel — right-sidebar panel showing Cron tasks and background Shell tasks.
 *
 * Calls Web task REST APIs (via useTasks hook),
 * refreshing on session switch and every 30 seconds automatically.
 *
 * Icons: ⏰ cron task, ▶ running bg command, ✓ completed, ✗ failed.
 */
import { useEffect, useMemo, useState } from 'react'
import { flattenSubAgentTree } from '@/components/session/session-tree'
import { AlarmClock, Bot, Check, Inbox, Loader2, Play, Square, Trash2, X } from 'lucide-react'
import { useI18n } from '@/providers/i18n'
import { useWSConnection } from '@/hooks/useWSConnection'
import { useSessionStore } from '@/hooks/useSessionStore'
import { useTasks } from '@/hooks/useTasks'
import { openMobileAgent } from '@/lib/mobileNav'
import { StructuredLink } from '@/components/StructuredLink'
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
  // 2026-10-26 用户令：**所有子代理都要显示**，不论 busy 还是 idle；
  // busy 的排在最上面。旧实现只取「当前节点 children」并 `filter(isActiveSubAgent)`，
  // 于是 idle 的子代理、以及没能挂到父节点下的 orphan（后端 orphan_subagents）
  // 在 UI 里彻底消失 —— 表现为「首辅明明 busy 却找不到它」。
  // store 的 session.subAgents 已经是 flattenTreeAgents(整棵树) 的结果（含 orphan），
  // 直接用它，不要在这里重新裁剪。
  const [showIdleSubAgents, setShowIdleSubAgents] = useState(false)

  // 2026-10-26 修正：数据源必须是「当前会话节点的子代理子树」。
  // 之前误用 session.subAgents —— 那是**所有会话树里全部 agent** 的 flatten，
  // 会把 sili / proj.xbot.* / tester / taizi 这些与当前对话无关的会话也列进来。
  // 同时**不再按状态过滤**：idle 的子代理同样要能看到并点开查历史。
  const subAgents = useMemo(() => {
    const activeNode = findSessionNode(session.sessions, sessionForFocusedAgent(tabManager, session.activeSession))
    if (!activeNode) return []
    const children = activeNode.children ?? []
    if (children.length === 0) return []
    const all = flattenSubAgentTree([{ ...activeNode, children }])
    const busy = (a: (typeof all)[number]) => isActiveSubAgent(a)
    return [...all].sort((a, b) => Number(busy(b)) - Number(busy(a)))
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
        {/* 结构化消息入口：/structured 路由原本没有任何侧栏链接，用户只能靠猜 URL
            （2026-10-22：「面板完全没用处没人知道怎么用」）。用 <Link> 走项目已有的
            react-router 跳转，不用 window.location（会整页重载并丢掉 WS 连接）。 */}
        <StructuredLink
          className="flex w-full items-center gap-2 rounded-md bg-bg-tertiary px-2 py-2 text-left transition-colors hover:bg-bg-secondary"
          title={t('sidebar.structuredEntryTitle')}>
          <Inbox className="size-3.5 shrink-0 text-text-secondary" />
          <span className="min-w-0 flex-1">
            <span className="block truncate text-xs font-medium text-text-primary">{t('sidebar.structuredEntry')}</span>
            <span className="block truncate text-[10px] text-text-muted">{t('sidebar.structuredEntryDesc')}</span>
          </span>
          <span className="shrink-0 text-[10px] text-text-muted">{t('sidebar.openEntry')}</span>
        </StructuredLink>

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
            {t('sidebar.tasksSubAgents')}
          </h3>
          {hasSubAgents ? (
            <div className="flex flex-col gap-1.5">
              {/* busy 组：永远展开，带绿色脉冲点，一眼看出谁在跑 */}
              {subAgents.filter((a) => isActiveSubAgent(a)).map((agent) => (
                <button
                  type="button"
                  key={`busy:${agent.channel}:${agent.chatID}`}
                  className="flex w-full items-start gap-2 rounded-md bg-bg-tertiary px-2 py-1.5 text-left hover:bg-bg-hover"
                  onClick={() => openSubAgent(agent)}
                >
                  <span className="mt-1 size-2 shrink-0 animate-pulse rounded-full bg-green-500" aria-label="busy" />
                  <Bot className="mt-0.5 size-3.5 shrink-0 text-text-secondary" />
                  <div className="min-w-0 flex-1">
                    <p className="truncate text-xs font-medium text-text-primary">{subAgentTitle(agent)}</p>
                    <p className="mt-0.5 truncate text-xs text-text-muted">
                      {agent.preview || (agent.status === 'waiting_input' ? 'waiting' : 'busy')}
                    </p>
                  </div>
                </button>
              ))}
              {/* idle / 历史组：默认收起，避免把一屏都刷满；点开可查历史 */}
              {(() => {
                const idle = subAgents.filter((a) => !isActiveSubAgent(a))
                if (idle.length === 0) return null
                return (
                  <>
                    <button
                      type="button"
                      className="mt-1 flex w-full items-center justify-between rounded-md px-2 py-1 text-left text-xs text-text-muted hover:bg-bg-hover"
                      onClick={() => setShowIdleSubAgents((v) => !v)}
                    >
                      <span>{showIdleSubAgents ? '收起空闲/历史' : `空闲/历史 (${idle.length})`}</span>
                      <span>{showIdleSubAgents ? '▾' : '▸'}</span>
                    </button>
                    {showIdleSubAgents && idle.map((agent) => (
                      <button
                        type="button"
                        key={`idle:${agent.channel}:${agent.chatID}`}
                        className="flex w-full items-start gap-2 rounded-md px-2 py-1 text-left hover:bg-bg-hover"
                        onClick={() => openSubAgent(agent)}
                      >
                        <span className="mt-1 size-2 shrink-0 rounded-full bg-border" />
                        <Bot className="mt-0.5 size-3.5 shrink-0 text-text-secondary" />
                        <div className="min-w-0 flex-1">
                          <p className="truncate text-xs text-text-primary">{subAgentTitle(agent)}</p>
                          <p className="mt-0.5 truncate text-xs text-text-muted">
                            {agent.historical ? 'history' : 'idle'}
                          </p>
                        </div>
                      </button>
                    ))}
                  </>
                )
              })()}
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
  const wantChannel = selector.channel
  const wantChatID = selector.chatID
  const walk = (nodes: SessionInfo[]): SessionInfo | null => {
    for (const n of nodes) {
      if ((!wantChannel || n.channel === wantChannel) && (!wantChatID || n.chatID === wantChatID)) return n
      const hit = n.children?.length ? walk(n.children) : null
      if (hit) return hit
    }
    return null
  }
  return walk(sessions)
}
