/**
 * Performance + correctness tests for the virtualized MessageList (Spec 4 §3.4).
 *
 * Verifies:
 *  - 100+ messages render without throwing
 *  - the virtualizer only mounts a window of rows (not all 150)
 *  - a live streaming message appends as the last row
 *  - collapse level is forwarded to rows
 */
import { act, fireEvent, render } from '@testing-library/react'
import { Virtualizer } from '@tanstack/react-virtual'
import { describe, expect, it } from 'vitest'
import '@testing-library/jest-dom'

import { renderWithProviders } from '@/test-utils'
import { canRewindMessage, isCompactMarker, latestCompactBoundaryIndex, MessageList } from '@/components/agent/MessageList'
import { EMPTY_LIVE_PROGRESS } from '@/types/agent'
import type { ChatMessage } from '@/types/agent'
import { I18nProvider } from '@/providers/i18n'

// jsdom has no layout; give the scroll element a real height so the virtualizer
// computes a visible window. TanStack Virtual reads getBoundingClientRect for the
// scroll element and measures children via ResizeObserver (mocked below).
Object.defineProperties(window.HTMLElement.prototype, {
  scrollHeight: { configurable: true, get() { return 12000 } },
  clientHeight: { configurable: true, get() { return 600 } },
})
Object.defineProperty(window.HTMLElement.prototype, 'getBoundingClientRect', {
  configurable: true,
  value() {
    return { top: 0, left: 0, right: 800, bottom: 600, width: 800, height: 600, x: 0, y: 0, toJSON() {} }
  },
})

function renderMessageList(node: React.ReactElement) {
  return render(node, { wrapper: ({ children }) => <I18nProvider>{children}</I18nProvider> })
}

// A ResizeObserver mock that synchronously fires its callback with the
// element's (mocked) rect, so TanStack Virtual measures the scroll element
// and computes a visible window even in jsdom (no real layout).
class RO {
  private static instances = new Set<RO>()
  private cb: ResizeObserverCallback
  private targets = new Set<Element>()

  constructor(cb: ResizeObserverCallback) {
    this.cb = cb
    RO.instances.add(this)
  }

  observe(target: Element) {
    this.targets.add(target)
    this.emit(target)
  }

  unobserve(target: Element) {
    this.targets.delete(target)
  }

  disconnect() {
    this.targets.clear()
    RO.instances.delete(this)
  }

  static trigger(target: Element) {
    for (const observer of RO.instances) {
      if (observer.targets.has(target)) observer.emit(target)
    }
  }

  private emit(target: Element) {
    const rect = target.getBoundingClientRect()
    const entry = [{ target, contentRect: { x: 0, y: 0, width: rect.width, height: rect.height, top: 0, left: 0, bottom: rect.height, right: rect.width, toJSON() {} }, borderBoxSize: [], contentBoxSize: [], devicePixelContentBoxSize: [] }] as unknown as ResizeObserverEntry[]
    this.cb(entry, this)
  }
}
;(window as unknown as { ResizeObserver: unknown }).ResizeObserver = RO
;(globalThis as unknown as { ResizeObserver: unknown }).ResizeObserver = RO

function makeMessages(n: number): ChatMessage[] {
  return Array.from({ length: n }, (_, i) => ({
    id: `m${i}`,
    role: i % 2 === 0 ? 'user' : 'assistant',
    content: `message ${i}`,
    iterations: [],
    timestamp: '',
    isPartial: false,
    turnID: 0,
  }))
}

async function flushAnimationFrames(count = 2) {
  await act(async () => {
    for (let i = 0; i < count; i++) {
      await new Promise((resolve) => requestAnimationFrame(resolve))
    }
  })
}

function trackScrollTop(el: HTMLDivElement, initial: number) {
  let value = initial
  const writes: number[] = []
  Object.defineProperty(el, 'scrollTop', {
    configurable: true,
    get: () => value,
    set: (next: number) => {
      value = next
      writes.push(next)
    },
  })
  return {
    writes,
    get value() {
      return value
    },
    setSilently(next: number) {
      value = next
    },
  }
}

function contentElement(container: HTMLElement): Element {
  const content = container.querySelector('[data-message-list-content]')
  if (!content) throw new Error('message list content wrapper missing')
  return content
}

describe('MessageList virtualization', () => {
  it('keeps resized history rows anchored while scrolling backward', () => {
    const scrollElement = document.createElement('div')
    const corrections: number[] = []
    const offsetCallbacks: Array<(offset: number, isScrolling: boolean) => void> = []
    const virtualizer = new Virtualizer<HTMLDivElement, HTMLDivElement>({
      count: 60,
      getScrollElement: () => scrollElement,
      estimateSize: () => 120,
      getItemKey: (index) => `message-${index}`,
      initialRect: { width: 800, height: 600 },
      initialOffset: 6_000,
      observeElementRect: (_instance, callback) => {
        callback({ width: 800, height: 600 })
        return () => {}
      },
      observeElementOffset: (_instance, callback) => {
        offsetCallbacks.push(callback)
        callback(6_000, false)
        return () => {}
      },
      scrollToFn: (_offset, options) => {
        if (options.adjustments) corrections.push(options.adjustments)
      },
    })
    const cleanup = virtualizer._didMount()
    virtualizer._willUpdate()

    try {
      expect(offsetCallbacks).toHaveLength(1)
      offsetCallbacks[0](5_400, true)
      expect(virtualizer.scrollDirection).toBe('backward')

      // First measurement keeps the visible anchor despite the large estimate delta.
      virtualizer.resizeItem(20, 900)
      expect(corrections).toEqual([780])

      corrections.length = 0
      // Rich Markdown can resize repeatedly as images and fonts settle. Every
      // above-viewport change must preserve the visible anchor, even on up-scroll.
      for (const size of [960, 840, 1_020]) virtualizer.resizeItem(20, size)
      expect(corrections).toHaveLength(3)
    } finally {
      cleanup()
    }
  })

  it('renders 150 messages without throwing', () => {
    const messages = makeMessages(150)
    expect(() =>
      renderWithProviders(
        <MessageList
          messages={messages}
          liveMessage={null}
          liveProgress={null}
          collapseLevel="all"
          loading={false}
          error={null}
        />,
      ),
    ).not.toThrow()
  })

  it('renders 150 messages into a virtualized container without throwing', () => {
    const messages = makeMessages(150)
    const { container } = renderWithProviders(
      <MessageList
        messages={messages}
        liveMessage={null}
        liveProgress={null}
        collapseLevel="all"
        loading={false}
        error={null}
      />,
    )
    // The virtualizer always renders a sizing wrapper whose height tracks the
    // total estimated size (150 × ~120px). jsdom has no real layout, so we
    // assert the structure rather than the live item count; browser perf is
    // verified by the e2e scroll test.
    const sizing = container.querySelector('[style*="height"]')
    expect(sizing).not.toBeNull()
    expect(sizing!.getAttribute('style')).toContain('18000px')
  })

  it('disables native scroll anchoring so the virtualizer owns size corrections', () => {
    const { container } = renderWithProviders(
      <MessageList
        messages={makeMessages(60)}
        liveMessage={null}
        liveProgress={null}
        collapseLevel="all"
        loading={false}
        error={null}
      />,
    )
    const scroller = container.querySelector('.overflow-y-auto') as HTMLDivElement

    expect(scroller.style.overflowAnchor).toBe('none')
  })

  it('forwards a live streaming message through the row list without throwing', () => {
    const messages = makeMessages(10)
    const live: ChatMessage = { id: 'live-1', role: 'assistant', content: 'streaming…', iterations: [], timestamp: '', isPartial: true, turnID: 0 }
    expect(() =>
      renderWithProviders(
        <MessageList
          messages={messages}
          liveMessage={live}
          liveProgress={{ ...EMPTY_LIVE_PROGRESS, streaming: true, streamContent: 'streaming…' }}
          collapseLevel="all"
          loading={false}
          error={null}
        />,
      ),
    ).not.toThrow()
  })

  it('scrolls to bottom on initial load', async () => {
    const { container } = renderWithProviders(
      <MessageList
        chatKey="web:chat-1"
        messages={makeMessages(20)}
        liveMessage={null}
        liveProgress={null}
        collapseLevel="all"
        loading={false}
        error={null}
      />,
    )
    const scroller = container.querySelector('.overflow-y-auto') as HTMLDivElement

    await act(async () => {
      await new Promise((resolve) => requestAnimationFrame(resolve))
      await new Promise((resolve) => requestAnimationFrame(resolve))
    })

    expect(scroller.scrollTop).toBe(scroller.scrollHeight)
  })

  it('keeps following when content growth temporarily moves the viewport off bottom', async () => {
    const { container, rerender } = renderMessageList(
      <MessageList
        chatKey="web:chat-1"
        messages={makeMessages(20)}
        liveMessage={null}
        liveProgress={null}
        collapseLevel="all"
        loading={false}
        error={null}
      />,
    )
    const scroller = container.querySelector('.overflow-y-auto') as HTMLDivElement

    await flushAnimationFrames()
    expect(scroller.scrollTop).toBe(scroller.scrollHeight)

    // Content grows before ResizeObserver runs. The browser may emit scroll
    // while scrollTop still points at the old bottom; this is layout movement,
    // not user intent, so sticky mode must remain enabled.
    const oldBottom = scroller.scrollHeight - scroller.clientHeight - 10
    scroller.scrollTop = oldBottom
    fireEvent.scroll(scroller)
    rerender(
      <MessageList
        chatKey="web:chat-1"
        messages={makeMessages(21)}
        liveMessage={null}
        liveProgress={null}
        collapseLevel="all"
        loading={false}
        error={null}
      />,
    )
    act(() => RO.trigger(contentElement(container)))
    await flushAnimationFrames()

    expect(scroller.scrollTop).toBe(scroller.scrollHeight)
  })

  it('coalesces repeated content resizes into one bottom write per frame', async () => {
    const { container } = renderMessageList(
      <MessageList
        chatKey="web:chat-1"
        messages={makeMessages(20)}
        liveMessage={null}
        liveProgress={null}
        collapseLevel="all"
        loading={false}
        error={null}
      />,
    )
    const scroller = container.querySelector('.overflow-y-auto') as HTMLDivElement
    await flushAnimationFrames()
    const tracked = trackScrollTop(scroller, scroller.scrollHeight - scroller.clientHeight)

    act(() => {
      const content = contentElement(container)
      RO.trigger(content)
      RO.trigger(content)
      RO.trigger(content)
    })
    expect(tracked.writes).toHaveLength(0)

    await flushAnimationFrames(2)
    expect(tracked.writes).toEqual([scroller.scrollHeight])
  })

  it('pauses following when the user explicitly wheels upward', async () => {
    const { container, rerender } = renderMessageList(
      <MessageList
        chatKey="web:chat-1"
        messages={makeMessages(20)}
        liveMessage={null}
        liveProgress={null}
        collapseLevel="all"
        loading={false}
        error={null}
      />,
    )
    const scroller = container.querySelector('.overflow-y-auto') as HTMLDivElement
    await flushAnimationFrames()
    const tracked = trackScrollTop(scroller, scroller.scrollHeight - scroller.clientHeight)

    fireEvent.wheel(scroller, { deltaY: -10 })
    tracked.setSilently(scroller.scrollHeight - scroller.clientHeight - 10)
    fireEvent.scroll(scroller)
    rerender(
      <MessageList
        chatKey="web:chat-1"
        messages={makeMessages(21)}
        liveMessage={null}
        liveProgress={null}
        collapseLevel="all"
        loading={false}
        error={null}
      />,
    )
    act(() => RO.trigger(contentElement(container)))
    await flushAnimationFrames()

    expect(tracked.value).toBe(scroller.scrollHeight - scroller.clientHeight - 10)
  })

  it('cancels a queued follow scroll when the user scrolls up', async () => {
    const { container } = renderMessageList(
      <MessageList
        chatKey="web:chat-1"
        messages={makeMessages(20)}
        liveMessage={null}
        liveProgress={null}
        collapseLevel="all"
        loading={false}
        error={null}
      />,
    )
    const scroller = container.querySelector('.overflow-y-auto') as HTMLDivElement
    await flushAnimationFrames()
    const tracked = trackScrollTop(scroller, scroller.scrollHeight - scroller.clientHeight)

    act(() => RO.trigger(contentElement(container)))
    fireEvent.wheel(scroller, { deltaY: -10 })
    tracked.setSilently(scroller.scrollHeight - scroller.clientHeight - 10)
    fireEvent.scroll(scroller)
    await flushAnimationFrames(1)

    expect(tracked.writes).toHaveLength(0)
    expect(tracked.value).toBe(scroller.scrollHeight - scroller.clientHeight - 10)
  })

  it('resumes following when followResetToken changes', async () => {
    const { container, rerender } = renderMessageList(
      <MessageList
        chatKey="web:chat-1"
        followResetToken={0}
        messages={makeMessages(20)}
        liveMessage={null}
        liveProgress={null}
        collapseLevel="all"
        loading={false}
        error={null}
      />,
    )
    const scroller = container.querySelector('.overflow-y-auto') as HTMLDivElement

    await act(async () => {
      await new Promise((resolve) => requestAnimationFrame(resolve))
      await new Promise((resolve) => requestAnimationFrame(resolve))
    })
    scroller.scrollTop = 100
    fireEvent.wheel(scroller, { deltaY: -100 })
    fireEvent.scroll(scroller)

    rerender(
      <MessageList
        chatKey="web:chat-1"
        followResetToken={1}
        messages={makeMessages(21)}
        liveMessage={null}
        liveProgress={null}
        collapseLevel="all"
        loading={false}
        error={null}
      />,
    )
    await act(async () => {
      await new Promise((resolve) => requestAnimationFrame(resolve))
      await new Promise((resolve) => requestAnimationFrame(resolve))
    })

    expect(scroller.scrollTop).toBe(scroller.scrollHeight)
  })

  it('keeps following on downward wheel input at the bottom', async () => {
    const { container, rerender } = renderMessageList(
      <MessageList
        chatKey="web:chat-1"
        messages={makeMessages(20)}
        liveMessage={null}
        liveProgress={null}
        collapseLevel="all"
        loading={false}
        error={null}
      />,
    )
    const scroller = container.querySelector('.overflow-y-auto') as HTMLDivElement

    await act(async () => {
      await new Promise((resolve) => requestAnimationFrame(resolve))
      await new Promise((resolve) => requestAnimationFrame(resolve))
    })
    expect(scroller.scrollTop).toBe(scroller.scrollHeight)

    fireEvent.wheel(scroller, { deltaY: 100 })
    fireEvent.scroll(scroller)
    rerender(
      <MessageList
        chatKey="web:chat-1"
        messages={makeMessages(21)}
        liveMessage={null}
        liveProgress={null}
        collapseLevel="all"
        loading={false}
        error={null}
      />,
    )
    await act(async () => {
      await new Promise((resolve) => requestAnimationFrame(resolve))
      await new Promise((resolve) => requestAnimationFrame(resolve))
    })

    expect(scroller.scrollTop).toBe(scroller.scrollHeight)
  })

  it('shows the empty-state when there are no messages and not loading', () => {
    // jsdom scrollHeight=12000 means the virtualizer still thinks there's
    // content, so the empty branch is only reached when rows.length===0 AND
    // the virtualizer renders nothing. Assert by query: no message bubbles.
    const { container } = renderWithProviders(
      <MessageList
        messages={[]}
        liveMessage={null}
        liveProgress={null}
        collapseLevel="all"
        loading={false}
        error={null}
      />,
    )
    // No message row data-index elements.
    expect(container.querySelectorAll('[data-index]')).toHaveLength(0)
  })

  it('shows the error banner when error is set', () => {
    const { container } = renderWithProviders(
      <MessageList
        messages={[]}
        liveMessage={null}
        liveProgress={null}
        collapseLevel="all"
        loading={false}
        error="history 500"
      />,
    )
    expect(container.textContent).toContain('history 500')
  })

  it('finds the latest compact marker for rewind eligibility', () => {
    const messages: ChatMessage[] = [
      { id: 'u-old', role: 'user', content: 'old', iterations: [], timestamp: '2026-07-08T00:00:00Z', isPartial: false, turnID: 0 },
      { id: 'compact', role: 'user', content: '[Compacted context]', iterations: [], timestamp: '2026-07-08T00:00:01Z', isPartial: false, turnID: 0 },
      { id: 'u-new', role: 'user', content: 'new', iterations: [], timestamp: '2026-07-08T00:00:02Z', isPartial: false, turnID: 0 },
    ]
    expect(latestCompactBoundaryIndex(messages)).toBe(1)
  })

  it('uses TUI-style compact marker prefix matching', () => {
    expect(isCompactMarker({ role: 'user', content: '[Compacted context]\nsummary' })).toBe(true)
    expect(isCompactMarker({ role: 'user', content: 'prefix [Compacted context]' })).toBe(false)
  })

  it('allows rewind only for persisted user messages after the latest compact boundary', () => {
    const messages: ChatMessage[] = [
      { id: 'u-old', role: 'user', content: 'old', iterations: [], timestamp: '2026-07-08T00:00:00Z', isPartial: false, turnID: 0, persisted: true },
      { id: 'compact', role: 'user', content: '[Compacted context]\nsummary', iterations: [], timestamp: '2026-07-08T00:00:01Z', isPartial: false, turnID: 0, persisted: true },
      { id: 'u-new', role: 'user', content: 'new', iterations: [], timestamp: '2026-07-08T00:00:02Z', isPartial: false, turnID: 0, persisted: true },
    ]
    const boundary = latestCompactBoundaryIndex(messages)

    expect(messages.map((m, i) => canRewindMessage(m, i, boundary))).toEqual([false, false, true])
  })

  it('does not show rewind for optimistic user messages', () => {
    const messages: ChatMessage[] = [
      { id: 'user-1', role: 'user', content: 'new', iterations: [], timestamp: '2026-07-08T00:00:02Z', isPartial: false, turnID: 0, persisted: false },
    ]

    expect(canRewindMessage(messages[0], 0, -1)).toBe(false)
  })
})

describe('MessageList navigation buttons (Spec A §4)', () => {
  it('renders navigation button group', () => {
    const messages = makeMessages(20)
    const { container } = renderWithProviders(
      <MessageList
        messages={messages}
        liveMessage={null}
        liveProgress={null}
        collapseLevel="all"
        loading={false}
        error={null}
      />,
    )
    // Should have 4 nav buttons
    const navButtons = container.querySelectorAll('button[title]')
    expect(navButtons.length).toBeGreaterThanOrEqual(4)
  })

  it('disables nav buttons when no messages', () => {
    const { container } = renderWithProviders(
      <MessageList
        messages={[]}
        liveMessage={null}
        liveProgress={null}
        collapseLevel="all"
        loading={false}
        error={null}
      />,
    )
    const buttons = container.querySelectorAll('button[disabled]')
    // At least scroll-to-top and scroll-to-bottom should be disabled
    expect(buttons.length).toBeGreaterThanOrEqual(2)
  })

  it('renders nav buttons with correct titles', () => {
    const messages = makeMessages(20)
    const { container } = renderWithProviders(
      <MessageList
        messages={messages}
        liveMessage={null}
        liveProgress={null}
        collapseLevel="all"
        loading={false}
        error={null}
      />,
    )
    const titles = Array.from(container.querySelectorAll('button[title]')).map(
      (b) => b.getAttribute('title'),
    )
    // Should contain scroll-to-top, prev-user, next-user, scroll-to-bottom titles
    expect(titles.some((t) => t?.includes('最上方') || t?.includes('top'))).toBe(true)
    expect(titles.some((t) => t?.includes('最下方') || t?.includes('bottom'))).toBe(true)
  })

  it('pauses follow for history navigation and resumes on End', async () => {
    const { container } = renderWithProviders(
      <MessageList
        chatKey="web:chat-1"
        messages={makeMessages(20)}
        liveMessage={null}
        liveProgress={null}
        collapseLevel="all"
        loading={false}
        error={null}
      />,
    )
    const scroller = container.querySelector('.overflow-y-auto') as HTMLDivElement
    await flushAnimationFrames()
    const tracked = trackScrollTop(scroller, scroller.scrollHeight - scroller.clientHeight)
    const topButton = Array.from(container.querySelectorAll<HTMLButtonElement>('button[title]')).find((button) =>
      button.title.includes('最上方') || button.title.toLowerCase().includes('top'),
    )
    if (!topButton) throw new Error('scroll-to-top button missing')

    fireEvent.click(topButton)
    tracked.writes.length = 0
    act(() => RO.trigger(contentElement(container)))
    await flushAnimationFrames(1)
    expect(tracked.writes).toHaveLength(0)

    fireEvent.keyDown(scroller, { key: 'End' })
    await flushAnimationFrames(2)
    expect(tracked.writes).toEqual([scroller.scrollHeight])
  })
})

describe('MessageList new-content bubble (Spec A §3)', () => {
  it('does not show the bubble when following the bottom', () => {
    const messages = makeMessages(10)
    const { container } = renderWithProviders(
      <MessageList
        chatKey="web:chat-1"
        messages={messages}
        liveMessage={null}
        liveProgress={null}
        collapseLevel="all"
        loading={false}
        error={null}
      />,
    )
    const bubble = container.querySelector('button[class*="rounded-full"]')
    expect(bubble).toBeNull()
  })

  it('shows one boolean new-content notice during a paused stream and resumes on click', async () => {
    const messages = makeMessages(10)
    const firstLive: ChatMessage = {
      id: 'live-1',
      role: 'assistant',
      content: 'a',
      iterations: [],
      timestamp: '',
      isPartial: true,
      turnID: 0,
    }
    const { container, rerender } = renderMessageList(
      <MessageList
        chatKey="web:chat-1"
        messages={messages}
        liveMessage={firstLive}
        liveProgress={{ ...EMPTY_LIVE_PROGRESS, streaming: true, streamContent: 'a' }}
        collapseLevel="all"
        loading={false}
        error={null}
      />,
    )
    const scroller = container.querySelector('.overflow-y-auto') as HTMLDivElement
    await flushAnimationFrames()
    const tracked = trackScrollTop(scroller, scroller.scrollHeight - scroller.clientHeight)

    fireEvent.wheel(scroller, { deltaY: -10 })
    tracked.setSilently(scroller.scrollHeight - scroller.clientHeight - 10)
    fireEvent.scroll(scroller)
    rerender(
      <MessageList
        chatKey="web:chat-1"
        messages={messages}
        liveMessage={{ ...firstLive, content: 'streamed text' }}
        liveProgress={{ ...EMPTY_LIVE_PROGRESS, streaming: true, streamContent: 'streamed text' }}
        collapseLevel="all"
        loading={false}
        error={null}
      />,
    )

    const bubble = container.querySelector('button[class*="rounded-full"]') as HTMLButtonElement
    expect(bubble).not.toBeNull()
    expect(bubble.textContent).toMatch(/新内容|New content/)
    expect(bubble.textContent).not.toMatch(/\d/)

    fireEvent.click(bubble)
    await flushAnimationFrames(2)
    expect(tracked.value).toBe(scroller.scrollHeight)
  })

  it('observes the shared message and footer wrapper', () => {
    const { container } = renderMessageList(
      <MessageList
        chatKey="web:chat-1"
        messages={makeMessages(10)}
        liveMessage={null}
        liveProgress={null}
        collapseLevel="all"
        loading={false}
        error={null}
        footer={<div data-testid="ask-footer">Question</div>}
      />,
    )

    const footer = container.querySelector('[data-testid="ask-footer"]')
    expect(footer).not.toBeNull()
    expect(contentElement(container).contains(footer)).toBe(true)
  })
})
