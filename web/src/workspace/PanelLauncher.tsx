/**
 * PanelLauncher — 底部面板启动器（替代 SideChips）。
 *
 * 列出所有可用面板（内置 + 插件），点击在 Dockview 中打开。
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

const BUILTIN_PANELS: LauncherEntry[] = [
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
      tabManager.openTab({
        type: 'panel' as never,
        title: entry.label,
        closable: true,
        data: { panelId: entry.panelId },
      } as never)
    } else if (entry.pluginId && entry.viewId) {
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
      {BUILTIN_PANELS.map((entry) => (
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
