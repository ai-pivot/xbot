/**
 * editorRegistry —— 插件可控制的编辑器实例注册表。
 *
 * 插件经 ctx.ui.openFileTab/openDiffTab 打开编辑器后拿到 EditorHandle /
 * DiffHandle（命令式 API：跳行/高亮/语言/内容/视图切换/diff 导航）。
 * handle 与实例解耦：方法执行时实时查注册表，tab 关闭（实例卸载）后
 * 调用变为 no-op（返回 false）——插件无需关心编辑器生命周期。
 *
 * editorId 确定性派生（ed-file:<path> / ed-diff:<key>）：
 * 同一文件/同一 diff 的 id 恒定 → 重复 open 拿到同一 handle、刷新后布局
 * 恢复的 tab（params 携带 id）与 handle 天然对上，无需会话级映射。
 */
import type { DiffHandle, EditorHandle } from '@/plugin-api'

/** FilePanel 挂载时注册的控制器（Monaco 实例的受控子集）。 */
export interface EditorController {
  revealLine(line: number, center?: boolean): void
  revealRange(startLine: number, endLine: number): void
  setSelection(startLine: number, startCol: number, endLine: number, endCol: number): void
  setCursorPosition(line: number, column: number): void
  highlightLines(startLine: number, endLine: number, className?: string): void
  clearHighlights(): void
  getContent(): string | null
  setContent(text: string): void
  setLanguage(language: string): void
  setTitle(title: string): void
  setViewMode(mode: 'editor' | 'preview'): void
  close(): void
}

/** DiffPanel 挂载时注册的控制器。 */
export interface DiffController {
  nextDiff(): void
  prevDiff(): void
  setRenderSideBySide(sideBySide: boolean): void
  setTitle(title: string): void
  close(): void
}

type Entry = { controller: EditorController | DiffController; onClose: Set<() => void> }

const registry = new Map<string, Entry>()

/** 派生 file tab 的 editorId（同一路径恒定）。 */
export function editorIdForFile(path: string): string {
  return `ed-file:${path}`
}

/** 派生 diff tab 的 editorId（同一 diffKey 恒定）。 */
export function editorIdForDiff(diffKey: string): string {
  return `ed-diff:${diffKey}`
}

/** Panel 挂载时注册控制器；返回 detach（卸载时调用——广播 onClose + 移除）。 */
export function attachEditor(editorId: string, controller: EditorController | DiffController): () => void {
  registry.set(editorId, { controller, onClose: new Set() })
  return () => {
    const e = registry.get(editorId)
    if (!e || e.controller !== controller) return // 已被新实例覆盖，勿动
    for (const cb of e.onClose) {
      try {
        cb()
      } catch {
        /* 插件回调异常不影响卸载 */
      }
    }
    registry.delete(editorId)
  }
}

/** handle.close() 的执行方：detach + close 由 controller.close 自带（panel api）。 */

function withEntry<T>(editorId: string, fn: (c: T) => void): boolean {
  const e = registry.get(editorId)
  if (!e) return false
  try {
    fn(e.controller as T)
    return true
  } catch {
    return false
  }
}

function onCloseOf(editorId: string, cb: () => void): void {
  const e = registry.get(editorId)
  if (e) e.onClose.add(cb)
}

/** 构造 EditorHandle（file tab 的命令式 API；实例不在时全部 no-op）。 */
export function createEditorHandle(editorId: string): EditorHandle {
  return {
    editorId,
    revealLine: (line, o) => withEntry<EditorController>(editorId, c => c.revealLine(line, o?.center ?? true)),
    revealRange: (s, e2) => withEntry<EditorController>(editorId, c => c.revealRange(s, e2)),
    setSelection: (sl, sc, el, ec) => withEntry<EditorController>(editorId, c => c.setSelection(sl, sc ?? 1, el ?? sl, ec ?? 1)),
    setCursorPosition: (line, col) => withEntry<EditorController>(editorId, c => c.setCursorPosition(line, col ?? 1)),
    highlightLines: (s, e2, o) => withEntry<EditorController>(editorId, c => c.highlightLines(s, e2 ?? s, o?.className)),
    clearHighlights: () => withEntry<EditorController>(editorId, c => c.clearHighlights()),
    getContent: () => {
      const e = registry.get(editorId)
      if (!e) return null
      try {
        return (e.controller as EditorController).getContent()
      } catch {
        return null
      }
    },
    setContent: text => withEntry<EditorController>(editorId, c => c.setContent(text)),
    setLanguage: lang => withEntry<EditorController>(editorId, c => c.setLanguage(lang)),
    setTitle: t => withEntry<EditorController>(editorId, c => c.setTitle(t)),
    setViewMode: m => withEntry<EditorController>(editorId, c => c.setViewMode(m)),
    isVisible: () => registry.has(editorId),
    close: () => withEntry<EditorController>(editorId, c => c.close()),
    onClose: cb => onCloseOf(editorId, cb),
  }
}

/** 构造 DiffHandle（diff tab 的命令式 API）。 */
export function createDiffHandle(editorId: string): DiffHandle {
  return {
    editorId,
    nextDiff: () => withEntry<DiffController>(editorId, c => c.nextDiff()),
    prevDiff: () => withEntry<DiffController>(editorId, c => c.prevDiff()),
    setRenderSideBySide: v => withEntry<DiffController>(editorId, c => c.setRenderSideBySide(v)),
    setTitle: t => withEntry<DiffController>(editorId, c => c.setTitle(t)),
    isVisible: () => registry.has(editorId),
    close: () => withEntry<DiffController>(editorId, c => c.close()),
    onClose: cb => onCloseOf(editorId, cb),
  }
}
