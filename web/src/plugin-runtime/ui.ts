/**
 * UI 能力实现——宿主级语义操作（toast/面板）。不暴露 DOM。
 */
import type { UIAPI } from '@/plugin-api'
import type { ReactNode } from 'react'

export interface UIServices {
  showToast(text: ReactNode, kind?: 'info' | 'success' | 'error'): void
  openPanel(container: string): void
  closePanel(container: string): void
}

export class PluginUI implements UIAPI {
  private readonly svc: UIServices

  constructor(svc: UIServices) {
    this.svc = svc
  }

  showToast(text: ReactNode, kind: 'info' | 'success' | 'error' = 'info'): void {
    this.svc.showToast(text, kind)
  }

  openPanel(container: string): void {
    this.svc.openPanel(container)
  }

  closePanel(container: string): void {
    this.svc.closePanel(container)
  }

  async runKeybinding(_keybinding: string): Promise<void> {
    // 保留给未来：语义化快捷键执行。
  }
}
