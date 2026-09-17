import { describe, expect, it, vi } from 'vitest'
import { createSettleScheduler } from './iterationSettleScheduler'

/**
 * 确定性时钟：手动推进时间、记录 install/remove 次数。
 * 这正是复现 2026-09-17 卡顿的关键量：**定时器 install/clear 的频率**。
 */
function makeClock() {
  let t = 0
  let nextId = 1
  const timers = new Map<number, { at: number; fn: () => void }>()
  let installs = 0
  let clears = 0
  return {
    now: () => t,
    setTimer: (fn: () => void, ms: number) => {
      installs++
      const id = nextId++
      timers.set(id, { at: t + ms, fn })
      return id
    },
    clearTimer: (h: number) => {
      clears++
      timers.delete(h)
    },
    advance(ms: number) {
      const target = t + ms
      for (;;) {
        const due = [...timers.entries()]
          .filter(([, v]) => v.at <= target)
          .sort((a, b) => a[1].at - b[1].at)[0]
        if (!due) break
        timers.delete(due[0])
        t = due[1].at
        due[1].fn()
      }
      t = target
    },
    count: () => ({ installs, clears, live: timers.size }),
  }
}

describe('createSettleScheduler（合并式 settle 采样）', () => {
  it('同一 tick 内 N 次 schedule ⇒ 只装 1 个定时器、0 次 clear（旧实现是 N 装 + N-1 清）', () => {
    const clk = makeClock()
    const samples: string[] = []
    const s = createSettleScheduler({
      delayMs: 250,
      onSample: (k) => samples.push(k),
      now: clk.now,
      setTimer: clk.setTimer,
      clearTimer: clk.clearTimer,
    })

    for (let i = 0; i < 200; i++) s.schedule(`k${i}`)
    const { installs, clears } = clk.count()
    expect(installs).toBe(1) // ← 修复前：200
    expect(clears).toBe(0)
    expect(s.pendingCount()).toBe(200)

    clk.advance(250)
    expect(samples).toHaveLength(200)
    expect(s.pendingCount()).toBe(0)
    expect(s.isArmed()).toBe(false)
  })

  it('每个 key 都在「最后一次变化之后 ≥ delayMs」被采样一次（重排语义不变）', () => {
    const clk = makeClock()
    const at: Record<string, number[]> = {}
    const s = createSettleScheduler({
      delayMs: 250,
      onSample: (k) => (at[k] = [...(at[k] ?? []), clk.now()]),
      now: clk.now,
      setTimer: clk.setTimer,
      clearTimer: clk.clearTimer,
    })

    s.schedule('a') // t=0 → deadline 250
    clk.advance(150)
    s.schedule('a') // t=150 再次变化 → deadline 400（重排）
    clk.advance(150) // t=300：还没到 400
    expect(at.a).toBeUndefined()
    clk.advance(100) // t=400
    expect(at.a).toEqual([400]) // 只采一次，且距最后一次变化 250ms ✓
  })

  it('更晚的 deadline 不会被提前采样，必要时只重新武装一次', () => {
    const clk = makeClock()
    const order: string[] = []
    const s = createSettleScheduler({
      delayMs: 250,
      onSample: (k) => order.push(`${k}@${clk.now()}`),
      now: clk.now,
      setTimer: clk.setTimer,
      clearTimer: clk.clearTimer,
    })

    s.schedule('a') // deadline 250
    clk.advance(200)
    s.schedule('b') // deadline 450
    const before = clk.count().installs // 仍只有 1 个定时器（b 不新建）
    expect(before).toBe(1)
    clk.advance(50) // t=250：a 到期，b 未到期
    expect(order).toEqual(['a@250'])
    expect(clk.count().installs).toBe(2) // 为 b 重新武装一次
    clk.advance(200) // t=450
    expect(order).toEqual(['a@250', 'b@450'])
  })

  it('cancelAll 解除定时器并清空待采样（卸载/切换会话时不留悬挂定时器）', () => {
    const clk = makeClock()
    const onSample = vi.fn()
    const s = createSettleScheduler({
      delayMs: 250,
      onSample,
      now: clk.now,
      setTimer: clk.setTimer,
      clearTimer: clk.clearTimer,
    })

    s.schedule('a')
    s.cancelAll()
    expect(s.isArmed()).toBe(false)
    expect(s.pendingCount()).toBe(0)
    expect(clk.count().live).toBe(0)
    clk.advance(1000)
    expect(onSample).not.toHaveBeenCalled()
  })
})
