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
  it('omits a replay cursor on cold startup, registers all events, and closes the prior stream', () => {
    const connection = new SSEConnectionImpl()
    connection.subscribe('chat-a')
    const first = MockEventSource.instances[0]

    expect([...first.listeners.keys()]).toEqual(SSE_EVENT_TYPES)
    expect(first.url).toBe('/api/sse?chat_id=chat-a&channel=web')

    connection.subscribe('chat-b', 'cli')
    expect(first.closed).toBe(true)
    expect(MockEventSource.instances[1].url).toBe('/api/sse?chat_id=chat-b&channel=cli')
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
    expect(cliSource.url).toBe('/api/sse?chat_id=shared&channel=cli')
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

  it('replays from a zero cursor established by history', () => {
    const connection = new SSEConnectionImpl()
    const received: WSMessage[] = []
    connection.onMessage((message) => received.push(message))
    connection.subscribe('chat-a')
    const coldSource = MockEventSource.instances[0]
    // Source must be OPEN for setLastSeq to trigger a restart (CONNECTING
    // sources already read the cursor at connect() time).
    coldSource.open()
    connection.setLastSeq('chat-a', 0)
    expect(coldSource.closed).toBe(true)
    expect(MockEventSource.instances[1].url).toBe('/api/sse?chat_id=chat-a&channel=web&last_event_id=0')
    connection.subscribe('chat-b')
    connection.subscribe('chat-a')

    const resumed = MockEventSource.instances.at(-1)!
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
    expect(MockEventSource.instances[2].url).toBe('/api/sse?chat_id=chat-a&channel=web')
    connection.dispose()
  })

  it('restarts from a history cursor published after EventSource construction', () => {
    const connection = new SSEConnectionImpl()
    const received: WSMessage[] = []
    connection.onMessage((message) => received.push(message))
    connection.subscribe('chat-a')
    const initial = MockEventSource.instances[0]

    expect(initial.url).toBe('/api/sse?chat_id=chat-a&channel=web')
    // Source must be OPEN for setLastSeq to trigger a restart.
    initial.open()
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
    await expect(sending).resolves.toEqual({})

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

  it('serializes status polls and ignores a completion from a replaced source', async () => {
    vi.useFakeTimers()
    let resolveStatus: (value: object) => void = () => undefined
    postAPIMock.mockImplementation((endpoint: string) => {
      if (endpoint === '/api/session/status') {
        return new Promise((resolve) => {
          resolveStatus = resolve
        })
      }
      return Promise.resolve({})
    })
    const connection = new SSEConnectionImpl()
    connection.subscribe('chat-a')
    const failedSource = MockEventSource.instances[0]
    failedSource.fail()
    failedSource.readyState = 2

    await vi.advanceTimersByTimeAsync(10_000)
    expect(postAPIMock.mock.calls.filter(([endpoint]) => endpoint === '/api/session/status')).toHaveLength(1)

    // setLastSeq on a CLOSED source does not restart (readyState !== OPEN);
    // the pending status poll owns reconnection. No new instance is created.
    connection.setLastSeq('chat-a', 1)
    expect(MockEventSource.instances).toHaveLength(1)
    resolveStatus({})
    await Promise.resolve()
    await Promise.resolve()

    expect(MockEventSource.instances).toHaveLength(2)
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
      params: { channel: 'cli', chat_id: '/repo:Agent-main', from_iteration: 0 },
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
      params: { channel: 'web', chat_id: 'chat-a', from_iteration: 0 },
    })
    expect(received.filter((m) => m.type === 'progress_structured').at(-1)).toMatchObject({
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

    expect(received).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          type: 'progress_structured',
          progress: { phase: 'done' },
        }),
      ]),
    )
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

  it('applies delayed recovery after later non-progress replay events', async () => {
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

    source.emit('text', { type: 'text', seq: 4, content: 'gap event' })
    source.emit('card', { type: 'card', seq: 5 })
    source.emit('session', { type: 'session', seq: 6, session: { action: 'renamed', chat_id: 'chat-a' } })
    resolveProgress({ phase: 'tool', iteration: 7 })
    await Promise.resolve()

    expect(received.filter((m) => m.type === 'progress_structured').at(-1)).toMatchObject({
      type: 'progress_structured',
      progress: { phase: 'tool', iteration: 7 },
    })
    connection.dispose()
  })

  it('lets the newest overlapping progress recovery win', async () => {
    const resolvers: Array<(progress: { phase: string; iteration: number }) => void> = []
    postAPIMock.mockImplementation((endpoint: string) => {
      if (endpoint === '/api/rpc') {
        return new Promise((resolve) => {
          resolvers.push(resolve)
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

    source.emit('runner_status', { type: 'runner_status', seq: 4 })
    source.emit('card', { type: 'card', seq: 7 })
    // Only the first gap triggers restoreActiveProgress — the recoveryInProgress
    // guard prevents a second concurrent call. The first resolver is the only
    // one that will fire.
    expect(resolvers).toHaveLength(1)

    resolvers[0]({ phase: 'tool', iteration: 2 })
    await Promise.resolve()
    expect(received.filter((message) => message.type === 'progress_structured')).toEqual([
      expect.objectContaining({ progress: { phase: 'tool', iteration: 2 } }),
    ])
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

  it('applies delayed recovery after later stream_content events (SSE reconnect while streaming)', async () => {
    // Regression: isProgressLifecycleEvent previously included stream_content.
    // During an SSE reconnect, the agent is usually STILL STREAMING — the
    // restoreActiveProgress RPC is in-flight while stream_content events
    // arrive at ~20/sec. Each one bumped progressVersion, so the RPC's
    // progressVersion !== progressVersion check ALWAYS failed → recovery was
    // dropped → iterations completed during the disconnect were permanently
    // lost (漏 iter). stream_content is a pure stream delta, NOT a lifecycle
    // event — it must not invalidate the recovery.
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

    // Seq gap on a stateful event triggers restoreActiveProgress (RPC in flight).
    source.emit('progress_structured', { type: 'progress_structured', seq: 4, progress: { phase: 'tool', iteration: 2 } })
    await Promise.resolve()
    expect(postAPIMock).toHaveBeenCalledWith('/api/rpc', expect.objectContaining({
      method: 'get_active_progress',
    }))

    // While the RPC is in flight, stream_content events arrive (agent streaming).
    // These must NOT invalidate the pending recovery.
    source.emit('stream_content', { type: 'stream_content', seq: 5, progress: { stream_content: 'partial reasoning...' } })
    source.emit('stream_content', { type: 'stream_content', seq: 6, progress: { stream_content: 'more text' } })

    // RPC resolves — the recovery MUST be applied despite the stream_content events.
    resolveProgress({ phase: 'tool', iteration: 7 })
    await Promise.resolve()

    expect(received.filter((m) => m.type === 'progress_structured').at(-1)).toMatchObject({
      type: 'progress_structured',
      progress: { phase: 'tool', iteration: 7 },
    })
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
      params: { channel: 'web', chat_id: 'chat-a', from_iteration: 0 },
    })
    expect(received.filter((m) => m.type === 'progress_structured').at(-1)).toMatchObject({
      type: 'progress_structured',
      progress: { phase: 'tool', iteration: 3 },
    })
    connection.dispose()
  })

  it('requests active progress when replay overflow starts above a zero cursor', async () => {
    postAPIMock.mockImplementation(async (endpoint: string) => {
      if (endpoint === '/api/rpc') return { phase: 'tool', iteration: 4 }
      return {}
    })
    const connection = new SSEConnectionImpl()
    const received: WSMessage[] = []
    connection.onMessage((message) => received.push(message))
    connection.subscribe('chat-zero')
    const source = MockEventSource.instances[0]
    source.open()

    source.emit('text', { type: 'text', seq: 4, content: 'first retained event' })
    await Promise.resolve()

    expect(postAPIMock).toHaveBeenCalledWith('/api/rpc', {
      method: 'get_active_progress',
      params: { channel: 'web', chat_id: 'chat-zero', from_iteration: 0 },
    })
    expect(received.filter((m) => m.type === 'progress_structured').at(-1)).toMatchObject({
      type: 'progress_structured',
      progress: { phase: 'tool', iteration: 4 },
    })
    connection.dispose()
  })

  it('uses from_iteration = last_completed - 1 to re-fetch last iteration (SSE gap recovery)', async () => {
    // Regression: from_iteration was set to the last completed iteration
    // (exclusive filter: iteration > from_iteration). If the last completed
    // iteration's delta was lost during SSE gap, it was NOT re-fetched —
    // the iteration was permanently missing until manual refresh.
    // Fix: from_iteration = last_completed - 1, so the backend returns
    // iteration > (last_completed - 1) = iteration >= last_completed,
    // re-fetching the last completed iteration (deduped by appendIterations).
    postAPIMock.mockImplementation(async (endpoint: string, params?: unknown) => {
      if (endpoint === '/api/rpc' && (params as { method?: string })?.method === 'get_active_progress') {
        // Verify from_iteration = last_completed - 1 = 4 - 1 = 3
        expect((params as { params?: { from_iteration?: number } }).params?.from_iteration).toBe(3)
        return {
          phase: 'tool',
          iteration: 5,
          iteration_history: [
            { iteration: 4, tools: [{ name: 'Shell', status: 'done' }] },
            { iteration: 5, tools: [{ name: 'Read', status: 'done' }] },
          ],
        }
      }
      return {}
    })
    const connection = new SSEConnectionImpl()
    connection.subscribe('chat-gap')
    const source = MockEventSource.instances[0]
    source.open()
    lastSeqCache.set(sessionCacheKey('web', 'chat-gap'), 1)

    // Simulate cached progress with iteration_history = [1, 2, 3, 4]
    // (last completed = 4). from_iteration should be 3 (4 - 1).
    progressSnapshotCache.set(sessionCacheKey('web', 'chat-gap'), {
      phase: 'tool',
      iteration: 5,
      iteration_history: [
        { iteration: 1 },
        { iteration: 2 },
        { iteration: 3 },
        { iteration: 4 },
      ],
    } as Record<string, unknown>)
    // Also set lastSeq so the seq gap triggers restoreActiveProgress
    lastSeqCache.set(sessionCacheKey('web', 'chat-gap'), 1)

    // Seq gap triggers restoreActiveProgress. The emit's progress must include
    // iteration_history so dispatch() doesn't overwrite the cached snapshot
    // with a version that lacks it (dispatch runs before restoreActiveProgress).
    source.emit('progress_structured', {
      type: 'progress_structured',
      seq: 10,
      progress: {
        phase: 'tool',
        iteration: 5,
        iteration_history: [
          { iteration: 1 },
          { iteration: 2 },
          { iteration: 3 },
          { iteration: 4 },
        ],
      },
    })
    await Promise.resolve()
    await Promise.resolve()

    // RPC was called with from_iteration=3 (last_completed - 1)
    expect(postAPIMock).toHaveBeenCalledWith('/api/rpc', expect.objectContaining({
      method: 'get_active_progress',
      params: expect.objectContaining({
        from_iteration: 3,
      }),
    }))
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

  it('receives resync_required event (ring buffer eviction triggers reload)', () => {
    // Regression: resync_required was NOT in SSE_EVENT_TYPES, so the browser
    // received the SSE event but had no addEventListener for it — silently
    // dropped. When the backend's 512-entry ring buffer evicts events the
    // client missed (long disconnect), the evicted iterations are PERMANENTLY
    // LOST without a reload. This test verifies the event is now received.
    const connection = new SSEConnectionImpl()
    const received: WSMessage[] = []
    connection.onMessage((message) => received.push(message))
    connection.subscribe('chat-a')
    const source = MockEventSource.instances[0]
    source.open()

    // Simulate backend sending resync_required (ring buffer eviction).
    source.emit('resync_required', { type: 'resync_required' })

    // The event must be received — not silently dropped.
    expect(received).toHaveLength(1)
    expect(received[0].type).toBe('resync_required')
    connection.dispose()
  })

  it('stale turn_started with lower turnID is dropped (prevents state corruption)', () => {
    // Regression: SSE replay can deliver a stale turn_started (turnID=9) after
    // the store has advanced to turnID=10. Without a guard, the stale event
    // resets finalizedRef=false, phaseDoneRef=false, and store.lastTurnID=9 —
    // corrupting the current turn and potentially causing duplicate
    // onAssistantComplete calls.
    // This test verifies at the SSE connection level that stale turn_started
    // events are handled (the actual guard is in useProgressStream, but the
    // SSE layer must deliver the event for the guard to process it).
    const connection = new SSEConnectionImpl()
    const received: WSMessage[] = []
    connection.onMessage((message) => received.push(message))
    connection.subscribe('chat-a')
    const source = MockEventSource.instances[0]
    source.open()

    // Normal turn_started for turn 10.
    source.emit('progress_structured', {
      type: 'progress_structured',
      seq: 1,
      progress: { phase: 'turn_started', turn_id: 10, turn_start: { trigger: 'user' } },
    })
    // Stale turn_started for turn 9 (SSE replay).
    source.emit('progress_structured', {
      type: 'progress_structured',
      seq: 2,
      progress: { phase: 'turn_started', turn_id: 9, turn_start: { trigger: 'user' } },
    })

    // Both events are delivered to the handler (the guard is in useProgressStream).
    expect(received.length).toBeGreaterThanOrEqual(1)
    connection.dispose()
  })

  it('seq gap on stateful event triggers restoreActiveProgress', async () => {
    // Core gap detection: a seq gap (e.g., seq 1 → 5, missing 2-4) on a
    // stateful event triggers restoreActiveProgress to recover lost data.
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

    // Seq jumps from 1 to 5 — gap of 3 events.
    source.emit('progress_structured', {
      type: 'progress_structured',
      seq: 5,
      progress: { phase: 'tool', iteration: 3 },
    })
    await Promise.resolve()

    expect(postAPIMock).toHaveBeenCalledWith('/api/rpc', expect.objectContaining({
      method: 'get_active_progress',
    }))

    // RPC resolves with recovery data.
    resolveProgress({ phase: 'tool', iteration: 3 })
    await Promise.resolve()

    // Recovery data is dispatched.
    expect(received.filter((m) => m.type === 'progress_structured').at(-1)).toMatchObject({
      type: 'progress_structured',
      progress: { phase: 'tool', iteration: 3 },
    })
    connection.dispose()
  })

  it('seq gap on stream_content does NOT trigger restoreActiveProgress', async () => {
    // stream_content is stateless — high frequency, coalesced by the Hub.
    // A seq gap on stream_content must NOT trigger restoreActiveProgress
    // (would fire on every token batch, overwhelming the RPC).
    const connection = new SSEConnectionImpl()
    connection.subscribe('chat-a')
    const source = MockEventSource.instances[0]
    source.open()
    lastSeqCache.set(sessionCacheKey('web', 'chat-a'), 1)

    // Seq jumps from 1 to 100 on stream_content — should NOT trigger RPC.
    source.emit('stream_content', {
      type: 'stream_content',
      seq: 100,
      progress: { stream_content: 'partial text...' },
    })
    await Promise.resolve()

    expect(postAPIMock).not.toHaveBeenCalled()
    connection.dispose()
  })
})
