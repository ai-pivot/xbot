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
  /** 复用宿主文件系统的 tab：在主编辑区打开一个文件预览 tab。 */
  openFileTab(path: string): void
  /**
   * 打开宿主原生 diff 编辑器 tab（VSCode DiffEditor 语义）：插件只传两侧
   * 内容，宿主负责语言推断 + Monaco 渲染——插件零渲染代码。
   */
  openDiffTab(options: OpenDiffTabOptions): void
  /** 在当前会话内执行快捷键语义（保留给未来）。 */
  runKeybinding(keybinding: string): Promise<void>
}
