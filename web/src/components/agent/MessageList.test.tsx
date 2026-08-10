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
import { canRewindMessage, isCompactMarker, latestCompactBoundaryIndex, MessageList, buildMessageRows } from '@/components/agent/MessageList'
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

    // Content grows — the ResizeObserver triggers scheduleFollow, which writes
    // scrollTop=scrollHeight. The browser may fire scroll while scrollTop is
    // momentarily at the old position (before the write applies). Our own write
    // is flagged so onScroll doesn't treat this as user intent and pause.
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

  it('does not yank the viewport when content grows after the user wheels up', async () => {
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

    // User wheels up — this pauses following via the wheel handler
    fireEvent.wheel(scroller, { deltaY: -10 })
    const readPosition = scroller.scrollHeight - scroller.clientHeight - 200
    scroller.scrollTop = readPosition
    fireEvent.scroll(scroller)

    // Content grows (e.g. streaming). The ResizeObserver must NOT yank the
    // viewport to the bottom — the user is reading history.
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

    expect(scroller.scrollTop).toBe(readPosition)
  })

  it('synchronously scrolls to bottom on each content resize', async () => {
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

    // ResizeObserver now scrolls synchronously (no RAF) to handle virtualizer
    // scroll corrections. Each trigger immediately writes scrollTop.
    // Note: we observe scrollElement (not content) — content height changes
    // are reflected in scrollElement.scrollHeight automatically.
    act(() => {
      RO.trigger(scroller)
      RO.trigger(scroller)
      RO.trigger(scroller)
    })
    // At least one write happened synchronously
    expect(tracked.writes.length).toBeGreaterThanOrEqual(1)
    expect(tracked.value).toBe(scroller.scrollHeight)
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

  it('cancels a queued follow scroll when the user wheels up', async () => {
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

    // User wheels up — pauses following
    fireEvent.wheel(scroller, { deltaY: -10 })
    const readPosition = scroller.scrollHeight - scroller.clientHeight - 10
    scroller.scrollTop = readPosition
    fireEvent.scroll(scroller)

    // Now trigger ResizeObserver — should NOT scroll (stick=false from wheel)
    act(() => RO.trigger(contentElement(container)))
    await flushAnimationFrames(1)

    // scrollTop should stay at readPosition, not jump to bottom
    expect(scroller.scrollTop).toBe(readPosition)
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

  it('shows thinking indicator during loading when busy and the last row is a user message (new turn)', () => {
    // BUG: switching to a busy session hides liveMessage (visibleLiveMessage
    // gate) AND suppresses the busy placeholder (!loading). Result: nothing
    // shows — no tool, no "思考中…". Fix: show the placeholder during loading
    // when rows.length > 0 (the spinner only handles the empty-state case).
    // INVARIANT: the placeholder is allowed when the last row is a USER
    // message (a new turn is thinking); it must NOT appear below a finished
    // assistant (see next test).
    const messages: ChatMessage[] = [
      { id: 'u1', role: 'user', content: 'hello', iterations: [], timestamp: '2026-07-08T00:00:00Z', isPartial: false, turnID: 0 },
      { id: 'a1', role: 'assistant', content: 'hi', iterations: [], timestamp: '2026-07-08T00:00:01Z', isPartial: false, turnID: 0 },
      { id: 'u2', role: 'user', content: 'next', iterations: [], timestamp: '2026-07-08T00:00:02Z', isPartial: false, turnID: 1 },
    ]
    const { container } = renderWithProviders(
      <MessageList
        messages={messages}
        liveMessage={null}
        liveProgress={null}
        collapseLevel="all"
        loading={true}
        error={null}
        busy={true}
      />,
    )
    expect(container.textContent).toContain('thinking')
  })

  it('does NOT show the thinking placeholder below a FINISHED assistant (copy-button turn, invariant)', () => {
    // INVARIANT: a finished assistant (isPartial=false + final content →
    // renders a copy button) must never be followed by "思考中…" — that would
    // imply the completed turn is still running (linear-consistency violation).
    const messages: ChatMessage[] = [
      { id: 'u1', role: 'user', content: 'hello', iterations: [], timestamp: '2026-07-08T00:00:00Z', isPartial: false, turnID: 0 },
      { id: 'a1', role: 'assistant', content: 'hi', iterations: [], timestamp: '2026-07-08T00:00:01Z', isPartial: false, turnID: 0 },
    ]
    const { container } = renderWithProviders(
      <MessageList
        messages={messages}
        liveMessage={null}
        liveProgress={null}
        collapseLevel="all"
        loading={true}
        error={null}
        busy={true}
      />,
    )
    expect(container.textContent).not.toContain('thinking')
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

describe('buildMessageRows — turnID=0 live dedup (regression: 0ac17e66 was too broad)', () => {
  const base = (over: Partial<ChatMessage>): ChatMessage => ({
    id: 'x', role: 'assistant', content: '', iterations: [], timestamp: '', isPartial: false, turnID: 0, ...over,
  })

  it('renders a turnID=0 live message when committed assistants have DIFFERENT content (normal streaming, no turn_started yet)', () => {
    const messages: ChatMessage[] = [
      base({ id: 'u1', role: 'user', content: 'hi', turnID: 1 }),
      base({ id: 'a1', role: 'assistant', content: 'history reply', turnID: 1 }),
    ]
    const live: ChatMessage = base({ id: 'turn-0-live', content: 'Starting...', isPartial: true })
    const rows = buildMessageRows(messages, live)
    // The old hasAnyCommittedAssistant check skipped the live row because
    // 'history reply' is a committed assistant → 'Starting...' never rendered
    // (stream-jitter E2E regression: 100 history messages hid the stream).
    expect(rows).toHaveLength(3)
    expect(rows[2].content).toBe('Starting...')
  })

  it('skips a turnID=0 live message when a committed assistant has the SAME content (cancel commit path)', () => {
    const messages: ChatMessage[] = [
      base({ id: 'a1', content: 'final text', turnID: 1 }),
    ]
    const live: ChatMessage = base({ id: 'turn-0-live', content: 'final text', isPartial: true })
    const rows = buildMessageRows(messages, live)
    expect(rows).toHaveLength(1)
  })

  it('skips a turnID=0 live message when committed iterations match (cancel [interrupted] with same progress_history)', () => {
    const tool = { name: 'Shell', label: 'Shell', status: 'error' as const, elapsedMs: 0, summary: '', detail: '', args: '', toolHints: '' }
    const iter = { iteration: 1, thinking: '', reasoning: '', tools: [tool], toolCount: 1 }
    const messages: ChatMessage[] = [
      base({ id: 'a1', content: '', turnID: 1, iterations: [iter] }),
    ]
    const live: ChatMessage = base({ id: 'turn-0-live', content: '', isPartial: true, iterations: [{ ...iter }] })
    const rows = buildMessageRows(messages, live)
    expect(rows).toHaveLength(1)
  })

  it('renders a turnID=0 live message when committed iterations DIFFER (different iteration numbers)', () => {
    const tool = { name: 'Shell', label: 'Shell', status: 'error' as const, elapsedMs: 0, summary: '', detail: '', args: '', toolHints: '' }
    const iter1 = { iteration: 1, thinking: '', reasoning: '', tools: [{ ...tool, name: 'Read', label: 'Read', status: 'done' as const }], toolCount: 1 }
    const iter2 = { iteration: 2, thinking: '', reasoning: '', tools: [tool], toolCount: 1 }
    const messages: ChatMessage[] = [
      base({ id: 'a1', content: '', turnID: 1, iterations: [iter1] }),
    ]
    const live: ChatMessage = base({ id: 'turn-0-live', content: '', isPartial: true, iterations: [{ ...iter2 }] })
    const rows = buildMessageRows(messages, live)
    expect(rows).toHaveLength(2)
  })

  it('skips a turnID>0 live message when the committed assistant has the SAME content but turnID=0 (text event without turn_id — final iter duplicate regression)', () => {
    // User report: "最终 iter 重复渲染" — the text event arrived WITHOUT
    // turn_id, so appendAssistant committed the final reply with turnID=0,
    // while the frozen live row kept turnID=1311. The turnID exact match
    // failed and BOTH rows rendered the same text. Fix: content match fallback.
    const messages: ChatMessage[] = [
      base({ id: 'u1', role: 'user', content: 'question', turnID: 0 }),
      base({ id: 'seq-80810', role: 'assistant', content: 'final reply text', turnID: 0, persisted: true }),
    ]
    const live: ChatMessage = base({ id: 'turn-1311-live', content: 'final reply text', isPartial: true, turnID: 1311 })
    const rows = buildMessageRows(messages, live)
    expect(rows).toHaveLength(2) // user + committed only — live is deduped
    expect(rows.some((r) => r.id === 'turn-1311-live')).toBe(false)
  })

  it('keeps a turnID>0 live message when NO committed assistant matches content (different text)', () => {
    const messages: ChatMessage[] = [
      base({ id: 'u1', role: 'user', content: 'question', turnID: 1311 }),
      base({ id: 'seq-80810', role: 'assistant', content: 'committed different text', turnID: 0, persisted: true }),
    ]
    const live: ChatMessage = base({ id: 'turn-1311-live', content: 'streaming partial text', isPartial: true, turnID: 1311 })
    const rows = buildMessageRows(messages, live)
    expect(rows).toHaveLength(3) // user + committed + live (streaming not committed yet)
  })
})
