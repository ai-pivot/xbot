/**
 * 会话身份归属（**纯函数**）—— 插件 RPC 注入 cwd 的唯一身份来源判定。
 *
 * ── 为什么需要这条判据（用户 2026-10-17 真机 P0）──────────────────────────────
 *  症状：原生鸿蒙里点 web 版 git 插件，面板显示「当前项目不是 git 目录」，而**原生** git 面板
 *       显示正确。
 *  真因：web 插件经 plugin RPC 取 `cwd`，RPC 的会话身份读全局
 *       `window.__xbot_session__ = {channel, chatID}`（`git-fancy/shared.tsx:200`
 *       的 `resolveChat()`）。嵌入模式下这个全局由**原生宿主**在页面脚本之前注入
 *       （原生 `runJavaScriptOnDocumentStart`）。但 `useSessionStore.ts:945-949` 启动时会按
 *       **缓存的会话树**自动选中一个 `activeSession`（`isCurrent` 或第一个非 synthetic 会话），
 *       随后 `usePluginRuntimeHost` 无条件写回全局 ⇒ **覆盖宿主身份** ⇒ 插件带着**另一个会话**
 *       的 (channel, chatID) 找后端 ⇒ 后端按那个会话注入 cwd（非 repo）⇒ 面板报"不是 git 目录"。
 *
 * ── 规则 ─────────────────────────────────────────────────────────────────────
 *  · 嵌入模式（`?xbot_embed=1`）**且**宿主已注入非空身份 ⇒ **宿主权威**，web 不得覆盖；
 *  · 其余情况（浏览器直开 / 宿主没注入 / 宿主注入为空）⇒ web 自己管，行为逐字节不变。
 *
 * ⛔ 不许"两条路都写"再靠时序取胜（谁后写谁生效 ⇒ 不稳定，实测就是本 bug 的形态）。
 */
import { parseEmbedQuery } from '@/workspace/panelRequest'

export interface HostIdentity {
  channel: string
  chatID: string
}

export interface SessionIdentityInput {
  /** 宿主注入的 `window.__xbot_session__`（可能为 null / 空 chatID）。 */
  hostIdentity: HostIdentity | null
  /** web 会话 store 的 `activeSession`（可能为 null）。 */
  active: HostIdentity | null
  /** 当前页面是否嵌入模式（`parseEmbedQuery(location.search)`）。 */
  embedded: boolean
}

export interface SessionIdentityResult {
  /** 生效身份（null = 两条来源都没有 ⇒ 调用方不发射事件、不写全局）。 */
  identity: HostIdentity | null
  /** true = 宿主权威（web 不得覆盖 `window.__xbot_session__`）。 */
  hostOwns: boolean
}

/**
 * 读宿主注入的会话身份（`window.__xbot_session__`）—— **唯一**读取出口。
 *
 * ⛔ 别在别处再写 `(window as ...).__xbot_session__`：本文件是「宿主身份」的唯一真源，
 * 插件 RPC（`usePluginRuntimeHost`）与 web 会话 store（`useSessionStore` 的 activeSession）
 * 必须读**同一份**判定，否则又会退化成"两处各判、靠时序取胜"（那正是 git-fancy 报
 * 「当前项目不是 git 目录」的形态）。
 *
 * 无 `window`（单测 / SSR）/ 字段非法 ⇒ `null`（调用方按"宿主未注入"处理）。
 */
export function readHostIdentity(): HostIdentity | null {
  try {
    const w = window as unknown as { __xbot_session__?: { channel?: unknown; chatID?: unknown } }
    const raw = w.__xbot_session__
    if (!raw || typeof raw.chatID !== 'string' || raw.chatID.length === 0) {
      return null
    }
    return {
      channel: typeof raw.channel === 'string' && raw.channel.length > 0 ? raw.channel : 'web',
      chatID: raw.chatID,
    }
  } catch {
    return null
  }
}

/** 判定生效的会话身份（见文件头规则；纯函数、无副作用）。 */
export function resolveSessionIdentity(input: SessionIdentityInput): SessionIdentityResult {
  const host = input.hostIdentity
  const hostValid = host !== null && host !== undefined && typeof host.chatID === 'string' && host.chatID.length > 0
  if (input.embedded && hostValid) {
    return {
      identity: { channel: host.channel && host.channel.length > 0 ? host.channel : 'web', chatID: host.chatID },
      hostOwns: true,
    }
  }
  const active = input.active
  if (active !== null && active !== undefined && typeof active.chatID === 'string' && active.chatID.length > 0) {
    return { identity: { channel: active.channel ?? 'web', chatID: active.chatID }, hostOwns: false }
  }
  return { identity: null, hostOwns: false }
}

/**
 * 当前页面是否「宿主嵌入态」（`?xbot_embed=1`）—— 运行时便利出口。
 *
 * ⛔ 真源是 `workspace/panelRequest.ts:parseEmbedQuery`（纯函数、可单测）；这里只读
 *   `window.location.search`，⛔ 别在此另写一份判定（`xbot_embed=0` 必须是"否"）。
 */
export function isEmbeddedHostPage(): boolean {
  try {
    return parseEmbedQuery(window.location.search)
  } catch {
    return false
  }
}

/**
 * `activeSession` 的**唯一**判定（首帧初始化 + 每次会话树对账复用）。
 *
 * 为什么必须收成一个函数：`useSessionStore`（面板/SSE/REST 的数据源）与
 * `usePluginRuntimeHost`（插件 RPC 的身份）**必须同源**。此前前者按"缓存会话树自动选中"
 * 决定、后者按"宿主注入"决定 ⇒ 嵌入态一页两身份（面板显示别的会话的数据，而插件 RPC 用
 * 宿主会话）。本函数把"嵌入 + 宿主已注入 ⇒ 宿主权威"这条规则复用 `resolveSessionIdentity`
 * （同一真源），⛔ 不要在调用点再判一次。
 *
 * @param cached 缓存会话树里"按 isCurrent/第一个非 synthetic"选出的候选（非嵌入态的唯一来源）。
 * @param hostIdentity 宿主注入的身份（`readHostIdentity()`）。
 * @param embedded 是否嵌入态（`isEmbeddedHostPage()`）。
 * @returns 生效的 `{channel, chatID}`；null = 两条来源都没有。
 */
export function initialActiveSession(input: {
  cached: { channel: string; chatID: string } | null
  hostIdentity: HostIdentity | null
  embedded: boolean
}): { channel: string; chatID: string } | null {
  const verdict = resolveSessionIdentity({
    hostIdentity: input.hostIdentity,
    active: input.cached,
    embedded: input.embedded,
  })
  if (!verdict.identity) return null
  return { channel: verdict.identity.channel, chatID: verdict.identity.chatID }
}
