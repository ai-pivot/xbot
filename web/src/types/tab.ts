/**
 * Tab param types shared between useTabManager and DockviewContainer.
 *
 * `PanelParams` is what dockview hands to a panel content renderer and to a
 * custom tab header renderer (via the panel's `.params`). It carries the
 * logical tab id/type plus the domain payload (agent sessionId / file path).
 */
import type { TabType } from './shared'

export interface PanelParams {
  tabId: string
  type: TabType
  title: string
  /** Lucide icon name resolved by the TabHeader. */
  icon?: string
  sessionId?: string
  filePath?: string
  /** Frontend terminal id (TerminalSession.id) for terminal tabs. */
  terminalId?: string
  /** False suppresses the close button and blocks closeTab (agent tabs). */
  closable: boolean
  /** SubAgent role (only for agent tabs viewing a SubAgent conversation). */
  subAgentRole?: string
  /** SubAgent instance (only for agent tabs viewing a SubAgent conversation). */
  subAgentInstance?: string
  /** Parent chatID for SubAgent tabs. */
  parentChatID?: string
  /** Parent channel for SubAgent tabs. */
  parentChannel?: string
  /** Full persisted agent tenant chatID for historical SubAgent tabs. */
  agentChatID?: string
  /** Background task id for background-output tabs. */
  taskID?: string
  /** Background task command for tab title/content. */
  command?: string
  /** Session channel for background task RPCs. */
  taskChannel?: string
  /** Session chatID for background task RPCs. */
  taskChatID?: string
  /** Plugin view id（container='main' 的插件主视图 tab）。 */
  viewId?: string
  /** Plugin id（配合 viewId 定位插件视图）。 */
  pluginId?: string
  /**
   * 插件 view tab 的去重逻辑键（ctx.ui.openViewTab 传入）：同 key 聚焦
   * 已有 tab，不同 key 各开一个 tab（VSCode 多 editor tab 语义，如
   * `git-diff:src/main.go`）。缺省按 viewId 去重。
   */
  viewKey?: string
  /** 传给插件 view 组件的参数（作为 props，如 { path, commit }）。 */
  viewParams?: Record<string, unknown>
  /** 插件编辑器控制 id（editorRegistry 确定性派生；handle 方法路由到 panel）。 */
  editorId?: string
  /** openFileTab opts.line——打开后跳转行（FilePanel 初始定位）。 */
  initialLine?: number
  /** openFileTab opts.highlight——打开后高亮行范围。 */
  initialHighlight?: { startLine: number; endLine?: number }
  /** openFileTab opts.language——覆盖语法高亮语言。 */
  fileLanguage?: string
  /** openFileTab opts.viewMode——覆盖初始视图（markdown preview/editor）。 */
  fileViewMode?: 'editor' | 'preview'
  /** 原生 diff tab 的去重逻辑键（ctx.ui.openDiffTab 传入）。 */
  diffKey?: string
  /** diff 旧内容（左/上侧）。 */
  original?: string
  /** diff 新内容（右/下侧）。 */
  modified?: string
  /** diff 文件路径（语言推断 + 图标）。 */
  diffPath?: string
  /** diff 范围标注（如 "commit abc1234" / "工作区"）。 */
  diffScope?: string
  /** True when this dockview panel is the active panel. */
  active?: boolean
}
