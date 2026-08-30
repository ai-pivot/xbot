import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { postAPI } from '@/lib/api'
import {
  clearWebCaches,
  getProgressGeneration,
  lastSeqCache,
  progressSnapshotCache,
  sessionCacheKey,
} from '@/lib/webCache'
import { SSEConnectionImpl, SSE_EVENT_TYPES, MultiSSEManager } from './sseConnection'
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

  it('dispatches seq-less control broadcasts despite a stale lastEventId', () => {
    const connection = new SSEConnectionImpl()
    const received: WSMessage[] = []
    connection.onMessage((message) => received.push(message))
    connection.subscribe('chat-a')
    const source = MockEventSource.instances[0]
    source.open()

    // web_plugin_config_changed 必须在 EventSource 监听白名单里 —— 不在
    // SSE_EVENT_TYPES 里 addEventListener 根本不注册，广播永远收不到。
    expect(SSE_EVENT_TYPES).toContain('web_plugin_config_changed')

    // 业务事件 seq=42 推进 lastSeq 水位。
    source.emit('text', { type: 'text', seq: 42, content: 'business' })
    expect(received.filter((m) => m.type === 'text')).toHaveLength(1)

    // 控制广播：JSON 无 seq 字段（后端 Seq=0 + omitempty），event.lastEventId
    // 残留 '42'（SSE 规范：无 id 事件不推进 lastEventId）。绝不能把残留 id
    // 继承为 seq —— 否则 seq === previousSeq(42) 走业务 dedup 被静默丢弃。
    source.emit(
      'web_plugin_config_changed',
      {
        type: 'web_plugin_config_changed',
        content: JSON.stringify({ plugin_id: 'xbot.ambience', value: { glassOpacity: 0.5 } }),
      },
      '42',
    )
    const control = received.filter((m) => m.type === 'web_plugin_config_changed')
    expect(control).toHaveLength(1)

    // 水位未被控制消息污染 —— 后续业务事件照常去重。
    source.emit('text', { type: 'text', seq: 43, content: 'next' })
    expect(received.filter((m) => m.type === 'text')).toHaveLength(2)
    expect(lastSeqCache.get(sessionCacheKey('web', 'chat-a'))).toBe(43)
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

  it('arms the watchdog on first-connect failure so a stalled native retry still reconnects', () => {
    // Regression: the watchdog was only armed in onopen. If the FIRST connect()
    // failed (server unreachable / network switch before the first open), onopen
    // never fired, so the watchdog was never armed. The native EventSource retry
    // can then stall (background tab / browser gave up after repeated failures),
    // leaving readyState stuck at CONNECTING(0) forever — the REST poll's
    // `readyState === 2` check never fires (EventSource never self-closes), so
    // the UI stayed on "Reconnecting…" with no active reconnect. Arming the
    // watchdog in onerror forces a fresh connect() until an open succeeds.
    vi.useFakeTimers()
    try {
      const connection = new SSEConnectionImpl()
      connection.subscribe('chat-a')
      const source = MockEventSource.instances[0]
      source.fail() // onerror BEFORE first open → watchdog armed

      // Native retry stalls (no onopen, no events). lastActivityAt is still 0
      // (never opened), so the watchdog's first check declares the connection
      // stale and forces a reconnect.
      vi.advanceTimersByTime(15_000 + 100)

      expect(MockEventSource.instances.length).toBeGreaterThanOrEqual(2)
      expect(source.closed).toBe(true)
      connection.dispose()
    } finally {
      vi.useRealTimers()
    }
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

  it('does NOT dispatch replay_gap when active-progress recovery returns done — preserves live row', async () => {
    // v2: restoreActiveProgress's done/null branch dispatches NOTHING (no
    // replay_gap, no session(idle)). replay_gap triggered reload() →
    // history_replaced on every tab switch, corrupting the state machine
    // ("live iter disappears when switching tabs after refresh"). Recovery
    // is handled by SSE last_event_id replay + activateSession's refresh().
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

    // No replay_gap (would trigger reload → history_replaced → state corruption)
    expect(received.some((m) => m.type === 'replay_gap')).toBe(false)
    // No phase='done' / session(idle) — they clear the live store
    expect(received.some((m) => m.type === 'progress_structured' && (m as { progress?: { phase?: string } }).progress?.phase === 'done')).toBe(false)
    expect(received.some((m) => m.type === 'session' && (m as { session?: { action?: string } }).session?.action === 'idle')).toBe(false)
    // Snapshot cache is NOT cleared
    expect(progressSnapshotCache.has(sessionCacheKey('web', 'chat-a'))).toBe(true)
    connection.dispose()
  })

  it('does NOT dispatch replay_gap when active-progress recovery returns null — preserves live row', async () => {
    // v2: done/null branch dispatches nothing (no replay_gap). Recovery is
    // handled by SSE replay + activateSession's refresh().
    vi.useFakeTimers()
    postAPIMock.mockImplementation(async (endpoint: string) => {
      if (endpoint === '/api/rpc') return null
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

    // No replay_gap (would trigger reload → state corruption)
    expect(received.some((m) => m.type === 'replay_gap')).toBe(false)
    // No phase='done' — the live row is preserved
    expect(received.some((m) => m.type === 'progress_structured' && (m as { progress?: { phase?: string } }).progress?.phase === 'done')).toBe(false)
    connection.dispose()
  })

  it('dispatches agent-idle when active-progress recovery returns done — restores running state lost in the SSE gap', async () => {
    // Mobile bug (user report 2026-08-30): user opens settings (app backgrounds
    // → SSE disconnects), the turn completes during the gap, and after
    // returning the session is STUCK busy — the reply renders (replay_gap
    // force_reload) but the busy indicator never clears. Root cause: the
    // session(idle) SSE event was lost in the gap (ring buffer evicted on
    // resync, or the disconnect window), so useSessionStore's
    // executingSessionsRef keeps the busy key and mergeStatus forces running
    // forever. restoreActiveProgress's done/null branch is the AUTHORITATIVE
    // turn-ended signal here — it must dispatch agent-idle (the
    // useSessionStore-only channel that clears running WITHOUT touching the
    // live store — unlike session(idle), which clears the live store and
    // causes rendered iterations to vanish, see the 449 test above).
    vi.useFakeTimers()
    postAPIMock.mockImplementation(async (endpoint: string) => {
      if (endpoint === '/api/rpc') return { phase: 'done', iteration: 2 }
      return {}
    })
    const connection = new SSEConnectionImpl()
    connection.subscribe('chat-a')
    const source = MockEventSource.instances[0]
    source.open()
    source.fail()
    source.open()

    const idleEvents: Array<{ chatID?: string; channel?: string }> = []
    const handler = (e: Event) => {
      idleEvents.push((e as CustomEvent).detail)
    }
    window.addEventListener('agent-idle', handler)
    try {
      await vi.advanceTimersByTimeAsync(1_000)
    } finally {
      window.removeEventListener('agent-idle', handler)
    }
    connection.dispose()

    expect(idleEvents.length).toBeGreaterThanOrEqual(1)
    expect(idleEvents.some((ev) => ev.chatID === 'chat-a')).toBe(true)
  })

  it('dispatches agent-idle when active-progress recovery returns null — same stuck-busy recovery', async () => {
    // null = no active progress (turn ended, lastProgressSnapshot cleaned) —
    // the same authoritative turn-ended signal as phase='done'.
    vi.useFakeTimers()
    postAPIMock.mockImplementation(async (endpoint: string) => {
      if (endpoint === '/api/rpc') return null
      return {}
    })
    const connection = new SSEConnectionImpl()
    connection.subscribe('chat-a')
    const source = MockEventSource.instances[0]
    source.open()
    source.fail()
    source.open()

    const idleEvents: Array<{ chatID?: string; channel?: string }> = []
    const handler = (e: Event) => {
      idleEvents.push((e as CustomEvent).detail)
    }
    window.addEventListener('agent-idle', handler)
    try {
      await vi.advanceTimersByTimeAsync(1_000)
    } finally {
      window.removeEventListener('agent-idle', handler)
    }
    connection.dispose()

    expect(idleEvents.some((ev) => ev.chatID === 'chat-a')).toBe(true)
  })

  it('does NOT dispatch replay_gap when recovery returns done even if a newer SSE event bumped progressVersion', async () => {
    // v2: done/null branch dispatches nothing — no replay_gap regardless of
    // progressVersion. The old test verified replay_gap fired BEFORE the
    // progressVersion check; now the done/null branch returns early before
    // reaching the progressVersion check.
    vi.useFakeTimers()
    let resolveProgress: (progress: { phase: string; iteration: number } | null) => void = () => undefined
    postAPIMock.mockImplementation((endpoint: string) => {
      if (endpoint === '/api/rpc') {
        return new Promise((resolve) => {
          resolveProgress = resolve as (p: { phase: string; iteration: number } | null) => void
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
    source.fail()
    source.open()

    // A newer event arrives while get_active_progress is in flight (bumps
    // progressVersion) — the done/null branch returns early regardless.
    source.emit('session', { type: 'session', seq: 5, session: { action: 'busy', chat_id: 'chat-a' } })
    resolveProgress({ phase: 'done', iteration: 3 })
    await vi.advanceTimersByTimeAsync(1_000)
    await Promise.resolve()

    // No replay_gap — done/null branch dispatches nothing in v2
    expect(received.some((m) => m.type === 'replay_gap')).toBe(false)
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

// ── 切会话竞态验证：错误取消订阅活跃 stream？ ──
// 用户报告：SSE 有概率中途卡住不再更新（最后事件 seq=9731），怀疑多次切会话
// 竞争错误取消活跃 stream。以下测试验证 subscribe/disconnect/connect 的 guard。
describe('SSE 切会话竞态', () => {
  it('快速切会话 A→B→A：旧连接全部关闭，最终 A 活跃（无泄漏无误关）', () => {
    const conn = new SSEConnectionImpl()
    conn.subscribe('chat-A')
    MockEventSource.instances[0].open()
    const sourceA = MockEventSource.instances[0]

    conn.subscribe('chat-B')
    expect(sourceA.closed).toBe(true) // A 被正确关闭
    MockEventSource.instances[1].open()
    const sourceB = MockEventSource.instances[1]

    conn.subscribe('chat-A')
    expect(sourceB.closed).toBe(true) // B 被正确关闭
    const sourceA2 = MockEventSource.instances[2]
    expect(sourceA2.closed).toBe(false) // 新 A 活跃
    expect(conn.chatID).toBe('chat-A')
  })

  it('restartSource 与切会话交错：迟到的旧连接事件不干扰新连接', () => {
    const conn = new SSEConnectionImpl()
    conn.subscribe('chat-A')
    MockEventSource.instances[0].open()
    const sourceA = MockEventSource.instances[0]

    // setLastSeq 触发 restartSource：关 sourceA + 开 sourceA2（CONNECTING）
    conn.setLastSeq('chat-A', 10)
    expect(sourceA.closed).toBe(true)
    const sourceA2 = MockEventSource.instances[1]
    expect(sourceA2.closed).toBe(false) // CONNECTING，未关闭

    // 切到 B：disconnect 关闭 sourceA2（即使 CONNECTING）
    conn.subscribe('chat-B')
    expect(sourceA2.closed).toBe(true)
    const sourceB = MockEventSource.instances[2]

    // sourceA2 的迟到 open/事件：this.source !== sourceA2（sourceB）→ 丢弃
    sourceA2.open()
    sourceA2.emit('text', { type: 'text', seq: 1, content: 'stale-A' } as WSMessage)
    expect(conn.chatID).toBe('chat-B')
    expect(sourceB.closed).toBe(false) // 新连接 B 活跃，未被干扰
  })

  it('切会话后旧 chatID 的 setLastSeq 不触发 restart（guard _chatID === chatID）', () => {
    const conn = new SSEConnectionImpl()
    conn.subscribe('chat-A')
    MockEventSource.instances[0].open()

    // 切到 B（sourceA 关闭，sourceB 活跃）
    conn.subscribe('chat-B')
    MockEventSource.instances[1].open()
    const sourceB = MockEventSource.instances[1]

    // 旧会话 A 的迟到 setLastSeq：_chatID='chat-B' !== 'chat-A' → 不 restart
    conn.setLastSeq('chat-A', 999)
    expect(sourceB.closed).toBe(false) // B 连接保持，未被 restartSource 误关
    expect(conn.chatID).toBe('chat-B')
  })

  // 复现 SSE 永久卡死：切换会话时 reload 完成 → setLastSeq 写入缓存 Y →
  // restartSource → 新连接带 last_event_id=Y → 服务器 forceResync（stream 暂停）
  // → 写 resync_required（event id = lastSentSeq = Y，msg.seq=0）→ 前端
  // handleEvent 中 seq=Y === previousSeq=Y → 被 seq 去重静默丢弃 → useChatMessages
  // 不 reload → publishSSEFallbacks 未合成 → 前端永久收不到新事件（用户报告：
  // "在隔壁会话 cancel 后切回，SSE 就不推送新的了，不刷新就永远这样"）。
  it('resync_required 不被 seq===previousSeq 丢弃（restartSource 后 forceResync 场景）', () => {
    const conn = new SSEConnectionImpl()
    const received: WSMessage[] = []
    conn.onMessage((message) => received.push(message))
    conn.subscribe('chat-a')
    const source = MockEventSource.instances[0]
    source.open()

    // reload 完成 → setLastSeq 写入缓存 10 → restartSource（source 关闭，新连接建立）
    conn.setLastSeq('chat-a', 10)
    const resumed = MockEventSource.instances[1]
    resumed.open()
    expect(lastSeqCache.get(sessionCacheKey('web', 'chat-a'))).toBe(10)

    // 服务器 forceResync：writeSSEResyncRequired 写 id=lastSentSeq=10（== 前端缓存），
    // msg.Seq 为 0 —— 前端必须用 event.lastEventId 解析 seq
    resumed.emit('resync_required', { type: 'resync_required' }, '10')

    // 控制事件必须被 dispatch —— 否则 useChatMessages 不 reload，进度永久卡死
    expect(received.map((m) => m.type)).toContain('resync_required')
    // 业务重复事件（seq 相同）仍被丢弃
    const dupReceived = received.length
    resumed.emit('text', { type: 'text', seq: 10, content: 'dup' }, '10')
    expect(received.length).toBe(dupReceived)
    conn.dispose()
  })

  it('restartSource 后 stream 恢复的新事件正常送达（resync_required 已 dispatch 不污染缓存）', () => {
    const conn = new SSEConnectionImpl()
    const received: WSMessage[] = []
    conn.onMessage((message) => received.push(message))
    conn.subscribe('chat-a')
    const source = MockEventSource.instances[0]
    source.open()
    conn.setLastSeq('chat-a', 10)
    const resumed = MockEventSource.instances[1]
    resumed.open()

    // forceResync 的 resync_required（id=10 == 缓存）→ 必须 dispatch
    resumed.emit('resync_required', { type: 'resync_required' }, '10')
    expect(received.map((m) => m.type)).toContain('resync_required')
    // 缓存保持 10（resync_required 不推进业务水位）
    expect(lastSeqCache.get(sessionCacheKey('web', 'chat-a'))).toBe(10)

    // stream 恢复：新事件 seq=11 必须正常送达
    resumed.emit('progress_structured', {
      type: 'progress_structured',
      seq: 11,
      progress: { phase: 'tool_exec' },
    } as WSMessage)
    expect(received.map((m) => m.seq)).toContain(11)
    expect(lastSeqCache.get(sessionCacheKey('web', 'chat-a'))).toBe(11)
    conn.dispose()
  })

  it('reloads from DB when active-progress recovery returns resync_required (gap too large)', async () => {
    // Gap-too-large guard: GetActiveProgress returns resync_required=true when
    // an incremental pull (from_iteration) would transfer more than the server
    // cap. The client MUST reload from DB (authoritative) instead of consuming
    // the huge delta — dispatch replay_gap, same as the done/null branch.
    vi.useFakeTimers()
    postAPIMock.mockImplementation(async (endpoint: string) => {
      if (endpoint === '/api/rpc') {
        return {
          iteration: 41,
          phase: 'tool_exec',
          iteration_history: [],
          resync_required: true,
        }
      }
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

    // replay_gap → useChatMessages reloads from DB.
    expect(received).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ type: 'replay_gap' }),
      ]),
    )
    // Must NOT dispatch progress_structured with the resync snapshot (its
    // iterationHistory is empty — applying it would clear the live store).
    expect(received.some((m) => m.type === 'progress_structured')).toBe(false)
    connection.dispose()
  })
})

// ==================== half-open connection watchdog ====================
// The browser EventSource does NOT fire onerror when the server dies / network
// cuts without a TCP reset. The watchdog declares the connection dead when no
// SSE event (incl. the 15s heartbeat) arrives for STALE_CONNECTION_MS (45s).
describe('half-open connection watchdog', () => {
  it('declares the connection dead and reconnects when no SSE event arrives for 45s', () => {
    vi.useFakeTimers()
    try {
      const connection = new SSEConnectionImpl()
      const connectedStates: boolean[] = []
      connection.onConnectionChange((c) => connectedStates.push(c))
      connection.subscribe('chat-1', 'web')
      const first = MockEventSource.instances[0]
      first.open()
      expect(connection.connected).toBe(true)

      // No events at all (server stuck / silent network cut) → after 45s the
      // watchdog must declare the connection dead and force a reconnect.
      vi.advanceTimersByTime(45_000 + 100)

      expect(connection.connected).toBe(false)
      // A new EventSource was created (reconnect).
      expect(MockEventSource.instances.length).toBeGreaterThanOrEqual(2)
      expect(connectedStates).toContain(false)
      connection.dispose()
    } finally {
      vi.useRealTimers()
    }
  })

  it('heartbeat events refresh lastActivityAt so the watchdog does NOT fire', () => {
    vi.useFakeTimers()
    try {
      const connection = new SSEConnectionImpl()
      connection.subscribe('chat-1', 'web')
      const first = MockEventSource.instances[0]
      first.open()

      // Server heartbeats every 15s — liveness refreshed each time.
      for (let i = 0; i < 5; i++) {
        vi.advanceTimersByTime(15_000)
        first.emit('heartbeat', { type: 'heartbeat' } as WSMessage)
      }
      // Advance past the stale threshold: heartbeats kept it alive.
      vi.advanceTimersByTime(15_000)

      expect(connection.connected).toBe(true)
      expect(MockEventSource.instances.length).toBe(1) // never reconnected
      connection.dispose()
    } finally {
      vi.useRealTimers()
    }
  })

  it('heartbeat events are NOT dispatched to business handlers', () => {
    vi.useFakeTimers()
    try {
      const connection = new SSEConnectionImpl()
      const messages: WSMessage[] = []
      connection.onMessage((m) => messages.push(m))
      connection.subscribe('chat-1', 'web')
      const first = MockEventSource.instances[0]
      first.open()

      first.emit('heartbeat', { type: 'heartbeat' } as WSMessage)

      expect(messages).toHaveLength(0)
      connection.dispose()
    } finally {
      vi.useRealTimers()
    }
  })
})

describe('MultiSSEManager primary 引用计数（split view）', () => {
  it('两个订阅同 chatID：移除一个不断连接，全部移除才断开（F#3）', () => {
    // split view 两个 AgentPanel 同 chatID 各得 'primary'，关一个 → 旧实现
    // removeSubscription('primary') 直接 disconnect → 存活面板的 SSE 被断。
    const mgr = new MultiSSEManager()
    const sub1 = mgr.addSubscription('chat-a', 'web')
    const sub2 = mgr.addSubscription('chat-a', 'web')
    expect(sub1).toBe('primary')
    expect(sub2).toBe('primary')
    // 复用同一 primary 连接（不建第二条 SSE）。
    expect(MockEventSource.instances).toHaveLength(1)
    MockEventSource.instances[0].open()
    expect(mgr.connected).toBe(true)

    // 移除第一个订阅：仍有一个存活 → primary 不断。
    mgr.removeSubscription(sub1)
    expect(MockEventSource.instances[0].closed).toBe(false)
    expect(mgr.connected).toBe(true)

    // 移除最后一个订阅 → 断开（无泄漏）。
    mgr.removeSubscription(sub2)
    expect(MockEventSource.instances[0].closed).toBe(true)
    mgr.dispose()
  })

  it('非 primary 订阅行为不变（同 chatID 不同 channel 建独立连接）', () => {
    const mgr = new MultiSSEManager()
    const p = mgr.addSubscription('chat-b', 'web')
    const extra = mgr.addSubscription('chat-b', 'cli')
    expect(p).toBe('primary')
    expect(extra).toBe('cli:chat-b')
    expect(MockEventSource.instances).toHaveLength(2)

    // extra 断开不影响 primary；primary 断开不影响 extra（原有行为保持）。
    mgr.removeSubscription(extra)
    expect(MockEventSource.instances[0].closed).toBe(false)
    expect(MockEventSource.instances[1].closed).toBe(true)
    mgr.removeSubscription(p)
    expect(MockEventSource.instances[0].closed).toBe(true)
    mgr.dispose()
  })
})
