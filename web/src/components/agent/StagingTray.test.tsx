/**
 * StagingTray — 唯一展开 + 结构化对齐（重新设计 2026-09-13）+ 拖动调序（#366）。
 *
 * 上一版被否的两条设计缺陷，本文件用断言钉死「不得回退」：
 *   1. **两级展开**（面板 collapsed + 列表 expanded/MAX_VISIBLE=3 + footer 的
 *      Show all/Show fewer）→ 现在**只有一个展开概念**：header 的 toggle。
 *      展开 = 全部队列项渲染，长队列靠容器内部滚动（几何在有界性由 e2e 断言）。
 *   2. **靠缩进凑对齐**（`▸ Next to run` 是卡片下方兄弟节点，用 pl-8 去凑预览
 *      文本的 x）→ 现在 Next 徽章是 `staging-card-preview` 容器的第一个子元素，
 *      与预览文本同一条 flex 行 ⇒ 左边界由**结构**保证。真实像素级断言在 e2e
 *      （jsdom 无布局引擎），这里只断言结构关系。
 *   3. header 一行两个控件：toggle（唯一 chevron）+ clear（Trash2 图标按钮，
 *      独立动作）；不得出现 `Collapse` 文案，也不得出现缩进魔数。
 *
 * 拖拽调序：jsdom 没有布局引擎，也没有 PointerEvent —— 需要三处 polyfill：
 *   1. PointerEvent 用 MouseEvent 派生（clientX/clientY 由此可得）；
 *   2. document.elementFromPoint 返回测试指定的卡片；
 *   3. 给卡片打 getBoundingClientRect 桩（决定落点是 before 还是 after）。
 */
import { beforeAll, describe, expect, it, vi } from 'vitest'
import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import '@testing-library/jest-dom'

// 源码级 grep 断言：用 vite 的 `?raw` 把源文件当字符串读入（避免依赖 node 类型）。
import stagingSrc from './StagingTray.tsx?raw'
import zhCNSrc from '../../i18n/zh-CN.ts?raw'
import enSrc from '../../i18n/en.ts?raw'
import jaSrc from '../../i18n/ja.ts?raw'

import { renderWithProviders } from '@/test-utils'
import i18n from '@/i18n'
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

const t = (key: string, params?: Record<string, string | number>) => i18n.t(key, params) as string

// ─── 表头 / 布局结构 ────────────────────────────────────────────────

function renderTray(count = 2, busy = true) {
  return renderWithProviders(
    <StagingTray
      items={Array.from({ length: count }, (_, i) => queueItem(i))}
      busy={busy}
      onCancel={() => {}}
      onInterject={() => {}}
      onClear={() => {}}
    />,
  )
}

/** Default tray is collapsed — click the toggle to reveal the card list. */
function expandTray() {
  fireEvent.click(screen.getByTestId('staging-toggle'))
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
    expect(chevronsIn(toggle as Element)).toHaveLength(1)
  })

  it('renders exactly two header controls: the toggle and the clear button', () => {
    const { container } = renderTray(8)
    const header = container.querySelector('[data-testid="staging-header"]') as Element
    // 展开前：header = toggle + clear（两个不同动作，不是两个「收起」）。
    const buttons = header.querySelectorAll('button')
    expect(buttons).toHaveLength(2)
    expect(buttons[0]).toBe(screen.getByTestId('staging-toggle'))
    expect(buttons[1]).toBe(screen.getByTestId('staging-clear'))
    // 清空按钮不在 toggle 内部（否则是嵌套 button / 点击双触发）。
    expect(screen.getByTestId('staging-toggle').contains(screen.getByTestId('staging-clear'))).toBe(false)
    expect(container.querySelectorAll('button[aria-expanded]')).toHaveLength(1)

    // 展开后：header 结构不变 —— 仍然只有这两个按钮，chevron 仍恰好一个。
    expandTray()
    const headerAfter = container.querySelector('[data-testid="staging-header"]') as Element
    const toggles = headerAfter.querySelectorAll('button')
    expect(toggles).toHaveLength(2)
    expect(toggles[0]).toBe(container.querySelector('[data-testid="staging-toggle"]'))
    expect(toggles[1]).toBe(container.querySelector('[data-testid="staging-clear"]'))
    expect(chevronsIn(toggles[0])).toHaveLength(1)
    expect(chevronsIn(headerAfter)).toHaveLength(1)
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

// ─── 布局重写（#374）：Next 标签 / 缩进 / footer 单行 ─────────────────

function renderExpanded(count = 4, busy = true) {
  const utils = renderWithProviders(
    <StagingTray
      items={Array.from({ length: count }, (_, i) => queueItem(i))}
      busy={busy}
      onCancel={() => {}}
      onInterject={() => {}}
      onClear={() => {}}
    />,
  )
  expandTray()
  return utils
}

describe('StagingTray layout (single expand + structural alignment)', () => {
  it('renders the Next marker INSIDE the card preview row — no detached hint line', () => {
    const { container } = renderExpanded(4)
    // 旧设计的「卡片下方兄弟行 + pl-8 缩进」节点已彻底删除。
    expect(screen.queryByTestId('staging-next-hint')).toBeNull()

    const mark = screen.getByTestId('staging-next-mark')
    expect(mark.textContent).toBe(t('agent.staging.next'))

    const headCard = container.querySelector('[data-queue-id="m0"]') as Element
    expect(headCard.classList.contains('staging-card')).toBe(true)
    const preview = headCard.querySelector('[data-testid="staging-card-preview"]') as Element
    expect(preview).not.toBeNull()
    // 标记 = preview 容器的第一个子元素 ⇒ 与预览文本同一条布局链
    // （左边界相同由该结构保证；像素级断言见 e2e）。
    expect(mark.parentElement).toBe(preview)
    expect(preview.firstElementChild).toBe(mark)
    // 标记与预览文本同处一个「卡片内容行」。内容行只有一条 = 没有第二行。
    const row = headCard.querySelector('[data-testid="staging-card-row"]') as Element
    expect(row).not.toBeNull()
    expect(row.contains(preview)).toBe(true)
    expect(headCard.querySelectorAll('[data-testid="staging-card-row"]')).toHaveLength(1)
    // 标记不再靠绝对定位脱离布局链。
    expect((mark.getAttribute('class') ?? '')).not.toContain('absolute')
    // 队首卡另有左侧 accent 条（2px）—— 第二重「这是队首」的结构标记（零魔数）。
    expect(headCard.getAttribute('class') ?? '').toMatch(/border-l-2/)
    expect(headCard.getAttribute('class') ?? '').toMatch(/border-l-indigo-500/)
    // 内容行只有一条 flex 行 → 标记与预览文本不会换行到第二行（几何见 e2e）。
    expect(headCard.querySelectorAll('[data-testid="staging-card-row"]')).toHaveLength(1)
  })

  it('shows no Next marker when the tray is not busy', () => {
    renderExpanded(4, false)
    expect(screen.queryByTestId('staging-next-mark')).toBeNull()
  })

  it('places no indent magic numbers on the marker or its row', () => {
    const { container } = renderExpanded(4)
    const mark = screen.getByTestId('staging-next-mark')
    const cls = mark.getAttribute('class') ?? ''
    // 不得用 pl-*/ml-* 猜值、pl-[calc(...)] 魔数或绝对定位来「凑」对齐。
    expect(cls).not.toMatch(/(^|\s)(pl-|ml-|pl-\[|ml-\[|left-)/)
    expect(container.querySelectorAll('[class*="pl-8.5"], [class*="pl-[calc"], [class*="ml-[calc"]')).toHaveLength(0)
  })

  it('has no footer and no second expand/overflow control', () => {
    const { container } = renderExpanded(6)
    expect(container.querySelector('[data-testid="staging-footer"]')).toBeNull()
    expect(screen.queryByText(/Show all|Show fewer|显示全部|收起列表|すべて表示|折りたたむ/)).toBeNull()
  })

  it('renders every queued item when expanded (no 3-item truncation)', () => {
    const { container } = renderExpanded(12)
    expect(container.querySelectorAll('[data-queue-id]')).toHaveLength(12)
    expect(screen.getByTestId('staging-list').children).toHaveLength(12)
  })

  it('never renders a "Collapse" label (collapse is expressed by the chevron only)', () => {
    const { container } = renderExpanded(4)
    const text = container.textContent ?? ''
    for (const copy of ['Collapse', '收起', '折叠', '折りたたむ', 'Show all', 'Show fewer', '显示全部']) {
      expect(text).not.toContain(copy)
    }
    const labels = Array.from(container.querySelectorAll('[aria-label]')).map((el) => el.getAttribute('aria-label') ?? '')
    expect(labels.join('|')).not.toMatch(/Collapse|收起|折叠/)
  })
})

// ─── 源码级防回退：不得再有第二级展开 ─────────────────────────────

describe('StagingTray source guards (no leftover two-level expand)', () => {
  const src = stagingSrc
  // aria-expanded 是合法 a11y 属性，先剥掉再查 expanded 状态残留。
  const stripped = src.replace(/aria-expanded/g, 'aria-state')

  it('has no MAX_VISIBLE truncation in the component source', () => {
    expect(src).not.toMatch(/MAX_VISIBLE/)
  })

  it('has no `expanded` state left in the component source', () => {
    expect(stripped).not.toMatch(/expanded/)
    expect(stripped).not.toMatch(/setExpanded/)
  })

  it('references no removed i18n keys and they no longer exist in any locale', () => {
    expect(src).not.toMatch(/showAll|showFewer|staging\.collapse/)
    for (const dict of [zhCNSrc, enSrc, jaSrc]) {
      const staging = dict.slice(dict.indexOf('staging: {'))
      const block = staging.slice(0, staging.indexOf('\n    },'))
      expect(block).not.toMatch(/showAll|showFewer|collapse:/)
    }
  })
})

// ─── 行为契约（取消 / 插话 / 清空 / MAX_VISIBLE）────────────────────

describe('StagingTray actions', () => {
  it('calls onInterject with the card msg_id', () => {
    const onInterject = vi.fn()
    renderWithProviders(
      <StagingTray items={[makeItem('a'), makeItem('b')]} busy onCancel={() => {}} onInterject={onInterject} onClear={() => {}} />,
    )
    expandTray()
    fireEvent.click(screen.getAllByLabelText(t('agent.staging.toInterject'))[0])
    expect(onInterject).toHaveBeenCalledWith('a')
  })

  it('calls onCancel after the leave animation', async () => {
    const onCancel = vi.fn()
    renderWithProviders(
      <StagingTray items={[makeItem('a')]} busy onCancel={onCancel} onInterject={() => {}} onClear={() => {}} />,
    )
    expandTray()
    fireEvent.click(screen.getByLabelText(t('common.cancel')))
    await waitFor(() => expect(onCancel).toHaveBeenCalledWith('a'), { timeout: 1000 })
  })

  it('calls onClear when the clear button is pressed', async () => {
    const onClear = vi.fn()
    renderWithProviders(
      <StagingTray items={[makeItem('a'), makeItem('b')]} busy onCancel={() => {}} onInterject={() => {}} onClear={onClear} />,
    )
    expandTray()
    fireEvent.click(screen.getByLabelText(t('agent.staging.clearQueue')))
    await waitFor(() => expect(onClear).toHaveBeenCalledTimes(1), { timeout: 1000 })
  })

  it('caps the visible list at MAX_VISIBLE=3 until overflow is expanded', () => {
    renderExpanded(5)
    expect(document.querySelectorAll('[data-queue-id]')).toHaveLength(5)
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
