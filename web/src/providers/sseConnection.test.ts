import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { postAPI } from '@/lib/api'
import {
  clearWebCaches,
  getProgressGeneration,
  lastSeqCache,
  progressSnapshotCache,
  sessionCacheKey,
} from '@/lib/webCache'
import { SSEConnectionImpl, SSE_EVENT_TYPES } from './sseConnection'
import type { WSMessage } from '@/types/shared'

vi.mock('@/lib/api', () => ({
  postAPI: vi.fn(),
}))

const postAPIMock = vi.mocked(postAPI)

class MockEventSource {
  static instances: MockEventSource[] = []

  readonly url: string
  readyState = 0
  onopen: ((event: Event) => void) | null = null
  onerror: ((event: Event) => void) | null = null
  closed = false
  listeners = new Map<string, Set<(event: MessageEvent<string>) => void>>()

  constructor(url: string | URL) {
    this.url = String(url)
    MockEventSource.instances.push(this)
  }

  addEventListener(type: string, listener: EventListenerOrEventListenerObject): void {
    const handler = listener as (event: MessageEvent<string>) => void
    const handlers = this.listeners.get(type) ?? new Set()
    handlers.add(handler)
    this.listeners.set(type, handlers)
  }

  close(): void {
    this.closed = true
    this.readyState = 2
  }

  open(): void {
    this.readyState = 1
    this.onopen?.(new Event('open'))
  }

  fail(): void {
    this.readyState = 0
    this.onerror?.(new Event('error'))
  }

  emit(type: string, message: WSMessage, lastEventId = String(message.seq ?? '')): void {
    const event = new MessageEvent<string>(type, {
      data: JSON.stringify(message),
      lastEventId,
    })
    this.listeners.get(type)?.forEach((handler) => handler(event))
  }
}

beforeEach(() => {
  MockEventSource.instances = []
  clearWebCaches()
  postAPIMock.mockReset()
  postAPIMock.mockResolvedValue({})
  vi.stubGlobal('EventSource', MockEventSource)
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('SSEConnectionImpl', () => {
  it('registers all contract event types and closes the prior chat stream', () => {
    const connection = new SSEConnectionImpl()
    connection.subscribe('chat-a')
    const first = MockEventSource.instances[0]

    expect([...first.listeners.keys()]).toEqual(SSE_EVENT_TYPES)
    expect(first.url).toBe('/api/sse?chat_id=chat-a&channel=web&last_event_id=0')

    connection.subscribe('chat-b', 'cli')
    expect(first.closed).toBe(true)
    expect(MockEventSource.instances[1].url).toBe('/api/sse?chat_id=chat-b&channel=cli&last_event_id=0')
    connection.dispose()
  })

  it('isolates replay cursors and progress for matching chat IDs on different channels', () => {
    const connection = new SSEConnectionImpl()
    connection.subscribe('shared', 'web')
    MockEventSource.instances[0].emit('progress_structured', {
      type: 'progress_structured',
      seq: 7,
      progress: { phase: 'web-progress' },
    })

    connection.subscribe('shared', 'cli')
    const cliSource = MockEventSource.instances[1]
    expect(cliSource.url).toBe('/api/sse?chat_id=shared&channel=cli&last_event_id=0')
    cliSource.emit('progress_structured', {
      type: 'progress_structured',
      seq: 1,
      progress: { phase: 'cli-progress' },
    })

    expect(lastSeqCache.get(sessionCacheKey('web', 'shared'))).toBe(7)
    expect(lastSeqCache.get(sessionCacheKey('cli', 'shared'))).toBe(1)
    expect(progressSnapshotCache.get(sessionCacheKey('web', 'shared'))).toMatchObject({ phase: 'web-progress' })
    expect(progressSnapshotCache.get(sessionCacheKey('cli', 'shared'))).toMatchObject({ phase: 'cli-progress' })

    connection.subscribe('shared', 'web')
    expect(MockEventSource.instances[2].url).toBe('/api/sse?chat_id=shared&channel=web&last_event_id=7')
    connection.dispose()
  })

  it('replays from zero after switching away before the first event', () => {
    const connection = new SSEConnectionImpl()
    const received: WSMessage[] = []
    connection.onMessage((message) => received.push(message))
    connection.subscribe('chat-a')
    connection.subscribe('chat-b')
    connection.subscribe('chat-a')

    const resumed = MockEventSource.instances[2]
    expect(resumed.url).toBe('/api/sse?chat_id=chat-a&channel=web&last_event_id=0')
    resumed.emit('text', { type: 'text', seq: 1, content: 'buffered while inactive' })

    expect(received.map((message) => message.content)).toEqual(['buffered while inactive'])
    connection.dispose()
  })

  it('resumes from the cached cursor after switching A to B to A', () => {
    const connection = new SSEConnectionImpl()
    connection.subscribe('chat-a')
    MockEventSource.instances[0].emit('text', { type: 'text', seq: 7, content: 'cached' })

    connection.subscribe('chat-b')
    connection.subscribe('chat-a')

    expect(MockEventSource.instances[2].url).toBe('/api/sse?chat_id=chat-a&channel=web&last_event_id=7')
    connection.dispose()
  })

  it('stores a history cursor for its explicit chat instead of the active stream', () => {
    const connection = new SSEConnectionImpl()
    connection.subscribe('chat-a')

    connection.setLastSeq('chat-b', 9)
    connection.subscribe('chat-b')
    connection.subscribe('chat-a')

    expect(MockEventSource.instances[1].url).toBe('/api/sse?chat_id=chat-b&channel=web&last_event_id=9')
    expect(MockEventSource.instances[2].url).toBe('/api/sse?chat_id=chat-a&channel=web&last_event_id=0')
    connection.dispose()
  })

  it('restarts from a history cursor published after EventSource construction', () => {
    const connection = new SSEConnectionImpl()
    const received: WSMessage[] = []
    connection.onMessage((message) => received.push(message))
    connection.subscribe('chat-a')
    const initial = MockEventSource.instances[0]

    expect(initial.url).toBe('/api/sse?chat_id=chat-a&channel=web&last_event_id=0')
    connection.setLastSeq('chat-a', 2)
    const resumed = MockEventSource.instances[1]
    expect(initial.closed).toBe(true)
    expect(resumed.url).toBe('/api/sse?chat_id=chat-a&channel=web&last_event_id=2')

    initial.emit('text', { type: 'text', seq: 1, content: 'ignored closed source' })
    resumed.emit('text', { type: 'text', seq: 3, content: 'after history' })

    expect(received.map((message) => message.content)).toEqual(['after history'])
    connection.dispose()
  })

  it('deduplicates sequences and records structured progress', () => {
    const connection = new SSEConnectionImpl()
    const received: WSMessage[] = []
    connection.onMessage((message) => received.push(message))
    connection.subscribe('chat-a')
    const source = MockEventSource.instances[0]
    source.open()

    source.emit('text', { type: 'text', seq: 3, content: 'first' })
    source.emit('text', { type: 'text', seq: 3, content: 'duplicate' })
    source.emit('progress_structured', {
      type: 'progress_structured',
      seq: 4,
      progress: { phase: 'tool' },
    })

    expect(received.map((message) => message.seq)).toEqual([3, 4])
    const cacheKey = sessionCacheKey('web', 'chat-a')
    expect(lastSeqCache.get(cacheKey)).toBe(4)
    expect(progressSnapshotCache.get(cacheKey)).toMatchObject({ phase: 'tool' })
    expect(getProgressGeneration(cacheKey)).toBeGreaterThan(0)
    connection.dispose()
  })

  it('clears the cached progress snapshot on terminal text', () => {
    const connection = new SSEConnectionImpl()
    connection.subscribe('chat-a')
    const source = MockEventSource.instances[0]
    source.open()
    source.emit('progress_structured', {
      type: 'progress_structured',
      seq: 1,
      progress: { phase: 'tool', completed_tools: [{ name: 'Read' }] },
    })
    expect(progressSnapshotCache.has(sessionCacheKey('web', 'chat-a'))).toBe(true)

    source.emit('text', { type: 'text', seq: 2, content: 'done' })

    expect(progressSnapshotCache.has(sessionCacheKey('web', 'chat-a'))).toBe(false)
    connection.dispose()
  })

  it('retries message POST with exponential delays at most three attempts', async () => {
    vi.useFakeTimers()
    postAPIMock
      .mockRejectedValueOnce(new Error('offline'))
      .mockRejectedValueOnce(new Error('offline'))
      .mockResolvedValueOnce({})
    const connection = new SSEConnectionImpl()

    const sending = connection.send({ type: 'message', chat_id: 'chat-a', content: 'hello' })
    await vi.runAllTimersAsync()
    await expect(sending).resolves.toBeUndefined()

    expect(postAPIMock).toHaveBeenCalledTimes(3)
    expect(postAPIMock).toHaveBeenLastCalledWith('/api/message', expect.objectContaining({
      chat_id: 'chat-a',
      content: 'hello',
    }))
    const requestIDs = postAPIMock.mock.calls.map(([, body]) => (
      body as { id?: string }
    ).id)
    expect(requestIDs[0]).toBeTruthy()
    expect(new Set(requestIDs).size).toBe(1)
    connection.dispose()
  })

  it('polls session status after an SSE error and stops when SSE reopens', async () => {
    vi.useFakeTimers()
    const connection = new SSEConnectionImpl()
    connection.subscribe('chat-a')
    const source = MockEventSource.instances[0]
    source.fail()

    await vi.advanceTimersByTimeAsync(5_000)
    expect(postAPIMock).toHaveBeenCalledWith('/api/session/status', {
      channel: 'web',
      chat_id: 'chat-a',
    })

    postAPIMock.mockClear()
    source.open()
    await vi.advanceTimersByTimeAsync(5_000)
    expect(postAPIMock).not.toHaveBeenCalledWith('/api/session/status', expect.anything())
    connection.dispose()
  })

  it('resumes from the cached cursor when polling recreates a closed source', async () => {
    vi.useFakeTimers()
    const connection = new SSEConnectionImpl()
    connection.subscribe('chat-a')
    const source = MockEventSource.instances[0]
    source.open()
    source.emit('text', { type: 'text', seq: 5, content: 'before disconnect' })
    source.fail()
    source.readyState = 2

    await vi.advanceTimersByTimeAsync(5_000)

    expect(MockEventSource.instances).toHaveLength(2)
    expect(MockEventSource.instances[1].url).toBe('/api/sse?chat_id=chat-a&channel=web&last_event_id=5')
    connection.dispose()
  })

  it('uses the subscribed CLI channel for polling and progress recovery', async () => {
    vi.useFakeTimers()
    postAPIMock.mockImplementation(async (endpoint: string) => {
      if (endpoint === '/api/rpc') return { phase: 'tool', iteration: 4 }
      return {}
    })
    const connection = new SSEConnectionImpl()
    connection.subscribe('/repo:Agent-main', 'cli')
    const source = MockEventSource.instances[0]
    source.open()
    source.fail()

    await vi.advanceTimersByTimeAsync(5_000)
    expect(postAPIMock).toHaveBeenCalledWith('/api/session/status', {
      channel: 'cli',
      chat_id: '/repo:Agent-main',
    })

    source.open()
    await vi.advanceTimersByTimeAsync(1_000)
    expect(postAPIMock).toHaveBeenCalledWith('/api/rpc', {
      method: 'get_active_progress',
      params: { channel: 'cli', chat_id: '/repo:Agent-main' },
    })
    connection.dispose()
  })

  it('requests active progress when reconnect replay is empty', async () => {
    vi.useFakeTimers()
    postAPIMock.mockImplementation(async (endpoint: string) => {
      if (endpoint === '/api/rpc') return { phase: 'tool', iteration: 2 }
      return {}
    })
    const connection = new SSEConnectionImpl()
    const received: WSMessage[] = []
    connection.onMessage((message) => received.push(message))
    connection.subscribe('chat-a')
    const source = MockEventSource.instances[0]
    source.open()
    source.fail()
    source.open()

    await vi.advanceTimersByTimeAsync(1_000)

    expect(postAPIMock).toHaveBeenCalledWith('/api/rpc', {
      method: 'get_active_progress',
      params: { channel: 'web', chat_id: 'chat-a' },
    })
    expect(received.at(-1)).toMatchObject({
      type: 'progress_structured',
      progress: { phase: 'tool', iteration: 2 },
    })
    connection.dispose()
  })

  it('ignores a completed active-progress recovery snapshot', async () => {
    vi.useFakeTimers()
    postAPIMock.mockImplementation(async (endpoint: string) => {
      if (endpoint === '/api/rpc') return { phase: 'done', iteration: 2 }
      return {}
    })
    const connection = new SSEConnectionImpl()
    const received: WSMessage[] = []
    connection.onMessage((message) => received.push(message))
    progressSnapshotCache.set(sessionCacheKey('web', 'chat-a'), { phase: 'tool' })
    connection.subscribe('chat-a')
    const source = MockEventSource.instances[0]
    source.open()
    source.fail()
    source.open()

    await vi.advanceTimersByTimeAsync(1_000)

    expect(received).toEqual([
      expect.objectContaining({
        type: 'progress_structured',
        progress: { phase: 'done' },
      }),
    ])
    expect(progressSnapshotCache.has(sessionCacheKey('web', 'chat-a'))).toBe(false)
    connection.dispose()
  })

  it('does not apply delayed recovery after a newer SSE event', async () => {
    let resolveProgress: (progress: { phase: string; iteration: number }) => void = () => undefined
    postAPIMock.mockImplementation((endpoint: string) => {
      if (endpoint === '/api/rpc') {
        return new Promise((resolve) => {
          resolveProgress = resolve
        })
      }
      return Promise.resolve({})
    })
    const connection = new SSEConnectionImpl()
    const received: WSMessage[] = []
    connection.onMessage((message) => received.push(message))
    connection.subscribe('chat-a')
    const source = MockEventSource.instances[0]
    source.open()
    lastSeqCache.set(sessionCacheKey('web', 'chat-a'), 1)

    source.emit('text', { type: 'text', seq: 4, content: 'gap event' })
    source.emit('text', { type: 'text', seq: 5, content: 'newer event' })
    resolveProgress({ phase: 'tool', iteration: 1 })
    await Promise.resolve()

    expect(received.map((message) => message.content)).toEqual(['gap event', 'newer event'])
    expect(progressSnapshotCache.has(sessionCacheKey('web', 'chat-a'))).toBe(false)
    connection.dispose()
  })

  it('does not apply delayed recovery after switching sessions', async () => {
    let resolveProgress: (progress: { phase: string; iteration: number }) => void = () => undefined
    postAPIMock.mockImplementation((endpoint: string) => {
      if (endpoint === '/api/rpc') {
        return new Promise((resolve) => {
          resolveProgress = resolve
        })
      }
      return Promise.resolve({})
    })
    const connection = new SSEConnectionImpl()
    const received: WSMessage[] = []
    connection.onMessage((message) => received.push(message))
    connection.subscribe('chat-a')
    const source = MockEventSource.instances[0]
    source.open()
    lastSeqCache.set(sessionCacheKey('web', 'chat-a'), 1)

    source.emit('text', { type: 'text', seq: 4, content: 'gap event' })
    connection.subscribe('chat-b')
    resolveProgress({ phase: 'tool', iteration: 1 })
    await Promise.resolve()

    expect(received).toHaveLength(1)
    expect(progressSnapshotCache.has(sessionCacheKey('web', 'chat-a'))).toBe(false)
    expect(progressSnapshotCache.has(sessionCacheKey('web', 'chat-b'))).toBe(false)
    connection.dispose()
  })

  it('requests active progress when an event sequence gap reveals replay overflow', async () => {
    postAPIMock.mockImplementation(async (endpoint: string) => {
      if (endpoint === '/api/rpc') return { phase: 'tool', iteration: 3 }
      return {}
    })
    const connection = new SSEConnectionImpl()
    const received: WSMessage[] = []
    connection.onMessage((message) => received.push(message))
    connection.subscribe('chat-a')
    const source = MockEventSource.instances[0]
    source.open()
    lastSeqCache.set(sessionCacheKey('web', 'chat-a'), 1)

    source.emit('text', { type: 'text', seq: 4, content: 'after gap' })
    await Promise.resolve()

    expect(postAPIMock).toHaveBeenCalledWith('/api/rpc', {
      method: 'get_active_progress',
      params: { channel: 'web', chat_id: 'chat-a' },
    })
    expect(received.at(-1)).toMatchObject({
      type: 'progress_structured',
      progress: { phase: 'tool', iteration: 3 },
    })
    connection.dispose()
  })

  it('accepts a lower sequence after the server sequence restarts', () => {
    const connection = new SSEConnectionImpl()
    const received: WSMessage[] = []
    connection.onMessage((message) => received.push(message))
    connection.subscribe('chat-a')
    const source = MockEventSource.instances[0]
    source.open()
    lastSeqCache.set(sessionCacheKey('web', 'chat-a'), 9)

    source.emit('text', { type: 'text', seq: 1, content: 'after restart' })

    expect(received).toHaveLength(1)
    expect(lastSeqCache.get(sessionCacheKey('web', 'chat-a'))).toBe(1)
    connection.dispose()
  })
})
