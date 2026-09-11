/**
 * StagingTray — 表头结构（#364）+ 拖动调序（#366）。
 *
 * 表头回归（2026-09-11）：外层曾是 <button> 且内含另一个 <button>（"收起"），
 * 既是无效 HTML 又会双触发点击（同 AskUserPanel 的嵌套 checkbox 坑）；列表
 * 展开时还会同时渲染两个 chevron；标签上叠了一个 📨 emoji（多余，且在缺 emoji
 * 字体的环境渲染成方框）。现结构：外层 <div>（容器）+ 折叠开关 <button>
 * （唯一一个状态 chevron）+ 「收起列表」按钮作为兄弟节点。
 *
 * 拖拽调序：jsdom 没有布局引擎，也没有 PointerEvent —— 需要三处 polyfill：
 *   1. PointerEvent 用 MouseEvent 派生（clientX/clientY 由此可得）；
 *   2. document.elementFromPoint 返回测试指定的卡片；
 *   3. 给卡片打 getBoundingClientRect 桩（决定落点是 before 还是 after）。
 */
import { beforeAll, describe, expect, it, vi } from 'vitest'
import { fireEvent, screen, within } from '@testing-library/react'
import '@testing-library/jest-dom'

import { renderWithProviders } from '@/test-utils'
import { StagingTray } from './StagingTray'
import type { QueueItemPayload } from '@/types/shared'

beforeAll(() => {
  // jsdom lacks PointerEvent — MouseEvent carries clientX/clientY.
  if (typeof window.PointerEvent === 'undefined') {
    class PointerEventPolyfill extends MouseEvent {
      pointerId: number
      constructor(type: string, params: PointerEventInit = {}) {
        super(type, params as MouseEventInit)
        this.pointerId = (params as { pointerId?: number }).pointerId ?? 1
      }
    }
    // @ts-expect-error test polyfill
    window.PointerEvent = PointerEventPolyfill
  }
})

function makeItem(msgID: string, preview = msgID): QueueItemPayload {
  return {
    msg_id: msgID,
    turn_id: 7,
    content: preview,
    preview,
    source: 'user',
    enqueued_at: Date.now(),
  }
}

function queueItem(i: number): QueueItemPayload {
  return {
    msg_id: `m${i}`,
    turn_id: 1040 + i,
    content: `/goal 继续迭代 ${i}`,
    preview: `/goal 继续迭代 ${i}`,
    source: 'user',
    enqueued_at: Date.now(),
  }
}

// ─── 表头结构（#364）────────────────────────────────────────────────

function renderTray(count = 2) {
  return renderWithProviders(
    <StagingTray
      items={Array.from({ length: count }, (_, i) => queueItem(i))}
      busy={true}
      onCancel={() => {}}
      onInterject={() => {}}
      onClear={() => {}}
    />,
  )
}

const chevronsIn = (root: Element) =>
  Array.from(root.querySelectorAll('svg')).filter((s) => {
    const cls = s.getAttribute('class') ?? ''
    return cls.includes('lucide-chevron-down') || cls.includes('lucide-chevron-right')
  })

describe('StagingTray header', () => {
  it('never nests a <button> inside another <button>', () => {
    const { container } = renderTray()
    const nested = Array.from(container.querySelectorAll('button')).filter(
      (b) => b.querySelector('button') !== null,
    )
    expect(
      nested.map((b) => b.getAttribute('aria-label') ?? (b.textContent ?? '').slice(0, 24)),
    ).toEqual([])
  })

  it('exposes exactly one collapse-state chevron in the header toggle', () => {
    const { container } = renderTray()
    const toggle = container.querySelector('button[aria-expanded]')
    expect(toggle).not.toBeNull()
    // Collapsed by default → a single ChevronRight; the ⋯ /收起 control (which
    // also carries a chevron) lives outside the toggle, not inside it.
    expect(chevronsIn(toggle as Element)).toHaveLength(1)
  })

  it('shows the ⋯/收起 control as a sibling, not inside the toggle', () => {
    const { container } = renderTray(8)
    const toggle = container.querySelector('button[aria-expanded]') as Element
    expect(chevronsIn(toggle)).toHaveLength(1)
    // The list is collapsed by default, so nothing else is rendered yet; the
    // point is only that the toggle itself holds one indicator.
    expect(container.querySelectorAll('button[aria-expanded]')).toHaveLength(1)
  })

  it('labels the tray without an emoji (the icon comes from lucide)', () => {
    const { container } = renderTray()
    expect(container.textContent ?? '').not.toContain('📨')
  })

  it('still toggles collapse from the header', () => {
    const { container } = renderTray()
    expect(container.querySelector('button[aria-expanded]')?.getAttribute('aria-expanded')).toBe('false')
    // fireEvent wraps the click in act() so React flushes the state update.
    fireEvent.click(container.querySelector('button[aria-expanded]') as Element)
    const toggle = container.querySelector('button[aria-expanded]') as Element
    expect(toggle.getAttribute('aria-expanded')).toBe('true')
    // Expanded → the single indicator flips to ChevronDown (still exactly one).
    expect(chevronsIn(toggle)).toHaveLength(1)
  })
})

// ─── 拖动调序（#366）───────────────────────────────────────────────

/** jsdom returns zero rects — stub the geometry the drop logic measures. */
function stubRect(el: HTMLElement, top: number, height = 40) {
  el.getBoundingClientRect = () =>
    ({ top, bottom: top + height, height, left: 0, right: 200, width: 200, x: 0, y: top, toJSON: () => ({}) }) as DOMRect
}

function cardFor(msgID: string): HTMLElement {
  const card = document.querySelector(`[data-queue-id="${msgID}"]`)
  if (!card) throw new Error(`card ${msgID} not found`)
  return card as HTMLElement
}

function expandTray() {
  // 默认折叠 —— 点表头开关展开卡片列表
  fireEvent.click(screen.getByTestId('staging-toggle'))
}

/** Drag `srcID`'s handle onto `targetID`, landing on its top (before) or bottom (after).
 *  Pointer events go to the HANDLE — it captures the pointer on pointerdown, so
 *  move/up are retargeted there (no global window listeners by design). */
function dragOnto(srcID: string, targetID: string, edge: 'before' | 'after') {
  const handle = within(cardFor(srcID)).getByTestId('staging-drag-handle')
  fireEvent.pointerDown(handle)
  const target = cardFor(targetID)
  stubRect(target, 100)
  const y = edge === 'before' ? 105 : 135
  document.elementFromPoint = () => target
  fireEvent.pointerMove(handle, { clientX: 10, clientY: y })
  fireEvent.pointerUp(handle)
}

function setup(items: QueueItemPayload[], onReorder?: (ids: string[]) => void) {
  renderWithProviders(
    <StagingTray
      items={items}
      busy
      onCancel={() => {}}
      onInterject={() => {}}
      onClear={() => {}}
      onReorder={onReorder}
    />,
  )
  expandTray()
}

describe('StagingTray drag-to-reorder', () => {
  it('shows a drag handle per card when reordering is wired up', () => {
    setup([makeItem('a'), makeItem('b')], () => {})
    expect(screen.getAllByTestId('staging-drag-handle')).toHaveLength(2)
  })

  it('hides drag handles when no onReorder handler is provided (read-only tray)', () => {
    setup([makeItem('a'), makeItem('b')])
    expect(screen.queryAllByTestId('staging-drag-handle')).toHaveLength(0)
  })

  it('commits the new order when a card is dragged below another one', () => {
    const onReorder = vi.fn()
    setup([makeItem('a'), makeItem('b'), makeItem('c')], onReorder)
    dragOnto('a', 'c', 'after')
    expect(onReorder).toHaveBeenCalledWith(['b', 'c', 'a'])
  })

  it('commits the new order when a card is dragged above another one', () => {
    const onReorder = vi.fn()
    setup([makeItem('a'), makeItem('b'), makeItem('c')], onReorder)
    dragOnto('c', 'a', 'before')
    expect(onReorder).toHaveBeenCalledWith(['c', 'a', 'b'])
  })

  it('does not call onReorder when the drag is a no-op (dropped in place)', () => {
    const onReorder = vi.fn()
    setup([makeItem('a'), makeItem('b')], onReorder)
    dragOnto('a', 'a', 'after')
    expect(onReorder).not.toHaveBeenCalled()
  })

  it('renders a drop indicator while dragging, cleared on drop', () => {
    const onReorder = vi.fn()
    setup([makeItem('a'), makeItem('b')], onReorder)
    const handle = within(cardFor('a')).getByTestId('staging-drag-handle')
    fireEvent.pointerDown(handle)
    const target = cardFor('b')
    stubRect(target, 100)
    document.elementFromPoint = () => target
    fireEvent.pointerMove(handle, { clientX: 10, clientY: 135 })
    expect(screen.getAllByTestId('staging-drop-line')).toHaveLength(1)
    fireEvent.pointerUp(handle)
    expect(screen.queryAllByTestId('staging-drop-line')).toHaveLength(0)
  })

  it('skips entries without a msg_id (unaddressable notification rows)', () => {
    setup([makeItem(''), makeItem('a')], () => {})
    expect(screen.getAllByTestId('staging-drag-handle')).toHaveLength(1)
  })
})
