/**
 * StagingTray header structure.
 *
 * Regression (2026-09-11): the header was an outer <button> that CONTAINED
 * another <button> ("收起"), which is invalid HTML and double-fires clicks
 * (same trap as the nested checkbox in AskUserPanel). It also rendered two
 * chevrons at once when the list was expanded, and the label carried a 📨
 * emoji on top of the lucide Inbox icon (redundant, and emoji render as tofu
 * boxes wherever the environment lacks an emoji font).
 */
import { describe, expect, it } from 'vitest'
import '@testing-library/jest-dom'

import { fireEvent } from '@testing-library/react'

import { renderWithProviders } from '@/test-utils'
import { StagingTray } from '@/components/agent/StagingTray'
import type { QueueItemPayload } from '@/types/shared'

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
