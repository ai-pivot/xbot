/**
 * panelRender —— **面板本体**渲染（唯一真源，不含任何外壳 chrome）。
 *
 * ── 为什么单独一份（用户定向，2026-10-14）────────────────────────────────────
 * 同 5 个内置面板有**两个**宿主外壳：
 *   ① 浏览器里的移动端外壳 `MobileAppShell`（自带 `← 工具` 头 + 页签行 + InfoBar）；
 *   ② 原生壳的嵌入模式 `EmbedPanelShell`（**只挂本体**，chrome 由原生提供 —— 用户原话
 *      「去掉多余的 ui」「不要做多余的渲染」）。
 * ⇒ 本体只有一份实现，两个外壳都从这里取；⛔ 别在任一外壳里再写一遍 switch（那必然分叉，
 *   上一次分叉就是这个仓库里"面板两套 UI"的病根）。
 *
 * ⚠️ 插件面板（`pluginViews` 命中）也在本体内渲染（`PluginView`）：嵌入模式下插件面板
 *    同样**没有** web 外壳，原生只提供边框与标题。
 */
import { FileExplorer } from '@/components/sidebar/FileExplorer'
import { FileSearch } from '@/components/sidebar/FileSearch'
import { SessionInfo } from '@/components/sidebar/SessionInfo'
import { TasksPanel } from '@/components/sidebar/TasksPanel'
import { TerminalList } from '@/components/sidebar/TerminalList'
import { PluginView } from '@/plugin-runtime/PluginView'
import type { SidebarPanel } from '@/components/sidebar/RightSidebar'

/** 插件视图条目（`usePluginViewPanels` 的产物；本模块只需这两项）。 */
export interface PluginViewRef {
  pluginId: string
  view: import('@/plugin-api').ViewContribution
}

/** `useTerminal` 的返回类型（此处只需 `TerminalList` 用到的部分 ⇒ 结构契约）。 */
type TerminalManagerLike = Parameters<typeof TerminalList>[0]['terminalManager']
/** `useTabManager` 的返回类型（各面板 `tabManager` 属性所需）。 */
type TabManagerLike = Parameters<typeof FileExplorer>[0]['tabManager']

/**
 * 渲染某个面板的**本体**（无 chrome）。未知 id ⇒ `null`（⛔ 不渲染空壳）。
 */
export function renderPanelBody(
  panel: SidebarPanel,
  tabManager: TabManagerLike,
  pluginViews: Map<string, PluginViewRef>,
  terminalManager?: TerminalManagerLike,
) {
  switch (panel) {
    case 'files':
      return <FileExplorer tabManager={tabManager} />
    case 'search':
      return <FileSearch tabManager={tabManager} />
    case 'info':
      return <SessionInfo tabManager={tabManager} />
    case 'tasks':
      return <TasksPanel tabManager={tabManager} />
    case 'terminal':
      return terminalManager ? <TerminalList terminalManager={terminalManager} /> : null
    default: {
      const entry = pluginViews.get(panel)
      if (!entry) return null
      return <PluginView pluginId={entry.pluginId} view={entry.view} />
    }
  }
}
