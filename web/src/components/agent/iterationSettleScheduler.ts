/**
 * 合并式 settle 采样调度器（窗口化性能修复，2026-09-17）。
 *
 * 背景：窗口化需要「某块尺寸变化后 ≥ delayMs 再采样一次」才能判定 settled
 * （RO 只在尺寸变化时回调，必须主动补一次"同值二次测量"）。
 *
 * 旧实现：**每个 key 一个定时器**，且每次变化都 `clearTimeout + setTimeout`
 * （`TurnBody.scheduleSettleSample`）。而 ResizeObserver 在流式期间对**每个块每帧**
 * 的尺寸变化都会回调 ⇒ 约 125 个 key/帧 × 60fps ≈ **7.5k 次 install/remove 每秒**。
 * V8 的 `clearTimeout` 需要在定时器列表里线性查找 ⇒ 实测 `clearTimeout` 独占
 * **21.4% CPU**、`setTimeout` 2.8%，并出现 18 个 ≥50ms 长任务（最长 486ms）——
 * 这就是「会话卡顿/掉帧」的直接来源（Trace-20260918T000005：TimerInstall 50,678 次 /
 * 6.7s，其中 timeout=250ms 占 50,544，`TimerFire` 仅 779 次 ⇒ 全是被清掉的重排）。
 *
 * 现在：只维护**一个**定时器 + 每个 key 的 deadline 表。
 *  - `schedule(key)`：只更新该 key 的 deadline（= now + delayMs），**不碰定时器**
 *    （若尚未武装则武装一次）。语义与旧实现一致：每次变化都把采样推后。
 *  - 定时器到点：只采样**已到期**的 key；若还有更晚的 deadline，则重新武装一次
 *    （保证更晚的 key 不会被提前采样）。
 *  ⇒ 定时器 install 从 ~7.5k/s 降到 ±1 次/采样窗口，且每个 key 仍在"最后一次变化
 *  之后 ≥ delayMs"被采样一次。
 *
 * 依赖全部可注入（now/setTimer/clearTimer），因此可离线确定性测试。
 */

export interface SettleSchedulerDeps {
  /** 变化后等待多久才采样（毫秒） */
  delayMs: number
  /** 采样回调：对每个"最后一次变化已过去 ≥ delayMs"的 key 调用一次 */
  onSample: (key: string) => void
  /** 注入点（测试用）；默认用 performance.now / window.setTimeout / window.clearTimeout */
  now?: () => number
  setTimer?: (fn: () => void, ms: number) => number
  clearTimer?: (handle: number) => void
}

export interface SettleScheduler {
  /** 记录 key 的一次尺寸变化（deadline 推后到 now + delayMs），不会为每个 key 建定时器 */
  schedule(key: string): void
  /** 取消全部待采样并解除定时器 */
  cancelAll(): void
  /** 待采样 key 数（测试/诊断用） */
  pendingCount(): number
  /** 当前是否已武装定时器（测试/诊断用） */
  isArmed(): boolean
}

export function createSettleScheduler(deps: SettleSchedulerDeps): SettleScheduler {
  const now = deps.now ?? (() => performance.now())
  const setTimer = deps.setTimer ?? ((fn: () => void, ms: number) => window.setTimeout(fn, ms))
  const clearTimer = deps.clearTimer ?? ((h: number) => window.clearTimeout(h))

  const deadlines = new Map<string, number>()
  let handle: number | undefined

  function fire(): void {
    handle = undefined
    const t = now()
    let next: number | undefined
    for (const [key, deadline] of Array.from(deadlines)) {
      if (deadline <= t) {
        deadlines.delete(key)
        deps.onSample(key)
      } else if (next === undefined || deadline < next) {
        next = deadline
      }
    }
    // 还有更晚的 deadline：重新武装一次（绝不会提前采样它们）
    if (next !== undefined) handle = setTimer(fire, Math.max(1, next - now()))
  }

  return {
    schedule(key: string): void {
      deadlines.set(key, now() + deps.delayMs)
      if (handle === undefined) handle = setTimer(fire, deps.delayMs)
    },
    cancelAll(): void {
      if (handle !== undefined) {
        clearTimer(handle)
        handle = undefined
      }
      deadlines.clear()
    },
    pendingCount: () => deadlines.size,
    isArmed: () => handle !== undefined,
  }
}
