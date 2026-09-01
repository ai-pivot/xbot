/**
 * AppShell — 布局 v6「全卡片化」。
 *
 *   Dockview workspace (flex-1, 全卡片) · PanelLauncher (底部)
 *
 * 所有面板（会话/文件/搜索/终端/Agent/插件）均为 Dockview 卡片，可自由分屏/拖拽/关闭。
 * 连接状态/会话名在主卡片（AgentPanel）header 内（卡片自持）；检查更新 +
 * 设置入口在 PanelLauncher 右侧组。Ambience 壁纸从卡片间距透出。
 */
import { useEffect } from 'react'

import { RightSidebarControlContext } from '@/components/sidebar/RightSidebarControl'
import { AmbienceBackground } from '@/ambience/AmbienceRoot'
import { DockviewContainer } from '@/workspace/DockviewContainer'
import { PanelLauncher } from '@/workspace/PanelLauncher'
import { MobileAppShell } from '@/layouts/MobileAppShell'
import { useIsMobile } from '@/hooks/useIsMobile'
import { useTabManager } from '@/hooks/useTabManager'
import { usePanelManager } from '@/hooks/usePanelManager'

import { registerEditorTabOpener } from '@/plugin-runtime/editorTabs'
import { pushMobileWorkView } from '@/workspace/mobileWorkView'
import { useSessionStore } from '@/hooks/useSessionStore'
import { useLayoutPersistence } from '@/hooks/useLayoutPersistence'

export function AppShell() {
  const isMobile = useIsMobile()
  const tabManager = useTabManager()
  const panelManager = usePanelManager()
  const sessionStore = useSessionStore()

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

  if (isMobile) return <MobileAppShell />

  return (
    <RightSidebarControlContext.Provider value={{ openPanel: () => undefined }}>
      <div className="fixed inset-0 flex flex-col overflow-hidden bg-app-bg text-text-primary">
        <AmbienceBackground />
        <main className="relative flex min-h-0 flex-1 flex-col z-10">
          <DockviewContainer tabManager={tabManager} panelManager={panelManager} />
          <PanelLauncher panelManager={panelManager} tabManager={tabManager} />
        </main>
      </div>
    </RightSidebarControlContext.Provider>
  )
}
