/**
 * builtinPanels —— 布局 v4 内置面板（source='core'）。
 *
 * 六个 core.* 面板注册进 panelRegistry，与插件面板共用 PanelChrome 外壳：
 *  - core.sessions：SessionList + SessionSearch 主体，docked flex-1 占满剩余
 *  - core.files / core.search / core.info / core.tasks：原右栏面板原样迁入
 *  - core.terminal：TerminalList + useTerminal（仅面板 body 渲染时挂载，
 *    初始 collapsed=true 不触发后端请求——与原 LeftTerminalPanel 同语义）
 *
 * 需要 hooks 的 render 主体在模块级组件里实现（render 回调只返回元素，
 * 不在回调里调 hooks——组件身份稳定，状态不随重渲染丢失）。
 */
import { useCallback, useMemo, useState } from 'react'

import { panelRegistry, type PanelDefinition, type PanelRenderContext } from '@/plugin-runtime/panelRegistry'
import { FileExplorer } from '@/components/sidebar/FileExplorer'
import { FileSearch } from '@/components/sidebar/FileSearch'
import { SessionInfo as SessionInfoPanel } from '@/components/sidebar/SessionInfo'
import { TasksPanel } from '@/components/sidebar/TasksPanel'
import { TerminalList } from '@/components/sidebar/TerminalList'
import { useTerminal } from '@/hooks/useTerminal'
import { useSessionStore } from '@/hooks/useSessionStore'
import { SessionList } from '@/components/session/SessionList'
import { SessionSearch } from '@/components/session/SessionSearch'
import { NewSessionDialog } from '@/components/session/NewSessionDialog'
import {
  groupSessions,
  isSubAgentSession,
  parseAgentChatID,
  sameSession,
  sortSessions,
} from '@/lib/session-grouping'
import type { SessionInfo, SessionSelector } from '@/types/shared'
import type { ExportFormat } from '@/components/agent/api'
import { downloadSession } from '@/components/agent/api'

/** core.sessions：SessionList + SessionSearch 主体（docked flex-1）。 */
function CoreSessionsPanel({ ctx }: { ctx: PanelRenderContext }) {
  const store = useSessionStore()
  const tabManager = ctx.tabManager
  const [search, setSearch] = useState('')
  const [newOpen, setNewOpen] = useState(false)

  const filteredSessions = useMemo(() => {
    if (!store.activeChannel) return store.sessions
    return store.sessions.filter((s) =>
      s.channel === store.activeChannel ||
      s.parentChannel === store.activeChannel ||
      (s.children || []).some((c) => c.parentChannel === store.activeChannel),
    )
  }, [store.sessions, store.activeChannel])

  const filteredGroups = useMemo(
    () => groupSessions(filteredSessions, store.category, store.starredIds),
    [filteredSessions, store.category, store.starredIds],
  )
  const filteredSorted = useMemo(
    () => sortSessions(filteredSessions, store.starredIds),
    [filteredSessions, store.starredIds],
  )

  // 主会话点击：desktop 打开/聚焦 agent tab（session-per-tab）+ 侧栏高亮。
  const handleSelect = useCallback(
    (id: string, channel: string) => {
      const selector = { channel: channel || 'web', chatID: id }
      const matched = findSessionInTree(store.sessions, selector) ?? store.subAgents.find((sa) => sameSession(sa, selector))
      if (matched && isSubAgentSession(matched)) {
        const fullKey = matched.fullKey || matched.agentChatID || matched.chatID
        const parsed = parseAgentChatID(fullKey)
        tabManager.openTab({
          type: 'agent',
          title: matched.role
            ? matched.instance ? `${matched.role}/${matched.instance}` : matched.role
            : (matched.label || fullKey),
          icon: 'bot',
          closable: true,
          data: {
            subAgentRole: parsed?.role || matched.role,
            subAgentInstance: parsed?.instance || matched.instance,
            parentChatID: parsed?.parentChatID || matched.parentChatID,
            parentChannel: parsed?.parentChannel || matched.parentChannel,
            agentChatID: fullKey,
          },
        })
        return
      }
      const session = matched ?? store.sessions.find((s) => s.chatID === id && s.channel === (channel || 'web'))
      tabManager.openTab({
        type: 'agent',
        title: session?.label ?? id,
        icon: 'bot',
        closable: true,
        data: { filePath: id, channel: channel || 'web' },
      })
      store.activateSession(id, channel)
    },
    [store.sessions, store.subAgents, store.activateSession, tabManager],
  )

  const handleExport = useCallback(async (session: SessionInfo, format: ExportFormat) => {
    const selector: SessionSelector = { channel: session.channel || 'web', chatID: session.chatID }
    try {
      await downloadSession(selector, format)
    } catch (err) {
      console.error('export session failed:', err)
    }
  }, [])

  return (
    <div className="flex h-full min-h-0 flex-col">
      <SessionSearch value={search} onChange={setSearch} />
      {/* 新建会话按钮（全宽 accent，v5.2 加回——桌面端面板版漏掉了） */}
      <div className="shrink-0 px-2.5 pt-1.5 pb-1">
        <button
          type="button"
          onClick={() => setNewOpen(true)}
          className="flex w-full items-center justify-center gap-1.5 rounded-lg py-1.5 text-[11.5px] font-medium text-text-primary transition-opacity hover:opacity-90"
          style={{ background: 'var(--accent)' }}
        >
          <svg className="size-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d="M12 5v14M5 12h14" /></svg>
          新建会话
        </button>
      </div>
      <div className="min-h-0 flex-1">
        {store.loading ? (
          <div className="flex h-full items-center justify-center px-4 text-xs text-text-muted">加载中…</div>
        ) : filteredSessions.length === 0 && store.activeChannel ? (
          <div className="flex h-full items-center justify-center px-4 text-center text-xs text-text-muted">
            该渠道暂无会话
          </div>
        ) : (
          <SessionList
            sessions={filteredSessions}
            groups={filteredGroups}
            sortedSessions={filteredSorted}
            category={store.category}
            starredIds={store.starredIds}
            unreadIds={store.unreadIds}
            activeSession={store.activeSession}
            search={search}
            subAgents={store.subAgents}
            onSelect={handleSelect}
            onToggleStar={store.toggleStar}
            onRename={store.renameSession}
            onDelete={store.deleteSession}
            onExport={handleExport}
            onReorder={store.reorderSessions}
            hasMore={store.hasMore}
            onLoadMore={store.loadMore}
          />
        )}
      </div>
      <NewSessionDialog open={newOpen} onOpenChange={setNewOpen} onCreate={store.createSession} />
    </div>
  )
}

function findSessionInTree(sessions: SessionInfo[], selector: SessionSelector): SessionInfo | null {
  for (const session of sessions) {
    if (sameSession(session, selector)) return session
    const child = findSessionInTree(session.children || [], selector)
    if (child) return child
  }
  return null
}

/** core.terminal：仅 body 渲染时挂载 useTerminal（避免无条件后端请求）。 */
function CoreTerminalPanel({ ctx }: { ctx: PanelRenderContext }) {
  const terminalManager = useTerminal(ctx.tabManager)
  return <TerminalList terminalManager={terminalManager} />
}

const BUILTIN_PANELS: PanelDefinition[] = [
  {
    id: 'core.sessions',
    title: '会话',
    icon: 'message',
    defaultSlot: 'left',
    defaultMode: 'docked',
    render: (ctx) => <CoreSessionsPanel ctx={ctx} />,
    source: 'core',
  },
  {
    id: 'core.files',
    title: '文件',
    icon: 'files',
    defaultSlot: 'left',
    defaultMode: 'docked',
    render: (ctx) => <FileExplorer tabManager={ctx.tabManager} />,
    source: 'core',
  },
  {
    id: 'core.search',
    title: '搜索',
    icon: 'search',
    defaultSlot: 'left',
    defaultMode: 'docked',
    render: (ctx) => <FileSearch tabManager={ctx.tabManager} />,
    source: 'core',
  },
  {
    id: 'core.info',
    title: '信息',
    icon: 'info',
    defaultSlot: 'left',
    defaultMode: 'docked',
    render: (ctx) => <SessionInfoPanel tabManager={ctx.tabManager} />,
    source: 'core',
  },
  {
    id: 'core.tasks',
    title: '任务',
    icon: 'tasks',
    defaultSlot: 'left',
    defaultMode: 'docked',
    render: (ctx) => <TasksPanel tabManager={ctx.tabManager} />,
    source: 'core',
  },
  {
    id: 'core.terminal',
    title: '终端',
    icon: 'terminal',
    defaultSlot: 'left',
    defaultMode: 'docked',
    render: (ctx) => <CoreTerminalPanel ctx={ctx} />,
    source: 'core',
  },
]

/** 注册全部内置面板（AppShell 挂载时调用；幂等——同 id 覆盖）。 */
export function registerBuiltinPanels(): void {
  for (const def of BUILTIN_PANELS) panelRegistry.registerPanel(def)
}
