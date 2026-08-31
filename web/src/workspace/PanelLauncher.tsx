/**
 * PanelLauncher — 底部面板启动器（替代 SideChips）。
 *
 * 列出所有可用面板（内置 + 插件），点击在 Dockview 中打开。
 *
 * 概念区分：
 *   - Panel（卡片）：独立分屏区域，不经过 tab 系统。
 *     sidebar 面板（sessions/files/search/info/tasks/terminal）用 addPanel 创建。
 *   - Tab：主卡片内部的标签页，用于多会话切换。
 *     Agent 面板和插件面板用 openTab 创建。
 *
 * 底部固定栏，位于 DockviewContainer 和 InfoBar 之间。
 */
import { MessageSquare, FolderOpen, Search, Info, CheckSquare, TerminalSquare, type LucideIcon } from 'lucide-react'

import { pluginIcon } from '@/plugin-runtime/pluginIcons'
import { usePluginViewPanels } from '@/plugin-runtime/usePluginViewPanels'
import type { TabManager } from '@/hooks/useTabManager'

interface LauncherEntry {
  id: string
  icon: LucideIcon
  label: string
  panelId?: string
  pluginId?: string
  viewId?: string
}

/** sidebar 面板 → 独立 Panel（addPanel + direction）；插件面板 → Tab（openTab） */
const SIDEBAR_PANELS: LauncherEntry[] = [
  { id: 'sessions', icon: MessageSquare, label: '会话', panelId: 'sessions' },
  { id: 'files', icon: FolderOpen, label: '文件', panelId: 'files' },
  { id: 'search', icon: Search, label: '搜索', panelId: 'search' },
  { id: 'info', icon: Info, label: '信息', panelId: 'info' },
  { id: 'tasks', icon: CheckSquare, label: '任务', panelId: 'tasks' },
  { id: 'terminal', icon: TerminalSquare, label: '终端', panelId: 'terminal' },
]

export function PanelLauncher({ tabManager }: { tabManager: TabManager }) {
  const pluginPanels = usePluginViewPanels('right_sidebar')

  const handleClick = (entry: LauncherEntry) => {
    if (entry.panelId) {
      // Panel（卡片）：直接用 addPanel 创建独立分屏，不经过 tab 系统
      tabManager.addPanel({
        component: 'panel',
        title: entry.label,
        params: { type: 'panel', panelId: entry.panelId, closable: true },
        direction: 'left',
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
      {SIDEBAR_PANELS.map((entry) => (
        <button key={entry.id} type="button" title={entry.label} onClick={() => handleClick(entry)}
          className="flex size-7 items-center justify-center rounded-lg transition-colors hover:bg-accent/10"
          style={{ color: 'var(--text-secondary)' }}>
          <entry.icon className="size-4" />
        </button>
      ))}
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
    </div>
  )
}
