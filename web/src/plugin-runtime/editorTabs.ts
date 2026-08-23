/**
 * editorTabs —— 插件 editor-view 打开器注册器。
 *
 * 为什么需要模块级桥：PluginRuntime/PluginUI 是纯 TS 实例（不在 React 树内），
 * 而 tabManager 在 AppShell（React 树内）创建。插件调 ctx.ui.openViewTab()
 * 时无法直接拿到 tabManager——这里用一个模块级可变注册器解耦：
 *
 *   AppShell（挂载时）→ registerEditorTabOpener(tabManager.openTab)
 *   PluginUI.openViewTab()            → openEditorViewTab(options)
 *
 * 语义（VSCode webviewPanel 模型）：
 *   - viewId 必须是插件已声明的 view 贡献点（container 任意，渲染在主编辑区）
 *   - key 是 tab 去重逻辑键：同 key 聚焦已有 tab；不同 key 各开一个 tab
 *   - params 作为 props 传给 view 组件
 */

import type { OpenViewTabOptions } from '@/plugin-api'

export type EditorTabOpener = (
  input: {
    type: 'plugin' | 'file' | 'diff'
    title: string
    icon?: string
    closable?: boolean
    data?: Record<string, unknown>
  },
) => string

let opener: EditorTabOpener | null = null

/** AppShell 挂载时注册 tabManager.openTab（卸载时传 null 清理）。 */
export function registerEditorTabOpener(fn: EditorTabOpener | null): void {
  opener = fn
}

/**
 * 打开一个插件 view 的主编辑区 tab。返回 tab id（opener 未注册时返回空串）。
 * 校验 viewId 存在性由宿主 openTab 之后的渲染链负责（查不到 view 渲染为
 * 空面板），这里只做参数组装。
 */
export function openEditorViewTab(options: OpenViewTabOptions): string {
  if (!opener) {
    console.warn('[plugin-runtime] openViewTab: editor tab opener 尚未注册（AppShell 未挂载）', options)
    return ''
  }
  return opener({
    type: 'plugin',
    title: options.title,
    icon: options.icon,
    closable: true,
    data: {
      viewId: options.viewId,
      viewKey: options.key,
      viewParams: options.params,
    },
  })
}

/** 复用宿主文件系统的 tab：打开文件预览 tab。 */
export function openEditorFileTab(path: string): string {
  if (!opener) {
    console.warn('[plugin-runtime] openFileTab: editor tab opener 尚未注册（AppShell 未挂载）', path)
    return ''
  }
  return opener({
    type: 'file',
    title: path.split('/').pop() ?? path,
    icon: 'file',
    closable: true,
    data: { filePath: path },
  })
}

/**
 * 打开宿主原生 diff 编辑器 tab（VSCode DiffEditor 语义）：插件只传两侧
 * 内容，宿主负责语言推断 + Monaco 渲染。key 去重（同 key 聚焦已有 tab）。
 */
export function openEditorDiffTab(options: {
  title: string
  original: string
  modified: string
  /** 文件路径（语言推断用，可选）。 */
  path?: string
  /** 去重逻辑键（如 `git-diff:abc1234:src/a.go`）。 */
  key?: string
  /** 范围标注（如 "commit abc1234" / "工作区"）。 */
  scope?: string
}): string {
  if (!opener) {
    console.warn('[plugin-runtime] openDiffTab: editor tab opener 尚未注册（AppShell 未挂载）', options)
    return ''
  }
  return opener({
    type: 'diff',
    title: options.title,
    icon: 'file-diff',
    closable: true,
    data: {
      diffKey: options.key ?? `diff:${options.title}`,
      original: options.original,
      modified: options.modified,
      diffPath: options.path,
      diffScope: options.scope,
    },
  })
}
