/**
 * UI 能力实现——宿主级语义操作（toast/面板/editor tab）。不暴露 DOM。
 */
import type { DiffHandle, EditorHandle, OpenDiffTabOptions, OpenFileTabOptions, OpenViewTabOptions, UIAPI } from '@/plugin-api'
import type { ReactNode } from 'react'

import { openEditorDiffTab, openEditorFileTab, openEditorViewTab } from './editorTabs'

export interface UIServices {
  showToast(text: ReactNode, kind?: 'info' | 'success' | 'error'): void
  openPanel(container: string): void
  closePanel(container: string): void
  /** 打开主编辑区 view tab 的宿主实现（测试替身用；缺省走 editorTabs 注册器）。 */
  openViewTab?: (options: OpenViewTabOptions) => void
  /** 打开 diff tab 并返回句柄的宿主实现（缺省走 editorTabs 注册器）。 */
  openDiffTab?: (options: OpenDiffTabOptions) => DiffHandle
  /** 打开文件 tab 并返回句柄的宿主实现（缺省走 editorTabs 注册器）。 */
  openFileTab?: (path: string, opts?: OpenFileTabOptions) => EditorHandle
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
    if (this.svc.openViewTab) {
      this.svc.openViewTab(options)
      return
    }
    openEditorViewTab(options)
  }

  openFileTab(path: string, opts?: OpenFileTabOptions): EditorHandle {
    if (this.svc.openFileTab) {
      return this.svc.openFileTab(path, opts)
    }
    return openEditorFileTab(path, opts)
  }

  openDiffTab(options: OpenDiffTabOptions): DiffHandle {
    if (this.svc.openDiffTab) {
      return this.svc.openDiffTab(options)
    }
    return openEditorDiffTab(options)
  }

  async runKeybinding(_keybinding: string): Promise<void> {
    // 保留给未来：语义化快捷键执行。
  }
}
