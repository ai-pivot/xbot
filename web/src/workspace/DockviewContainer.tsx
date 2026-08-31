/**
 * DockviewContainer — mounts the imperative Dockview layout and bridges it
 * to React.
 *
 * `dockview` (v7) ships only a framework-agnostic core — there is no
 * `<DockviewReact>`. So we:
 *   1. create a `DockviewComponent` on a host div in a mount-once effect,
 *   2. register `createComponent`/`createTabComponent` factories that mount
 *      React (createRoot) on the dockview-provided `element`,
 *   3. hand the resulting `DockviewApi` up to the parent's `useTabManager`
 *      via `bindApi` so tab ops drive the layout,
 *   4. seed an Agent tab (always present, not closable) on first ready.
 *
 * Context bridging: dockview hands the renderer its own detached DOM element,
 * so each `createRoot` is an isolated React tree that does NOT inherit the
 * app's Context providers. We bridge all needed values through a single
 * `DockviewContext` (aggregating Theme, I18n, WS, Cwd, Auth, SessionStore)
 * so panels read from one typed source via `useDockviewContext()`.
 */
import { lazy, Suspense, createElement, useEffect, useMemo, useRef, type ReactElement, type RefObject } from 'react'
import {
  DockviewComponent,
  themeVisualStudio,
  type DockviewApi,
  type DockviewComponentOptions,
  type DockviewIDisposable,
  type GroupPanelPartInitParameters,
  type IContentRenderer,
  type ITabRenderer,
  type TabPartInitParameters,
} from 'dockview'
import { createRoot, type Root } from 'react-dom/client'

import { AgentPanel } from '@/workspace/panels/AgentPanel'
// Lazy-load heavy panels: FilePanel pulls in Monaco editor (~3-5MB),
// TerminalPanel pulls in xterm + addons (~200KB), BackgroundPanel pulls in
// xterm too (task output rendering). None is needed for the initial Agent
// tab render. BackgroundPanel previously imported statically — defeating the
// xterm lazy split (TerminalPanel's lazy design was bypassed by it).
const FilePanel = lazy(() =>
  import('@/workspace/panels/FilePanel').then(m => ({ default: m.FilePanel })))
const TerminalPanel = lazy(() =>
  import('@/workspace/panels/TerminalPanel').then(m => ({ default: m.TerminalPanel })))
const DiffPanel = lazy(() =>
  import('@/workspace/panels/DiffPanel').then(m => ({ default: m.DiffPanel })))
const BackgroundPanel = lazy(() =>
  import('@/workspace/panels/BackgroundPanel').then(m => ({ default: m.BackgroundPanel })))
import { TabHeader } from '@/workspace/TabHeader'
import {
  DockviewContext,
  type DockviewContextValue,
} from '@/workspace/types'
import { useTheme } from '@/hooks/useTheme'
import { useI18n } from '@/providers/i18n'
import { useWSConnection } from '@/providers/WSProvider'
import { useCwd } from '@/providers/CwdProvider'
import { useAuth } from '@/hooks/useAuth'
import { useSessionStore } from '@/hooks/useSessionStore'
import { ThemeContext } from '@/providers/theme'
import { I18nContext } from '@/providers/i18n'
import { WSContext } from '@/providers/WSProvider'
import { CwdContext } from '@/providers/CwdProvider'
import { AuthContext } from '@/providers/AuthProvider'
import { SessionStoreContext } from '@/hooks/useSessionStore'
import { useOptionalPluginRuntime, PluginRuntimeContext } from '@/plugin-runtime'
import { PluginView } from '@/plugin-runtime/PluginView'
import { RightSidebarControlContext, useRightSidebarControl } from '@/components/sidebar/RightSidebarControl'
import { useDockviewContext } from '@/workspace/types'
import { useTerminal } from '@/hooks/useTerminal'
import { TerminalList } from '@/components/sidebar/TerminalList'
import { FileExplorer } from '@/components/sidebar/FileExplorer'
import { FileSearch } from '@/components/sidebar/FileSearch'
import { SessionInfo } from '@/components/sidebar/SessionInfo'
import { TasksPanel } from '@/components/sidebar/TasksPanel'
import { SessionSidebar } from '@/components/session/SessionSidebar'
import { TooltipProvider } from '@/components/ui/tooltip'
import type { PanelParams } from '@/types/tab'
import type { TabManager } from '@/hooks/useTabManager'
import type { PanelManager } from '@/hooks/usePanelManager'

interface DockviewContainerProps {
  /** The tab manager that owns tab operations; its api is bound on ready. */
  tabManager: TabManager
  /** The panel manager that owns independent panel (card) operations. */
  panelManager: PanelManager
  /** Called once dockview is ready and seeded (for App-level wiring). */
  onReady?: () => void
}

/** Registry of content components keyed by TabType. */
const CONTENT_COMPONENTS = {
  agent: AgentPanel,
  file: FilePanel,
  terminal: TerminalPanel,
  background: BackgroundPanel,
  diff: DiffPanel,
  panel: PanelTabHost,
} as const

/**
 * PanelTabHost — 布局 v2：侧栏「面板」区点击后以 dockview 全高 tab 打开的
 * 内置面板宿主（原右侧栏 RightSidebar 的五个面板）。按 params.panelId 分发。
 * terminal 用 useTerminal 包装（与原 LeftTerminalPanel 相同的语义）。
 */
function PanelTabHost({ params }: { params: PanelParams }) {
  const ctx = useDockviewContext()
  const tabManager = ctx.tabManager
  const panelId = params.panelId ?? ''
  if (!tabManager) {
    return <div className="p-4 text-xs text-text-muted">面板加载中…</div>
  }
  switch (panelId) {
    case 'sessions':
      return <SessionSidebar tabManager={tabManager} />
    case 'files':
      return <FileExplorer tabManager={tabManager} />
    case 'search':
      return <FileSearch tabManager={tabManager} />
    case 'info':
      return <SessionInfo tabManager={tabManager} />
    case 'tasks':
      return <TasksPanel tabManager={tabManager} />
    case 'terminal':
      return <LeftTerminalPanel tabManager={tabManager} />
    default:
      return <div className="p-4 text-xs text-text-muted">未知面板：{panelId}</div>
  }
}

/** terminal 面板的 useTerminal 包装（仅当 terminal tab 打开时挂载）。 */
function LeftTerminalPanel({ tabManager }: { tabManager: TabManager }) {
  const terminalManager = useTerminal(tabManager)
  return <TerminalList terminalManager={terminalManager} />
}

export function DockviewContainer({ tabManager, panelManager, onReady }: DockviewContainerProps) {
  const hostRef = useRef<HTMLDivElement>(null)
  const apiRef = useRef<DockviewApi | null>(null)
  const seededRef = useRef(false)
  const tabManagerRef = useRef(tabManager)
  tabManagerRef.current = tabManager
  const panelManagerRef = useRef(panelManager)
  panelManagerRef.current = panelManager

  // Collect live context values from the outer tree.
  const themeValue = useTheme()
  const i18nValue = useI18n()
  const wsValue = useWSConnection()
  const cwdValue = useCwd()
  const authValue = useAuth()
  const sessionStoreValue = useSessionStore()
  const rightSidebarValue = useRightSidebarControl()
  // PluginRuntime for the isolated dockview roots — without this the
  // iteration UI injection point (IterationSlot → usePluginRuntime) returns
  // null inside panels, so plugin views (e.g. iteration-stats) only rendered
  // on mobile (which stays inside the PluginRuntimeProvider tree).
  const pluginRuntimeValue = useOptionalPluginRuntime()

  // Single aggregated value — new reference when any sub-value changes.
  const ctxValue = useMemo<DockviewContextValue>(
    () => ({
      theme: themeValue,
      i18n: i18nValue,
      ws: wsValue,
      cwd: cwdValue,
      auth: authValue,
      sessionStore: sessionStoreValue,
      rightSidebar: rightSidebarValue ?? { openPanel: () => undefined },
      tabManager,
      pluginRuntime: pluginRuntimeValue,
      openTab: tabManager.openTab,
    }),
    [themeValue, i18nValue, wsValue, cwdValue, authValue, sessionStoreValue, rightSidebarValue, pluginRuntimeValue, tabManager.openTab],
  )

  // Keep ctxRef in sync so isolated panel roots read the latest values.
  const ctxRef = useRef<DockviewContextValue>(ctxValue)
  ctxRef.current = ctxValue

  // Force all panels + tab headers to re-render when the aggregated
  // context value changes. Panels live in isolated React roots that don't
  // re-render when outer-tree Context values change; this bridges them.
  useEffect(() => {
    const api = apiRef.current
    if (!api) return
    for (const panel of api.panels) {
      panel.update({ params: panel.params as Record<string, unknown> })
    }
  }, [ctxValue])

  useEffect(() => {
    const host = hostRef.current
    if (!host) return

    const options: DockviewComponentOptions = {
      theme: themeVisualStudio,
      createComponent: (opts) => new ReactContentRenderer(opts.name, ctxRef),
      createTabComponent: () => new ReactTabRenderer(ctxRef),
      // Without this, dockview falls back to its built-in DefaultTab which
      // always shows an X close button regardless of our closable flag.
      defaultTabComponent: 'react',
      // Suppress the right-click context menu which has a "close" action.
      getTabContextMenuItems: () => [],
    }

    let dockview: DockviewComponent
    try {
      dockview = new DockviewComponent(host, options)
    } catch {
      return
    }
    const api: DockviewApi = (dockview as unknown as { api: DockviewApi }).api
    apiRef.current = api
    const mgr = tabManagerRef.current
    mgr.bindApi(api)
    panelManagerRef.current.bindApi(api)

    // Track active panel changes: when an agent tab becomes active, update
    // store.activeSession so the sidebar highlight + terminal/context-ring
    // follow the active tab. This replaces the old model where the single
    // agent tab followed activeSession — now each tab carries its own session.
    const offActiveChange = api.onDidActivePanelChange((e) => {
      if (!e.panel) return
      const params = e.panel.params as PanelParams | undefined
      if (!params || params.type !== 'agent') return
      // Only update for main agent tabs (not SubAgent tabs — those have their
      // own parentChatID and don't represent a main session).
      if (params.subAgentRole || params.agentChatID) return
      if (params.sessionId) {
        ctxRef.current.sessionStore.activateSession(params.sessionId, params.channel ?? 'web')
      }
    })

    if (!seededRef.current) {
      seededRef.current = true
      // Master 布局：会话列表卡片（左侧 20%）+ Agent 卡片（右侧 80%）
      const activeSession = ctxRef.current.sessionStore.activeSession
      // 1. 先创建 Agent tab（主卡片）
      const agentTabId = mgr.openTab({
        type: 'agent',
        title: activeSession?.chatID ?? 'Agent',
        icon: 'bot',
        closable: true,
        data: activeSession?.chatID ? {
          filePath: activeSession.chatID,
          channel: activeSession.channel ?? 'web',
        } : undefined,
      })
      // 2. 在 Agent 卡片左侧创建会话列表 Panel（独立卡片）
      const sessionsPanelId = panelManagerRef.current.addPanel({
        component: 'panel',
        title: '会话',
        params: { type: 'panel', panelId: 'sessions', closable: false },
        direction: 'left',
        referencePanelId: `dv-${agentTabId}`,
      })
      // 3. Master 布局比例：sessions group 设为 20% 宽度
      //    addPanel 同步触发 layout change，onDidLayoutChange 注册在 addPanel 之后会错过事件
      //    所以直接同步调 setSize（gridview 此时已完成 split 布局）
      const totalWidth = host.clientWidth
      if (totalWidth > 0) {
        const sidebarWidth = Math.max(200, Math.round(totalWidth * 0.2))
        const sp = api.getPanel(sessionsPanelId)
        if (sp?.group) {
          sp.group.api.setSize({ width: sidebarWidth })
        }
      }
      const agentPanel = api.getPanel(`dv-${agentTabId}`)
      agentPanel?.api.setActive()
      onReady?.()
    }

    return () => {
      offActiveChange.dispose()
      tabManagerRef.current.bindApi(null)
      panelManagerRef.current.bindApi(null)
      apiRef.current = null
      try { dockview.dispose() } catch { /* ignore */ }
    }
  }, [])

  // bg 层交给 AppShell 根容器的 bg-app-bg（单层）。Dockview host 加 padding
  // 让卡片间缝隙透出 Ambience 壁纸。
  return <div ref={hostRef} className="min-h-0 w-full flex-1 p-1.5" />
}

/* ── React ↔ dockview renderers ── */

/**
 * Wrap a node in the single aggregated DockviewContext for an isolated
 * React root. Panels read all context values via `useDockviewContext()`.
 *
 * Individual context providers are also included (driven by the single
 * aggregated `ctx` value) so child components that still call `useI18n()`,
 * `useTheme()`, etc. work inside the isolated root. One bridge value →
 * one force-re-render dep — the simplification over the old per-context
 * tracking.
 */
export function withDockviewProviders(node: ReactElement, ctx: DockviewContextValue): ReactElement {
  return createElement(
    DockviewContext.Provider,
    { value: ctx },
    createElement(ThemeContext.Provider, { value: ctx.theme },
      createElement(I18nContext.Provider, { value: ctx.i18n },
        createElement(WSContext.Provider, { value: ctx.ws },
          createElement(CwdContext.Provider, { value: ctx.cwd },
            createElement(AuthContext.Provider, { value: ctx.auth },
              createElement(SessionStoreContext.Provider, { value: ctx.sessionStore },
                createElement(RightSidebarControlContext.Provider, { value: ctx.rightSidebar },
                  createElement(PluginRuntimeContext.Provider, { value: ctx.pluginRuntime },
                    createElement(TooltipProvider, { delayDuration: 200, children: node }),
                  ),
                ),
              ),
            ),
          ),
        ),
      ),
    ),
  )
}

/**
 * Mounts a content panel React component on the dockview element.
 * `name` is the `component` string from addPanel, matching a TabType.
 */
export class ReactContentRenderer implements IContentRenderer {
  readonly element: HTMLElement
  private root: Root | null = null
  private params: GroupPanelPartInitParameters | null = null
  private readonly name: string
  private readonly ctxRef: RefObject<DockviewContextValue>

  constructor(name: string, ctxRef: RefObject<DockviewContextValue>) {
    this.name = name
    this.ctxRef = ctxRef
    this.element = document.createElement('div')
    this.element.className = 'h-full w-full overflow-hidden'
  }

  init(parameters: GroupPanelPartInitParameters): void {
    this.params = parameters
    this.root = createRoot(this.element)
    this.render()
  }

  /** Re-render on params update (dockview calls update() → we re-render). */
  update(): void {
    this.render()
  }

  private render(): void {
    if (!this.root || !this.params) return
    const Component = CONTENT_COMPONENTS[this.name as keyof typeof CONTENT_COMPONENTS]
    if (!Component) {
      // 插件 view（container='main'）：component name 就是 view.id，查不到内置
      // panel 时回退到插件 view —— 插件即可在主编辑区全宽渲染（editor tab）。
      this.renderPluginView()
      return
    }
    this.root.render(
      withDockviewProviders(
        <Suspense fallback={<div className="flex h-full items-center justify-center text-sm text-text-muted">Loading…</div>}>
          <Component
            params={this.params.params as PanelParams}
            api={this.params.api}
            containerApi={this.params.containerApi}
          />
        </Suspense>,
        this.ctxRef.current,
      ),
    )
  }

  /** 渲染 container='main' 的插件 view（component name === view.id）。 */
  private renderPluginView(): void {
    const runtime = this.ctxRef.current.pluginRuntime
    if (!runtime || !this.root) return
    const entry = runtime.listAllViews().find(({ view }) => view.id === this.name)
    if (!entry) return
    // 透传 panel params（含 viewParams）——动态视图（openViewTab）的参数
    // 经 props 传给插件 view 组件。
    this.root.render(
      withDockviewProviders(
        <PluginView
          pluginId={entry.pluginId}
          view={entry.view}
          panelParams={(this.params?.params ?? undefined) as PanelParams | undefined}
        />,
        this.ctxRef.current,
      ),
    )
  }

  dispose(): void {
    this.root?.unmount()
    this.root = null
    this.params = null
  }
}

/**
 * Mounts the custom TabHeader React component as the dockview tab.
 *
 * Active state is computed from `containerApi.activePanel?.id === api.id`
 * on init, then kept in sync via `onDidActivePanelChange`.
 *
 * VS theme borders (`.dv-tab` border-top, `.dv-tabs-and-actions-container`
 * border-bottom) are suppressed via inline styles on the parent elements
 * rather than CSS overrides, so no `.dv-dockview`/`.dv-tab` CSS rules
 * are needed.
 */
class ReactTabRenderer implements ITabRenderer {
  readonly element: HTMLElement
  private root: Root | null = null
  private params: TabPartInitParameters | null = null
  private activeSub: DockviewIDisposable | null = null
  private readonly ctxRef: RefObject<DockviewContextValue>

  constructor(ctxRef: RefObject<DockviewContextValue>) {
    this.ctxRef = ctxRef
    this.element = document.createElement('div')
    // Ensure the renderer element fills its .dv-tab parent and constrains content
    this.element.style.height = '100%'
    this.element.style.width = '100%'
    this.element.style.minWidth = '0'
    this.element.style.maxWidth = '100%'
    this.element.style.boxSizing = 'border-box'
    this.element.style.display = 'flex'
    this.element.style.overflow = 'hidden'
  }

  init(parameters: TabPartInitParameters): void {
    this.params = parameters
    this.root = createRoot(this.element)

    // Subscribe to active-panel changes to keep the accent bar in sync.
    const onActive = parameters.containerApi.onDidActivePanelChange
    this.activeSub = onActive((e) => {
      this.render(e.panel ? e.panel.id === this.params?.api.id : false)
    })

    // Initial active state from the dockview API.
    this.render(this.isActive())
  }

  update(): void {
    this.render(this.isActive())
  }

  /** Initial active state: this panel is active iff containerApi.activePanel is it. */
  private isActive(): boolean {
    if (!this.params) return false
    const active = this.params.containerApi.activePanel
    if (!active) return false
    return active.id === this.params.api.id
  }

  private render(isActive: boolean): void {
    if (!this.root || !this.params) return
    const panelParams = this.params.params as PanelParams
    this.root.render(
      withDockviewProviders(
        <TabHeader
          params={panelParams}
          api={this.params.api}
          isActive={isActive}
          onActivate={() => this.params?.api.setActive()}
        />,
        this.ctxRef.current,
      ),
    )
  }

  dispose(): void {
    this.activeSub?.dispose()
    this.activeSub = null
    this.root?.unmount()
    this.root = null
    this.params = null
  }
}
