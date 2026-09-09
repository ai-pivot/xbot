/**
 * AppShell — 布局 v4「一切皆面板」。
 *
 *   PanelDock (200–460px, collapsible) · Dockview workspace (flex-1) · FloatingLayer
 *
 * 左栏 = PanelDock（docked 面板堆叠：会话/文件/搜索/信息/任务/终端/插件面板，
 * 统一经 panelRegistry 注册 + PanelChrome 外壳）；floating 面板渲染在根容器的
 * FloatingLayer（absolute inset-0，非 body portal）。原 SessionSidebar 容器与
 * 「会话|面板」segmented 已删——面板开 tab 的 dockview 入口一并移除（panel
 * 布局 v5：原 header 中列「模型 pill + think pill」删除（将来由居中插件实现），
 * 改嵌引擎路 TopRail（min-w-0 flex-1，插件徽章只在 rail 内排布，绝不推挤内置
 * 元素）；底部状态栏行 = SWUpdateButton + BottomRailBadges + 设置。
 * header（☰ + 连接点 + 会话名 / TopRail / 上下文环 + ⚙）承担折叠与设置入口。
 */
import { Suspense, lazy, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Loader2, PanelLeft, Settings } from 'lucide-react'

import { PanelDockProvider, PanelDock, FloatingLayer } from '@/components/panel/PanelLayout'
import { TopRail, BottomRailBadges } from '@/components/panel/rails'
import { ActivityBar } from '@/components/panel/ActivityBar'
import { registerBuiltinPanels } from '@/components/panel/builtinPanels'
import type { SidebarPanel } from '@/components/sidebar/RightSidebar'
import { RightSidebarControlContext } from '@/components/sidebar/RightSidebarControl'
import { AmbienceBackground } from '@/ambience/AmbienceRoot'
import { DockviewContainer } from '@/workspace/DockviewContainer'
import { MobileAppShell } from '@/layouts/MobileAppShell'
import { useIsMobile } from '@/hooks/useIsMobile'
import { useTabManager } from '@/hooks/useTabManager'

import { registerEditorTabOpener } from '@/plugin-runtime/editorTabs'
import { pushMobileWorkView } from '@/workspace/mobileWorkView'
import { useSessionStore } from '@/hooks/useSessionStore'
import { useWSConnection } from '@/hooks/useWSConnection'
import { useI18n } from '@/providers/i18n'
import { useLayoutPersistence } from '@/hooks/useLayoutPersistence'
import { syncSettingToServer, SETTINGS_SYNCED_EVENT } from '@/lib/userSettings'
import { commands } from '@/lib/commandRouter'
import type { SettingsCategory } from '@/components/settings/SettingsDialog'

// 内置面板（core.*）注册——模块级幂等调用（同 id 覆盖，与
// registerBuiltinLayoutItems 在 App.tsx 模块级注册的模式一致）。
registerBuiltinPanels()

// SettingsDialog is only needed when the user opens settings — lazy-load it
// so its code (form components, etc.) is not on the initial render path.
const SWUpdateButton = lazy(() => import('@/components/SWUpdateButton').then(m => ({ default: m.SWUpdateButton })))
const SettingsDialog = lazy(() =>
  import('@/components/settings/SettingsDialog').then(m => ({ default: m.SettingsDialog })))

const MIN_LEFT_WIDTH = 200
const MAX_LEFT_WIDTH = 460
const LEFT_RATIO = 0.22
const LEFT_WIDTH_KEY = 'xbot:leftSidebarWidth'
const LEFT_COLLAPSED_KEY = 'xbot:leftSidebarCollapsed'

export function AppShell() {
  const isMobile = useIsMobile()
  const { t } = useI18n()
  const tabManager = useTabManager()
  const ws = useWSConnection()
  const sessionStore = useSessionStore()
  const [leftWidth, setLeftWidth] = useState(() => {
    const stored = localStorage.getItem(LEFT_WIDTH_KEY)
    if (stored) {
      const w = Number(stored)
      if (!Number.isNaN(w)) return clampLeftWidth(w)
    }
    return adaptiveLeftWidth()
  })
  const [settingsOpen, setSettingsOpen] = useState(false)
  // 命令路由指定的设置分类（xbot://settings.open?section=llm）。
  const [settingsSection, setSettingsSection] = useState<SettingsCategory | undefined>()
  const [sidebarCollapsed, setSidebarCollapsed] = useState(() => {
    try {
      return localStorage.getItem(LEFT_COLLAPSED_KEY) === '1'
    } catch {
      return false
    }
  })
  // header 上下文环：当前会话的上下文用量（get_context_usage）。
  const leftDragging = useRef(false)
  const leftUserSized = useRef(localStorage.getItem(LEFT_WIDTH_KEY) !== null)
  const leftWidthRef = useRef(leftWidth)

  // 侧栏收起状态持久化（刷新后保持）——收起时 ActivityBar 常驻，点任意图标即展开。
  useEffect(() => {
    try {
      localStorage.setItem(LEFT_COLLAPSED_KEY, sidebarCollapsed ? '1' : '0')
    } catch {
      /* storage unavailable */
    }
  }, [sidebarCollapsed])

  // Persist and restore tab layout per session (Child 5 §3).
  useLayoutPersistence(tabManager, sessionStore)

  // ── 通用命令注册（commandRouter）──────────────────────────────────────
  // 让任何 UI（引导卡、插件、深链 `xbot://settings.open?section=llm`）都能
  // 直接唤起宿主面板，而不是写一句「点击右下角齿轮」让用户自己找。
  useEffect(
    () =>
      commands.registerAll([
        {
          id: 'settings.open',
          titleKey: 'settings.title',
          category: 'navigation',
          handler: (args) => {
            setSettingsSection(args.section as SettingsCategory | undefined)
            setSettingsOpen(true)
          },
        },
        {
          id: 'sidebar.toggle',
          titleKey: 'sidebar.toggle',
          category: 'navigation',
          handler: () => setSidebarCollapsed((v) => !v),
        },
        {
          id: 'session.new',
          titleKey: 'sidebar.newSession',
          category: 'sessions',
          handler: () => {
            void sessionStore.createSession()
          },
        },
        {
          id: 'input.focus',
          titleKey: 'agent.focusInput',
          category: 'agent',
          handler: () => {
            document.querySelector<HTMLElement>('[contenteditable="true"], textarea')?.focus()
          },
        },
      ]),
    [sessionStore],
  )

  // 桥接插件 editor-view API：PluginUI.openViewTab/openFileTab（React 树外）
  // 经模块级注册器走到 tabManager.openTab（VSCode webviewPanel 语义）。
  // 手机端没有 Dockview workspace——openTab 会进 pending 队列静默丢失，
  // 改路由到全屏工作视图（MobileAppShell 的 'work' 视图渲染）。
  useEffect(() => {
    registerEditorTabOpener((input) => {
      const { type, title, icon, closable, data } = input
      if (isMobile) {
        if (type === 'file' && data?.filePath) {
          pushMobileWorkView({ kind: 'file', title, filePath: data.filePath as string })
        } else if (type === 'plugin' && data?.viewId) {
          const d = data as {
            viewId: string
            viewKey?: string
            viewParams?: Record<string, unknown>
          }
          pushMobileWorkView({
            kind: 'plugin',
            title,
            viewId: d.viewId,
            viewKey: d.viewKey,
            viewParams: d.viewParams,
          })
        } else if (type === 'diff') {
          const d = data as {
            diffKey?: string
            original?: string
            modified?: string
            diffPath?: string
            diffScope?: string
          }
          pushMobileWorkView({
            kind: 'diff',
            title,
            diffKey: d.diffKey,
            original: d.original ?? '',
            modified: d.modified ?? '',
            diffPath: d.diffPath,
            diffScope: d.diffScope,
          })
        }
        return ''
      }
      return tabManager.openTab({
        type,
        title,
        icon,
        closable: closable ?? true,
        data: data as never,
      })
    })
    return () => registerEditorTabOpener(null)
  }, [tabManager, isMobile])

  // 布局 v4：openPanel 展开对应 core.* 面板（停靠/浮动归 PanelLayout 持有，
  // 经轻量事件请求展开——面板已展开时 no-op）。
  const openPanel = useCallback((panel: SidebarPanel) => {
    window.dispatchEvent(new CustomEvent('xbot:panel-request', { detail: { id: `core.${panel}` } }))
  }, [tabManager])
  // Memoize so the context value is stable — prevents DockviewContainer's
  // ctxValue from changing on every AppShell render (e.g. sidebar toggle),
  // which would force panel.update() on ALL dockview panels.
  const rightSidebarControl = useMemo(() => ({ openPanel }), [openPanel])

  // ── header 状态条数据 ──


  // 会话名：activeSession 只带 channel/chatID，label 从会话列表匹配。

    // 布局 v4：插件面板已由 panelRegistry 统一渲染（view 贡献点自动注册，
  // 经 PanelDock/FloatingLayer 展示）——无需单独的 openTab 入口。

  const onLeftResizeStart = useCallback((e: React.PointerEvent) => {
    e.preventDefault()
    leftDragging.current = true
    document.body.style.userSelect = 'none'
  }, [])

  useEffect(() => {
    const onMove = (e: PointerEvent) => {
      if (!leftDragging.current) return
      leftUserSized.current = true
      // 布局 v2：无 ActivityBar（原 48px 补偿删除），指针 x 即侧栏右缘。
      const next = clampLeftWidth(e.clientX)
      leftWidthRef.current = Math.round(next)
      setLeftWidth(leftWidthRef.current)
    }
    const onUp = () => {
      if (!leftDragging.current) return
      leftDragging.current = false
      document.body.style.userSelect = ''
      // Persist the user-chosen width so it survives refresh.
      const w = leftWidthRef.current
      localStorage.setItem(LEFT_WIDTH_KEY, String(w))
      syncSettingToServer(LEFT_WIDTH_KEY, String(w))
    }
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
    return () => {
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
    }
  }, [])

  useEffect(() => {
    const onResize = () => {
      setLeftWidth((current) => leftUserSized.current ? clampLeftWidth(current) : adaptiveLeftWidth())
    }
    window.addEventListener('resize', onResize)
    return () => window.removeEventListener('resize', onResize)
  }, [])

  // Re-read sidebar width from localStorage when server sync updates the value.
  useEffect(() => {
    const handler = () => {
      const stored = localStorage.getItem(LEFT_WIDTH_KEY)
      if (stored) {
        const w = Number(stored)
        if (!Number.isNaN(w)) {
          leftUserSized.current = true
          setLeftWidth(clampLeftWidth(w))
        }
      }
    }
    window.addEventListener(SETTINGS_SYNCED_EVENT, handler)
    return () => window.removeEventListener(SETTINGS_SYNCED_EVENT, handler)
  }, [])

  if (isMobile) return <MobileAppShell />

  return (
    <PanelDockProvider tabManager={tabManager}>
      {/* fixed inset-0 — same iOS PWA standalone full-bleed guarantee as
          MobileAppShell (100dvh/height:100% stop at the safe area there). */}
      <div className="fixed inset-0 flex flex-col overflow-hidden bg-bg-primary text-text-primary">
      {/* Ambience 壁纸层（z:0，pointer-events:none）——第一子元素 */}
      <AmbienceBackground />
      {/* Left sidebar — 布局 v4 面板坞（docked 面板堆叠，折叠由 header ☰ 控制） */}
      

      <div className="flex min-h-0 flex-1">
      {/* Activity Bar —— 最左边缘垂直图标列（VSCode 模型）：点击图标切换左栏内容 */}
      <ActivityBar collapsed={sidebarCollapsed} onToggleCollapse={() => setSidebarCollapsed((v) => !v)} />
{!sidebarCollapsed && (
        <div
          className="relative flex h-full shrink-0 flex-col overflow-hidden"
          style={{ width: leftWidth, borderRight: '1px solid var(--border)' }}
        >
          <PanelDock />
          <div
            role="separator"
            aria-orientation="vertical"
            aria-label="Resize sessions sidebar"
            onPointerDown={onLeftResizeStart}
            className="absolute right-0 top-0 h-full w-1 cursor-col-resize bg-transparent transition-colors hover:bg-app-accent/40"
          />
          <button
            type="button"
            aria-label={t('sidebar.toggle')}
            title={t('sidebar.toggle')}
            onClick={() => setSidebarCollapsed((v) => !v)}
            className="group absolute right-0 top-1/2 z-10 flex size-5 -translate-y-1/2 items-center justify-center rounded border border-border bg-bg-elevated text-text-muted opacity-0 transition-opacity hover:bg-bg-tertiary"
            style={{ right: '-10px' }}
          >
            <PanelLeft className="size-3" />
          </button>
        </div>
      )}
      <RightSidebarControlContext.Provider value={rightSidebarControl}>
        {/* Workspace — always present (Agent tab lives here). */}
        <main className="relative flex h-full min-w-0 flex-1 flex-col">
          {/* 布局 v5 header：左 ☰ + 连接点 + 会话名（shrink-0 刚性）/
              TopRail（min-w-0 flex-1，插件徽章溢出由 rail 内部收纳，绝不推挤
              内置元素）/ 右 上下文环 + ⚙（shrink-0 刚性）。header 常驻渲染。 */}
          <DockviewContainer tabManager={tabManager} />
                  </main>
      </RightSidebarControlContext.Provider>
      </div>

      {/* 全局底栏：连接状态 + chips + TopRail + InfoBar + Badges + SW 更新 + 设置 */}
      <RightSidebarControlContext.Provider value={rightSidebarControl}><div className="relative z-10 flex h-10 min-w-0 shrink-0 items-center gap-1.5 border-t border-border px-2 text-xs" style={{ background: 'var(--bg-secondary-src)' }}>
            {/* 左：连接状态（VS Code 远程连接风格：色点+文本，含会话名） */}
            <span className="flex shrink-0 items-center gap-1.5 whitespace-nowrap" title={ws.connected ? t('layout.connected') : t('layout.connecting')}>
              <span
                className={
                  ws.connected
                    ? 'size-1.5 shrink-0 rounded-full bg-emerald-500 shadow-[0_0_6px_0_rgba(16,185,129,0.55)]'
                    : 'size-1.5 shrink-0 animate-pulse rounded-full bg-amber-500 shadow-[0_0_6px_0_rgba(245,158,11,0.55)]'
                }
              />
              <span className="text-text-muted">{ws.connected ? t('layout.connected') : t('layout.connecting')}</span>
            </span>
            <TopRail className="min-w-0 flex-1" />
            <BottomRailBadges />
            <Suspense fallback={null}>
              <SWUpdateButton />
            </Suspense>
            <button
              type="button"
              aria-label={t('layout.openSettings')}
              title={t('settings.title')}
              onClick={() => setSettingsOpen(true)}
              className="flex shrink-0 items-center rounded-md p-1.5 transition-spring hover:bg-bg-tertiary active:scale-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-app-accent/50"
              style={{ color: 'var(--text-secondary)' }}
            >
              <Settings className="size-3.5" />
            </button>
          </div>
      </RightSidebarControlContext.Provider>

      {/* Floating panel layer — 窗口内浮层（根容器内 absolute inset-0，非 body portal），
          floating 面板 pointer-events-auto，其余透明不拦截。 */}
      <FloatingLayer />

      {/* Settings dialog — slides in from the right (Spec 7 Sheet). */}
      <Suspense fallback={<div className="flex h-full items-center justify-center"><Loader2 className="size-6 animate-spin text-muted-foreground" /></div>}>
        <SettingsDialog
          open={settingsOpen}
          onOpenChange={setSettingsOpen}
          initialSection={settingsSection}
        />
      </Suspense>
      </div>
    </PanelDockProvider>
  )
}

function adaptiveLeftWidth(): number {
  if (typeof window === 'undefined') return 260
  return clampLeftWidth(window.innerWidth * LEFT_RATIO)
}

function clampLeftWidth(width: number): number {
  const viewportMax = typeof window === 'undefined' ? MAX_LEFT_WIDTH : Math.max(MIN_LEFT_WIDTH, Math.min(MAX_LEFT_WIDTH, window.innerWidth * 0.36))
  return Math.round(Math.max(MIN_LEFT_WIDTH, Math.min(viewportMax, width)))
}
