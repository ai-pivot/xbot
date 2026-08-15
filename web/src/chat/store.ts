/**
 * store.ts — ChatStore：状态机唯一可变点（一个 state 引用 + rAF 合并通知）。
 *
 * - dispatch：同步 reduce；返回原引用 → 零通知（迟到事件路径零渲染）
 * - turn 边界原子性（T2）：commit 是 reduce 内单次 state 替换；一个快照一帧
 *   （rAF 合并 ≤60Hz）—— live/committed 同帧切换，无中间帧。
 * - 流式节流：高频 stream 事件合并为一次渲染。
 */

import { reduce } from './reduce'
import { initialChatState, type ChatState, type DomainEvent } from './types'

export class ChatStore {
  private state: ChatState
  private listeners = new Set<() => void>()
  private raf = 0

  constructor(chatID: string) {
    this.state = initialChatState(chatID)
  }

  dispatch(ev: DomainEvent): void {
    const next = reduce(this.state, ev)
    if (next === this.state) return // 无变化 → 零渲染（I5 重放/迟到路径）
    this.state = next
    if (this.raf === 0) {
      this.raf = requestAnimationFrame(() => {
        this.raf = 0
        for (const l of this.listeners) l()
      })
    }
  }

  /** 直接替换（仅 history hydration 装配用 —— 装配后仍经 dispatch 走状态机）。 */
  hydrate(state: ChatState): void {
    this.state = state
    this.notify()
  }

  getSnapshot = (): ChatState => this.state

  subscribe = (l: () => void): (() => void) => {
    this.listeners.add(l)
    return () => {
      this.listeners.delete(l)
    }
  }

  private notify(): void {
    if (this.raf === 0) {
      this.raf = requestAnimationFrame(() => {
        this.raf = 0
        for (const l of this.listeners) l()
      })
    }
  }

  dispose(): void {
    if (this.raf !== 0) cancelAnimationFrame(this.raf)
    this.raf = 0
    this.listeners.clear()
  }
}
