import { lazy, Suspense, useMemo, useState } from 'react'
import { ArrowLeft, Loader2 } from 'lucide-react'
import { Bot, Files, Info, ListChecks, Menu, Plus, Search, Settings, SquareTerminal, Blocks } from 'lucide-react'

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
import { PluginPanelContainer } from '@/plugins/manager/PluginPanelContainer'

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
  { panel: 'plugins', icon: Blocks, labelKey: 'sidebar.plugins' },
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

  const rightSidebar = useMemo(() => ({
    openPanel: (panel: SidebarPanel) => {
      setActivePanel(panel)
      setView('detail')
    },
  }), [])

  const ctxValue = useMemo<DockviewContextValue>(() => ({
    theme,
    i18n,
    ws,
    cwd,
    auth,
    sessionStore,
    rightSidebar,
  }), [auth, cwd, i18n, rightSidebar, sessionStore, theme, ws])

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
            <Button type="button" variant="ghost" size="icon-sm" aria-label={t('session.newSession')} onClick={() => void createSession()}>
              <Plus />
            </Button>
            <Button type="button" variant="ghost" size="icon-sm" aria-label={t('settings.appearance')} onClick={() => setSettingsOpen(true)}>
              <Settings className="size-4" />
            </Button>
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

          <nav className="grid shrink-0 grid-cols-2 border-t border-border bg-bg-secondary" style={{ paddingBottom: 'var(--safe-area-bottom)', height: 'calc(3.5rem + var(--safe-area-bottom))' }}>
            <button
              type="button"
              className="flex flex-col items-center justify-center gap-0.5 text-xs"
              style={{ color: view === 'agent' ? 'var(--text-primary)' : 'var(--text-secondary)' }}
              onClick={() => setView('agent')}
            >
              <Bot className="size-5" />
              <span>会话</span>
            </button>
            <button
              type="button"
              className="flex flex-col items-center justify-center gap-0.5 text-xs"
              style={{ color: view === 'detail' || view === 'terminal' ? 'var(--text-primary)' : 'var(--text-secondary)' }}
              onClick={() => setView('detail')}
            >
              <SquareTerminal className="size-5" />
              <span>工具</span>
            </button>
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
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex shrink-0 items-center gap-1 border-b border-border px-2" style={{ paddingTop: 'var(--safe-area-top)', height: 'calc(2.5rem + var(--safe-area-top))' }}>
        {PANEL_BUTTONS.map(({ panel, icon: Icon, labelKey }) => (
          <Button
            key={panel}
            type="button"
            variant={activePanel === panel ? 'secondary' : 'ghost'}
            size="icon-sm"
            aria-label={t(labelKey)}
            onClick={() => onPanelChange(panel)}
          >
            <Icon />
          </Button>
        ))}
      </div>
      <div className="min-h-0 flex-1 overflow-hidden">
        {renderMobilePanel(activePanel, tabManager, terminalManager)}
      </div>
    </div>
  )
}

function renderMobilePanel(panel: SidebarPanel, tabManager: ReturnType<typeof useTabManager>, terminalManager?: ReturnType<typeof useTerminal>) {
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
    case 'plugins':
      return <PluginPanelContainer container="right_sidebar" />
  }
}
