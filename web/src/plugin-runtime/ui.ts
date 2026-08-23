/**
 * UI 能力实现——宿主级语义操作（toast/面板/editor tab）。不暴露 DOM。
 */
import type { OpenViewTabOptions, UIAPI } from '@/plugin-api'
import type { ReactNode } from 'react'

import { openEditorDiffTab, openEditorFileTab, openEditorViewTab } from './editorTabs'

export interface UIServices {
  showToast(text: ReactNode, kind?: 'info' | 'success' | 'error'): void
  openPanel(container: string): void
  closePanel(container: string): void
  /**
   * 打开主编辑区 tab 的宿主实现（AppShell 经 editorTabs 注册器注入）。
   * 缺省落到 editorTabs 模块级注册器——与 PluginUI 的默认注入一致，
   * 便于测试直接替换。
   */
  openViewTab?: (options: OpenViewTabOptions) => void
  /** 打开宿主原生 diff 编辑器 tab 的宿主实现（缺省走 editorTabs 注册器）。 */
  openDiffTab?: (options: import('@/plugin-api').OpenDiffTabOptions) => void
  /** 打开文件预览 tab（复用宿主文件系统 tab 机制）。 */
  openFileTab?: (path: string) => void
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

  openViewTab(options: OpenViewTabOptions): void {
    // 宿主可显式注入实现（测试替身）；缺省走模块级注册器。
    if (this.svc.openViewTab) {
      this.svc.openViewTab(options)
      return
    }
    openEditorViewTab(options)
  }

  openFileTab(path: string): void {
    if (this.svc.openFileTab) {
      this.svc.openFileTab(path)
      return
    }
    openEditorFileTab(path)
  }

  openDiffTab(options: import('@/plugin-api').OpenDiffTabOptions): void {
    if (this.svc.openDiffTab) {
      this.svc.openDiffTab(options)
      return
    }
    openEditorDiffTab(options)
  }

  async runKeybinding(_keybinding: string): Promise<void> {
    // 保留给未来：语义化快捷键执行。
  }
}
