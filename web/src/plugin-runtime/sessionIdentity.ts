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
