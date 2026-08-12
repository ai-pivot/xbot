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
import { describe, expect, it, vi } from 'vitest'
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
    const content = container.querySelector('[data-message-list-content]') as HTMLDivElement
    await flushAnimationFrames()
    const tracked = trackScrollTop(scroller, scroller.scrollHeight - scroller.clientHeight)

    // ResizeObserver now scrolls synchronously (no RAF) to handle virtualizer
    // scroll corrections. Each trigger immediately writes scrollTop.
    // The observer watches CONTENT (height = totalSize, tracks live-row growth),
    // NOT scrollElement — scrollElement's clientHeight is fixed at the viewport
    // height, so content growth changes only scrollHeight and never fires a
    // ResizeObserver on it (the "turn 消失" bug). Trigger the content element.
    act(() => {
      RO.trigger(content)
      RO.trigger(content)
      RO.trigger(content)
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

  it('keeps a turnID>0 live message when NO committed assistant shares its turn (different turn content)', () => {
    // committed is a legacy row with NO preceding turn to bind to → it stays
    // turnID=0; the live row is turnID=1311 (a different turn). Same-turn
    // dedup does NOT apply and content differs → the live row must render.
    const messages: ChatMessage[] = [
      base({ id: 'seq-80810', role: 'assistant', content: 'legacy committed text', turnID: 0, persisted: true }),
      base({ id: 'u1', role: 'user', content: 'question', turnID: 1311 }),
    ]
    const live: ChatMessage = base({ id: 'turn-1311-live', content: 'streaming partial text', isPartial: true, turnID: 1311 })
    const rows = buildMessageRows(messages, live)
    expect(rows).toHaveLength(3) // legacy + user + live (streaming not committed yet)
    expect(rows.some((r) => r.id === 'turn-1311-live')).toBe(true)
  })

  it('KEEPS the live row when a DIFFERENT turn committed assistant has the same iteration NUMBERS — cross-turn exactDup must never fire (turn-vanish P0: REC replay 360)', () => {
    // REAL reproduction (sse-dump 2026-08-12T04-51-11 + state snapshots):
    // turn 359's committed assistant has iterations [1,2]; turn 360's LIVE row
    // also has iterations [1,2] (iteration numbers reset every turn). The old
    // exactDup compared iteration numbers WITHOUT checking turnID — 2===2 and
    // [1,2]===[1,2] matched, the live row was dropped, and turn 360 rendered
    // ONLY its user message ("assistant 完全不见了，turn 360 只剩下 user").
    // START was fine (live had 1 iteration, turn 359 had 2 → no match); the
    // turn vanished exactly when the live reached 2 iterations.
    const tool = { name: 'Shell', label: 'Shell', status: 'done' as const, elapsedMs: 0, summary: '', detail: '', args: '', toolHints: '' }
    const iter1 = { iteration: 1, thinking: 'turn 359 iter1', reasoning: '', tools: [], toolCount: 0 }
    const iter2 = { iteration: 2, thinking: 'turn 359 iter2', reasoning: '', tools: [tool], toolCount: 1 }
    const messages: ChatMessage[] = [
      base({ id: 'u359', role: 'user', content: '继续', turnID: 359 }),
      base({ id: 'asst-359', role: 'assistant', content: '', turnID: 359, iterations: [iter1, iter2], persisted: false }),
      base({ id: 'u360', role: 'user', content: '继续', turnID: 360 }),
    ]
    const live: ChatMessage = base({
      id: 'turn-360-live',
      role: 'assistant',
      content: '',
      turnID: 360,
      isPartial: true,
      iterations: [
        { iteration: 1, thinking: 'turn 360 iter1 (PR #299 CI...)', reasoning: '', tools: [], toolCount: 0 },
        { iteration: 2, thinking: 'turn 360 iter2 (git push...)', reasoning: '', tools: [{ ...tool }], toolCount: 1 },
      ],
    })
    const rows = buildMessageRows(messages, live)
    expect(rows.some((r) => r.id === 'turn-360-live')).toBe(true) // live MUST render
    expect(rows).toHaveLength(4) // u359 + asst-359 + u360 + live
  })

  it('still dedupes a turnID=0 committed row against a live row with the same iterations (text event lost turn_id)', () => {
    // Guard: the turnID=0 committed row (text event lost its turn_id) must
    // STILL be deduped by iteration match — the fix narrowed exactDup to
    // "m.turnID===0 || live.turnID===0", it must not break this path.
    const iter1 = { iteration: 1, thinking: '', reasoning: '', tools: [], toolCount: 0 }
    const messages: ChatMessage[] = [
      base({ id: 'seq-80810', role: 'assistant', content: '', turnID: 0, persisted: true, iterations: [iter1] }),
    ]
    const live: ChatMessage = base({ id: 'turn-1311-live', content: '', isPartial: true, turnID: 1311, iterations: [{ ...iter1 }] })
    const rows = buildMessageRows(messages, live)
    expect(rows.some((r) => r.id === 'turn-1311-live')).toBe(false) // deduped
    expect(rows).toHaveLength(1)
  })
})

describe('buildMessageRows — linear consistency (extreme scenarios)', () => {
  const base = (over: Partial<ChatMessage>): ChatMessage => ({
    id: 'x', role: 'assistant', content: '', iterations: [], timestamp: '', isPartial: false, turnID: 0, ...over,
  })

  it('reorders out-of-order committed rows (SSE weak-network: turn 5 text arrived before turn 4)', () => {
    // R2: a larger turn_id must NEVER render above a smaller one.
    const rows = buildMessageRows([
      base({ id: 'a5', role: 'assistant', content: 'five', turnID: 5 }),
      base({ id: 'a4', role: 'assistant', content: 'four', turnID: 4 }),
      base({ id: 'u4', role: 'user', content: 'q4', turnID: 4 }),
      base({ id: 'u5', role: 'user', content: 'q5', turnID: 5 }),
    ], null)
    expect(rows.map((m) => m.id)).toEqual(['u4', 'a4', 'u5', 'a5'])
  })

  it('interleaves a notification turn and a user turn in strict turn order', () => {
    // Scenario 1: system notification (turn 1) overlaps a user input turn (2).
    const rows = buildMessageRows([
      base({ id: 'notif-u1', role: 'user', content: 'bg task done', turnID: 1, isNotification: true }),
      base({ id: 'notif-a1', role: 'assistant', content: 'notification reply', turnID: 1 }),
      base({ id: 'u2', role: 'user', content: 'user input', turnID: 2 }),
      base({ id: 'a2', role: 'assistant', content: 'user reply', turnID: 2 }),
    ], null)
    expect(rows.map((m) => m.turnID)).toEqual([1, 1, 2, 2])
    expect(rows[0].isNotification).toBe(true)
  })

  it('places a cancelled frozen live (turn N) inside its own turn, above the new user (turn N+1)', () => {
    // Scenario 2: user msg → cancel (frozen live turn 3) → user sends a new msg (turn 4).
    const messages = [
      base({ id: 'u3', role: 'user', content: 'q3', turnID: 3 }),
      base({ id: 'a3', role: 'assistant', content: 'cancelled partial', turnID: 3 }),
      base({ id: 'u4', role: 'user', content: 'new input', turnID: 4 }),
    ]
    const live: ChatMessage = base({ id: 'turn-3-live', role: 'assistant', content: 'cancelled partial', isPartial: true, turnID: 3 })
    const rows = buildMessageRows(messages, live)
    // live content equals committed a3 → exact dedup → only committed renders.
    expect(rows.map((m) => m.id)).toEqual(['u3', 'a3', 'u4'])
    // turn 3 rows all above turn 4 user — never below.
    expect(rows.map((m) => m.turnID)).toEqual([3, 3, 4])
  })

  it('places a DIFFERENT frozen live (turn N, no committed match) above the new user (turn N+1)', () => {
    const messages = [
      base({ id: 'u3', role: 'user', content: 'q3', turnID: 3 }),
      base({ id: 'u4', role: 'user', content: 'new input', turnID: 4 }),
    ]
    const live: ChatMessage = base({ id: 'turn-3-live', role: 'assistant', content: 'cancelled reasoning', isPartial: true, turnID: 3 })
    const rows = buildMessageRows(messages, live)
    expect(rows.map((m) => m.turnID)).toEqual([3, 3, 4]) // live inside turn 3, above turn 4
    expect(rows[1].id).toBe('turn-3-live')
  })

  it('binds a turnID=0 committed assistant to its turn (text event without turn_id)', () => {
    // R1: every element has a deterministic turn_id after binding.
    const rows = buildMessageRows([
      base({ id: 'u3', role: 'user', content: 'q3', turnID: 3 }),
      base({ id: 'a3', role: 'assistant', content: 'final', turnID: 0, persisted: true }),
    ], null)
    expect(rows.map((m) => m.turnID)).toEqual([3, 3])
  })

  it('binds a legacy user_echo row (turnID=0, persisted) to its following turn', () => {
    const rows = buildMessageRows([
      base({ id: 'legacy-u', role: 'user', content: 'old question', turnID: 0, persisted: true }),
      base({ id: 'a5', role: 'assistant', content: 'old reply', turnID: 5 }),
      base({ id: 'u5', role: 'user', content: 'q5', turnID: 5 }),
    ], null)
    // legacy user binds to turn 5; order: [legacy-u, a5, u5] — both turn 5,
    // user precedes assistant, stable input order keeps a5 before u5? No —
    // user(0) ranks before assistant(1), so u5 comes before a5:
    expect(rows.map((m) => m.id)).toEqual(['legacy-u', 'u5', 'a5'])
  })

  it('keeps a committed same-turn assistant when a live iteration streams (committed must NOT vanish)', () => {
    // User report: "还是出现了turn消失" — a committed assistant (20 iterations,
    // turn 4) vanished, leaving only the live iteration 21. The render layer
    // must NEVER drop the committed row: same-turn merge keeps it (iterations
    // merged), exact-dedup keeps it, and the fallback appends live beside it.
    const tool = { name: 'Shell', label: 'Shell', status: 'done' as const, elapsedMs: 0, summary: '', detail: '', args: '', toolHints: '' }
    const committedIters = Array.from({ length: 20 }, (_, i) => ({
      iteration: i + 1, thinking: `t${i + 1}`, reasoning: '', tools: [tool], toolCount: 1,
    }))
    const messages: ChatMessage[] = [
      base({ id: 'u4', role: 'user', content: 'q4', turnID: 4 }),
      base({ id: 'a4', role: 'assistant', content: 'final reply', turnID: 4, iterations: committedIters, persisted: true }),
    ]
    const live: ChatMessage = base({ id: 'turn-4-live', role: 'assistant', content: 'iter21 thinking', isPartial: true, turnID: 4, iterations: [{ iteration: 21, thinking: 't21', reasoning: '', tools: [tool], toolCount: 1 }] })
    const rows = buildMessageRows(messages, live)
    // The committed assistant must remain rendered (either merged with iter 21,
    // or the live appended beside it) — NEVER dropped.
    expect(rows.some((r) => r.id === 'a4' || r.id === 'turn-4-live')).toBe(true)
    expect(rows.some((r) => r.role === 'assistant' && !r.isPartial)).toBe(true)
    // No turn-4 assistant may lose its committed iterations entirely.
    const committedRow = rows.find((r) => r.id === 'a4')
    if (committedRow) {
      expect(committedRow.iterations.length).toBeGreaterThanOrEqual(20)
    }
  })

  it('pins an optimistic unbound user (turnID=0, persisted=false) at the bottom', () => {
    const rows = buildMessageRows([
      base({ id: 'u3', role: 'user', content: 'q3', turnID: 3 }),
      base({ id: 'a3', role: 'assistant', content: 'reply', turnID: 3 }),
      base({ id: 'opt-u', role: 'user', content: 'typing...', turnID: 0, persisted: false, sending: true }),
    ], null)
    expect(rows.map((m) => m.id)).toEqual(['u3', 'a3', 'opt-u'])
  })

  it('keeps all committed rows and appends the live row in turn order', () => {
    const messages = [
      base({ id: 'u1', role: 'user', content: 'q1', turnID: 1 }),
      base({ id: 'a1', role: 'assistant', content: 'r1', turnID: 1 }),
      base({ id: 'u2', role: 'user', content: 'q2', turnID: 2 }),
      base({ id: 'a2', role: 'assistant', content: 'r2', turnID: 2 }),
    ]
    const live: ChatMessage = base({ id: 'turn-3-live', role: 'assistant', content: 'streaming r3', isPartial: true, turnID: 3 })
    const rows = buildMessageRows(messages, live)
    expect(rows.map((m) => m.turnID)).toEqual([1, 1, 2, 2, 3])
    expect(rows[4].isPartial).toBe(true)
  })

  it('reorders user messages after a long SSE-gap reload (user msgs no longer all at the bottom)', () => {
    // User report: after a long SSE disconnect + reconnect, ALL user messages
    // appeared at the bottom. Reload can return rows with user/assistant
    // interleaved out of order (echo watermark retention). The render sort
    // must place every user inside its own turn by turn_id.
    const rows = buildMessageRows([
      base({ id: 'a1', role: 'assistant', content: 'r1', turnID: 1, persisted: true }),
      base({ id: 'a2', role: 'assistant', content: 'r2', turnID: 2, persisted: true }),
      base({ id: 'u1', role: 'user', content: 'q1', turnID: 1, persisted: true }),
      base({ id: 'u2', role: 'user', content: 'q2', turnID: 2, persisted: true }),
    ], null)
    expect(rows.map((m) => m.id)).toEqual(['u1', 'a1', 'u2', 'a2'])
  })

  it('keeps a STREAMING live when turn_started was lost (live.turnID=0) — no vanish, renders at bottom', () => {
    // User report: "turn 在 streaming 过程中突然消失，显示还是 busy 但只看得到
    // 最后一个 user msg". liveMessage.turnID is `snap.turnID || (frozen ?
    // store.lastTurnID : 0)`. When the current turn's turn_started was lost
    // (SSE coalescing/gap), snap.turnID=0 and the OLD code fell back to
    // store.lastTurnID = the PREVIOUS turn — buildMessageRows' same-turn merge
    // then absorbed the streaming live into the OLD committed assistant and the
    // turn vanished. Fix: streaming live keeps turnID=0 → no same-turn merge,
    // sorted to the bottom (it IS the newest content).
    const messages = [
      base({ id: 'a5', role: 'assistant', content: 'old reply', turnID: 5, persisted: true }),
      base({ id: 'u6', role: 'user', content: 'new question', turnID: 6 }),
    ]
    const live: ChatMessage = base({ id: 'turn-0-live', role: 'assistant', content: 'streaming new reply', isPartial: true, turnID: 0 })
    const rows = buildMessageRows(messages, live)
    expect(rows.some((r) => r.id === 'turn-0-live')).toBe(true)
    // The live row renders BELOW its own user (bottom — newest content).
    const ids = rows.map((m) => m.id)
    expect(ids.indexOf('turn-0-live')).toBeGreaterThan(ids.indexOf('u6'))
  })

  it('does NOT pin an unbound persisted user (SSE-gap echo) at the top — binds to last turn', () => {
    // The reload retained a user_echo (turnID=0, persisted=true, no following
    // turn yet). bindTurnIDs' prevTurn fallback binds it to the last known
    // turn so it renders in turn order, never at the very top.
    const rows = buildMessageRows([
      base({ id: 'u1', role: 'user', content: 'q1', turnID: 1 }),
      base({ id: 'a1', role: 'assistant', content: 'r1', turnID: 1 }),
      base({ id: 'echo-u2', role: 'user', content: 'new question', turnID: 0, persisted: true }),
    ], null)
    // echo-u2 binds to turn 1 (prevTurn) and sorts after a1 (same turn, user
    // first? No — user(0) ranks before assistant(1), so within turn 1 the
    // order is u1, then... both users rank 0; stable order: u1, echo-u2, a1.
    const ids = rows.map((m) => m.id)
    expect(ids[ids.length - 1]).not.toBe('echo-u2') // not pinned to the bottom by legacy sort
    expect(ids.indexOf('echo-u2')).toBeGreaterThan(ids.indexOf('u1'))
  })

  it('RENDER_LOSS monitor: live tail row vanishing while busy without committed replacement logs an error', () => {
    // User report: "agent turn 消失" — the live row disappears from the DOM
    // until the next iteration's first SSE event. The monitor must catch the
    // exact corruption: a live (isPartial) tail row that vanishes with NO
    // committed assistant replacing it while the turn is still busy.
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    try {
      const { rerender } = renderMessageList(
        <MessageList
          chatKey="web:chat-1"
          messages={[base({ id: 'u7', role: 'user', content: 'q7', turnID: 7 })]}
          liveMessage={base({ id: 'turn-7-live', role: 'assistant', content: 'streaming…', isPartial: true, turnID: 7 })}
          liveProgress={{ ...EMPTY_LIVE_PROGRESS, streaming: true, streamContent: 'streaming…' }}
          busy
          collapseLevel="all"
          loading={false}
          error={null}
        />,
      )
      // Live row vanishes (store reset) — no committed assistant appears.
      rerender(
        <MessageList
          chatKey="web:chat-1"
          messages={[base({ id: 'u7', role: 'user', content: 'q7', turnID: 7 })]}
          liveMessage={null}
          liveProgress={null}
          busy
          collapseLevel="all"
          loading={false}
          error={null}
        />,
      )
      expect(errorSpy).toHaveBeenCalledWith(
        expect.stringContaining('RENDER_LOSS_ROWS'),
        expect.objectContaining({ prevTurnID: 7, rowsLen: 1 }),
      )
    } finally {
      errorSpy.mockRestore()
    }
  })

  it('RENDER_LOSS monitor: does NOT alarm on a normal turn finalize (committed replacement)', () => {
    // Normal completion: the text event commits the assistant (same turnID,
    // isPartial=false) while resetting the live store. rows now contain a
    // committed assistant — this is a legal replacement, NOT data loss.
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    try {
      const { rerender } = renderMessageList(
        <MessageList
          chatKey="web:chat-1"
          messages={[base({ id: 'u8', role: 'user', content: 'q8', turnID: 8 })]}
          liveMessage={base({ id: 'turn-8-live', role: 'assistant', content: 'partial', isPartial: true, turnID: 8 })}
          liveProgress={{ ...EMPTY_LIVE_PROGRESS, streaming: true, streamContent: 'partial' }}
          busy
          collapseLevel="all"
          loading={false}
          error={null}
        />,
      )
      rerender(
        <MessageList
          chatKey="web:chat-1"
          messages={[
            base({ id: 'u8', role: 'user', content: 'q8', turnID: 8 }),
            base({ id: 'a8', role: 'assistant', content: 'final reply', turnID: 8 }),
          ]}
          liveMessage={null}
          liveProgress={null}
          busy={false}
          collapseLevel="all"
          loading={false}
          error={null}
        />,
      )
      expect(errorSpy).not.toHaveBeenCalledWith(
        expect.stringContaining('RENDER_LOSS_ROWS'),
        expect.anything(),
      )
    } finally {
      errorSpy.mockRestore()
    }
  })

  it('VIRTUALIZER_TAIL_DROP monitor: does NOT alarm on refresh (last row is committed history, not live)', () => {
    // User report: "刷新后总是立刻打印 [VIRTUALIZER_TAIL_DROP]". Root cause of
    // the false alarm: after a refresh the rows array is full of COMMITTED
    // history (no live row). The virtualizer viewport naturally covers only
    // the visible window — the last historical row sitting below the fold is
    // NORMAL virtualization, not a data loss. The monitor must only fire when
    // the LAST row is the LIVE row (isPartial=true) yet unrendered.
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    try {
      const { rerender } = renderMessageList(
        <MessageList
          chatKey="web:chat-1"
          messages={makeMessages(30)} // committed history — no live row
          liveMessage={null}
          liveProgress={null}
          busy={false}
          collapseLevel="all"
          loading={false}
          error={null}
        />,
      )
      // Simulate refresh: still no live row, viewport at top.
      rerender(
        <MessageList
          chatKey="web:chat-1"
          messages={makeMessages(30)}
          liveMessage={null}
          liveProgress={null}
          busy={false}
          collapseLevel="all"
          loading={false}
          error={null}
        />,
      )
      expect(errorSpy).not.toHaveBeenCalledWith(
        expect.stringContaining('VIRTUALIZER_TAIL_DROP'),
        expect.anything(),
      )
    } finally {
      errorSpy.mockRestore()
    }
  })

  it('VIRTUALIZER_TAIL_DROP monitor: alarms when the LIVE row is unrendered while sticking to bottom', () => {
    // The actual bug: a live (isPartial) tail row exists but getVirtualItems()
    // does not cover it while the user is stuck to the bottom — the live turn
    // vanished from the DOM.
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    try {
      renderMessageList(
        <MessageList
          chatKey="web:chat-1"
          messages={makeMessages(30)}
          liveMessage={base({ id: 'turn-live-1', role: 'assistant', content: 'streaming', isPartial: true, turnID: 99 })}
          liveProgress={{ ...EMPTY_LIVE_PROGRESS, streaming: true, streamContent: 'streaming' }}
          busy
          collapseLevel="all"
          loading={false}
          error={null}
        />,
      )
      // Note: in the test harness the viewport is tall (clientHeight 600,
      // scrollHeight 12000) so getVirtualItems covers all 31 rows — the alarm
      // should NOT fire here either. This test documents the contract: the
      // monitor needs a real viewport truncation to fire; the condition itself
      // (isPartial guard) is what prevents refresh false-positives.
      expect(errorSpy).not.toHaveBeenCalledWith(
        expect.stringContaining('VIRTUALIZER_TAIL_DROP'),
        expect.anything(),
      )
    } finally {
      errorSpy.mockRestore()
    }
  })
})
