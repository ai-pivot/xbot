/**
 * useRegionWindow —— 区域段加载编排（D1 线）：IO arm/disarm 状态机守护。
 *
 * 历史事故（MessageList 的 loadMore）：observer 的 effect deps 含 loading/回调 ⇒
 * 每次 loading 翻转都重建 observer，而**新建 observer 会立刻投递一次初始回调** ⇒
 * 哨兵仍在视口内 ⇒ 自激请求风暴（实测一次手势 11 次请求）。
 * 本文件的 ①③ 两条用例就是那个事故的判别器（去掉 disarm / 把 loading 放进 deps 必红）。
 */
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'

import { RegionsDivider } from '@/components/agent/RegionsDivider'
import { RegionActionsContext, type RegionActions } from '@/components/agent/RegionActionsContext'
import { MAX_AUTO_REGION_FAILURES, useRegionWindow } from '@/hooks/useRegionWindow'
import i18n from '@/i18n'

beforeAll(async () => {
  await i18n.changeLanguage('zh-CN')
})

// ── 可控 IntersectionObserver ────────────────────────────────────────────────
// 复刻真实行为：observe() 之后**立刻投递一次初始回调**（正是风暴事故的放大器）。
class FakeIO {
  static instances: FakeIO[] = []
  /** observe 时投递的初始可见性（默认 true = 挂载即在视口内）。 */
  static initialVisible = true
  static observeCount = 0
  private cb: IntersectionObserverCallback
  private els: Element[] = []
  private disconnected = false
  constructor(cb: IntersectionObserverCallback) {
    this.cb = cb
    FakeIO.instances.push(this)
  }
  observe(el: Element) {
    this.els.push(el)
    FakeIO.observeCount += 1
    queueMicrotask(() => {
      if (!this.disconnected) this.emit(FakeIO.initialVisible)
    })
  }
  unobserve() {}
  disconnect() {
    this.disconnected = true
  }
  emit(isIntersecting: boolean) {
    this.cb(
      [{ isIntersecting, target: this.els[0] } as unknown as IntersectionObserverEntry],
      this as unknown as IntersectionObserver,
    )
  }
}

beforeEach(() => {
  FakeIO.instances = []
  FakeIO.observeCount = 0
  FakeIO.initialVisible = true
  ;(globalThis as unknown as { IntersectionObserver: unknown }).IntersectionObserver = FakeIO
})

afterEach(() => {
  delete (globalThis as unknown as { IntersectionObserver?: unknown }).IntersectionObserver
})

/** 受控 deferred：让"请求在飞"成为一个可断言的稳定态。 */
function deferred<T>() {
  let resolve!: (v: T) => void
  const promise = new Promise<T>((r) => { resolve = r })
  return { promise, resolve }
}

function Harness({ regionsBefore, turnID = 7, beforeIteration = 52, gapTop }: { regionsBefore: number; turnID?: number; beforeIteration?: number; gapTop?: number }) {
  const win = useRegionWindow({ turnID, regionsBefore, beforeIteration, gapTop })
  if (!win.enabled) return <div data-testid="no-divider" />
  return (
    <RegionsDivider
      count={regionsBefore}
      status={win.status}
      onRetry={win.retry}
      sentinelRef={win.sentinelRef}
    />
  )
}

function renderHarness(actions: Partial<RegionActions>, props: { regionsBefore: number; turnID?: number; beforeIteration?: number; gapTop?: number } = { regionsBefore: 12 }) {
  const full: RegionActions = {
    loadIterationDetail: vi.fn(async () => true),
    fetchInFlight: () => false,
    loadRegionSegment: vi.fn(async () => true),
    segmentInFlight: () => false,
    ...actions,
  }
  const utils = render(
    <RegionActionsContext.Provider value={full}>
      <Harness {...props} />
    </RegionActionsContext.Provider>,
  )
  return { ...utils, actions: full }
}

const io = () => FakeIO.instances[0]

describe('useRegionWindow：IO 触发权（arm/disarm）', () => {
  it('① 一次手势 = 一次请求：连续 IO 回调（哨兵持续可见）只发 1 个段请求', async () => {
    const d = deferred<boolean>()
    const { actions } = renderHarness({ loadRegionSegment: vi.fn(() => d.promise) })
    // observer 的初始回调（哨兵可见）⇒ 触发一次
    await waitFor(() => expect(actions.loadRegionSegment).toHaveBeenCalledTimes(1))
    expect(screen.getByTestId('regions-divider').getAttribute('data-state')).toBe('loading')
    // 同一「可见回合」内再来两次回调（真实 IO 会重复投递）——触发权已归还，必须静默
    act(() => { io().emit(true); io().emit(true) })
    expect(actions.loadRegionSegment).toHaveBeenCalledTimes(1)
    await act(async () => { d.resolve(true) })
    expect(screen.getByTestId('regions-divider').getAttribute('data-state')).toBe('idle')
    // ⚠️ 关键判别点（= 历史事故的形状）：loading 翻转完成后，**未离开视口**的重复回调
    // 不得再发请求（没有 disarm 时 armed 仍为 true ⇒ 这里会打出第 2 个请求 —— 必红）。
    act(() => { io().emit(true); io().emit(true) })
    await act(async () => {})
    expect(actions.loadRegionSegment).toHaveBeenCalledTimes(1)
  })

  it('② re-arm 路径「离开视口 → 回到视口」⇒ 新请求', async () => {
    const { actions } = renderHarness({})
    await waitFor(() => expect(actions.loadRegionSegment).toHaveBeenCalledTimes(1))
    await act(async () => {}) // 让第一次请求落地（resolved true）
    act(() => { io().emit(false) })  // 用户真的滚离顶部 ⇒ 归还触发权
    act(() => { io().emit(true) })   // 再一次"滑到顶"手势
    await waitFor(() => expect(actions.loadRegionSegment).toHaveBeenCalledTimes(2))
  })

  it('③ re-arm 路径「不可见 → 可见」（含首次回调就不可见的退路）⇒ 触发', async () => {
    FakeIO.initialVisible = false
    const { actions } = renderHarness({})
    await act(async () => {})
    expect(actions.loadRegionSegment).not.toHaveBeenCalled()
    act(() => { io().emit(true) })
    await waitFor(() => expect(actions.loadRegionSegment).toHaveBeenCalledTimes(1))
  })

  it('④ observer 不随 loading/状态翻转重建（deps 只含哨兵存在性）', async () => {
    const { actions } = renderHarness({})
    await waitFor(() => expect(actions.loadRegionSegment).toHaveBeenCalledTimes(1))
    await act(async () => {}) // loading → idle 翻转
    // 重建 observer 会再投递一次初始回调 ⇒ 请求风暴；实例数必须恒为 1，observe 只调一次
    expect(FakeIO.instances.length).toBe(1)
    expect(FakeIO.observeCount).toBe(1)
    expect(actions.loadRegionSegment).toHaveBeenCalledTimes(1)
  })

  it('⑤ regions_before = 0（到顶）⇒ 不渲染分隔条、零 observer、零请求', async () => {
    const { actions } = renderHarness({}, { regionsBefore: 0 })
    expect(screen.getByTestId('no-divider')).toBeTruthy()
    expect(screen.queryByTestId('regions-divider')).toBeNull()
    await act(async () => {})
    expect(FakeIO.instances.length).toBe(0)
    expect(actions.loadRegionSegment).not.toHaveBeenCalled()
  })

  it('⑥ 无 turn 归属（turnID=0 / 无迭代）⇒ 不挂哨兵（standalone/legacy 行零差异）', async () => {
    const { actions } = renderHarness({}, { regionsBefore: 12, turnID: 0 })
    expect(screen.getByTestId('no-divider')).toBeTruthy()
    await act(async () => {})
    expect(actions.loadRegionSegment).not.toHaveBeenCalled()
  })
})

describe('useRegionWindow：失败降级（不自动循环）', () => {
  it('⑦ 失败 ⇒ retry 态；点击重试 ⇒ 再发一次请求', async () => {
    const load = vi.fn(async () => false)
    const { actions } = renderHarness({ loadRegionSegment: load })
    await waitFor(() => expect(screen.getByTestId('regions-divider-retry')).toBeTruthy())
    expect(actions.loadRegionSegment).toHaveBeenCalledTimes(1)
    fireEvent.click(screen.getByTestId('regions-divider-retry'))
    await waitFor(() => expect(actions.loadRegionSegment).toHaveBeenCalledTimes(2))
  })

  it('⑧ 连续失败 2 次 ⇒ 停止自动触发（手动重试仍可用）', async () => {
    const load = vi.fn(async () => false)
    const { actions } = renderHarness({ loadRegionSegment: load })
    await waitFor(() => expect(actions.loadRegionSegment).toHaveBeenCalledTimes(1)) // 自动触发 #1（失败）
    fireEvent.click(screen.getByTestId('regions-divider-retry'))               // 手动 #2（失败）
    await waitFor(() => expect(actions.loadRegionSegment).toHaveBeenCalledTimes(2))
    expect(MAX_AUTO_REGION_FAILURES).toBe(2)
    // 新手势（滚离再回来）不得再自动发请求 —— 保守停止，避免弱网下的请求循环
    act(() => { io().emit(false) })
    act(() => { io().emit(true) })
    expect(actions.loadRegionSegment).toHaveBeenCalledTimes(2)
    // 用户手动重试仍然放行
    fireEvent.click(screen.getByTestId('regions-divider-retry'))
    await waitFor(() => expect(actions.loadRegionSegment).toHaveBeenCalledTimes(3))
  })

  it('⑨ 失败后成功 ⇒ 失败计数归零（再次手势恢复自动触发）', async () => {
    const load = vi.fn()
      .mockResolvedValueOnce(false) // 自动 #1 失败
      .mockResolvedValue(true)      // 手动 #2 成功
    const { actions } = renderHarness({ loadRegionSegment: load })
    await waitFor(() => expect(screen.getByTestId('regions-divider-retry')).toBeTruthy())
    fireEvent.click(screen.getByTestId('regions-divider-retry'))
    await waitFor(() => expect(screen.getByTestId('regions-divider').getAttribute('data-state')).toBe('idle'))
    act(() => { io().emit(false) })
    act(() => { io().emit(true) })
    await waitFor(() => expect(actions.loadRegionSegment).toHaveBeenCalledTimes(3))
  })
})

describe('useRegionWindow：gapTop 自动追赶（2026-10-02 P0——熄屏恢复 × 折叠窗口的洞）', () => {
  it('⑦ 检测到洞 ⇒ 不等 IO 立即取段（哨兵不可见也必须追赶——历史冻结 + tool done 消失的修复）', async () => {
    // 熄屏恢复场景：本地 [1..40] ∪ 恢复窗口 [52..90] ⇒ 洞 [41..51]，gapTop=52；
    // 用户在底部看 live，分隔条（哨兵）不在视口 —— IO 永远不会触发；不自动追赶则
    // contiguous 在洞处截断 ⇒ 历史冻结在熄屏时刻、完成的 tool 落在截断区外「消失」。
    FakeIO.initialVisible = false
    const { actions } = renderHarness({}, { regionsBefore: 6, turnID: 7, beforeIteration: 52, gapTop: 52 })
    await waitFor(() => expect(actions.loadRegionSegment).toHaveBeenCalledTimes(1))
    expect(actions.loadRegionSegment).toHaveBeenCalledWith(7, 52) // before_iteration = 洞上边界
  })

  it('⑧ 无洞（gapTop=undefined）⇒ 绝不自动发请求（既有 IO 手势语义零变化）', async () => {
    FakeIO.initialVisible = false // 哨兵不可见 + 无洞 ⇒ 两条路径都不触发
    const { actions } = renderHarness({}, { regionsBefore: 6, turnID: 7, beforeIteration: 52 })
    await act(async () => {})
    expect(actions.loadRegionSegment).not.toHaveBeenCalled()
  })
})
