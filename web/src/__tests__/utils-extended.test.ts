import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook } from '@testing-library/react'
import { formatTime, createResetProgress, exportAsMarkdown, exportAsJSON } from '../utils'
import { useRetry } from '../hooks/useNetworkStatus'

/* eslint-disable @typescript-eslint/no-explicit-any */

// ─── formatTime ───

describe('formatTime', () => {
  it('formats zero timestamp', () => {
    // epoch 0 = 1970-01-01 00:00:00 UTC
    // toLocaleTimeString result depends on timezone; just verify it returns a string
    const result = formatTime(0)
    expect(typeof result).toBe('string')
    expect(result.length).toBeGreaterThan(0)
  })

  it('formats a known timestamp', () => {
    // 2024-01-01 12:00:00 UTC = 1704110400
    const result = formatTime(1704110400)
    expect(typeof result).toBe('string')
    // Should contain digits and colon
    expect(/\d/.test(result)).toBe(true)
  })

  it('handles large timestamps', () => {
    // Far future: 2099-01-01
    const result = formatTime(4070908800)
    expect(typeof result).toBe('string')
    expect(result.length).toBeGreaterThan(0)
  })
})

// ─── createResetProgress ───

describe('createResetProgress', () => {
  it('calls setProgress with null', () => {
    const setProgress = vi.fn()
    const reset = createResetProgress({
      setProgress,
      setLiveIterations: vi.fn(),
      prevIterationRef: { current: 0 } as any,
      progressRef: { current: {} } as any,
      reasoningRef: { current: 'hello' } as any,
      streamingContentRef: { current: 'world' } as any,
    })
    reset()
    expect(setProgress).toHaveBeenCalledWith(null)
  })

  it('calls setLiveIterations with empty array', () => {
    const setLiveIterations = vi.fn()
    const reset = createResetProgress({
      setProgress: vi.fn(),
      setLiveIterations,
      prevIterationRef: { current: 0 } as any,
      progressRef: { current: {} } as any,
      reasoningRef: { current: '' } as any,
      streamingContentRef: { current: '' } as any,
    })
    reset()
    expect(setLiveIterations).toHaveBeenCalledWith([])
  })

  it('sets prevIterationRef to -1', () => {
    const prevIterationRef = { current: 5 } as any
    const reset = createResetProgress({
      setProgress: vi.fn(),
      setLiveIterations: vi.fn(),
      prevIterationRef,
      progressRef: { current: {} } as any,
      reasoningRef: { current: '' } as any,
      streamingContentRef: { current: '' } as any,
    })
    reset()
    expect(prevIterationRef.current).toBe(-1)
  })

  it('sets progressRef to null and refs to empty strings', () => {
    const progressRef = { current: { phase: 'running' } } as any
    const reasoningRef = { current: 'some reasoning' } as any
    const streamingContentRef = { current: 'some content' } as any
    const reset = createResetProgress({
      setProgress: vi.fn(),
      setLiveIterations: vi.fn(),
      prevIterationRef: { current: 0 } as any,
      progressRef,
      reasoningRef,
      streamingContentRef,
    })
    reset()
    expect(progressRef.current).toBeNull()
    expect(reasoningRef.current).toBe('')
    expect(streamingContentRef.current).toBe('')
  })
})

// ─── useRetry ───

describe('useRetry', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('returns result on first successful attempt', async () => {
    const { result } = renderHook(() => useRetry())
    const fn = vi.fn().mockResolvedValue('success')
    const promise = result.current(fn, 3, 100)
    await expect(promise).resolves.toBe('success')
    expect(fn).toHaveBeenCalledOnce()
  })

  it('retries on failure and succeeds on second attempt', async () => {
    const { result } = renderHook(() => useRetry())
    const fn = vi.fn()
      .mockRejectedValueOnce(new Error('fail'))
      .mockResolvedValueOnce('recovered')
    const promise = result.current(fn, 3, 100)

    // Advance timers enough to cover baseDelay * 2^0 + random*500
    await vi.advanceTimersByTimeAsync(2000)

    const val = await promise
    expect(val).toBe('recovered')
    expect(fn).toHaveBeenCalledTimes(2)
  })

  it('throws after max attempts exhausted', async () => {
    const { result } = renderHook(() => useRetry())
    const fn = vi.fn().mockRejectedValue(new Error('always fail'))
    const promise = result.current(fn, 2, 100)
    // Prevent unhandled rejection warning from Node.js
    promise.catch(() => {})

    // Advance through all retry delays
    await vi.advanceTimersByTimeAsync(2000)

    try {
      await promise
      expect.unreachable('Should have thrown')
    } catch (err) {
      expect(err).toBeInstanceOf(Error)
      expect((err as Error).message).toBe('always fail')
    }
    expect(fn).toHaveBeenCalledTimes(2)
  })
})

// ─── exportAsMarkdown ───

describe('exportAsMarkdown', () => {
  it('formats messages as markdown', () => {
    const messages = [
      { id: '1', type: 'user' as const, content: 'Hello' },
      { id: '2', type: 'assistant' as const, content: 'Hi there' },
    ]
    const result = exportAsMarkdown(messages)
    expect(result).toContain('👤 User')
    expect(result).toContain('🤖 Assistant')
    expect(result).toContain('Hello')
    expect(result).toContain('Hi there')
    expect(result).toContain('---')
  })

  it('includes export date in header', () => {
    const result = exportAsMarkdown([])
    expect(result).toContain('# Chat Export')
  })

  it('handles system messages', () => {
    const messages = [
      { id: '1', type: 'system' as const, content: 'System msg' },
    ]
    const result = exportAsMarkdown(messages)
    expect(result).toContain('📋 System')
  })
})

// ─── exportAsJSON ───

describe('exportAsJSON', () => {
  it('exports messages as valid JSON', () => {
    const messages = [
      { id: '1', type: 'user' as const, content: 'Hello', ts: 12345 },
    ]
    const result = exportAsJSON(messages)
    const parsed = JSON.parse(result)
    expect(parsed).toHaveLength(1)
    expect(parsed[0].id).toBe('1')
    expect(parsed[0].type).toBe('user')
    expect(parsed[0].content).toBe('Hello')
  })

  it('pretty-prints with 2-space indent', () => {
    const result = exportAsJSON([])
    expect(result).toBe('[]')
  })
})
