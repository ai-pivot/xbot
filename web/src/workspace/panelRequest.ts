/**
 * panelRequest —— 宿主（原生壳 / 外部 iframe）**直达某个内置面板**的深链协议。
 *
 * ── 为什么需要（用户定向，2026-10-14）──────────────────────────────────────────
 * 用户原话：「我要求我们原生要支持所有 web 插件…你应该支持渲染小 webview，然后代理一部分
 *   web 原生的事件」＋「文件管理也没有，终端和任务管理做的很差」。
 * ⇒ 原生的正确做法是**把 web 自己的面板（文件/搜索/信息/任务/终端）渲在 WebView 里**，
 *   而不是每个能力再写一套原生实现（那套永远追不上 web，且必然分叉）。原生侧只需要
 *   「一个 URL」即可直达 —— 本模块提供这一条 URL 协议。
 *
 * 与既有 `?xbot_view=`（`plugin-runtime/pluginViewRequest.ts`）的分工：
 *   · `xbot_view`  = **插件贡献的 editor view**（`ctx.ui.openViewTab` 那条路，动态 viewId）；
 *   · `xbot_panel` = **宿主内置面板**（`components/panel/builtinPanels.tsx` 的 `core.*`，
 *                    固定 5 个 id，见 `PANEL_DEEP_LINK_IDS`）。
 * 两者互不替代：内置面板不是插件视图，插件视图也不是面板。
 *
 * ⛔ 真源：面板 id 必须与 `layouts/MobileAppShell.tsx` 的 `PANEL_BUTTONS` 同口径
 *   （files/search/info/tasks/terminal）—— 本文件是**唯一**解析出口，别在组件里旁路判断。
 */

/** `?xbot_panel=<id>` 的查询键（宿主深链契约）。 */
export const PANEL_QUERY_KEY = 'xbot_panel'

/**
 * 允许直达的内置面板 id（白名单；⛔ 与 `MobileAppShell.PANEL_BUTTONS` 同源）。
 *
 * 为什么是白名单而不是"任意串传给 setActivePanel"：面板 id 会进
 * `panelRegistry` / `SidebarPanel` 查找，未知名会渲染出空壳（用户看到"点了没反应"的空白）。
 * 白名单 ⇒ 坏 id 一律 `null`（宿主退回默认视图），**fail-closed**。
 */
export const PANEL_DEEP_LINK_IDS: readonly string[] = ['files', 'search', 'info', 'tasks', 'terminal']

/**
 * 解析 URL 查询串 → 要直达的面板 id（无 `xbot_panel` 或不在白名单 ⇒ `null`）。**纯函数**。
 *
 * ⛔ 坏输入一律 `null`（不抛、不猜）：宿主可能拼错 id、也可能把多个参数混在一起。
 */
export function parsePanelQuery(search: string): string | null {
  if (!search || search.length < 2) {
    return null
  }
  let raw: string | null = null
  try {
    raw = new URLSearchParams(search).get(PANEL_QUERY_KEY)
  } catch {
    return null
  }
  if (!raw) {
    return null
  }
  return PANEL_DEEP_LINK_IDS.includes(raw) ? raw : null
}

/**
 * 该面板在**移动端外壳**里对应的视图（`MobileAppShell.MobileView`）；**非法 id ⇒ `null`**。
 *
 * `terminal` 面板是**终端列表**（不是某个终端会话）⇒ 与其他面板一样落 `detail`；
 * 具体某个终端会话由 `useTerminal` 的回调切到 `terminal` 视图（见 MobileAppShell）。
 * ⛔ 返回 `null` 而非"猜一个视图"：宿主传了白名单外的 id 时，宁可留在默认视图，
 *    也不要把用户丢进一个渲染不出东西的空壳（fail-closed）。
 */
export function mobileViewForPanel(panelId: string): 'detail' | null {
  return PANEL_DEEP_LINK_IDS.includes(panelId) ? 'detail' : null
}

/**
 * `?xbot_embed=1` —— 宿主**嵌入模式**开关（与 `xbot_panel` 配对使用）。
 *
 * ── 为什么需要（用户定向，2026-10-14 原话）────────────────────────────────────
 * 「**这种嵌套 web view 应该代理原 web 的导航事件，并且去掉多余的 ui**」＋
 * 「**然后嵌套 webview 不要做多余的渲染**」。
 *
 * 现状（真机截图）：原生壳把 web 整壳塞进 WebView ⇒ 屏幕上是**两层 chrome** ——
 * 原生 `‹ 文件 ⟳` 之上又出现 web 自己的 `← 工具` 头、`文件/搜索/信息/任务/终端` 页签行、
 * 底部 InfoBar、会话列表……用户看到的是"套娃"，且**为一个面板渲了整个 SPA**。
 *
 * ⇒ 契约：`xbot_embed=1` 时，宿主只挂**面板本体**（`EmbedPanelShell`），web 自己的
 *   头部/页签/InfoBar/会话列表一律**不渲染**（不是盖住，是不 mount —— 也省掉这份渲染开销）。
 *   面板内的"导航意图"（标题变化、要求打开文件…）经 `workspace/hostBridge.ts` 代理给原生壳。
 *
 * ⛔ 缺省（无 `xbot_embed`）行为**逐字节不变**：浏览器直接访问 `/` 仍是完整应用。
 */
export const EMBED_QUERY_KEY = 'xbot_embed'

/**
 * 解析 URL 查询串 → 是否处于宿主嵌入模式。**纯函数**。
 *
 * 只认字面量 `'1'`（⛔ 不用 `!!value` 的宽松判定：`?xbot_embed=0` 必须表示"否"，
 * 否则宿主拼接参数时的布尔渲染（如 `false`/`0`）会意外打开嵌入模式）。
 */
export function parseEmbedQuery(search: string): boolean {
  if (!search || search.length < 2) {
    return false
  }
  try {
    return new URLSearchParams(search).get(EMBED_QUERY_KEY) === '1'
  } catch {
    return false
  }
}
