/**
 * measureOffscreen —— Phase 1（离屏权威测量）的单测。
 *
 * ⚠️ jsdom **没有布局**（所有 rect 都是 0），所以这里不测「真实像素高度」，而是注入
 * `measure`/`raf` 验证**编排正确性**：逐项同序、稳定性收敛、超时保守返回、宿主必被销毁。
 * 真实像素级判据（用户口径：插入前后同一内容 bbox 一动不动）在 E2E 里测。
 */
import { describe, expect, it, vi } from 'vitest'

import { measureOffscreen } from '@/components/agent/measureOffscreen'

/** 让每个节点自带高度：<div data-h="120" /> */
function node(h: number): React.ReactNode {
  return <div data-h={h} />
}
const measureByDataH = (el: HTMLElement): number => Number(el.querySelector('[data-h]')?.getAttribute('data-h') ?? 0)
const syncRaf = (cb: () => void): void => cb()

describe('measureOffscreen（离屏权威测量 · Phase 1）', () => {
  it('同序返回每个节点的实测高度，并在结束后销毁离屏宿主', async () => {
    const { heights, timedOut } = await measureOffscreen([node(120), node(240), node(0)], {
      width: 640,
      measure: measureByDataH,
      raf: syncRaf,
    })
    expect(heights).toEqual([120, 240, 0])
    expect(timedOut).toBe(false)
    // 离屏副本必须彻底销毁（绝不残留 DOM）
    expect(document.querySelector('[data-testid="measure-offscreen-host"]')).toBeNull()
  })

  it('宿主是离屏的：绝对定位出视口 + aria-hidden + 用调用方给的宽度', async () => {
    let seen: { width: string; ariaHidden: string | null; position: string } | null = null
    await measureOffscreen([node(10)], {
      width: 512,
      className: 'row-typography',
      measure: (el) => {
        const host = el.closest('[data-testid="measure-offscreen-host"]') as HTMLElement
        seen = { width: host.style.width, ariaHidden: host.getAttribute('aria-hidden'), position: host.style.position }
        return 10
      },
      raf: syncRaf,
    })
    expect(seen).not.toBeNull()
    expect(seen!.width).toBe('512px')
    expect(seen!.ariaHidden).toBe('true')
    expect(seen!.position).toBe('absolute')
  })

  it('高度一直不稳定 ⇒ 到点保守返回并标记 timedOut（绝不死等）', async () => {
    let n = 0
    const { timedOut } = await measureOffscreen([node(1)], {
      width: 100,
      timeoutMs: 5,
      measure: () => 100 + n++, // 每次都不同 ⇒ 永不收敛
      raf: syncRaf,
    })
    expect(timedOut).toBe(true)
    expect(document.querySelector('[data-testid="measure-offscreen-host"]')).toBeNull()
  })

  it('空输入是零成本 no-op（不建宿主、不渲染）', async () => {
    const measure = vi.fn()
    const { heights, timedOut } = await measureOffscreen([], { width: 100, measure, raf: syncRaf })
    expect(heights).toEqual([])
    expect(timedOut).toBe(false)
    expect(measure).not.toHaveBeenCalled()
    expect(document.querySelector('[data-testid="measure-offscreen-host"]')).toBeNull()
  })

  it('渲染抛错也必须销毁宿主（不留残骸）', async () => {
    const Boom = (): React.ReactNode => {
      throw new Error('boom')
    }
    // React 19 在无错误边界时会把渲染错误 rethrow（且可能滞后到微任务）—— 显式吞掉
    // 日志与异常，只断言**清理**这一件事（离屏宿主绝不允许残留）。
    const errSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    try {
      await measureOffscreen([<Boom key="b" />], { width: 100, measure: measureByDataH, raf: syncRaf })
    } catch {
      /* 期望中的渲染错误 */
    }
    await Promise.resolve()
    errSpy.mockRestore()
    expect(document.querySelector('[data-testid="measure-offscreen-host"]')).toBeNull()
  })
})
