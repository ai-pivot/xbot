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
 *
 * openFileTab/openDiffTab 额外返回控制句柄（EditorHandle/DiffHandle）——
 * editorId 由 editorRegistry 确定性派生，panel 挂载时 attach 控制器，
 * handle 方法实时路由（tab 未挂载时 no-op 返回 false）。
 */

import type { DiffHandle, EditorHandle, OpenDiffTabOptions, OpenFileTabOptions, OpenViewTabOptions } from '@/plugin-api'
import { createDiffHandle, createEditorHandle, editorIdForDiff, editorIdForFile } from './editorRegistry'

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

/**
 * 复用宿主文件系统的 tab：打开文件编辑器 tab 并返回控制句柄。
 * editorId 确定性派生自 path（或显式 key）——重复打开/刷新恢复后 handle 恒有效。
 */
export function openEditorFileTab(path: string, opts?: OpenFileTabOptions): EditorHandle {
  const editorId = editorIdForFile(opts?.key ?? path)
  if (!opener) {
    console.warn('[plugin-runtime] openFileTab: editor tab opener 尚未注册（AppShell 未挂载）', path)
    return createEditorHandle(editorId) // no-op handle（isVisible=false）
  }
  opener({
    type: 'file',
    title: opts?.title ?? path.split('/').pop() ?? path,
    icon: 'file',
    closable: true,
    data: {
      filePath: path,
      editorId,
      initialLine: opts?.line,
      initialHighlight: opts?.highlight,
      fileLanguage: opts?.language,
      fileViewMode: opts?.viewMode,
    },
  })
  return createEditorHandle(editorId)
}

/**
 * 打开宿主原生 diff 编辑器 tab（VSCode DiffEditor 语义）并返回控制句柄：
 * 插件只传两侧内容，宿主负责语言推断 + Monaco 渲染 + diff 导航。
 */
export function openEditorDiffTab(options: OpenDiffTabOptions): DiffHandle {
  const diffKey = options.key ?? `diff:${options.title}`
  const editorId = editorIdForDiff(diffKey)
  if (!opener) {
    console.warn('[plugin-runtime] openDiffTab: editor tab opener 尚未注册（AppShell 未挂载）', options)
    return createDiffHandle(editorId)
  }
  opener({
    type: 'diff',
    title: options.title,
    icon: 'file-diff',
    closable: true,
    data: {
      diffKey,
      original: options.original,
      modified: options.modified,
      diffPath: options.path,
      diffScope: options.scope,
      editorId,
    },
  })
  return createDiffHandle(editorId)
}
