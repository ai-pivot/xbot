/**
 * PanelLauncher — 底部面板启动器（替代 SideChips）。
 *
 * 列出所有可用面板（内置 + 插件），点击在 Dockview 中打开。
 * sidebar 面板（sessions/files/search 等）创建为独立 split 卡片（direction: 'left'），
 * 不加入 Agent 的 tab 栏。插件面板用 openTab（作为 tab 打开）。
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

/** sidebar 面板 → 独立 split 卡片（direction: 'left'）；插件面板 → tab */
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
  // 拿到 dockview api（通过 tabManager.bindApi 注入的引用）
  // tabManager 不暴露 api，但我们可以从 DockviewContainer 的 context 间接拿
  // 简单方案：直接从 DOM 查 dockview 实例（hack-free：用 tabManager 的 splitRight 思路）
  // 更好方案：扩展 TabManager 暴露 addPanelToSplit 方法
  // 最简方案：给 openTab 加可选 position 参数
  const handleClick = (entry: LauncherEntry) => {
    if (entry.panelId) {
      // sidebar 面板：openTab 先作为 tab 打开，再 splitRight 拆为独立卡片
      const tabId = tabManager.openTab({
        type: 'panel' as never,
        title: entry.label,
        closable: true,
        data: { panelId: entry.panelId },
      } as never)
      // splitRight 把面板从 tab 栏拆出为右侧独立卡片
      if (tabId) {
        tabManager.splitRight(tabId)
      }
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
