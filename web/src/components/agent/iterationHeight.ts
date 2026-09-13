/**
 * iterationHeight.ts — 迭代块高度：**内容身份作用域**的实测缓存 + settle 语义 + 内容估算。
 *
 * 作用域 = 「会话身份 + 布局宽度」（见 `sharedIterationHeightTracker`），**不是组件实例**。
 *
 * 为什么不是纯 `turnID:iteration` 的模块级缓存（2026-09-13 用户报告「切换 session 后
 * 出现空 tool iter」）：**turnID 是每会话独立编号** —— 会话 A 的 `1084:810` 与会话 B
 * 的 `1084:810` 完全无关却会撞车；纯 key 的模块级缓存让 B 的块读到 A 的"已结算高度"
 * → 立刻被判定可冻结 → 内容卸载 → **空块**（用户现场）。⇒ 身份**必须含会话**。
 *
 * 为什么不能是"组件实例作用域"（同样来自 2026-09-13「手机上 iter 多了就卡 /
 * 开侧边栏慢 5-6 倍」的根因）：任何**扰动布局**的交互（手机端开工具页会让 AgentPanel
 * 外壳 `display:none`）会让消息行整体卸载/重挂 → 实例态随之销毁 → 返回时无实测高度
 * ⇒ 无块可冻结 ⇒ **每个迭代块的内容全部重新挂载 + markdown 全量重解析**
 * （实测 nodes 528→3708、muted 318→0）。而"同一 turn 的同一迭代块"的内容是**不变**的
 * （宽度不变 ⇒ 高度不变），所以身份的粒度必须是**内容**：重挂载必须能复用先前的
 * 实测高度与复核裁决。⇒ `sharedIterationHeightTracker(scope)`：scope 含会话 →
 * 会话间绝不串味；scope 不含实例 → 同一内容跨重挂载复用。宽度进 scope 的理由：
 * 高度不变性的前提就是宽度不变，布局宽度一变（面板挤压/旋转），旧高度一律作废。
 *
 * settle 契约（首版窗口化事故后确立，别再退化成"一次测量就冻结"）：
 *   1. `record` 只在**同值连续两次测量**（差 ≤ 2px）且间隔 ≥ `SETTLE_MS` 时标记 settled；
 *   2. **只有 settled 的高度才允许冻结内容**（窗口化卸载），且还必须通过「内容已挂载
 *      的复核确认」（见 `TurnBody` 的 `verified`）；
 *   3. 高度一变 → 立即 unsettle（必须重新稳定）；
 *   4. 估算只用于「从未渲染过」的块的显示占位，**绝不作为冻结依据**；
 *   5. ⛔ 占位高度绝不允许常数（`contain-intrinsic-size: auto 320px` 曾导致"鬼打墙"）；
 *   6. ⛔ **「没有布局的测量」永远不是测量**（2026-09-13 手机端「切换会话时正在
 *      stream 思考 → 思考之前的已提交内容整段不渲染」）：元素没有渲染盒
 *      （`display:none` → `offsetParent === null`）、或宽/高为 0、或值非有限 ——
 *      这类结果既不写缓存、也不置 settled，**更不能冒充"刚结算"去放行冻结**
 *      （`record` 返回 `settled:false`）。它在可见后由 RO 重新报告（忽略 + 重测）。
 */
import type { WebIteration } from '@/types/shared'

/** 两次一致测量之间的最小间隔（短于此不足以证明"稳定"）。 */
export const ITERATION_HEIGHT_SETTLE_MS = 200

export interface IterationHeightTracker {
  get(key: string): number | undefined
  isSettled(key: string): boolean
  /** @returns changed —— 缓存值是否变化（变化即原冻结不再可信）；settled —— 现在可否
   *  进入复核（settle 只证明"测量稳定"，**不等于**高度可信 —— 首帧瞬态/压扁态同值
   *  连续两次一致也会 settled；`layoutable === false` 时一律 `settled:false`）。 */
  record(key: string, height: number, now: number, layoutable?: boolean): { changed: boolean; settled: boolean }
  unsettle(key: string, now: number): void
  /** 冻结裁决：内容真的挂回来并被实测确认过（"settled + verified"才允许卸载内容）。 */
  isVerified(key: string): boolean
  /** 复核通过（内容实测高度 == 缓存高度）。 */
  markVerified(key: string): void
  /** 复核失败：撤销裁决并 unsettle（必须重新稳定 + 重新复核）。 */
  unverify(key: string, now: number): void
  /** 调试/测试：清空。 */
  clear(): void
}

/**
 * 建一份 tracker。**注意**：生产路径请用 `sharedIterationHeightTracker(scope)`
 * （内容身份作用域，跨重挂载复用）；本函数只用于"无作用域"的独立渲染（单测）
 * 与共享注册表内部。
 */
export function createIterationHeightTracker(): IterationHeightTracker {
  const heights = new Map<string, number>()
  const observedAt = new Map<string, number>()
  const settled = new Set<string>()
  /** 复核裁决（内容实测确认过）—— 与 settled 同生存期：高度一变即作废。 */
  const verified = new Set<string>()
  return {
    get: (key) => heights.get(key),
    isSettled: (key) => settled.has(key),
    record(key, height, now, layoutable = true) {
      // ⛔ 「没有布局的测量」不是测量：不写缓存、不结算、不冒充结算事件。
      // 无渲染盒（display:none / 已脱离文档）、宽高为 0、值非有限 —— 一律当"这次
      // 没测到"。已有的高度/结算状态保持不变（那份高度来自**上一次真实测量**，
      // 仍然可信；可见后 RO 会重新报一个真实高度并按需 unsettle）。
      if (!layoutable || !(height > 0) || !Number.isFinite(height)) {
        return { changed: false, settled: false }
      }
      const prev = heights.get(key)
      const changed = prev === undefined || Math.abs(prev - height) > 2
      if (changed) {
        heights.set(key, height)
        observedAt.set(key, now)
        settled.delete(key) // 值变了 → 必须重新稳定
        verified.delete(key) // 值变了 → 复核裁决一并作废
        return { changed: true, settled: false }
      }
      if (!settled.has(key)) {
        const at = observedAt.get(key)
        if (at !== undefined && now - at >= ITERATION_HEIGHT_SETTLE_MS) settled.add(key)
      }
      return { changed: false, settled: settled.has(key) }
    },
    unsettle(key, now) {
      settled.delete(key)
      observedAt.set(key, now)
    },
    isVerified: (key) => verified.has(key),
    markVerified(key) {
      verified.add(key)
    },
    unverify(key, now) {
      verified.delete(key)
      settled.delete(key)
      observedAt.set(key, now)
    },
    clear() {
      heights.clear()
      observedAt.clear()
      settled.clear()
      verified.clear()
    },
  }
}

// ── 内容身份作用域的共享 tracker ────────────────────────────────────────────
/**
 * scope → tracker 的注册表（跨组件实例/跨重挂载共享）。
 *
 * `scope` 由调用方按「**会话身份 + 布局宽度**」构造（`MessageList` 里是
 * `${chatKey}|${布局宽度}`）。两条硬约束：
 *   1. **必须含会话** —— 不含就会重演 `76b731de`（跨会话同 key 撞车 → 新会话读到
 *      旧会话的"已结算高度" → 立刻冻结成空块）；
 *   2. **必须含布局宽度** —— 高度不变性的前提是宽度不变；宽度一变，scope 就变，
 *      旧高度自然作废（否则会拿着旧宽度的占位高度去定新宽度的行高）。
 *
 * 有界（LRU，最多 `SHARED_TRACKER_LIMIT` 个 scope）：scope 数量 = 访问过的会话 ×
 * 其遇到的布局宽度，实际很小；上限只是防无限增长。
 */
const sharedTrackers = new Map<string, IterationHeightTracker>()
const SHARED_TRACKER_LIMIT = 8

export function sharedIterationHeightTracker(scope: string): IterationHeightTracker {
  const hit = sharedTrackers.get(scope)
  if (hit !== undefined) {
    sharedTrackers.delete(scope) // LRU：命中即刷新为最新
    sharedTrackers.set(scope, hit)
    return hit
  }
  const created = createIterationHeightTracker()
  sharedTrackers.set(scope, created)
  if (sharedTrackers.size > SHARED_TRACKER_LIMIT) {
    const oldest = sharedTrackers.keys().next().value
    if (oldest !== undefined) sharedTrackers.delete(oldest)
  }
  return created
}

/** 测试钩子：清空共享注册表（避免用例间串味）。 */
export function __resetSharedIterationHeightTrackers(): void {
  sharedTrackers.clear()
}

export function iterationHeightKey(turnID: number | undefined, iteration: number | undefined): string {
  return `${turnID ?? 0}:${iteration ?? 0}`
}

/**
 * 「内容身份作用域」是否可用：这一行的 `turnID` 在本会话内**唯一标识一个 turn**。
 *
 * 与 `MessageList.getItemKey` 的"稳定 turn 键"同一判据。不满足时必须退化为**实例
 * 作用域**（= 本行自己）：legacy 行（turnID 缺失/0）会让同一会话里多条行共用
 * `0:iteration` → 互相读到对方的高度（冻结成错块）；pending 行（MAX_SAFE_INTEGER）
 * 尚未绑定真实 turn，同理。⇒ 只有 `0 < turnID < MAX_SAFE_INTEGER` 才允许共享作用域。
 */
export function hasStableTurnKey(turnID: number | undefined): boolean {
  return typeof turnID === 'number' && Number.isFinite(turnID) && turnID > 0 && turnID < Number.MAX_SAFE_INTEGER
}

/**
 * 内容估算（仅用于「从未渲染过」的块显示占位；**不作为冻结依据**）。
 * 与 MessageList.estimateRowByContent 的迭代部分同源，宁可略高估不要低估。
 */
export function estimateIterationHeight(iter: WebIteration): number {
  const textLen = (iter.content?.length ?? 0) + (iter.reasoning?.length ?? 0)
  const lines = Math.ceil(textLen / 90) || 1
  const tools = iter.tools?.length ?? 0
  const subAgents = iter.subAgents?.length ?? 0
  const raw = 70 + lines * 21 + 34 + Math.ceil(tools / 4) * 20 + subAgents * 40
  return Math.min(Math.max(raw, 140), 6000)
}
