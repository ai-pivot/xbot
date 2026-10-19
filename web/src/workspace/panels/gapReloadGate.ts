/**
 * gapReloadGate —— 「出现缺口 ⇒ 权威重载会话」的**去重闸门**（纯函数）。
 *
 * ── 为什么必须有它（P0，2026-10-18 审查）──────────────────────────────────────
 * 状态机的 `gapReloadToken` 是**单调计数器**，而重载动作是 `markHistoryStale + reset +
 * reloadChat`；`reset()` 会把 token **归零**。旧实现（`AgentPanel` 内联）只比 token 数值：
 *
 *   t1: token 0 → 1 ⇒ reload（正确）
 *   t2: reset() ⇒ token 1 → 0 ⇒ `0 !== 1` ⇒ **再 reload 一次**（每次 gap 都多打一次）
 *   t3: 权威 hydration 重新算出**同一形状**的缺口 ⇒ token 0 → 1 ⇒ `1 !== 0` ⇒ **再 reload**
 *       ⇒ reset+reload **自激循环**（会话永远停在 loading 屏）
 *
 * 为什么 `liveAheadGapSig` 特别危险：`unreachableGapSig` 在 reset 后自熄（本地窗口为空 ⇒
 * 恒 `''`），而 `liveAheadGapSig` 只看「live 迭代计数器 vs 它自己的窗口」—— 权威 hydration
 * 若仍给出同样的「计数器领先」，形状**逐字节相同**地再现 ⇒ 必须靠**形状**去重，且去重基准
 * 不能随 reset 丢失。
 *
 * ── 规则 ─────────────────────────────────────────────────────────────────────
 *  · 会话 key 变化 ⇒ 只记录（新面板首帧不做重载 —— 与旧语义一致）；
 *  · `token` 变小/相等 ⇒ 已被 `reset()` 复位，**不是新缺口** ⇒ 只记录；
 *  · `token` 变大 **且** 形状 `sig` 与上次**相同** ⇒ 同一缺口（跨 reset 再现）⇒ 不重载；
 *  · `token` 变大 且 形状不同 ⇒ 新缺口（洞下界前移 / 换了一个洞）⇒ 重载。
 *  ⚠️ `sig === ''`（状态机没给形状，理论上不该发生）⇒ 退化为「token 增大就重载」，与旧行为
 *     一致（不引入新的"永不重载"死路）。
 */
export interface GapReloadSeen {
  /** 会话身份键（`channel:chatID:agentChatID`）。 */
  key: string
  /** 上一次触发（或记录）的缺口形状。 */
  sig: string
  /** 上一次观察到的 `gapReloadToken`。 */
  token: number
}

export interface GapReloadEvent {
  key: string
  token: number
  /** 缺口形状（`liveAheadGapSig || unreachableGapSig`）；'' = 无形状信息。 */
  sig: string
}

export interface GapReloadDecision {
  /** 下一次比较用的记录（**必须**存回，否则去重基准丢失）。 */
  next: GapReloadSeen
  /** 是否现在执行 `markHistoryStale + reset + reloadChat`。 */
  reload: boolean
}

/** 缺口重载去重判据（见文件头规则；无副作用）。 */
export function gateGapReload(seen: GapReloadSeen, ev: GapReloadEvent): GapReloadDecision {
  if (seen.key !== ev.key) {
    return { next: { key: ev.key, sig: ev.sig, token: ev.token }, reload: false }
  }
  if (ev.token <= seen.token) {
    // reset() 把计数器复位（或被并发 effect 观察到旧值）⇒ 不是新缺口。
    // ⚠️ 保留 sig：同形状在 reset 之后再现时仍要能认出来。
    return { next: { ...seen, token: ev.token }, reload: false }
  }
  const next: GapReloadSeen = { key: ev.key, sig: ev.sig, token: ev.token }
  if (ev.sig !== '' && ev.sig === seen.sig) {
    return { next, reload: false } // 同一缺口形状 ⇒ 已重载过（幂等，不循环）
  }
  return { next, reload: true }
}
