/**
 * 单一帧调度器 —— 「零掉帧」重构的基础设施（2026-09-18）。
 *
 * 背景（trace 9.gz 实测）：页面有 **6 条独立更新流**各自调度 rAF/interval
 * （chat store、progress store、typewriter 共享 interval、MessageList 的
 * scroll/observe/follow、TurnBody 的 rAF flush），8 秒内 React 渲染 **2047 次
 * ≈ 4 次/帧** ⇒ `te @ vendor-react` 2387ms + `UpdateLayoutTree` 2024ms ⇒
 * **249 次 DroppedFrame**、帧间隔 max 291ms。
 *
 * 修法：所有「每帧一次」的工作注册到本调度器 —— **一个 rAF 帧里最多跑一次**，
 * 每帧只触发一次 React 通知/一次几何测量。
 *
 * 语义（严格、确定性、可单测）：
 *  - 同帧内重复 `schedule(task)` 只跑一次（按 callback 身份去重）；
 *  - 任务按**注册顺序**执行（稳定顺序，不依赖除数组顺序以外的任何东西）；
 *  - `cancel(task)` 之后该任务在本帧不再执行；
 *  - 任务回调里再 `schedule(...)` 的任务排到**下一帧**（不在同帧内递归——
 *    防止"一帧内无限自旋"）；
 *  - `flushNow()` 仅供测试/紧急同步路径使用（生产路径禁止：会破坏帧预算）。
 *
 * ⚠️ 零行为变化红线：本模块**只做调度**，不含任何业务语义；迁移调用点时
 * 必须保持原有的通知顺序与幂等短路（引用不变即不通知）。
 */

export type FrameTask = () => void

export interface FrameSchedulerDeps {
  /** 调度下一帧（可注入，便于测试）。 */
  raf: (cb: (t: number) => void) => number
  /** 取消已调度的帧（可注入）。 */
  cancelRaf: (handle: number) => void
}

export interface FrameScheduler {
  /** 把一个任务安排到下一帧（同帧内重复调用只跑一次）。 */
  schedule(task: FrameTask): void
  /** 取消尚未执行的任务。 */
  cancel(task: FrameTask): void
  /** 同步跑完当前帧队列（测试/紧急路径；会取消已挂起的 rAF）。 */
  flushNow(): void
  /** 复位（取消挂起帧 + 清空队列）—— 仅测试用。 */
  reset(): void
  /** 当前帧待执行任务数（测试用）。 */
  readonly size: number
}

function defaultRaf(cb: (t: number) => void): number {
  if (typeof requestAnimationFrame === 'function') return requestAnimationFrame(cb)
  return setTimeout(() => cb(performance.now()), 16) as unknown as number
}

function defaultCancelRaf(handle: number): void {
  if (typeof cancelAnimationFrame === 'function') cancelAnimationFrame(handle)
  else clearTimeout(handle as unknown as ReturnType<typeof setTimeout>)
}

export function createFrameScheduler(deps?: Partial<FrameSchedulerDeps>): FrameScheduler {
  const raf = deps?.raf ?? defaultRaf
  const cancelRaf = deps?.cancelRaf ?? defaultCancelRaf

  let handle = 0
  let pending: FrameTask[] = []
  const queued = new Set<FrameTask>()

  const flush = () => {
    handle = 0
    // 先摘取当前帧队列：任务回调里再 schedule 的会被排到**下一帧**
    // （pending 已被替换成新的空数组）。
    const tasks = pending
    pending = []
    queued.clear()
    for (const task of tasks) task()
  }

  return {
    schedule(task: FrameTask) {
      if (queued.has(task)) return
      queued.add(task)
      pending.push(task)
      if (handle === 0) handle = raf(flush)
    },
    cancel(task: FrameTask) {
      if (!queued.has(task)) return
      queued.delete(task)
      pending = pending.filter((t) => t !== task)
    },
    flushNow() {
      if (handle !== 0) {
        cancelRaf(handle)
        handle = 0
      }
      flush()
    },
    /** 复位到干净状态（取消挂起的帧 + 清空队列）—— 仅测试用。 */
    reset() {
      if (handle !== 0) {
        cancelRaf(handle)
        handle = 0
      }
      pending = []
      queued.clear()
    },
    get size() {
      return pending.length
    },
  }
}

/**
 * 全局共享实例：所有「每帧一次」的更新流（store 通知 / 滚动 / typewriter /
 * 几何测量）都注册到这里，保证**每帧最多一次** React 通知。
 */
export const frameScheduler = createFrameScheduler()

/**
 * 仅测试用：复位全局单例。
 * ⚠️ 模块级单例**必须**提供显式复位（与 `__resetSharedIterationHeightTrackers()`
 * 同一约定）：每个测试文件用各自的 mock `requestAnimationFrame` 手动驱动帧，
 * 前一个用例遗留的"已排队但从未触发"的任务会让队列非空 ⇒ 后续 `schedule` 不再
 * 武装 ⇒ 通知永不 flush（实测症状：`progressStore` 的 V1 用例里 `phase` 停在 ''
 * 而期望 'thinking'、`eventSeq` 停在 0 而期望 3）。
 */
export function __resetFrameSchedulerForTests(): void {
  frameScheduler.reset()
}
