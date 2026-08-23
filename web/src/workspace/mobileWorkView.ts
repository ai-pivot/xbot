/**
 * mobileWorkView —— 手机端全屏工作视图单例。
 *
 * 手机端没有 Dockview workspace——所有 openTab（FileExplorer 文件点击、
 * 插件 editor-view tab）在手机上路由到这里，以全屏"工作视图"呈现
 * （MobileAppShell 的 'work' 视图渲染，顶栏返回按钮关闭）。
 *
 * 为什么模块级单例：请求方（AppShell 的 editorTabs opener、MobileAppShell
 * 的包装 tabManager）与渲染方（MobileAppShell）分属不同组件树位置，
 * 无法经 props/context 直达——与 plugin-runtime/editorTabs.ts 同模式。
 */
import { useEffect, useState } from 'react'

/** 一个手机端全屏工作视图（file = 宿主文件预览；plugin = 插件动态视图；diff = 原生 diff 编辑器）。 */
export type MobileWorkView =
  | { kind: 'file'; title: string; filePath: string }
  | {
      kind: 'plugin'
      title: string
      /** 插件 view 贡献点 id（PluginView 渲染，viewParams 作 props）。 */
      viewId: string
      viewKey?: string
      viewParams?: Record<string, unknown>
    }
  | {
      kind: 'diff'
      title: string
      diffKey?: string
      original: string
      modified: string
      diffPath?: string
      diffScope?: string
    }

let current: MobileWorkView | null = null
const listeners = new Set<(v: MobileWorkView | null) => void>()

/** 打开（或替换）手机端全屏工作视图。 */
export function pushMobileWorkView(view: MobileWorkView): void {
  current = view
  for (const fn of listeners) fn(current)
}

/** 关闭当前工作视图（返回上一级）。 */
export function closeMobileWorkView(): void {
  current = null
  for (const fn of listeners) fn(current)
}

/** 订阅当前工作视图（React hook；挂载时同步单例现值）。 */
export function useMobileWorkView(): MobileWorkView | null {
  const [view, setView] = useState<MobileWorkView | null>(current)
  useEffect(() => {
    listeners.add(setView)
    setView(current)
    return () => {
      listeners.delete(setView)
    }
  }, [])
  return view
}
