/**
 * 复现 React #185（Maximum update depth exceeded）—— 2026-09-24 生产崩溃：
 * `layout effect → flushMeasure → resizeItem → notify → onChange(rerender) →
 * layout effect → …` 50+ 层嵌套更新。
 *
 * 机制：MessageList 的 getItemKey 用 `turn-${turnID}-${role}`（刻意让 live→committed
 * 行复用同一元素）。TanStack 的 itemSizeCache 按 **key** 记账 —— 同一 turn 出现
 * 两行 assistant（live 行 + committed 行共存 / 同 turn 多条 assistant 行）时，
 * 两个 index 共享一个 key：
 *   resizeItem(i₁, h₁) → itemSizeCache[key] = h₁（delta≠0 → notify）
 *   resizeItem(i₂, h₂) → itemSizeCache[key] = h₂（delta≠0 → notify）
 *   下一轮 resizeItem(i₁, h₁) → itemSize = h₂ → delta≠0 → notify ……
 * **每轮都 notify** ⇒ 无限嵌套重渲染 ⇒ React #185。
 *
 * 与 mock 版（measureOnAppend.test.tsx）互补：这里用**真实** Virtualizer
 * （virtual-core，jsdom 可跑）断言原语层面的振荡。
 */
import { describe, expect, it } from 'vitest'
import {
  Virtualizer,
  elementScroll,
  observeElementOffset,
  observeElementRect,
  type VirtualizerOptions,
} from '@tanstack/react-virtual'
import { buildUniqueRowKeys } from './MessageList'
import type { ChatMessage } from '@/types/shared'

function mountRow(index: number, height: number): HTMLDivElement {
  const el = document.createElement('div')
  el.setAttribute('data-index', String(index))
  Object.defineProperty(el, 'offsetHeight', { configurable: true, get: () => height })
  document.body.appendChild(el)
  return el
}

/**
 * 模拟 MessageList 的真实键策略：`turn-${turnID}-${role}`。
 * sameKeyTurn 模拟「同一 turn 的两行 assistant」（live 行 + committed 行共存）。
 */
function makeVirtualizer(sameKeyTurn: boolean) {
  const scroll = document.createElement('div')
  const rows = sameKeyTurn
    ? [
        { turnID: 7, role: 'assistant' }, // live 行（id="turn-7-live"）
        { turnID: 7, role: 'assistant' }, // committed 行（id=assistant.id）
      ]
    : [
        { turnID: 7, role: 'assistant' },
        { turnID: 8, role: 'assistant' },
      ]
  const opts: VirtualizerOptions<HTMLDivElement, HTMLDivElement> = {
    count: rows.length,
    getScrollElement: () => scroll,
    estimateSize: () => 100,
    getItemKey: (i: number) =>
      rows[i].turnID > 0 ? `turn-${rows[i].turnID}-${rows[i].role}` : `row-${i}`,
    initialRect: { width: 800, height: 700 },
    initialOffset: 0,
    observeElementRect,
    observeElementOffset,
    scrollToFn: elementScroll,
  }
  const v = new Virtualizer<HTMLDivElement, HTMLDivElement>(opts)
  return { v, rows }
}

/** 模拟 flushMeasure 的写尺寸循环（MessageList.tsx ② 写尺寸）。 */
function flushSizes(v: Virtualizer<HTMLDivElement, HTMLDivElement>, heights: number[]) {
  for (let i = 0; i < heights.length; i++) v.resizeItem(i, heights[i])
}

describe('Virtualizer 同 key 两行 → resizeItem 振荡（React #185 根因）', () => {
  it('不同 key（正常）：第二轮起 resizeItem 全部早退（delta=0，不再 notify）', () => {
    const { v } = makeVirtualizer(false)
    v.measure()
    v.getTotalSize()
    let notifies = 0
    ;(v as unknown as { notify: (sync: boolean) => void }).notify = () => notifies++
    // 两行高度不同（live 8660px / committed 91px 之类）
    mountRow(0, 300)
    mountRow(1, 500)
    flushSizes(v, [300, 500])
    const first = notifies
    expect(first).toBeGreaterThan(0) // 首轮确实有尺寸变化
    flushSizes(v, [300, 500])
    flushSizes(v, [300, 500])
    expect(notifies).toBe(first) // 收敛：不再 notify
  })

  it('同 key（live+committed 共存）：每轮 resizeItem 都 notify —— 无限嵌套更新（#185）', () => {
    const { v } = makeVirtualizer(true)
    v.measure()
    v.getTotalSize()
    let notifies = 0
    ;(v as unknown as { notify: (sync: boolean) => void }).notify = () => notifies++
    mountRow(0, 300) // live 行
    mountRow(1, 500) // committed 行（同 turn 同 role → 同 key）
    flushSizes(v, [300, 500])
    const first = notifies
    expect(first).toBeGreaterThan(0)
    // 模拟后续每轮 layout effect 的 flushMeasure（高度不变！）
    flushSizes(v, [300, 500])
    const second = notifies
    flushSizes(v, [300, 500])
    const third = notifies
    // 振荡：高度完全没变，notify 却每轮递增 —— resizeItem 的早退条件
    // （itemSizeCache.get(key) === size）永远不成立，因为另一行共享同一 key。
    expect(second).toBeGreaterThan(first)
    expect(third).toBeGreaterThan(second)
  })
})

/** 造一个最小 ChatMessage 行（buildUniqueRowKeys 只读 turnID/role/id）。 */
function row(partial: Partial<ChatMessage>): ChatMessage {
  return {
    id: 'x',
    role: 'assistant',
    content: '',
    iterations: [],
    timestamp: '',
    isPartial: false,
    turnID: 0,
    ...partial,
  } as ChatMessage
}

describe('buildUniqueRowKeys —— 行键全表唯一化（#185 修复）', () => {
  it('正常行：键保持规范形态（live→committed 复用同一元素不重挂的既有设计不变）', () => {
    const keys = buildUniqueRowKeys([
      row({ id: 'turn-7-live', role: 'assistant', turnID: 7, isPartial: true }),
      row({ id: 'u1', role: 'user', turnID: 7 }),
      row({ id: 'legacy-1', role: 'assistant', turnID: 0 }),
      row({ id: 'pending-1', role: 'user', turnID: Number.MAX_SAFE_INTEGER }),
    ])
    expect(keys).toEqual(['turn-7-assistant', 'turn-7-user', 'legacy-1', 'pending-1'])
  })

  it('同 (turnID, role) 两行（如 bindTurnIDs 把 turn_id=0 的 legacy 行绑到已有同 role 行的 turn）：首行保规范键，后续行加 #dup 后缀 —— 全表唯一', () => {
    const keys = buildUniqueRowKeys([
      row({ id: 'turn-7-live', role: 'assistant', turnID: 7, isPartial: true }),
      row({ id: 'legacy-empty', role: 'assistant', turnID: 7 }), // 被绑到 turn 7 的 legacy 行
      row({ id: 'u1', role: 'user', turnID: 7 }),
      row({ id: 'u2', role: 'user', turnID: 7 }), // 第二个 user 行（同 turn）
    ])
    expect(keys).toEqual(['turn-7-assistant', 'turn-7-assistant#dup1', 'turn-7-user', 'turn-7-user#dup1'])
    // 全表唯一 —— itemSizeCache 不再互覆，resizeItem 第二轮起早退（#185 消失）
    expect(new Set(keys).size).toBe(4)
  })
})
