/**
 * RED→GREEN guard（2026-09-17，真实浏览器事故）—— **追加行时必须有权威重测**。
 *
 * 事故（用户 `!pwd` 输出"看不见"，真实浏览器取证）：长 assistant 行 DOM 实测
 * 1118.78px，但 virtualizer 缓存里仍是它早期的小尺寸（~115px）→ 追加的无 turn
 * standalone 输出行 `item.start` 只比上一行起点多 115px → 两个绝对定位行重叠
 * ~1004px ⇒ 输出**在 DOM 里但被上一行盖住**；滚动到底也没用（总高按旧尺寸算完，
 * scrollTop 到底 ≠ 看到输出）。
 *
 * 根因：TanStack 只在 `resizeItem` 时从该 index 往后重算 offsets，而 ResizeObserver
 * 的 entry 可能乱序/滞后（`entry.borderBoxSize` 是观察时刻的快照）→ 迟到的旧值把
 * 真值覆盖回去，此后尺寸不再变化 ⇒ RO 不再上报 ⇒ 旧值永久固化；且
 * `getMeasurements` 的 memo **不依赖 estimateSize**，未实测行会一直沿用 memo 住的
 * 旧尺寸。
 *
 * 修复（MessageList 的 append effect，三步顺序不可换）：`measure()` 清缓存 →
 * 逐个已挂载行读**当前真实几何** → 校正后贴底。
 *
 * 本文件用 mock virtualizer 在 jsdom 里断言**机制**（真实布局由
 * `web/e2e/standalone-command-layout.spec.ts` 在真实 Chromium 里断言）。
 */
import { act, render } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom'

// ── mock @tanstack/react-virtual ────────────────────────────────────────────
// 记录调用顺序：`measure()`（清缓存）必须早于逐个 `measureElement(node)`
// （读真实几何）；两者都必须发生在「尾部追加」时。
const v = vi.hoisted(() => {
  let count = 0
  const calls: string[] = []
  const inst = {
    getVirtualItems: () =>
      Array.from({ length: count }, (_, i) => ({
        index: i,
        key: `k${i}`,
        start: i * 100,
        size: 100,
        end: (i + 1) * 100,
      })),
    getTotalSize: () => count * 100,
    measure: vi.fn(() => {
      calls.push('measure')
    }),
    measureElement: vi.fn((node: HTMLElement | null) => {
      calls.push(node ? `measureElement:${node.dataset?.index ?? '?'}` : 'measureElement:null')
    }),
    scrollToIndex: vi.fn(),
    scrollRect: { width: 800, height: 600 },
    shouldAdjustScrollPositionOnItemSizeChange: undefined,
  }
  return { inst, calls, setCount: (n: number) => { count = n } }
})

vi.mock('@tanstack/react-virtual', () => ({
  useVirtualizer: (opts: { count: number }) => {
    v.setCount(opts.count)
    return v.inst
  },
  observeElementOffset: () => () => {},
  observeElementRect: () => () => {},
  measureElement: () => 100,
}))

import { MessageList, remeasureMountedRows } from '@/components/agent/MessageList'
import { I18nProvider } from '@/providers/i18n'
import { EMPTY_LIVE_PROGRESS } from '@/types/agent'
import type { ChatMessage } from '@/types/agent'

function makeMessages(n: number): ChatMessage[] {
  return Array.from({ length: n }, (_, i) => ({
    id: `m${i}`,
    role: (i % 2 === 0 ? 'user' : 'assistant') as ChatMessage['role'],
    content: `message ${i}`,
    iterations: [],
    timestamp: '',
    isPartial: false,
    turnID: i === n - 1 ? 1 : 0,
  }))
}

function renderList(messages: ChatMessage[]) {
  return render(
    <MessageList messages={messages} chatKey="chat-1" liveProgress={EMPTY_LIVE_PROGRESS} loading={false} error={null} />,
    { wrapper: ({ children }) => <I18nProvider>{children}</I18nProvider> },
  )
}

describe('remeasureMountedRows（纯函数）', () => {
  it('重测容器内每个 .virt-row[data-index]，其他子节点不参与', () => {
    const root = document.createElement('div')
    root.innerHTML = `
      <div class="virt-row" data-index="0"></div>
      <div class="not-a-row"></div>
      <div class="virt-row" data-index="1"></div>
      <div><div class="virt-row" data-index="2"></div></div>
    `
    const seen: string[] = []
    const n = remeasureMountedRows(root, (node) => seen.push(node.dataset.index ?? ''))
    expect(n).toBe(3)
    expect(seen).toEqual(['0', '1', '2'])
  })

  it('root 为空时返回 0（不抛）', () => {
    expect(remeasureMountedRows(null, () => {})).toBe(0)
  })
})

describe('追加行 ⇒ 权威重测（真实事故的机制守护）', () => {
  beforeEach(() => {
    v.calls.length = 0
    v.inst.measure.mockClear()
    v.inst.measureElement.mockClear()
  })

  it('尾部追加时：先 measure() 清缓存，再逐个已挂载行 measureElement', () => {
    const { rerender } = renderList(makeMessages(2))
    v.calls.length = 0 // 忽略挂载期的 ref 测量

    act(() => {
      rerender(<MessageList messages={makeMessages(3)} chatKey="chat-1" liveProgress={EMPTY_LIVE_PROGRESS} loading={false} error={null} />)
    })

    // 修复前：这里一次 measure/measureElement 都不会发生（缓存旧值固化 → 行重叠）
    expect(v.inst.measure, '追加时必须清尺寸缓存（否则未实测行沿用 memo 住的旧尺寸）').toHaveBeenCalled()
    const mi = v.calls.indexOf('measure')
    const after = v.calls.slice(mi + 1)
    // ⚠️ index 0 那行在追加时**不会**重新触发 ref（节点复用）——它出现在 measure()
    // 之后，正是"已挂载老行被权威重测"的证据（修复前不可能有）。
    expect(after, '追加后必须重测已挂载的老行（index 0）').toContain('measureElement:0')
    expect(after.filter((c) => c.startsWith('measureElement:')).length).toBeGreaterThan(0)
  })

  it('前插（loadMore）不触发重测 —— 其视口锚定由 restoreLoadMoreAnchor 负责', () => {
    const { rerender } = renderList(makeMessages(3))
    v.calls.length = 0

    act(() => {
      const msgs = makeMessages(3)
      rerender(
        <MessageList
          messages={[{ ...msgs[0], id: 'm-older' }, ...msgs]}
          chatKey="chat-1"
          liveProgress={EMPTY_LIVE_PROGRESS} loading={false} error={null}
        />,
      )
    })

    expect(v.inst.measure).not.toHaveBeenCalled()
  })
})
