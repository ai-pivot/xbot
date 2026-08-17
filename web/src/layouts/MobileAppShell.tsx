import { lazy, Suspense, useMemo, useState } from 'react'
import { ArrowLeft, Loader2 } from 'lucide-react'
import { Bot, Files, Info, ListChecks, Menu, Plus, Search, Settings, SquareTerminal } from 'lucide-react'

import { AgentPanel } from '@/workspace/panels/AgentPanel'
const TerminalPanel = lazy(() =>
  import('@/workspace/panels/TerminalPanel').then(m => ({ default: m.TerminalPanel })))
import { FileExplorer } from '@/components/sidebar/FileExplorer'
import { FileSearch } from '@/components/sidebar/FileSearch'
import { SessionInfo } from '@/components/sidebar/SessionInfo'
import type { SessionInfo as SessionInfoType } from '@/types/shared'
import { SessionSidebar } from '@/components/session/SessionSidebar'
import { TasksPanel } from '@/components/sidebar/TasksPanel'
import { TerminalList } from '@/components/sidebar/TerminalList'
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
import type { SidebarPanel } from '@/components/sidebar/RightSidebar'
import type { PanelProps } from '@/workspace/panels/types'

type MobileView = 'agent' | 'detail' | 'terminal'

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

  // 布局定制：底部导航 + 顶栏操作区由 slot 注册表驱动（用户可移动项到别处）。
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

  const title = subAgentView
    ? (subAgentView.subAgentRole ?? 'SubAgent')
    : sessionStore.activeSession
      ? sessionStore.sessions.find((s) => s.chatID === sessionStore.activeSession?.chatID && s.channel === sessionStore.activeSession?.channel)?.label
        ?? sessionStore.activeSession.chatID
      : 'Agent'

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

  return (
    <DockviewContext.Provider value={ctxValue}>
      <RightSidebarControlContext.Provider value={rightSidebar}>
        <div className="flex h-dvh w-full flex-col overflow-hidden bg-bg-primary text-text-primary">
          <header className="flex shrink-0 items-center gap-1 border-b border-border px-2" style={{ paddingTop: 'var(--safe-area-top)', height: 'calc(3rem + var(--safe-area-top))' }}>
            {subAgentView ? (
              <Button type="button" variant="ghost" size="icon-sm" aria-label={t('common.back')} onClick={() => setSubAgentView(null)}>
                <ArrowLeft />
              </Button>
            ) : (
              <Button type="button" variant="ghost" size="icon-sm" aria-label={t('sidebar.sessions')} onClick={() => setDrawerOpen(true)}>
                <Menu />
              </Button>
            )}
            <div className="min-w-0 flex-1 truncate text-sm font-medium">{title}</div>
            {topBarItems.map((item) => renderTopBarItem(item, {
              onCreateSession: () => void createSession(),
              onOpenSettings: () => setSettingsOpen(true),
              t,
            }))}
          </header>

          <main className="min-h-0 flex-1 overflow-hidden">
            {/* Agent 面板始终挂载（display:none 切换视图）。条件渲染卸载会
                销毁 AgentPanel 的 MessageStore/ProgressStore（useRef 随组件
                卸载丢失），切回会话时重建空 store → 依赖 fetchHistory +
                hydration 恢复，API 竞态下迭代可能少（用户报告："切换工具按钮
                再切回会话按钮，历史有可能少一些迭代"）。保持挂载则流式状态
                持续更新，切回立即显示完整历史。 */}
            <div className="h-full" style={{ display: view === 'agent' ? undefined : 'none' }}>
              <AgentPanel {...agentPanelProps} />
            </div>
            {view !== 'agent' && (
              view === 'terminal' && activeTerminalId ? (
              <div className="flex h-full flex-col">
                <div className="flex shrink-0 items-center gap-2 border-b border-border px-2" style={{ paddingTop: 'var(--safe-area-top)', height: 'calc(2.5rem + var(--safe-area-top))' }}>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon-sm"
                    aria-label="Back"
                    onClick={() => setView('detail')}
                  >
                    <ArrowLeft className="size-4" />
                  </Button>
                  <span className="text-sm font-medium">
                    {terminalManager.terminals.find((t) => t.id === activeTerminalId)?.title ?? 'Terminal'}
                  </span>
                </div>
                <div className="min-h-0 flex-1">
                  <Suspense fallback={<div className="flex h-full items-center justify-center"><Loader2 className="size-6 animate-spin text-muted-foreground" /></div>}>
                    <TerminalPanel {...mobileTerminalProps(activeTerminalId)} />
                  </Suspense>
                </div>
              </div>
              ) : (
              <MobileDetail
                activePanel={activePanel}
                onPanelChange={setActivePanel}
                tabManager={tabManager}
                terminalManager={terminalManager}
              />
              )
            )}
          </main>

          <nav className="grid shrink-0 border-t border-border bg-bg-secondary" style={{ paddingBottom: 'var(--safe-area-bottom)', height: 'calc(3.5rem + var(--safe-area-bottom))', gridTemplateColumns: `repeat(${Math.max(bottomNavItems.length, 1)}, minmax(0, 1fr))` }}>
            {bottomNavItems.map((item) => renderBottomNavItem(item, {
              view,
              onSelect: (v: MobileView) => setView(v),
              onOpenPanel: (panel: SidebarPanel) => { setActivePanel(panel); setView('detail') },
              t,
            }))}
          </nav>

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
 * 渲染底部导航的一个布局项。内置项按 id 分派到对应视图/动作；
 * 插件 view 项（如 xbot.git-fancy.panel）打开工具页对应 tab。
 */
function renderBottomNavItem(item: LayoutItem, actions: {
  view: MobileView
  onSelect: (v: MobileView) => void
  onOpenPanel: (panel: SidebarPanel) => void
  t: (k: string) => string
}) {
  const { view, onSelect, onOpenPanel, t } = actions
  const active =
    item.id === BUILTIN_LAYOUT_ITEMS.mobileAgent
      ? view === 'agent'
      : item.id === BUILTIN_LAYOUT_ITEMS.mobileTools
        ? view === 'detail' || view === 'terminal'
        : false
  const color = active ? 'var(--text-primary)' : 'var(--text-secondary)'
  const Icon = iconForItem(item)
  const label = item.labelKey ? t(item.labelKey) : item.title

  const handleClick = () => {
    switch (item.id) {
      case BUILTIN_LAYOUT_ITEMS.mobileAgent:
        onSelect('agent')
        break
      case BUILTIN_LAYOUT_ITEMS.mobileTools:
        onSelect('detail')
        break
      default:
        // 插件 view 项 → 打开工具页对应 tab。
        onOpenPanel(item.id as SidebarPanel)
        break
    }
  }

  return (
    <button
      key={item.id}
      type="button"
      className="flex flex-col items-center justify-center gap-0.5 text-xs"
      style={{ color }}
      onClick={handleClick}
    >
      {Icon ? <Icon className="size-5" /> : null}
      <span>{label}</span>
    </button>
  )
}

/** 按布局项 id 解析 lucide 图标组件（内置项 + 插件 view 图标）。 */
function iconForItem(item: LayoutItem) {
  const icons: Record<string, typeof Bot> = {
    [BUILTIN_LAYOUT_ITEMS.mobileAgent]: Bot,
    [BUILTIN_LAYOUT_ITEMS.mobileTools]: SquareTerminal,
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
 * 渲染顶栏操作区的一个布局项。内置 +/设置 按 id 分派动作；
 * 其他项（如用户把「会话」「工具」移到顶栏）渲染为图标按钮。
 */
function renderTopBarItem(item: LayoutItem, actions: {
  onCreateSession: () => void
  onOpenSettings: () => void
  t: (k: string) => string
}) {
  const { onCreateSession, onOpenSettings, t } = actions
  const Icon = iconForItem(item)
  const label = item.labelKey ? t(item.labelKey) : item.title

  const handleClick = () => {
    switch (item.id) {
      case BUILTIN_LAYOUT_ITEMS.mobileNewChat:
        onCreateSession()
        break
      case BUILTIN_LAYOUT_ITEMS.mobileSettings:
        onOpenSettings()
        break
      default:
        // 被移到顶栏的会话/工具/插件项：无顶栏动作（保持按钮形态）。
        break
    }
  }

  return (
    <Button key={item.id} type="button" variant="ghost" size="icon-sm" aria-label={label} onClick={handleClick}>
      {Icon ? <Icon className="size-4" /> : null}
    </Button>
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

  // 内置面板 tab + 插件 view tab（动态）。
  const buttons = [
    ...PANEL_BUTTONS.map((p) => ({ panel: p.panel, icon: p.icon, labelKey: p.labelKey })),
    ...pluginPanels.map((p) => ({ panel: p.id, icon: pluginIcon(p.view.icon), labelKey: p.title })),
  ]

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex shrink-0 items-center gap-1 border-b border-border px-2" style={{ paddingTop: 'var(--safe-area-top)', height: 'calc(2.5rem + var(--safe-area-top))' }}>
        {buttons.map(({ panel, icon: Icon, labelKey }) => (
          <Button
            key={panel}
            type="button"
            variant={activePanel === panel ? 'secondary' : 'ghost'}
            size="icon-sm"
            aria-label={pluginPanels.some((p) => p.id === panel) ? labelKey : t(labelKey)}
            onClick={() => onPanelChange(panel)}
          >
            <Icon />
          </Button>
        ))}
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
