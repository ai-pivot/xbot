/**
 * PanelLauncher — 底部面板启动器（替代 SideChips）。
 *
 * 列出所有可用面板（内置 + 插件），点击 toggle 打开/关闭独立卡片；
 * 尾部固定设置按钮（全局设置入口，原 AppShell 全局 header 迁入）。
 *
 * 概念区分：
 *   - Panel（卡片）：独立分屏区域，不经过 tab 系统。
 *     sidebar 面板（sessions/files/search/info/tasks/terminal）用 panelManager.togglePanel。
 *   - Tab：主卡片内部的标签页，用于多会话切换。
 *     Agent 面板和插件面板用 tabManager.openTab 创建。
 *
 * 底部固定栏，位于 DockviewContainer 和 InfoBar 之间。
 */
import { MessageSquare, FolderOpen, Search, Info, CheckSquare, TerminalSquare, Settings, type LucideIcon } from 'lucide-react'

import { SWUpdateButton } from '@/components/SWUpdateButton'
import { pluginIcon } from '@/plugin-runtime/pluginIcons'
import { usePluginViewPanels } from '@/plugin-runtime/usePluginViewPanels'
import type { PanelManager } from '@/hooks/usePanelManager'
import type { TabManager } from '@/hooks/useTabManager'

interface LauncherEntry {
  id: string
  icon: LucideIcon
  label: string
  panelId?: string
  pluginId?: string
  viewId?: string
}

const SIDEBAR_PANELS: LauncherEntry[] = [
  { id: 'sessions', icon: MessageSquare, label: '会话', panelId: 'sessions' },
  { id: 'files', icon: FolderOpen, label: '文件', panelId: 'files' },
  { id: 'search', icon: Search, label: '搜索', panelId: 'search' },
  { id: 'info', icon: Info, label: '信息', panelId: 'info' },
  { id: 'tasks', icon: CheckSquare, label: '任务', panelId: 'tasks' },
  { id: 'terminal', icon: TerminalSquare, label: '终端', panelId: 'terminal' },
]

export function PanelLauncher({
  panelManager,
  tabManager,
  onOpenSettings,
}: {
  panelManager: PanelManager
  tabManager: TabManager
  /** 打开全局设置（原 AppShell 全局 header 的设置按钮迁入） */
  onOpenSettings: () => void
}) {
  const pluginPanels = usePluginViewPanels('right_sidebar')

  const handleClick = (entry: LauncherEntry) => {
    if (entry.panelId) {
      // Panel（卡片）：toggle — 已打开则关闭，未打开则创建独立分屏
      panelManager.togglePanel({
        component: 'panel',
        title: entry.label,
        params: { type: 'panel', panelId: entry.panelId, closable: true, panelKey: entry.panelId },
        direction: 'left',
        panelKey: entry.panelId,
      })
    } else if (entry.pluginId && entry.viewId) {
      // Tab：插件面板作为主卡片内部的 tab
      tabManager.openTab({
        type: 'plugin',
        title: entry.label,
        closable: true,
        data: { viewId: entry.viewId, pluginId: entry.pluginId },
      } as never)
    }
  }

  return (
    <div className="flex shrink-0 items-center gap-1 border-t border-border bg-sidebar-bg px-2 py-1">
      {SIDEBAR_PANELS.map((entry) => {
        const isOpen = panelManager.isPanelOpen(entry.panelId!)
        return (
          <button key={entry.id} type="button" title={entry.label} onClick={() => handleClick(entry)}
            className="flex size-7 items-center justify-center rounded-lg transition-colors hover:bg-accent/10"
            style={{ color: isOpen ? 'var(--accent)' : 'var(--text-secondary)', background: isOpen ? 'color-mix(in srgb, var(--accent) 12%, transparent)' : undefined }}>
            <entry.icon className="size-4" />
          </button>
        )
      })}
      {pluginPanels.map((p) => {
        const Icon = pluginIcon(p.view.icon)
        return (
          <button key={p.id} type="button" title={p.title}
            onClick={() => handleClick({ id: p.id, icon: Icon, label: p.title, pluginId: p.pluginId, viewId: p.view.id })}
            className="flex size-7 items-center justify-center rounded-lg transition-colors hover:bg-accent/10"
            style={{ color: 'var(--text-secondary)' }}>
            <Icon className="size-4" />
          </button>
        )
      })}
      {/* 右侧组：检查更新 + 设置（推到最右）。检查更新三态：检查/下载中/重启加载 */}
      <div className="ml-auto flex items-center gap-1">
        <SWUpdateButton />
        <button type="button" title="设置" aria-label="打开设置" onClick={onOpenSettings}
          className="flex size-7 items-center justify-center rounded-lg transition-colors hover:bg-accent/10"
          style={{ color: 'var(--text-secondary)' }}>
          <Settings className="size-4" />
        </button>
      </div>
    </div>
  )
}
