/**
 * pluginViewRequest —— **宿主可触发的「打开任意插件视图」唯一入口**。
 *
 * ── 为什么必须有它（真根因，2026-10-13 跨仓定位）────────────────────────────────
 * web 侧插件 view **没有静态 URL**（`usePluginViewPanels.ts:36` 原文：没有静态入口，
 * 只能通过 `ctx.ui.openViewTab` 打开）⇒ 任何**不在 React 树内**的宿主（鸿蒙原生壳、
 * 桌面壳、外部自动化）都拿不到 runtime / ctx ⇒ 点插件只能落到**通用根页**（聊天页），
 * 看起来就是「插件都打不开」。
 *
 * ── 本模块的契约：**零插件特判** ────────────────────────────────────────────────
 * 把「打开任意插件视图」做成宿主可调用的一件事，数据全部来自插件自己的 view 贡献点
 * （`web_plugin_list` 的 `contributes.views[].id`）：
 *   · **URL 深链**：`?xbot_view=<viewId>&xbot_view_key=<k>&xbot_view_params=<json>&xbot_view_title=<t>`
 *   · **JS 钩子**：`window.__xbotOpenPluginView(viewId, key, params, title)`
 * ⇒ 以后**任何新插件**（内置 / 第三方 / web 新加）只要声明了 view 贡献点，宿主就能打开它，
 * **不需要改宿主一行代码**（这正是"为什么不该写插件特殊逻辑"的答案）。
 *
 * 请求在**打开器注册前 / 插件模块 import 完成前**都可能先到 ⇒ 这里**排队**，
 * 由两个就绪信号兑现：①`AppShell` 注册 tab 打开器之后；②`viewRegistrySync.sync()`
 * 每次重跑之后（= 插件 view 集合变化的信号）。`flush` 幂等，可反复调用。
 */
import type { OpenViewTabOptions } from '@/plugin-api'

import { hasEditorTabOpener, openEditorViewTab } from './editorTabs'

/** 待兑现的请求（只有一个：后到的覆盖先到的 —— 宿主语义是"切到某个视图"）。 */
let pending: OpenViewTabOptions | null = null
const listeners = new Set<(o: OpenViewTabOptions) => void>()

/** 请求打开一个插件视图（URL 深链 / 宿主 JS 钩子的**唯一**入口）。 */
export function requestOpenPluginView(options: OpenViewTabOptions): void {
  pending = options
  listeners.forEach((fn) => fn(options))
}

/** 取出待兑现请求（取出即清）；无 ⇒ null。 */
export function takePendingPluginView(): OpenViewTabOptions | null {
  const p = pending
  pending = null
  return p
}

/** 订阅请求（宿主挂载后立即订阅，用于"立刻尝试兑现"）。返回退订函数。 */
export function onPluginViewRequest(fn: (o: OpenViewTabOptions) => void): () => void {
  listeners.add(fn)
  return () => {
    listeners.delete(fn)
  }
}

/**
 * 尝试兑现待打开请求。**幂等**：
 * · 打开器未注册（AppShell 未挂载）或没有待办 ⇒ `false`（什么都不做，继续排队）；
 * · 兑现成功 ⇒ `true`。
 */
export function flushPluginViewRequest(): boolean {
  if (pending === null || !hasEditorTabOpener()) {
    return false
  }
  const o: OpenViewTabOptions | null = takePendingPluginView()
  if (o === null) {
    return false
  }
  openEditorViewTab(o)
  return true
}

/**
 * 解析 URL 深链参数 → 打开请求（无 `xbot_view` ⇒ null）。**纯函数**（便于单测）。
 *
 * `params` 是 JSON（插件 view 的 props，如 `{"path":"a.ts","commit":"abc"}`）；
 * 解析失败 ⇒ 不带参数打开（⛔ 不因为一个坏参数就让整个视图打不开）。
 */
export function parsePluginViewQuery(search: string): OpenViewTabOptions | null {
  if (!search || search.length < 2) {
    return null
  }
  const q = new URLSearchParams(search)
  const viewId = q.get('xbot_view')
  if (!viewId) {
    return null
  }
  const key = q.get('xbot_view_key') ?? undefined
  const title = q.get('xbot_view_title') ?? viewId
  const raw = q.get('xbot_view_params')
  let params: Record<string, unknown> | undefined
  if (raw) {
    try {
      params = JSON.parse(raw) as Record<string, unknown>
    } catch {
      params = undefined
    }
  }
  return { viewId, title, key, params }
}
