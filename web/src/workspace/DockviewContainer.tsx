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
import {
  createElement,
  Suspense,
  useEffect,
  useMemo,
  useRef,
  type ReactElement,
  type RefObject,
} from 'react'
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
import { BackgroundPanel } from '@/workspace/panels/BackgroundPanel'
import { FilePanel } from '@/workspace/panels/FilePanel'
import { TerminalPanel } from '@/workspace/panels/TerminalPanel'
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
import { RightSidebarControlContext, useRightSidebarControl } from '@/components/sidebar/RightSidebarControl'
import { TooltipProvider } from '@/components/ui/tooltip'
import type { PanelParams } from '@/types/tab'
import type { TabManager } from '@/hooks/useTabManager'

interface DockviewContainerProps {
  /** The tab manager that owns tab operations; its api is bound on ready. */
  tabManager: TabManager
  /** Called once dockview is ready and seeded (for App-level wiring). */
  onReady?: () => void
}

/** Registry of content components keyed by TabType. */
const CONTENT_COMPONENTS = {
  agent: AgentPanel,
  file: FilePanel,
  terminal: TerminalPanel,
  background: BackgroundPanel,
} as const

export function DockviewContainer({ tabManager, onReady }: DockviewContainerProps) {
  const hostRef = useRef<HTMLDivElement>(null)
  const apiRef = useRef<DockviewApi | null>(null)
  const seededRef = useRef(false)
  const tabManagerRef = useRef(tabManager)
  tabManagerRef.current = tabManager

  // Collect live context values from the outer tree.
  const themeValue = useTheme()
  const i18nValue = useI18n()
  const wsValue = useWSConnection()
  const cwdValue = useCwd()
  const authValue = useAuth()
  const sessionStoreValue = useSessionStore()
  const rightSidebarValue = useRightSidebarControl()

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
      openTab: tabManager.openTab,
    }),
    [themeValue, i18nValue, wsValue, cwdValue, authValue, sessionStoreValue, rightSidebarValue, tabManager.openTab],
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
      // 'always' keeps all panel content mounted in the DOM (hidden via
      // visibility instead of detached). This prevents virtual lists (e.g.
      // the message list) from collapsing to 0 height when their panel is
      // inactive — which would cause them to render 0 items and appear empty
      // when the user switches back to the tab.
      defaultRenderer: 'always',
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

    if (!seededRef.current) {
      seededRef.current = true
      // Seed the always-present Agent tab (not closable).
      mgr.openTab({
        type: 'agent',
        title: 'Agent',
        icon: 'bot',
        closable: false,
      })
      onReady?.()
    }

    return () => {
      tabManagerRef.current.bindApi(null)
      apiRef.current = null
      try { dockview.dispose() } catch { /* ignore */ }
    }
  }, [])

  return <div ref={hostRef} className="h-full w-full" />
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
                  createElement(TooltipProvider, { delayDuration: 200, children: node }),
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
    if (!Component) return
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
