/**
 * viewRegistrySync —— 把插件 view 贡献点同步进宿主登记表（panelRegistry /
 * layoutRegistry），并在**视图集合变化 + 宿主语言变化**时重跑。
 *
 * 为什么必须订阅语言：面板/tab 的标题是「**解析后的文本**」被登记进登记表
 * （`resolvePluginText` 按当前语言解析插件清单里的 key）。只在 mount 时同步一次 ⇒
 * 语言切换后登记表里仍是旧语言的文本 ⇒ 侧栏/顶栏/tab 标题不变
 * （2026-09-19 用户实测：「改了语言插件没动态变化」）。重跑是**幂等**的：
 * 同 id 覆盖登记（`registerPanel`/`register`），用户的布局覆盖（`overrides`/
 * `order`，存在 layoutRegistry 内部状态）不受影响 —— 只更新文本。
 *
 * 单一实现：`usePluginRuntimeHost` 的 Bootstrap 与单测共用这里的
 * `createViewRegistrySync`（禁止在宿主里另写一份同步逻辑）。
 */
import { createElement, useEffect } from 'react'

import type { ViewContribution } from '@/plugin-api'
import { onLocaleChanged } from '@/i18n'

import { resolvePluginText, type PluginI18nTable } from './i18n'
import { layoutRegistry, VIEW_CONTAINER_TO_SLOT } from './layoutRegistry'
import { buildPanelDefs, panelRegistry } from './panelRegistry'
import { PluginView } from './PluginView'

/**
 * 同步所需的最小 runtime 视图（结构化子集 —— 单测可传 stub，不必构造整个
 * PluginRuntime）。
 */
export interface ViewSourceRuntime {
  listAllViews(): Array<{ pluginId: string; view: ViewContribution }>
  registry: { manifestOf(pluginId: string): { i18n?: PluginI18nTable } | undefined }
  /** 订阅 plugin view 集合变化（注册/卸载 view）。返回退订函数。 */
  subscribeViews(listener: () => void): () => void
}

export interface ViewRegistrySync {
  /** 按**当前语言**重新解析标题并登记（幂等）。 */
  sync(): void
  /** 注销本次登记的全部项（宿主卸载 / Bootstrap 清理）。 */
  dispose(): void
}

/**
 * 创建 view → 登记表 的同步器。
 *
 * 语义（与旧的内联实现逐字一致，仅抽出来 + 可重跑）：
 *  - panel 类容器 → panelRegistry（docked 语义）+ layoutRegistry（侧栏堆叠项）；
 *    徽章面板（zone top/bottom）只进 panelRegistry（非堆叠项）；
 *  - `dynamic` 视图跳过（无静态入口，只能经 ctx.ui.openViewTab 打开）；
 *  - 标题在**同步时**用该插件清单的 `web.i18n` 表解析（内置插件无表 ⇒ 原样透传，
 *    其文本由宿主 i18n 惰性给出 —— 见各内置 manifest 的 getter）。
 */
export function createViewRegistrySync(runtime: ViewSourceRuntime): ViewRegistrySync {
  const syncedPanels = new Set<string>()
  const syncedLayout = new Set<string>()

  const sync = (): void => {
    const views = runtime
      .listAllViews()
      .filter(({ view }) => !view.dynamic)
      // 面板/dock 标题允许是**该插件 `web.i18n` 表里的 key**（内置插件的 title 是
      // 宿主 i18n 解析后的文本 ⇒ 无表 ⇒ 原样透传）。在源头解析一次，panelRegistry
      // 与 layoutRegistry 两处消费点同时受益。
      .map(({ pluginId, view }) => ({
        pluginId,
        view: {
          ...view,
          title: resolvePluginText(runtime.registry.manifestOf(pluginId)?.i18n, view.title) ?? view.title,
        },
      }))
    const built = buildPanelDefs(views, (pluginId, view) =>
      createElement(PluginView, { pluginId, view }),
    )
    const currentPanelIds = new Set<string>()
    const currentLayoutIds = new Set<string>()
    for (const { def, view } of built) {
      currentPanelIds.add(def.id)
      panelRegistry.registerPanel(def)
      // 徽章面板（zone 'top'/'bottom'）不进布局栈——非侧栏堆叠项。
      if (def.location?.zone === 'side') {
        currentLayoutIds.add(def.id)
        layoutRegistry.register({
          id: def.id,
          slot: VIEW_CONTAINER_TO_SLOT[view.container] ?? 'desktop.sidebar',
          title: view.title,
          icon: view.icon,
          weight: 100, // 插件项排在内置项之后
        })
      }
    }
    // 注销已消失的项（插件卸载/热加载移除贡献点时）。
    for (const id of syncedPanels) {
      if (!currentPanelIds.has(id)) panelRegistry.unregisterPanel(id)
    }
    for (const id of syncedLayout) {
      if (!currentLayoutIds.has(id)) layoutRegistry.unregister(id)
    }
    syncedPanels.clear()
    for (const id of currentPanelIds) syncedPanels.add(id)
    syncedLayout.clear()
    for (const id of currentLayoutIds) syncedLayout.add(id)
  }

  return {
    sync,
    dispose: () => {
      for (const id of syncedPanels) panelRegistry.unregisterPanel(id)
      syncedPanels.clear()
      for (const id of syncedLayout) layoutRegistry.unregister(id)
      syncedLayout.clear()
    },
  }
}

/**
 * 宿主挂载点：view 集合变化 + **宿主语言变化**时重跑同步。
 * 语言订阅走 `@/i18n` 的唯一 seam（`onLocaleChanged`），此处不直接碰 i18next。
 */
export function usePluginViewRegistrySync(runtime: ViewSourceRuntime): void {
  useEffect(() => {
    const sync = createViewRegistrySync(runtime)
    sync.sync()
    const unsubscribeViews = runtime.subscribeViews(sync.sync)
    const unsubscribeLocale = onLocaleChanged(sync.sync)
    return () => {
      unsubscribeViews()
      unsubscribeLocale()
      sync.dispose()
    }
  }, [runtime])
}
