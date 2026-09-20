/**
 * store.ts — ChatStore：状态机唯一可变点（一个 state 引用 + rAF 合并通知）。
 *
 * - dispatch：同步 reduce；返回原引用 → 零通知（迟到事件路径零渲染）
 * - turn 边界原子性（T2）：commit 是 reduce 内单次 state 替换；一个快照一帧
 *   （rAF 合并 ≤60Hz）—— live/committed 同帧切换，无中间帧。
 * - 流式节流：高频 stream 事件合并为一次渲染。
 * - 渲染暂停（pause/resume）：面板不可见（MobileAppShell display:none 切换
 *   视图 / 桌面 tab 切走）时挂起 rAF 通知 —— dispatch 照常（状态机数据流
 *   完整，SSE 事件不丢），React 不 re-render（JS 成本全停）。resume 时一次
 *   flush（useSyncExternalStore 读 getSnapshot 最新 state —— 与"持续渲染"的
 *   最终态逐位一致，正是 rAF 合并的结构保证）。IntersectionObserver 检测
 *   display:none（元素无渲染盒 → 0 交叉）。
 */

import { frameScheduler } from '@/lib/frameScheduler'
import { reduce } from './reduce'
import { initialChatState, type ChatState, type DomainEvent } from './types'

export class ChatStore {
  private state: ChatState
  private listeners = new Set<() => void>()
  /** 本帧是否已把通知排给**共享**帧调度器（去重；等价旧 `raf !== 0`）。 */
  private notifyQueued = false
  /** 通知任务：注册到共享调度器 ⇒ 同一帧内所有「每帧一次」的工作（另一个
   *  store 的通知、滚动、几何测量）落进**同一个 rAF 回调**，React 18 在同一
   *  task 内自动批处理 ⇒ 每帧最多一次渲染（零掉帧重构，2026-09-18）。 */
  private readonly notifyTask = (): void => {
    this.notifyQueued = false
    for (const l of this.listeners) l()
  }
  private paused = false
  private pausedDirty = false

  constructor(chatID: string) {
    this.state = initialChatState(chatID)
  }

  dispatch(ev: DomainEvent): void {
    const next = reduce(this.state, ev)
    if (next === this.state) return // 无变化 → 零渲染（I5 重放/迟到路径）
    this.state = next
    this.scheduleNotify()
  }

  /** 直接替换（仅 history hydration 装配用 —— 装配后仍经 dispatch 走状态机）。 */
  hydrate(state: ChatState): void {
    this.state = state
    this.scheduleNotify()
  }

  getSnapshot = (): ChatState => this.state

  subscribe = (l: () => void): (() => void) => {
    this.listeners.add(l)
    return () => {
      this.listeners.delete(l)
    }
  }

  /**
   * 暂停 React 通知（面板不可见时）。dispatch/hydrate 照常更新 state（状态机
   * 数据流完整），仅挂起 rAF 通知 —— pausedDirty 记录期间有更新。已排队的
   * rAF 取消（通知未发出 → 转 dirty，resume 时补）。
   */
  pause(): void {
    this.paused = true
    if (this.notifyQueued) {
      frameScheduler.cancel(this.notifyTask)
      this.notifyQueued = false
      this.pausedDirty = true
    }
  }

  /**
   * 恢复通知：pausedDirty 时立即 schedule 一次 flush（一帧全量 —— rAF 合并
   * 语义，与"持续渲染"的最终态一致）。
   */
  resume(): void {
    if (!this.paused) return
    this.paused = false
    if (this.pausedDirty) {
      this.pausedDirty = false
      this.notify()
    }
  }

  private scheduleNotify(): void {
    if (this.paused) {
      this.pausedDirty = true
      return
    }
    this.notify()
  }

  private notify(): void {
    if (this.notifyQueued) return
    this.notifyQueued = true
    frameScheduler.schedule(this.notifyTask)
  }

  dispose(): void {
    frameScheduler.cancel(this.notifyTask)
    this.notifyQueued = false
    this.listeners.clear()
  }
}
