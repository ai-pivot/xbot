/**
 * AppShell — unified three-column layout (Spec 2 + Spec 4 + Spec 6 + Spec 7).
 *
 *   ActivityBar (48px) · SessionSidebar (260px, collapsible) ·
 *   Dockview workspace (flex-1) · RightSidebar (0–280px, animated, collapsible) ·
 *   RightActivityBar (48px)
 *
 * The left ActivityBar owns session-list toggle + settings. Settings opens
 * a SettingsDialog Sheet (Spec 7) — NOT a sidebar view. The right sidebar hosts
 * file browser / search / info / tasks panels, each switchable via its
 * own RightActivityBar (Spec 6).
 */
import { Suspense, lazy, useCallback, useEffect, useMemo, useRef, useState } from 'react'

import { ActivityBar } from '@/layouts/ActivityBar'
import { SessionSidebar } from '@/components/session/SessionSidebar'
import { RightSidebar, type SidebarPanel } from '@/components/sidebar/RightSidebar'
import { RightActivityBar } from '@/components/sidebar/RightActivityBar'
import { RightSidebarControlContext } from '@/components/sidebar/RightSidebarControl'
import { DockviewContainer } from '@/workspace/DockviewContainer'
import { MobileAppShell } from '@/layouts/MobileAppShell'
import { useIsMobile } from '@/hooks/useIsMobile'
import { useTabManager } from '@/hooks/useTabManager'
import { useSessionStore } from '@/hooks/useSessionStore'
import { useLayoutPersistence } from '@/hooks/useLayoutPersistence'
import { syncSettingToServer, SETTINGS_SYNCED_EVENT } from '@/lib/userSettings'

// SettingsDialog is only needed when the user opens settings — lazy-load it
// so its code (form components, etc.) is not on the initial render path.
const SettingsDialog = lazy(() =>
  import('@/components/settings/SettingsDialog').then(m => ({ default: m.SettingsDialog })))

const MIN_LEFT_WIDTH = 200
const MAX_LEFT_WIDTH = 460
const LEFT_RATIO = 0.22
const LEFT_WIDTH_KEY = 'xbot:leftSidebarWidth'

export function AppShell() {
  const isMobile = useIsMobile()
  const tabManager = useTabManager()
  const sessionStore = useSessionStore()
  const [activePanel, setActivePanel] = useState<SidebarPanel | null>(null)
  const [leftWidth, setLeftWidth] = useState(() => {
    const stored = localStorage.getItem(LEFT_WIDTH_KEY)
    if (stored) {
      const w = Number(stored)
      if (!Number.isNaN(w)) return clampLeftWidth(w)
    }
    return adaptiveLeftWidth()
  })
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [settingsVersion, setSettingsVersion] = useState(0)
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false)
  const leftDragging = useRef(false)
  const leftUserSized = useRef(localStorage.getItem(LEFT_WIDTH_KEY) !== null)
  const leftWidthRef = useRef(leftWidth)

  // Persist and restore tab layout per session (Child 5 §3).
  useLayoutPersistence(tabManager, sessionStore)

  const togglePanel = useCallback((panel: SidebarPanel) => {
    setActivePanel((cur) => (cur === panel ? null : panel))
  }, [])

  const openPanel = useCallback((panel: SidebarPanel) => {
    setActivePanel(panel)
  }, [])
  // Memoize so the context value is stable — prevents DockviewContainer's
  // ctxValue from changing on every AppShell render (e.g. sidebar toggle),
  // which would force panel.update() on ALL dockview panels.
  const rightSidebarControl = useMemo(() => ({ openPanel }), [openPanel])

  const onLeftResizeStart = useCallback((e: React.PointerEvent) => {
    e.preventDefault()
    leftDragging.current = true
    document.body.style.userSelect = 'none'
  }, [])

  useEffect(() => {
    const onMove = (e: PointerEvent) => {
      if (!leftDragging.current) return
      leftUserSized.current = true
      const next = clampLeftWidth(e.clientX - 48)
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
    <div className="relative flex h-dvh w-full overflow-hidden bg-bg-primary text-text-primary">
      {/* Left ActivityBar */}
      <ActivityBar
        onOpenSettings={() => setSettingsOpen(true)}
        settingsVersion={settingsVersion}
        sidebarCollapsed={sidebarCollapsed}
        onToggleSidebar={() => setSidebarCollapsed((v) => !v)}
      />

      {/* Left sidebar — session list (collapsible) */}
      {!sidebarCollapsed && (
        <div
          className="relative h-full shrink-0"
          style={{ width: leftWidth, borderRight: '1px solid var(--border)' }}
        >
          <SessionSidebar tabManager={tabManager} />
          <div
            role="separator"
            aria-orientation="vertical"
            aria-label="Resize sessions sidebar"
            onPointerDown={onLeftResizeStart}
            className="absolute right-0 top-0 h-full w-1 cursor-col-resize bg-transparent transition-colors hover:bg-app-accent/40"
          />
        </div>
      )}

      <RightSidebarControlContext.Provider value={rightSidebarControl}>
        {/* Workspace — always present (Agent tab lives here). */}
        <main className="relative h-full min-w-0 flex-1">
          <DockviewContainer tabManager={tabManager} />
        </main>
      </RightSidebarControlContext.Provider>

      {/* Right sidebar overlays the workspace (like Settings dialog),
          doesn't squeeze it. Outside main so it's not constrained by it. */}
      <RightSidebar
        activePanel={activePanel}
        tabManager={tabManager}
      />

      {/* Right ActivityBar — always visible, toggles right panels. */}
      <RightActivityBar activePanel={activePanel} onTogglePanel={togglePanel} />

      {/* Settings dialog — slides in from the right (Spec 7 Sheet). */}
      <Suspense fallback={null}>
        <SettingsDialog
          open={settingsOpen}
          onOpenChange={(open) => {
            setSettingsOpen(open)
            if (!open) setSettingsVersion((v) => v + 1)
          }}
        />
      </Suspense>
    </div>
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
