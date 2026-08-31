/**
 * AppShell — 布局 v6「全卡片化」。
 *
 *   Header (固定) · Dockview workspace (flex-1, 全卡片) · PanelLauncher (底部) · InfoBar (固定)
 *
 * 所有面板（会话/文件/搜索/终端/Agent/插件）均为 Dockview 卡片，可自由分屏/拖拽/关闭。
 * 底部 PanelLauncher 列出可用面板，点击在 Dockview 中打开。Ambience 壁纸从卡片间距透出。
 */
import { Suspense, lazy, useEffect, useMemo, useState } from 'react'
import { Loader2, Settings } from 'lucide-react'

import { TopRail, BottomRailBadges } from '@/components/panel/rails'
import { PanelDockProvider } from '@/components/panel/PanelLayout'
import { registerBuiltinPanels } from '@/components/panel/builtinPanels'
import { RightSidebarControlContext } from '@/components/sidebar/RightSidebarControl'
import { InfoBar } from '@/plugins/InfoBar'
import { AmbienceBackground } from '@/ambience/AmbienceRoot'
import { DockviewContainer } from '@/workspace/DockviewContainer'
import { PanelLauncher } from '@/workspace/PanelLauncher'
import { MobileAppShell } from '@/layouts/MobileAppShell'
import { useIsMobile } from '@/hooks/useIsMobile'
import { useTabManager } from '@/hooks/useTabManager'

import { registerEditorTabOpener } from '@/plugin-runtime/editorTabs'
import { pushMobileWorkView } from '@/workspace/mobileWorkView'
import { useSessionStore } from '@/hooks/useSessionStore'
import { useWSConnection } from '@/hooks/useWSConnection'
import { useLayoutPersistence } from '@/hooks/useLayoutPersistence'
import type { ContextUsage } from '@/types/shared'

registerBuiltinPanels()

const SettingsDialog = lazy(() =>
  import('@/components/settings/SettingsDialog').then(m => ({ default: m.SettingsDialog })))

export function AppShell() {
  const isMobile = useIsMobile()
  const tabManager = useTabManager()
  const ws = useWSConnection()
  const sessionStore = useSessionStore()
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [ctxUsage, setCtxUsage] = useState<ContextUsage | null>(null)

  useLayoutPersistence(tabManager, sessionStore)

  useEffect(() => {
    registerEditorTabOpener((input) => {
      const { type, title, icon, closable, data } = input
      if (isMobile) {
        if (type === 'file' && data?.filePath) {
          pushMobileWorkView({ kind: 'file', title, filePath: data.filePath as string })
        } else if (type === 'plugin' && data?.viewId) {
          const d = data as { viewId: string; viewKey?: string; viewParams?: Record<string, unknown> }
          pushMobileWorkView({ kind: 'plugin', title, viewId: d.viewId, viewKey: d.viewKey, viewParams: d.viewParams })
        } else if (type === 'diff') {
          const d = data as { diffKey?: string; original?: string; modified?: string; diffPath?: string; diffScope?: string }
          pushMobileWorkView({ kind: 'diff', title, diffKey: d.diffKey, original: d.original ?? '', modified: d.modified ?? '', diffPath: d.diffPath, diffScope: d.diffScope })
        }
        return ''
      }
      return tabManager.openTab({ type, title, icon, closable: closable ?? true, data: data as never })
    })
    return () => registerEditorTabOpener(null)
  }, [tabManager, isMobile])

  const act = sessionStore.activeSession
  const actChannel = act?.channel ?? ''
  const actChatID = act?.chatID ?? ''
  const sessionLabel = useMemo(() => {
    if (!act) return ''
    const hit = sessionStore.sessions.find((s) => s.chatID === act.chatID && s.channel === act.channel)
    return hit?.label || act.chatID
  }, [sessionStore.sessions, act])

  useEffect(() => {
    if (!actChannel || !actChatID) { setCtxUsage(null); return }
    let cancelled = false
    ws.rpc<ContextUsage>('get_context_usage', { channel: actChannel, chat_id: actChatID })
      .then((u) => { if (!cancelled) setCtxUsage(u) })
      .catch(() => { if (!cancelled) setCtxUsage(null) })
    return () => { cancelled = true }
  }, [actChannel, actChatID, ws])

  if (isMobile) return <MobileAppShell />

  return (
    <PanelDockProvider tabManager={tabManager}>
    <RightSidebarControlContext.Provider value={{ openPanel: () => undefined }}>
      <div className="fixed inset-0 flex flex-col overflow-hidden bg-app-bg text-text-primary">
        <AmbienceBackground />
        <main className="relative flex min-h-0 flex-1 flex-col z-10">
          <header className="flex min-w-0 shrink-0 items-center gap-2 border-b border-border bg-sidebar-bg px-3 py-2 text-xs">
            <span className="flex shrink-0 items-center gap-1.5 whitespace-nowrap">
              <span className={ws.connected ? 'size-1.5 rounded-full bg-emerald-500' : 'size-1.5 animate-pulse rounded-full bg-amber-500'} />
              <span className="text-text-muted">{ws.connected ? '已连接' : '连接中…'}</span>
            </span>
            <span className="shrink-0 max-w-[200px] truncate font-medium" title={sessionLabel} style={{ color: 'var(--text-primary)' }}>
              {sessionLabel}
            </span>
            <TopRail className="min-w-0 flex-1" />
            <span className="flex shrink-0 items-center" title={ctxUsage?.max_context_tokens ? `上下文 ${ctxUsage.prompt_tokens.toLocaleString()} / ${ctxUsage.max_context_tokens.toLocaleString()} tokens` : '上下文用量'}>
              <svg width="16" height="16" viewBox="0 0 16 16" role="img" aria-label="上下文用量">
                <circle cx="8" cy="8" r="6" fill="none" stroke="var(--border)" strokeWidth="2" />
                {ctxUsage?.usage_percent != null && (
                  <circle cx="8" cy="8" r="6" fill="none" stroke="var(--accent)" strokeWidth="2" strokeLinecap="round"
                    strokeDasharray={`${(Math.max(0, Math.min(100, ctxUsage.usage_percent)) / 100) * 2 * Math.PI * 6} ${2 * Math.PI * 6}`} transform="rotate(-90 8 8)" />
                )}
              </svg>
            </span>
            <button type="button" aria-label="打开设置" title="设置" onClick={() => setSettingsOpen(true)}
              className="flex shrink-0 items-center rounded p-1 transition-colors hover:bg-surface-bg" style={{ color: 'var(--text-secondary)' }}>
              <Settings className="size-3.5" />
            </button>
          </header>
          <DockviewContainer tabManager={tabManager} />
          <PanelLauncher tabManager={tabManager} />
          <div className="flex min-w-0 shrink-0 items-stretch border-t border-border">
            <div className="min-w-0 flex-1"><InfoBar /></div>
            <div aria-hidden className="w-px shrink-0 bg-border" />
            <BottomRailBadges />
          </div>
        </main>
        <Suspense fallback={<div className="flex h-full items-center justify-center"><Loader2 className="size-6 animate-spin text-muted-foreground" /></div>}>
          <SettingsDialog open={settingsOpen} onOpenChange={setSettingsOpen} />
        </Suspense>
      </div>
    </RightSidebarControlContext.Provider>
    </PanelDockProvider>
  )
}
