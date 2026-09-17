/**
 * 用**真实** `Virtualizer`（TanStack virtual-core，jsdom 里可跑）复现并守护
 * 2026-09-17 的行重叠事故机制 —— 与 mock 版本（measureOnAppend.test.tsx）互补：
 * mock 版断言「我们的代码有没有调对原语」，这里断言「原语本身能不能把错位的
 * `item.start` 校正回来」。
 *
 * 事故数据（用户真实浏览器）：上一条 assistant 行 DOM 实测 1118.78px，但缓存里
 * 仍是它早期的小尺寸 ~115px → 追加的 standalone 行 `item.start` 只比上一行多
 * ~115px → 两个绝对定位行重叠 ~1004px（输出在 DOM 里但被盖住）。
 */
import { describe, expect, it } from 'vitest'
import { Virtualizer } from '@tanstack/react-virtual'

/** 造一个可被 measureElement 读取的假行节点（jsdom 无布局 → 显式给 offsetHeight）。 */
function mountRow(index: number, height: number): HTMLElement {
  const el = document.createElement('div')
  el.className = 'virt-row'
  el.setAttribute('data-index', String(index))
  Object.defineProperty(el, 'offsetHeight', { configurable: true, get: () => height })
  document.body.appendChild(el)
  return el
}

function makeVirtualizer(count: number) {
  const scroll = document.createElement('div')
  const opts = {
    count,
    getScrollElement: () => scroll,
    estimateSize: () => 115,
    getItemKey: (i: number) => `row-${i}`,
    initialRect: { width: 800, height: 700 },
    initialOffset: 0,
  }
  const v = new Virtualizer<HTMLDivElement, HTMLDivElement>(opts)
  // ⚠️ TanStack 的 `setOptions` 是**整体替换**（不与上一次 options 合并）——
  // 只传 `{count}` 会把 estimateSize/getItemKey 一起抹掉（实测报
  // "this.options.estimateSize is not a function"）。追加行必须带上完整 options。
  return { v, grow: (n: number) => v.setOptions({ ...opts, count: n }) }
}

function startOf(v: Virtualizer<HTMLDivElement, HTMLDivElement>, index: number): number {
  // ⚠️ 不能读 `getVirtualItems()`：它只返回**可见窗口**，而 jsdom 里容器几何为 0
  // （无 ResizeObserver）→ 窗口为空。`measurementsCache` 覆盖全部 index，与窗口无关。
  v.getTotalSize() // 触发 getMeasurements 重算（memo 失效时）
  const item = v.measurementsCache[index]
  if (!item) throw new Error(`index ${index} not measured`)
  return item.start
}

describe('Virtualizer 陈旧行高 → 追加行错位（真实库复现）', () => {
  it('缓存停在小尺寸时，追加行起点按旧尺寸算（= 用户看到的 ~115px 重叠）', () => {
    const { v, grow } = makeVirtualizer(6)
    v.measure()
    v.getTotalSize() // 先填充 measurementsCache —— 否则 measureElement 会因
    // `measurementsCache[index]` 未定义而直接 return（实测）
    // 6 行都被测成 115px —— 第 5 行真实高度后来长到 1118px，但 RO 没有上报
    // （乱序/滞后 entry 被忽略后尺寸不再变化 ⇒ 永久固化）。
    for (let i = 0; i < 6; i++) v.measureElement(mountRow(i, 115))
    expect(v.measurementsCache[5]!.size).toBe(115)

    // 追加无 turn 的输出行（index 6）：起点 = 5 × 115（陈旧），而不是 5×115+1118。
    grow(7)
    expect(startOf(v, 6)).toBe(6 * 115)
  })

  it('修复三步（measure() → 逐个已挂载行读真实几何）必须把起点校正回来', () => {
    const { v, grow } = makeVirtualizer(6)
    v.measure()
    v.getTotalSize() // 先填充 measurementsCache —— 否则 measureElement 会因
    // `measurementsCache[index]` 未定义而直接 return（实测）
    for (let i = 0; i < 6; i++) v.measureElement(mountRow(i, 115))
    grow(7)
    expect(startOf(v, 6)).toBe(6 * 115) // 错位状态（修复前）

    // ① measure() 清尺寸缓存 ② 逐个已挂载行读**当前真实几何**（第 5 行 = 1118）
    v.measure()
    v.getTotalSize() // 先填充 measurementsCache —— 否则 measureElement 会因
    // `measurementsCache[index]` 未定义而直接 return（实测）
    for (let i = 0; i < 6; i++) v.measureElement(mountRow(i, i === 5 ? 1118 : 115))

    // ⚠️ 断言前必须触发一次重算：resizeItem 只把新尺寸写进 itemSizeCache 并标记
    // pending，`measurementsCache` 在**下一次** getMeasurements() 调用时才刷新。
    v.getTotalSize()
    expect(v.measurementsCache[5]!.size).toBe(1118)
    // ③ 校正后：追加行起点 = 第 5 行的真实底边（不再重叠）
    expect(startOf(v, 6)).toBe(5 * 115 + 1118)
  })

  it('只 measure() 不做逐行重测不足以修正（证明第二步不可省）', () => {
    const { v, grow } = makeVirtualizer(6)
    v.measure()
    v.getTotalSize() // 先填充 measurementsCache —— 否则 measureElement 会因
    // `measurementsCache[index]` 未定义而直接 return（实测）
    for (let i = 0; i < 6; i++) v.measureElement(mountRow(i, 115))
    grow(7)

    // measure() 只清缓存 → 未实测行回落到 estimateSize（115）→ 起点仍是旧位置。
    v.measure()
    v.getTotalSize() // 先填充 measurementsCache —— 否则 measureElement 会因
    // `measurementsCache[index]` 未定义而直接 return（实测）
    expect(startOf(v, 6)).toBe(6 * 115)
  })
})
