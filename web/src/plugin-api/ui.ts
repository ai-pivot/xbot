/**
 * UI 能力——宿主级 UI 操作（toast/面板/折叠）。不暴露 DOM，只暴露语义操作。
 */
import type { ReactNode } from 'react'

export interface UIAPI {
  /** 顶部 toast 通知。 */
  showToast(text: ReactNode, kind?: 'info' | 'success' | 'error'): void
  /** 打开/关闭一个视图容器（对应 contributes.views 的 container）。 */
  openPanel(container: string): void
  closePanel(container: string): void
  /** 在当前会话内执行快捷键语义（保留给未来）。 */
  runKeybinding(keybinding: string): Promise<void>
}
