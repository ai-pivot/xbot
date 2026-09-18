import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'

import { ChatStore } from '@/chat/store'
import { ProgressStore } from '@/components/agent/progressStore'
import { __resetFrameSchedulerForTests } from '@/lib/frameScheduler'

/**
 * 「零掉帧」的**确定性**机制守护（2026-09-18）。
 *
 * 为什么不是计时断言：`e2e/frame-budget.spec.ts` 那类「帧间隔 ≤16.7ms」是**计时型**，
 * 在 shared + headless（无真实 vsync）的 CI runner 上必然假红（实测 3/3 次：
 * 8 / 14 / 3 帧 ≥33ms）——项目纪律明确禁止用计时断言做守护。
 *
 * 真正决定「每帧最多一次渲染」的是**机制**：同一帧内的多次更新只会武装**一个**
 * rAF、并在该帧回调里只通知**一次**（React 18 在同一 task 内自动批处理）——
 * 这件事与 runner 快慢无关，可以**确定性**断言（jsdom + mock rAF + 手动驱动帧）。
 *
 * 反例（修复前）：两条 store 各自 `requestAnimationFrame` ⇒ 同一帧内 N 次更新
 * 会排 N 个独立回调 ⇒ N 次通知 ⇒ N 次渲染 ⇒ 样式重算 + 掉帧（trace 9.gz：
 * 8 秒 2047 次渲染 ≈ 4 次/帧、249 次 DroppedFrame）。
 */

let rafCbs: Array<(t: number) => void>
let rafSpy: ReturnType<typeof vi.spyOn>

beforeEach(() => {
  // ⚠️ 调度器是模块级单例：本文件手动驱动 mock rAF，必须每个用例前复位
  // （否则上个用例遗留的"已排队但从未触发"任务会让队列非空 ⇒ 不再武装）。
  __resetFrameSchedulerForTests()
  rafCbs = []
  rafSpy = vi.spyOn(window, 'requestAnimationFrame').mockImplementation((cb) => {
    rafCbs.push(cb as (t: number) => void)
    return rafCbs.length
  })
})

afterEach(() => rafSpy.mockRestore())

/** 驱动"一帧"：把本帧排队的 rAF 回调全部执行。 */
const driveFrame = () => {
  const cbs = rafCbs.splice(0, rafCbs.length)
  for (const cb of cbs) cb(0)
}

describe('确定性机制：同帧内 N 次更新 ⇒ 每帧恰好一次通知（⇒ 每帧最多一次渲染）', () => {
  it('ProgressStore：同帧 50 次结构化事件 ⇒ 只武装 1 帧、只通知 1 次、最终态正确', () => {
    const store = new ProgressStore()
    let notified = 0
    store.subscribe(() => {
      notified++
    })

    for (let i = 1; i <= 50; i++) {
      store.setStructuredTools({ eventSeq: i, phase: 'thinking', iteration: i })
    }

    // 机制①：整个 burst 只排了 **1** 个 rAF —— 不是 50 个（修复前是每实例各自 rAF，
    // 跨 store 更会成倍放大）。
    expect(rafCbs.length).toBe(1)
    // 帧未到 ⇒ 尚未通知（不渲染）。
    expect(notified).toBe(0)

    driveFrame()

    // 机制②：一帧只通知 **1** 次。
    expect(notified).toBe(1)
    // 最终态正确（合并不丢事件、无降级）。
    expect(store.getSnapshot().iteration).toBe(50)
    expect(store.getSnapshot().phase).toBe('thinking')
  })

  it('ChatStore：同帧 50 次 dispatch ⇒ 只武装 1 帧、只通知 1 次、最终态正确', () => {
    const store = new ChatStore('chat-1')
    let notified = 0
    const unsub = store.subscribe(() => {
      notified++
    })

    for (let i = 1; i <= 50; i++) {
      store.dispatch({
        type: 'iteration',
        turnID: 1,
        iter: i,
        seq: i,
        content: `chunk-${i}`,
        reasoning: undefined,
        activeTools: [],
        completedTools: [],
        iterationsDelta: [],
        todos: undefined,
      } as never)
    }

    expect(rafCbs.length).toBe(1)
    driveFrame()
    expect(notified).toBe(1)

    const snap = store.getSnapshot()
    expect(snap.turns.size).toBeGreaterThan(0)
    unsub()
    store.dispose()
  })

  it('跨 store：两条更新流在同一帧内共享**同一个** rAF 回调（同 task ⇒ React 批处理）', () => {
    const chat = new ChatStore('chat-1')
    const progress = new ProgressStore()
    const order: string[] = []
    const unsubChat = chat.subscribe(() => order.push('chat'))
    const unsubProgress = progress.subscribe(() => order.push('progress'))

    chat.dispatch({
      type: 'iteration',
      turnID: 1,
      iter: 1,
      seq: 1,
      content: 'a',
      reasoning: undefined,
      activeTools: [],
      completedTools: [],
      iterationsDelta: [],
      todos: undefined,
    } as never)
    progress.setStructuredTools({ eventSeq: 1, phase: 'thinking', iteration: 1 })

    // 两条流各自更新，但**只武装了 1 个帧**（共享调度器）。
    expect(rafCbs.length).toBe(1)

    driveFrame()

    // 两条通知都在**同一帧回调**里执行，且保持注册顺序（零行为变化契约）。
    expect(order).toEqual(['chat', 'progress'])
    unsubChat()
    unsubProgress()
    chat.dispose()
  })
})
