/**
 * usePluginViewPanels —— 兼容 shim（布局 v4）。
 *
 * v4「一切皆面板」后，插件 view 贡献点由 usePluginRuntimeHost 的 syncViews
 * 自动注册进 panelRegistry。本 hook 保留旧签名与旧返回形状，存量消费点
 * （MobileAppShell / PluginPanelContainer）零改动地继续工作。
 *
 * 直接从 runtime.listAllViews() 获取（不走 panelRegistry）——手机端没有
 * usePluginRuntimeHost（panelRegistry 未 populate），必须从 runtime 读。
 * 桌面端 TopRail 走 panelRegistry（不走本 hook），两者不冲突。
 *
 * 只返回有 view 贡献点的面板——纯 ctx.panels.register 的面板没有 view 对象，
 * 由 PanelDock 直接渲染（不经本 hook）。
 */
import { useEffect, useState } from 'react'

import type { ViewContainer, ViewContribution } from '@/plugin-api'
import { onLocaleChanged } from '@/i18n'
import { useOptionalPluginRuntime } from '@/plugin-runtime'
import { pluginI18nTableOf, resolvePluginText } from './i18n'

export interface PluginViewPanel {
  /** 面板唯一 id（= view.id）。 */
  id: string
  /** 所属插件 id。 */
  pluginId: string
  title: string
  container: ViewContainer
  view: ViewContribution
}

/**
 * 返回所有插件 view 面板（含内置 plugin-manager、git-info 等）。
 *
 * dynamic 视图（参数化动态视图，如 git diff / commit 详情）被过滤——它们
 * 没有静态入口，只能通过 ctx.ui.openViewTab({viewId, params}) 打开。
 *
 * 直接从 runtime.listAllViews() 获取（不走 panelRegistry）——手机端没有
 * usePluginRuntimeHost（panelRegistry 未 populate），必须从 runtime 读。
 */
export function usePluginViewPanels(container: ViewContainer): PluginViewPanel[] {
  const runtime = useOptionalPluginRuntime()
  const [panels, setPanels] = useState<PluginViewPanel[]>([])

  useEffect(() => {
    if (!runtime) {
      setPanels([])
      return
    }
    const recompute = () => {
      setPanels(
        runtime
          .listAllViews()
          .filter(({ view }) => !view.dynamic && view.container === container)
          .map(({ pluginId, view }) => ({
            id: view.id,
            pluginId,
            // view 的 title 允许是**该插件 `web.i18n` 表里的 key** —— 用该插件的表解析；
            // 内置插件（builtin:）没有表 ⇒ 其 title 已是宿主 i18n 解析后的文本，原样透传。
            title: resolvePluginText(pluginI18nTableOf(runtime.registry, pluginId), view.title) ?? view.title,
            container: view.container,
            view,
          })),
      )
    }
    recompute()
    const unsubscribe = runtime.subscribeViews?.(recompute) ?? (() => {})
    // 标题是「**按当前语言解析后**的文本」（view.title 允许是清单 web.i18n 的 key），
    // 宿主切语言后必须重跑解析 —— 否则手机工具 tab / rail 标题停在旧语言。
    const unsubscribeLocale = onLocaleChanged(recompute)
    return () => {
      unsubscribe()
      unsubscribeLocale()
    }
  }, [runtime, container])

  return panels
}

/**
 * 按 view id 取**单个**插件 view 贡献点（**跨容器**查找；未就绪 ⇒ `null`）。
 *
 * ── 为什么需要（真机实证，2026-10-15）────────────────────────────────────────────
 * 宿主嵌入态只用 `?xbot_view=<viewId>` 指定要挂的视图（原生不认识 `container`）⇒ 需要一条
 * "只要 id"的取用路径。`usePluginViewPanels(container)` 天然要求先知道容器 ⇒ 不适用。
 *
 * ⛔ 与上面 `usePluginViewPanels` 的差异：**不过滤 `dynamic`** —— 宿主是显式点名某个 view，
 *   动态视图被点名时给插件自己的空态即可（这是插件自己的语义，见 `pluginViewRequest.ts`）。
 */
export function usePluginViewById(
  viewId: string,
): { pluginId: string; view: ViewContribution } | null {
  const runtime = useOptionalPluginRuntime()
  const [entry, setEntry] = useState<{ pluginId: string; view: ViewContribution } | null>(null)

  useEffect(() => {
    if (!runtime || !viewId) {
      setEntry(null)
      return
    }
    const recompute = () => {
      setEntry(runtime.listAllViews().find(({ view }) => view.id === viewId) ?? null)
    }
    recompute()
    const unsubscribe = runtime.subscribeViews?.(recompute) ?? (() => {})
    // 切语言同样要重算（与同文件既有 hook 同纪律）。
    const unsubscribeLocale = onLocaleChanged(recompute)
    return () => {
      unsubscribe()
      unsubscribeLocale()
    }
  }, [runtime, viewId])

  return entry
}
