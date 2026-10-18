/**
 * hostBridge —— 把**面板内的导航意图**代理给宿主（原生壳 / 外部 iframe）。唯一出口。
 *
 * ── 为什么需要（用户定向，2026-10-14 原话）────────────────────────────────────
 * 「这种嵌套 web view 应该**代理原 web 的导航事件**，并且去掉多余的 ui。然后文件面板应该
 *   **代理后触发原生 ui**。」
 *
 * 嵌入模式（`?xbot_panel=<id>&xbot_embed=1`，见 `workspace/panelRequest.ts`）下，web 只渲
 * 面板本体；面板里发生的"用户想往外走"的动作（切面板、打开文件、标题变化）不再是 web 内部
 * 导航 —— 它们要**代理**给宿主，由宿主决定：改自己的 chrome 标题、开原生预览、切原生面板。
 *
 * ── 契约 ────────────────────────────────────────────────────────────────────
 * · 走**已有**的原生代理对象 `window.xbotNative.postMessage(json)`（原生壳用 ArkWeb
 *   `javaScriptProxy` 注入，见 harmomy 仓 `components/WebSurface.ets`；既有 type 有
 *   `ready`/`clipboard`/`close`/`log`）—— 本模块新增 `type:'nav'`。
 * · **消息体与原生侧同源**：`{v:1,id:'nav',type:'nav',payload:{kind,text}}`，
 *   `kind ∈ 'ready'|'title'|'openFile'`（原生侧 `core/webbridge.ets` 是同名白名单，两侧改动必须同批）。
 * · ⛔ **纯浏览器里必须能跑**（无 `window.xbotNative` ⇒ 静默 no-op）：web 是同一份产物，
 *   宿主不是必须存在的。⛔ 绝不 throw（一个坏桥不能把面板打崩）。
 */
import { parseEmbedQuery } from './panelRequest'

/** 面板内的导航意图种类（⛔ 与原生 `core/webbridge.ets:HOST_NAV_KINDS` 同源，改动必须同批）。 */
export type HostNavKind = 'ready' | 'title' | 'openFile'

export interface HostNavEvent {
  kind: HostNavKind
  /** `title` ⇒ 展示标题；`openFile` ⇒ 文件路径（宿主用它开原生预览）；`ready` ⇒ 面板 id。 */
  text: string
}

/** `window.xbotNative` 的最小结构契约（宿主注入；不存在时本模块全程静默）。 */
interface NativeHostProxy {
  postMessage?: (json: string) => unknown
}

function nativeHost(): NativeHostProxy | null {
  try {
    const w = window as unknown as { xbotNative?: NativeHostProxy }
    const h = w.xbotNative
    return h && typeof h.postMessage === 'function' ? h : null
  } catch {
    return null
  }
}

/** 宿主桥是否可用（用于"有宿主才代理、没宿主就走 web 自己的路"的分支判断）。 */
export function hasNativeHost(): boolean {
  return nativeHost() !== null
}

/**
 * 发一条消息给宿主（**唯一**底层出口）。无宿主 / 序列化失败 / 宿主抛错 ⇒ 静默返回 `false`。
 */
export function postHostMessage(type: string, payload: unknown): boolean {
  const host = nativeHost()
  if (!host || !host.postMessage) {
    return false
  }
  try {
    host.postMessage(JSON.stringify({ v: 1, id: type, type, payload }))
    return true
  } catch {
    // 宿主实现可能抛（桥未就绪 / 参数不合法）——面板不能因此崩。
    return false
  }
}

/**
 * 代理一条面板导航意图（`type:'nav'`）。返回宿主是否收到（false ⇒ 调用方可回落 web 自身行为）。
 *
 * ⛔ **只有嵌入态才代理**（P1 2026-10-18 审查）：原生壳对**完整 Web UI** 也注入同一个
 * `window.xbotNative`（`WebSurface.ets` 的 `javaScriptProxy` 无条件注册），只判「有桥」会把
 * 完整 app 里的「打开文件」也代理成原生预览 ⇒ web 自己的 tab 永远不打开（用户点文件像没反应）。
 * 判定收在**本函数内部**（一处真源）：调用点一律写成
 * `if (postHostNav(...)) return`，⛔ 别在调用点各自再判一次 `isEmbedMode()`（迟早漏一个）。
 */
export function postHostNav(ev: HostNavEvent): boolean {
  if (!isEmbedMode()) return false
  return postHostMessage('nav', { kind: ev.kind, text: ev.text })
}

/**
 * 是否处于宿主嵌入模式（`?xbot_embed=1`）。
 *
 * ⚠️ 真源是 `workspace/panelRequest.ts:parseEmbedQuery`（纯函数、可单测）；
 * 这里只是读 `window.location.search` 的**运行时便利出口**，⛔ 别在此另写一份判定逻辑。
 */
export function isEmbedMode(): boolean {
  try {
    return parseEmbedQuery(window.location.search)
  } catch {
    return false
  }
}
