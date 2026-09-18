import { describe, expect, it } from 'vitest'

import { createFrameScheduler } from './frameScheduler'

/** 确定性假 rAF：手动驱动"帧"，避免依赖真实合成器时序。 */
function fakeRaf() {
  let next = 1
  const cbs = new Map<number, (t: number) => void>()
  return {
    raf: (cb: (t: number) => void) => {
      const h = next++
      cbs.set(h, cb)
      return h
    },
    cancelRaf: (h: number) => {
      cbs.delete(h)
    },
    /** 驱动一帧。 */
    frame: (now = 0) => {
      const list = [...cbs.entries()]
      cbs.clear()
      for (const [, cb] of list) cb(now)
    },
    pending: () => cbs.size,
  }
}

describe('frameScheduler：单一帧调度器语义（零掉帧重构的基础设施）', () => {
  it('同帧内重复 schedule 同一任务只执行一次（按身份去重）', () => {
    const f = fakeRaf()
    const s = createFrameScheduler(f)
    let n = 0
    const task = () => {
      n++
    }
    s.schedule(task)
    s.schedule(task)
    s.schedule(task)
    expect(s.size).toBe(1)
    f.frame()
    expect(n).toBe(1)
  })

  it('多任务按注册顺序执行（稳定顺序）', () => {
    const f = fakeRaf()
    const s = createFrameScheduler(f)
    const order: string[] = []
    s.schedule(() => order.push('a'))
    s.schedule(() => order.push('b'))
    s.schedule(() => order.push('c'))
    f.frame()
    expect(order).toEqual(['a', 'b', 'c'])
  })

  it('cancel 后该任务本帧不再执行', () => {
    const f = fakeRaf()
    const s = createFrameScheduler(f)
    let ran = false
    const task = () => {
      ran = true
    }
    s.schedule(task)
    s.cancel(task)
    f.frame()
    expect(ran).toBe(false)
    expect(s.size).toBe(0)
  })

  it('任务回调内再 schedule 排到下一帧（不在同帧递归）', () => {
    const f = fakeRaf()
    const s = createFrameScheduler(f)
    const log: string[] = []
    const second = () => log.push('b')
    s.schedule(() => {
      log.push('a')
      s.schedule(second)
    })
    f.frame()
    expect(log).toEqual(['a'])
    f.frame()
    expect(log).toEqual(['a', 'b'])
  })

  it('flushNow 同步执行并取消挂起的 rAF（紧急同步路径）', () => {
    const f = fakeRaf()
    const s = createFrameScheduler(f)
    let n = 0
    s.schedule(() => n++)
    s.flushNow()
    expect(n).toBe(1)
    expect(f.pending()).toBe(0)
  })

  it('reset 复位到干净状态（取消挂起帧 + 清空队列）—— 模块级单例的复位契约', () => {
    const f = fakeRaf()
    const s = createFrameScheduler(f)
    let n = 0
    s.schedule(() => n++)
    expect(f.pending()).toBe(1)
    s.reset()
    expect(s.size).toBe(0)
    expect(f.pending()).toBe(0) // 挂起的帧已被取消
    f.frame()
    expect(n).toBe(0) // 复位后不再执行
  })
})
