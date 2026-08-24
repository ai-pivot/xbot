/**
 * UI 能力——宿主级 UI 操作（toast/面板/折叠）。不暴露 DOM，只暴露语义操作。
 */
import type { ReactNode } from 'react'

/** 打开一个主编辑区 tab 的参数（VSCode webviewPanel 语义）。 */
export interface OpenViewTabOptions {
  /** 插件 view 贡献点 id（必须已声明，container 任意——editor tab 全宽渲染）。 */
  viewId: string
  /** tab 标题（如文件路径 / commit 短哈希）。 */
  title: string
  /** Lucide 图标名（可选）。 */
  icon?: string
  /**
   * 去重逻辑键：同 key 聚焦已有 tab，不同 key 开新 tab（VSCode 多 tab 语义）。
   * 缺省用 viewId（一个 view 只有一个 tab 实例）。
   */
  key?: string
  /** 传给 view 组件的 props（如 { path, commit }）。 */
  params?: Record<string, unknown>
}

/** 打开宿主原生 diff 编辑器的参数（VSCode DiffEditor 语义）。 */
export interface OpenDiffTabOptions {
  /** tab 标题（通常为文件名）。 */
  title: string
  /** 旧内容（左/上侧）。 */
  original: string
  /** 新内容（右/下侧）。 */
  modified: string
  /** 文件路径（宿主据此推断语法高亮语言，可选）。 */
  path?: string
  /** 去重逻辑键（同 key 聚焦已有 tab）。 */
  key?: string
  /** 范围标注（如 "commit abc1234" / "工作区"）。 */
  scope?: string
}

/** 打开宿主文件编辑器 tab 的选项（VSCode showTextDocument 语义的子集）。 */
export interface OpenFileTabOptions {
  /** tab 标题（缺省取文件名）。 */
  title?: string
  /** 去重逻辑键（同 key 聚焦已有 tab；缺省按 path）。 */
  key?: string
  /** 打开后跳转到该行（1-based，居中显示）。 */
  line?: number
  /** 打开后高亮的行范围（start ≤ end）。 */
  highlight?: { startLine: number; endLine?: number }
  /** 覆盖语法高亮语言（缺省按文件扩展名推断）。 */
  language?: string
  /** 覆盖初始视图（仅 markdown 可 preview）。缺省按扩展名。 */
  viewMode?: 'editor' | 'preview'
}

/** 文件编辑器控制句柄——打开后持续控制（实例关闭后方法变 no-op 返回 false）。 */
export interface EditorHandle {
  readonly editorId: string
  // 导航
  revealLine(line: number, opts?: { center?: boolean }): boolean
  revealRange(startLine: number, endLine: number): boolean
  setSelection(startLine: number, startCol?: number, endLine?: number, endCol?: number): boolean
  setCursorPosition(line: number, column?: number): boolean
  // 行高亮（可叠加；className 透传 monaco decoration）
  highlightLines(startLine: number, endLine?: number, opts?: { className?: string }): boolean
  clearHighlights(): boolean
  // 内容与视图（内容编辑不落盘——与用户手动编辑一致）
  getContent(): string | null
  setContent(text: string): boolean
  setLanguage(language: string): boolean
  setTitle(title: string): boolean
  setViewMode(mode: 'editor' | 'preview'): boolean
  // 生命周期
  isVisible(): boolean
  close(): boolean
  onClose(cb: () => void): void
}

/** diff 编辑器控制句柄。 */
export interface DiffHandle {
  readonly editorId: string
  nextDiff(): boolean
  prevDiff(): boolean
  /** 切换并排（side-by-side）/行内（inline）渲染。 */
  setRenderSideBySide(sideBySide: boolean): boolean
  setTitle(title: string): boolean
  isVisible(): boolean
  close(): boolean
  onClose(cb: () => void): void
}

export interface UIAPI {
  /** 顶部 toast 通知。 */
  showToast(text: ReactNode, kind?: 'info' | 'success' | 'error'): void
  /** 打开/关闭一个视图容器（对应 contributes.views 的 container）。 */
  openPanel(container: string): void
  closePanel(container: string): void
  /**
   * 在主编辑区打开一个插件 view tab（VSCode editor view 语义）。
   * 同 key 聚焦已有 tab；不同 key 各自开 tab——插件可同时打开多个
   * diff/commit 详情 tab。params 会作为 props 传给 view 组件。
   */
  openViewTab(options: OpenViewTabOptions): void
  /**
   * 打开宿主文件编辑器 tab 并返回控制句柄（VSCode showTextDocument 语义）：
   * 跳行/高亮/选区/语言/内容/视图切换持续可控；tab 关闭后方法变 no-op。
   */
  openFileTab(path: string, opts?: OpenFileTabOptions): EditorHandle
  /**
   * 打开宿主原生 diff 编辑器 tab 并返回控制句柄（Monaco DiffEditor：
   * 语法高亮 + 行级着色 + 并排/内联导航）。插件只传两侧内容，零渲染代码。
   */
  openDiffTab(options: OpenDiffTabOptions): DiffHandle
  /** 在当前会话内执行快捷键语义（保留给未来）。 */
  runKeybinding(keybinding: string): Promise<void>
}
