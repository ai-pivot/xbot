/**
 * UI 能力实现——宿主级语义操作（toast/面板/editor tab）。不暴露 DOM。
 */
import type { DiffHandle, EditorHandle, OpenDiffTabOptions, OpenFileTabOptions, OpenViewTabOptions, UIAPI } from '@/plugin-api'
import type { ReactNode } from 'react'

import { openEditorDiffTab, openEditorFileTab, openEditorViewTab } from './editorTabs'
import { parseEmbedQuery } from '@/workspace/panelRequest'

/**
 * 把 diff 请求交给**原生宿主**（嵌入模式唯一出口）。
 *
 * 桥信封与原生 `core/webbridge.ets` 逐字对齐：`{v:1, id, type:'diff', payload: JSON}`；
 * `id` 必须匹配原生 `BRIDGE_ID_RE = /^[A-Za-z0-9_.:-]{1,128}$/`。
 * 返回 `false` = 没有原生桥（浏览器环境）⇒ 调用方回落 web 自己的 diff tab。
 */
function postDiffToNative(options: OpenDiffTabOptions): boolean {
  const w = window as unknown as { xbotNative?: { postMessage?: (json: string) => string } }
  const bridge = w.xbotNative
  if (!bridge || typeof bridge.postMessage !== 'function') {
    return false
  }
  try {
    bridge.postMessage(JSON.stringify({
      v: 1,
      id: `diff:${Date.now()}`,
      type: 'diff',
      payload: JSON.stringify({ path: options.path ?? '', commit: options.commit ?? '' }),
    }))
    return true
  } catch {
    return false
  }
}

/** 原生已接管渲染 ⇒ 返回空句柄（插件只可能拿它做 next/prev/close，全是无操作）。 */
function noopDiffHandle(options: OpenDiffTabOptions): DiffHandle {
  return {
    editorId: options.key ?? 'native-diff',
    nextDiff: () => false,
    prevDiff: () => false,
    setRenderSideBySide: () => false,
    setTitle: () => false,
    isVisible: () => true,
    close: () => true,
    onClose: () => undefined,
  }
}

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
    // ⛔ **通用嵌入分流**（用户 2026-10-17 要求：点 diff 直接弹**原生**文件 diff；
    //    且"不能有特殊逻辑"—— 这里按**宿主能力**分流，与插件 id 无关，所有 web 插件同一出口）。
    //    判定：`?xbot_embed=1` 且页面里有原生同步桥 `window.xbotNative.postMessage`。
    //    ⛔ 浏览器直开（非嵌入）⇒ 走原路径（web 自己的 Monaco diff tab），行为逐字节不变。
    if (parseEmbedQuery(window.location.search) && postDiffToNative(options)) {
      return noopDiffHandle(options)
    }
    if (this.svc.openDiffTab) {
      return this.svc.openDiffTab(options)
    }
    return openEditorDiffTab(options)
  }

  async runKeybinding(_keybinding: string): Promise<void> {
    // 保留给未来：语义化快捷键执行。
  }
}
