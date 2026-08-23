/**
 * MobileAppShell — 手机端外壳（聊天为主角的重新设计）。
 *
 * 布局原则：
 *   - 聊天（agent 视图）是默认主视图，全屏沉浸 —— 无常驻底部导航。
 *   - 导航收进顶栏：☰ 抽屉（会话列表）/ 返回按钮（工具页、终端、SubAgent）/
 *     顶栏布局项（新会话、工具、设置，slot 系统驱动）。
 *   - `mobile.bottom_nav` slot 按需渲染：仅当用户把布局项移入该 slot 时
 *     才出现底部导航条（新样式：pill active + 触摸反馈）。默认配置为空。
 *
 * 不变量：AgentPanel 始终挂载（display:none 切换视图）。条件渲染卸载会
 * 销毁 AgentPanel 的 MessageStore/ProgressStore（useRef 随组件卸载丢失），
 * 切回会话时重建空 store → 依赖 fetchHistory + hydration 恢复，API 竞态下
 * 迭代可能少。保持挂载则流式状态持续更新，切回立即显示完整历史。
 */
import { lazy, Suspense, useEffect, useMemo, useState } from 'react'
import { ArrowLeft, Bot, Files, Info, ListChecks, Loader2, Menu, Plus, Search, Settings, SquareTerminal, Wrench } from 'lucide-react'

import { AgentPanel } from '@/workspace/panels/AgentPanel'
const TerminalPanel = lazy(() =>
  import('@/workspace/panels/TerminalPanel').then(m => ({ default: m.TerminalPanel })))
const FilePanel = lazy(() =>
  import('@/workspace/panels/FilePanel').then(m => ({ default: m.FilePanel })))
const DiffPanel = lazy(() =>
  import('@/workspace/panels/DiffPanel').then(m => ({ default: m.DiffPanel })))
import { FileExplorer } from '@/components/sidebar/FileExplorer'
import { FileSearch } from '@/components/sidebar/FileSearch'
import { SessionInfo } from '@/components/sidebar/SessionInfo'
import type { SessionInfo as SessionInfoType } from '@/types/shared'
import { SessionSidebar } from '@/components/session/SessionSidebar'
import { TasksPanel } from '@/components/sidebar/TasksPanel'
import { TerminalList } from '@/components/sidebar/TerminalList'
import { InfoBar } from '@/plugins/InfoBar'
import { PluginView } from '@/plugin-runtime/PluginView'
import { usePluginViewPanels } from '@/plugin-runtime/usePluginViewPanels'
import { pluginIcon } from '@/plugin-runtime/pluginIcons'
import { useLayoutItems } from '@/plugin-runtime/layoutRegistry'
import { BUILTIN_LAYOUT_ITEMS, type LayoutItem } from '@/plugin-runtime/layoutTypes'

const SettingsDialog = lazy(() =>
  import('@/components/settings/SettingsDialog').then(m => ({ default: m.SettingsDialog })))
import { Button } from '@/components/ui/button'
import { Sheet, SheetContent, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { DockviewContext, type DockviewContextValue } from '@/workspace/types'
import { RightSidebarControlContext } from '@/components/sidebar/RightSidebarControl'
import { useAuth } from '@/hooks/useAuth'
import { useCwd } from '@/providers/CwdProvider'
import { useI18n } from '@/providers/i18n'
import { useSessionStore } from '@/hooks/useSessionStore'
import { useOptionalPluginRuntime } from '@/plugin-runtime'
import { useTabManager } from '@/hooks/useTabManager'
import { useTheme } from '@/hooks/useTheme'
import { useWSConnection } from '@/providers/WSProvider'
import { useTerminal } from '@/hooks/useTerminal'
import { closeMobileWorkView, pushMobileWorkView, useMobileWorkView, type MobileWorkView } from '@/workspace/mobileWorkView'
import { cn } from '@/lib/utils'
import type { SidebarPanel } from '@/components/sidebar/RightSidebar'
import type { PanelProps } from '@/workspace/panels/types'

type MobileView = 'agent' | 'detail' | 'terminal' | 'work'

const PANEL_BUTTONS: { panel: SidebarPanel; icon: typeof Files; labelKey: string }[] = [
  { panel: 'files', icon: Files, labelKey: 'sidebar.files' },
  { panel: 'search', icon: Search, labelKey: 'sidebar.search' },
  { panel: 'info', icon: Info, labelKey: 'sidebar.info' },
  { panel: 'tasks', icon: ListChecks, labelKey: 'sidebar.tasks' },
  { panel: 'terminal', icon: SquareTerminal, labelKey: 'sidebar.terminal' },
]

const mobilePanelProps: PanelProps = {
  params: {
    tabId: 'mobile-agent',
    type: 'agent',
    title: 'Agent',
    icon: 'bot',
    closable: false,
    active: true,
  },
  api: {} as PanelProps['api'],
  containerApi: {} as PanelProps['containerApi'],
}

/** Construct PanelProps for a mobile terminal panel (no Dockview needed). */
function mobileTerminalProps(terminalId: string): PanelProps {
  return {
    params: {
      tabId: `mobile-terminal-${terminalId}`,
      type: 'terminal',
      title: 'Terminal',
      icon: 'terminal',
      closable: true,
      active: true,
      terminalId,
    },
    api: {} as PanelProps['api'],
    containerApi: {} as PanelProps['containerApi'],
  }
}

export function MobileAppShell() {
  const tabManager = useTabManager()
  const sessionStore = useSessionStore()
  const theme = useTheme()
  const i18n = useI18n()
  const ws = useWSConnection()
  const cwd = useCwd()
  const auth = useAuth()
  const { t } = i18n
  const [drawerOpen, setDrawerOpen] = useState(false)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [view, setView] = useState<MobileView>('agent')
  const [activePanel, setActivePanel] = useState<SidebarPanel>('info')
  const [activeTerminalId, setActiveTerminalId] = useState<string | null>(null)
  // SubAgent being viewed (mobile has no Dockview tab container — render the
  // AgentPanel in sub-agent mode directly, like a desktop tab).
  const [subAgentView, setSubAgentView] = useState<{
    subAgentRole?: string
    subAgentInstance?: string
    parentChatID?: string
    parentChannel?: string
    agentChatID?: string
  } | null>(null)

  const terminalManager = useTerminal(tabManager, (terminalId) => {
    setActiveTerminalId(terminalId)
    setView('terminal')
  })

  // 全屏工作视图（文件预览 / 插件动态视图）：手机端没有 Dockview，
  // openTab 的目标在这里以全屏页呈现。push 发生时自动切到 'work' 视图。
  const workView = useMobileWorkView()
  useEffect(() => {
    if (workView) setView('work')
  }, [workView])

  // 手机端无 Dockview——FileExplorer/FileSearch 等的 openTab(file/plugin)
  // 路由到全屏工作视图（其余类型维持原行为，如 SubAgent 专用机制）。
  const mobileTabManager = useMemo(() => ({
    ...tabManager,
    openTab: (input: Parameters<typeof tabManager.openTab>[0]): string => {
      if (input.type === 'file' && input.data?.filePath) {
        pushMobileWorkView({ kind: 'file', title: input.title, filePath: input.data.filePath })
        return ''
      }
      if (input.type === 'plugin' && input.data?.viewId) {
        const d = input.data as {
          viewId: string
          viewKey?: string
          viewParams?: Record<string, unknown>
        }
        pushMobileWorkView({
          kind: 'plugin',
          title: input.title,
          viewId: d.viewId,
          viewKey: d.viewKey,
          viewParams: d.viewParams,
        })
        return ''
      }
      return tabManager.openTab(input)
    },
  }), [tabManager])

  // 布局定制：顶栏操作区由 slot 注册表驱动；bottom_nav 按需渲染（默认空）。
  const bottomNavItems = useLayoutItems('mobile.bottom_nav')
  const topBarItems = useLayoutItems('mobile.top_bar')

  const rightSidebar = useMemo(() => ({
    openPanel: (panel: SidebarPanel) => {
      setActivePanel(panel)
      setView('detail')
    },
  }), [])

  const pluginRuntime = useOptionalPluginRuntime()

  const ctxValue = useMemo<DockviewContextValue>(() => ({
    theme,
    i18n,
    ws,
    cwd,
    auth,
    sessionStore,
    rightSidebar,
    pluginRuntime,
  }), [auth, cwd, i18n, pluginRuntime, rightSidebar, sessionStore, theme, ws])

  const agentTitle = subAgentView
    ? (subAgentView.subAgentRole ?? 'SubAgent')
    : sessionStore.activeSession
      ? sessionStore.sessions.find((s) => s.chatID === sessionStore.activeSession?.chatID && s.channel === sessionStore.activeSession?.channel)?.label
        ?? sessionStore.activeSession.chatID
      : 'Agent'

  // 顶栏标题随视图切换：agent → 会话名 / detail → 工具 / terminal → 终端名 /
  // work → 工作视图标题（文件名 / 插件视图名）。
  const headerTitle = view === 'terminal'
    ? (terminalManager.terminals.find((tm) => tm.id === activeTerminalId)?.title ?? 'Terminal')
    : view === 'work'
      ? (workView?.title ?? '')
      : view === 'detail'
        ? t('agent.tools')
        : agentTitle

  // Mobile AgentPanel props: when viewing a SubAgent, pass the sub-agent params
  // so AgentPanel switches into SubAgent mode (get_session_messages + agent
  // SSE route); otherwise render the main active session.
  const agentPanelProps: PanelProps = subAgentView
    ? {
        params: {
          tabId: `mobile-subagent-${subAgentView.agentChatID ?? subAgentView.subAgentRole ?? 'x'}`,
          type: 'agent',
          title: subAgentView.subAgentRole ?? 'SubAgent',
          icon: 'bot',
          closable: true,
          active: true,
          subAgentRole: subAgentView.subAgentRole,
          subAgentInstance: subAgentView.subAgentInstance,
          parentChatID: subAgentView.parentChatID,
          parentChannel: subAgentView.parentChannel,
          agentChatID: subAgentView.agentChatID,
        },
        api: {} as PanelProps['api'],
        containerApi: {} as PanelProps['containerApi'],
      }
    : mobilePanelProps

  const createSession = async () => {
    const id = await sessionStore.createSession()
    if (id) {
      setDrawerOpen(false)
      setSubAgentView(null)
      setView('agent')
    }
  }

  const handleSubAgentSelect = (subAgent: SessionInfoType) => {
    setSubAgentView({
      subAgentRole: subAgent.role,
      subAgentInstance: subAgent.instance,
      parentChatID: subAgent.parentChatID,
      parentChannel: subAgent.parentChannel,
      agentChatID: subAgent.agentChatID,
    })
    setDrawerOpen(false)
    setView('agent')
  }

  // 顶栏左侧按钮：非 agent 视图 → 返回上一级；SubAgent 视图 → 返回主会话；
  // agent 视图 → ☰ 打开会话抽屉。
  const handleHeaderNav = () => {
    if (view === 'work') {
      // 工作视图 → 关闭并回到工具页（大多数工作视图从工具页/插件面板打开）。
      closeMobileWorkView()
      setView('detail')
    } else if (view === 'terminal') {
      setView('detail')
    } else if (view === 'detail') {
      setView('agent')
    } else if (subAgentView) {
      setSubAgentView(null)
    } else {
      setDrawerOpen(true)
    }
  }
  const headerNavLabel = view === 'agent' && !subAgentView ? t('sidebar.sessions') : t('common.back')

  return (
    <DockviewContext.Provider value={ctxValue}>
      <RightSidebarControlContext.Provider value={rightSidebar}>
        {/* fixed inset-0 (NOT h-dvh / h-full): iOS PWA standalone stops BOTH
         *  100dvh AND the height:100% chain at the safe area (~34px above the
         *  screen edge) — a dead strip remained below the input box (user-
         *  reported 3×). Fixed positioning anchors to all four edges of the
         *  LAYOUT viewport, which DOES span the full screen under
         *  viewport-fit=cover. This is the community-verified iOS PWA
         *  full-bleed fix. */}
        <div className="fixed inset-0 flex flex-col overflow-hidden bg-bg-primary text-text-primary">
          <header className="flex shrink-0 items-center gap-0.5 border-b border-border bg-bg-secondary pr-1" style={{ paddingTop: 'var(--safe-area-top)', height: 'calc(3rem + var(--safe-area-top))' }}>
            <Button type="button" variant="ghost" size="icon" aria-label={headerNavLabel} onClick={handleHeaderNav}>
              {view === 'agent' && !subAgentView ? <Menu className="size-5" /> : <ArrowLeft className="size-5" />}
            </Button>
            <div className="min-w-0 flex-1 truncate px-1 text-base font-semibold">{headerTitle}</div>
            {topBarItems.map((item) => renderTopBarItem(item, {
              view,
              activePanel,
              onCreateSession: () => void createSession(),
              onOpenSettings: () => setSettingsOpen(true),
              onSelectView: (v: MobileView) => setView(v),
              onOpenPanel: (panel: SidebarPanel) => { setActivePanel(panel); setView('detail') },
              t,
            }))}
          </header>

          <main className="min-h-0 flex-1 overflow-hidden">
            {/* Agent 面板始终挂载（display:none 切换视图）——见文件头不变量说明。 */}
            <div className="h-full" style={{ display: view === 'agent' ? undefined : 'none' }}>
              <AgentPanel {...agentPanelProps} />
            </div>
            {view === 'work' && workView ? (
              <div className="h-full">
                {workView.kind === 'file' ? (
                  <Suspense fallback={<div className="flex h-full items-center justify-center"><Loader2 className="size-6 animate-spin text-muted-foreground" /></div>}>
                    <FilePanel
                      params={{
                        tabId: `mobile-file-${workView.filePath}`,
                        type: 'file',
                        title: workView.title,
                        icon: 'file',
                        closable: true,
                        active: true,
                        filePath: workView.filePath,
                      }}
                      api={{} as PanelProps['api']}
                      containerApi={{} as PanelProps['containerApi']}
                    />
                  </Suspense>
                ) : workView.kind === 'diff' ? (
                  <Suspense fallback={<div className="flex h-full items-center justify-center"><Loader2 className="size-6 animate-spin text-muted-foreground" /></div>}>
                    <DiffPanel
                      params={{
                        tabId: `mobile-diff-${workView.diffKey ?? workView.title}`,
                        type: 'diff',
                        title: workView.title,
                        icon: 'file-diff',
                        closable: true,
                        active: true,
                        diffKey: workView.diffKey,
                        original: workView.original,
                        modified: workView.modified,
                        diffPath: workView.diffPath,
                        diffScope: workView.diffScope,
                      }}
                      api={{} as PanelProps['api']}
                      containerApi={{} as PanelProps['containerApi']}
                    />
                  </Suspense>
                ) : (
                  <MobilePluginWorkView view={workView} />
                )}
              </div>
            ) : view !== 'agent' && (
              view === 'terminal' && activeTerminalId ? (
                <div className="h-full">
                  <Suspense fallback={<div className="flex h-full items-center justify-center"><Loader2 className="size-6 animate-spin text-muted-foreground" /></div>}>
                    <TerminalPanel {...mobileTerminalProps(activeTerminalId)} />
                  </Suspense>
                </div>
              ) : (
              <MobileDetail
                activePanel={activePanel}
                onPanelChange={setActivePanel}
                tabManager={mobileTabManager}
                terminalManager={terminalManager}
              />
              )
            )}
          </main>

          {/* Status bar (InfoBar) — same position as desktop AppShell (bottom
           *  of main). Its height absorbs the iOS safe area, so the rounded
           *  screen corners clip THIS bar's background instead of the input
           *  box above, and the plugin status spans finally show on mobile. */}
          <InfoBar />

          {/* 按需底部导航：仅当用户把布局项移入 mobile.bottom_nav 时渲染。 */}
          {bottomNavItems.length > 0 && (
            <nav className="grid shrink-0 border-t border-border bg-bg-secondary" style={{ paddingBottom: 'var(--safe-area-bottom)', height: 'calc(3.5rem + var(--safe-area-bottom))', gridTemplateColumns: `repeat(${bottomNavItems.length}, minmax(0, 1fr))` }}>
              {bottomNavItems.map((item) => renderBottomNavItem(item, {
                view,
                activePanel,
                onSelect: (v: MobileView) => setView(v),
                onOpenPanel: (panel: SidebarPanel) => { setActivePanel(panel); setView('detail') },
                t,
              }))}
            </nav>
          )}

          <Sheet open={drawerOpen} onOpenChange={setDrawerOpen}>
            <SheetContent side="left" className="w-[86vw] max-w-none p-0" showCloseButton={false}>
              <SheetHeader className="sr-only">
                <SheetTitle>{t('sidebar.sessions')}</SheetTitle>
              </SheetHeader>
              <SessionSidebar tabManager={tabManager} onSessionSelected={() => setDrawerOpen(false)} onSubAgentSelect={handleSubAgentSelect} />
            </SheetContent>
          </Sheet>

          <Suspense fallback={null}>
            <SettingsDialog open={settingsOpen} onOpenChange={setSettingsOpen} />
          </Suspense>
        </div>
      </RightSidebarControlContext.Provider>
    </DockviewContext.Provider>
  )
}

/**
 * 渲染按需底部导航的一个布局项（用户把项移入 mobile.bottom_nav 时出现）。
 * 内置项按 id 分派到对应视图/动作；插件 view 项打开工具页对应 tab。
 */
function renderBottomNavItem(item: LayoutItem, actions: {
  view: MobileView
  activePanel: SidebarPanel
  onSelect: (v: MobileView) => void
  onOpenPanel: (panel: SidebarPanel) => void
  t: (k: string) => string
}) {
  const { view, activePanel, onSelect, onOpenPanel, t } = actions
  const active =
    item.id === BUILTIN_LAYOUT_ITEMS.mobileTools
      ? view === 'detail' || view === 'terminal'
      : view === 'detail' && activePanel === (item.id as SidebarPanel)
  const Icon = iconForItem(item)
  const label = item.labelKey ? t(item.labelKey) : item.title

  const handleClick = () => {
    if (item.id === BUILTIN_LAYOUT_ITEMS.mobileTools) {
      onSelect('detail')
    } else {
      // 插件 view 项 → 打开工具页对应 tab。
      onOpenPanel(item.id as SidebarPanel)
    }
  }

  return (
    <button
      key={item.id}
      type="button"
      aria-label={label}
      aria-current={active ? 'page' : undefined}
      className="flex min-w-0 flex-col items-center justify-center gap-0.5 px-1 text-xs transition-transform active:scale-95"
      onClick={handleClick}
    >
      <span className={cn(
        'flex h-7 w-12 items-center justify-center rounded-full transition-colors',
        active && 'bg-accent/15',
      )}>
        {Icon ? <Icon className={cn('size-5', active && 'text-accent')} /> : null}
      </span>
      <span className={cn('w-full truncate text-center', active ? 'font-medium text-accent' : 'text-text-secondary')}>{label}</span>
    </button>
  )
}

/** 按布局项 id 解析 lucide 图标组件（内置项 + 插件 view 图标）。 */
function iconForItem(item: LayoutItem) {
  const icons: Record<string, typeof Bot> = {
    // mobileTools opens the AGGREGATE tools page (files/search/info/tasks/
    // terminal tabs) — NOT just a terminal. Wrench matches its "工具" label;
    // SquareTerminal here was misleading (it is the terminal PANEL's icon).
    [BUILTIN_LAYOUT_ITEMS.mobileTools]: Wrench,
    [BUILTIN_LAYOUT_ITEMS.mobileNewChat]: Plus,
    [BUILTIN_LAYOUT_ITEMS.mobileSettings]: Settings,
    [BUILTIN_LAYOUT_ITEMS.desktopSessions]: Menu,
    [BUILTIN_LAYOUT_ITEMS.desktopFiles]: Files,
    [BUILTIN_LAYOUT_ITEMS.desktopSearch]: Search,
    [BUILTIN_LAYOUT_ITEMS.desktopInfo]: Info,
    [BUILTIN_LAYOUT_ITEMS.desktopTasks]: ListChecks,
    [BUILTIN_LAYOUT_ITEMS.desktopTerminal]: SquareTerminal,
  }
  return icons[item.id] ?? (item.icon ? pluginIcon(item.icon) : null)
}

/**
 * 渲染顶栏操作区的一个布局项。内置 +/设置/工具 按 id 分派动作并带 active 态；
 * 插件 view 项打开工具页对应 tab。
 */
function renderTopBarItem(item: LayoutItem, actions: {
  view: MobileView
  activePanel: SidebarPanel
  onCreateSession: () => void
  onOpenSettings: () => void
  onSelectView: (v: MobileView) => void
  onOpenPanel: (panel: SidebarPanel) => void
  t: (k: string) => string
}) {
  const { view, activePanel, onCreateSession, onOpenSettings, onSelectView, onOpenPanel, t } = actions
  const Icon = iconForItem(item)
  const label = item.labelKey ? t(item.labelKey) : item.title

  const active =
    item.id === BUILTIN_LAYOUT_ITEMS.mobileTools
      ? view === 'detail' || view === 'terminal'
      : view === 'detail' && activePanel === (item.id as SidebarPanel)

  const handleClick = () => {
    switch (item.id) {
      case BUILTIN_LAYOUT_ITEMS.mobileNewChat:
        onCreateSession()
        break
      case BUILTIN_LAYOUT_ITEMS.mobileSettings:
        onOpenSettings()
        break
      case BUILTIN_LAYOUT_ITEMS.mobileTools:
        onSelectView('detail')
        break
      default:
        // 插件 view 项 → 打开工具页对应 tab。
        onOpenPanel(item.id as SidebarPanel)
        break
    }
  }

  return (
    <Button
      key={item.id}
      type="button"
      variant="ghost"
      size="icon"
      aria-label={label}
      aria-current={active ? 'page' : undefined}
      onClick={handleClick}
      className={active ? 'text-accent' : undefined}
    >
      {Icon ? <Icon className="size-5" /> : null}
    </Button>
  )
}

/**
 * 手机端插件动态工作视图：按 viewId 从 PluginRuntime 查 view 贡献点，
 * 经 PluginView 渲染（viewParams 作为 props 传给插件组件——与桌面端
 * DockviewContainer.renderPluginView 同一渲染链）。
 */
function MobilePluginWorkView({ view: w }: { view: Extract<MobileWorkView, { kind: 'plugin' }> }) {
  const runtime = useOptionalPluginRuntime()
  const [entry, setEntry] = useState<{ pluginId: string; view: import('@/plugin-api').ViewContribution } | null>(null)

  useEffect(() => {
    if (!runtime) {
      setEntry(null)
      return
    }
    const resolve = () => {
      setEntry(runtime.listAllViews().find(({ view }) => view.id === w.viewId) ?? null)
    }
    resolve()
    const unsubscribe = runtime.subscribeViews(resolve)
    return unsubscribe
  }, [runtime, w.viewId])

  if (!entry) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-2 p-4 text-center text-sm text-text-muted">
        <div>{`视图不可用：${w.viewId}`}</div>
        <div className="text-xs">插件未激活或已被卸载</div>
      </div>
    )
  }

  return (
    <PluginView
      pluginId={entry.pluginId}
      view={entry.view}
      panelParams={{ viewParams: w.viewParams, title: w.title, viewId: w.viewId, viewKey: w.viewKey }}
    />
  )
}

function MobileDetail({
  activePanel,
  onPanelChange,
  tabManager,
  terminalManager,
}: {
  activePanel: SidebarPanel
  onPanelChange: (panel: SidebarPanel) => void
  tabManager: ReturnType<typeof useTabManager>
  terminalManager: ReturnType<typeof useTerminal>
}) {
  const { t } = useI18n()
  const pluginPanels = usePluginViewPanels('right_sidebar')
  const pluginViewsMap: Map<string, { pluginId: string; view: import('@/plugin-api').ViewContribution }> = new Map(
    pluginPanels.map((p) => [p.id, { pluginId: p.pluginId, view: p.view }]),
  )

  // 内置面板 tab + 插件 view tab（动态）：图标 + 文字 label，可横滚。
  const buttons = [
    ...PANEL_BUTTONS.map((p) => ({ panel: p.panel, icon: p.icon, label: t(p.labelKey) })),
    ...pluginPanels.map((p) => ({ panel: p.id, icon: pluginIcon(p.view.icon), label: p.title })),
  ]

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div role="tablist" className="flex shrink-0 items-center gap-1 overflow-x-auto border-b border-border px-2 py-1.5 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
        {buttons.map(({ panel, icon: Icon, label }) => {
          const active = activePanel === panel
          return (
            <button
              key={panel}
              type="button"
              role="tab"
              aria-selected={active}
              onClick={() => onPanelChange(panel)}
              className={cn(
                'flex h-9 shrink-0 select-none items-center gap-1.5 rounded-full px-3.5 text-sm font-medium transition-all active:scale-95',
                active
                  ? 'bg-accent/15 text-accent'
                  : 'text-text-secondary active:bg-bg-tertiary',
              )}
            >
              <Icon className="size-4 shrink-0" />
              <span className="whitespace-nowrap">{label}</span>
            </button>
          )
        })}
      </div>
      <div className="min-h-0 flex-1 overflow-hidden">
        {renderMobilePanel(activePanel, tabManager, pluginViewsMap, terminalManager)}
      </div>
    </div>
  )
}

function renderMobilePanel(
  panel: SidebarPanel,
  tabManager: ReturnType<typeof useTabManager>,
  pluginViews: Map<string, { pluginId: string; view: import('@/plugin-api').ViewContribution }>,
  terminalManager?: ReturnType<typeof useTerminal>,
) {
  switch (panel) {
    case 'files':
      return <FileExplorer tabManager={tabManager} />
    case 'search':
      return <FileSearch tabManager={tabManager} />
    case 'info':
      return <SessionInfo tabManager={tabManager} />
    case 'tasks':
      return <TasksPanel tabManager={tabManager} />
    case 'terminal':
      return terminalManager ? <TerminalList terminalManager={terminalManager} /> : null
    default: {
      const entry = pluginViews.get(panel)
      if (!entry) return null
      return <PluginView pluginId={entry.pluginId} view={entry.view} />
    }
  }
}
