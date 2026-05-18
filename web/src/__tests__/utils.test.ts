import { describe, it, expect } from 'vitest'
import {
  formatElapsed,
  formatFileSize,
  normalizeIterationHistory,
  computeDisplayIterations,
} from '../utils'
import type { IterationSnapshot, WsProgressPayload } from '../components/ProgressPanel'

// ─── formatElapsed ───

describe('formatElapsed', () => {
  it('formats milliseconds < 1000', () => {
    expect(formatElapsed(0)).toBe('0ms')
    expect(formatElapsed(1)).toBe('1ms')
    expect(formatElapsed(500)).toBe('500ms')
    expect(formatElapsed(999)).toBe('999ms')
  })

  it('formats seconds >= 1000', () => {
    expect(formatElapsed(1000)).toBe('1.0s')
    expect(formatElapsed(1500)).toBe('1.5s')
    expect(formatElapsed(12345)).toBe('12.3s')
  })

  it('handles edge cases', () => {
    expect(formatElapsed(1001)).toBe('1.0s')
    expect(formatElapsed(1999)).toBe('2.0s')
  })
})

// ─── formatFileSize ───

describe('formatFileSize', () => {
  it('formats bytes', () => {
    expect(formatFileSize(0)).toBe('0 B')
    expect(formatFileSize(512)).toBe('512 B')
    expect(formatFileSize(1023)).toBe('1023 B')
  })

  it('formats kilobytes', () => {
    expect(formatFileSize(1024)).toBe('1.0 KB')
    expect(formatFileSize(1536)).toBe('1.5 KB')
    expect(formatFileSize(1024 * 1024 - 1)).toBe('1024.0 KB')
  })

  it('formats megabytes', () => {
    expect(formatFileSize(1024 * 1024)).toBe('1.0 MB')
    expect(formatFileSize(5 * 1024 * 1024)).toBe('5.0 MB')
    expect(formatFileSize(1024 * 1024 * 1024)).toBe('1024.0 MB')
  })
})

// ─── normalizeIterationHistory ───

describe('normalizeIterationHistory', () => {
  it('returns empty array for non-array input', () => {
    expect(normalizeIterationHistory(null)).toEqual([])
    expect(normalizeIterationHistory(undefined)).toEqual([])
    expect(normalizeIterationHistory('invalid')).toEqual([])
    expect(normalizeIterationHistory([])).toEqual([])
  })

  it('normalizes basic iteration data', () => {
    const input = [{
      iteration: 0,
      thinking: 'test thinking',
      tools: [{ name: 'Shell', status: 'done', elapsed_ms: 100 }],
    }]
    const result = normalizeIterationHistory(input)
    expect(result).toHaveLength(1)
    expect(result[0].iteration).toBe(0)
    expect(result[0].thinking).toBe('test thinking')
    expect(result[0].tools).toHaveLength(1)
    expect(result[0].tools[0].name).toBe('Shell')
  })

  it('handles case-insensitive field names', () => {
    const input = [{
      Iteration: 1,
      Thinking: 'caps thinking',
      Tools: [{ Name: 'Read', Status: 'done' }],
    }]
    const result = normalizeIterationHistory(input)
    expect(result).toHaveLength(1)
    expect(result[0].iteration).toBe(1)
    expect(result[0].thinking).toBe('caps thinking')
    expect(result[0].tools[0].name).toBe('Read')
  })

  it('sorts by iteration number', () => {
    const input = [
      { iteration: 2, tools: [] },
      { iteration: 0, tools: [] },
      { iteration: 1, tools: [] },
    ]
    const result = normalizeIterationHistory(input)
    expect(result.map(r => r.iteration)).toEqual([0, 1, 2])
  })

  it('deduplicates identical thinking text', () => {
    const input = [
      { iteration: 0, thinking: 'same thought', tools: [] },
      { iteration: 1, thinking: 'same thought', tools: [] },
      { iteration: 2, thinking: 'different thought', tools: [] },
    ]
    const result = normalizeIterationHistory(input)
    expect(result[0].thinking).toBe('same thought')
    expect(result[1].thinking).toBeUndefined()
    expect(result[2].thinking).toBe('different thought')
  })

  it('skips entries without iteration number', () => {
    const input = [
      { thinking: 'no iteration', tools: [] },
      { iteration: 1, tools: [] },
    ]
    const result = normalizeIterationHistory(input)
    expect(result).toHaveLength(1)
    expect(result[0].iteration).toBe(1)
  })
})

// ─── computeDisplayIterations ───

describe('computeDisplayIterations', () => {
  it('returns base iterations when no progress', () => {
    const base: IterationSnapshot[] = [
      { iteration: 0, tools: [] },
    ]
    expect(computeDisplayIterations(base, null)).toEqual(base)
    expect(computeDisplayIterations(undefined, null)).toEqual([])
  })

  it('returns base when progress has no completed tools', () => {
    const base: IterationSnapshot[] = [{ iteration: 0, tools: [] }]
    const progress: WsProgressPayload = {
      phase: 'thinking',
      iteration: 1,
      thinking: '',
      active_tools: [],
      completed_tools: [],
    }
    expect(computeDisplayIterations(base, progress)).toEqual(base)
  })

  it('infers previous iteration from progress completed_tools', () => {
    const progress: WsProgressPayload = {
      phase: 'tool_exec',
      iteration: 2,
      thinking: '',
      active_tools: [],
      completed_tools: [
        { name: 'Shell', label: 'Run command', status: 'done', elapsed_ms: 100 },
      ],
    }
    const result = computeDisplayIterations([], progress)
    expect(result).toHaveLength(1)
    expect(result[0].iteration).toBe(1) // prevIteration = 2 - 1
    expect(result[0].tools).toHaveLength(1)
    expect(result[0].tools[0].name).toBe('Shell')
  })

  it('does not duplicate existing iteration snapshot', () => {
    const base: IterationSnapshot[] = [
      { iteration: 1, tools: [{ name: 'Shell', status: 'done' }] },
    ]
    const progress: WsProgressPayload = {
      phase: 'tool_exec',
      iteration: 2,
      thinking: '',
      active_tools: [],
      completed_tools: [
        { name: 'Shell', label: 'Run', status: 'done', elapsed_ms: 100 },
      ],
    }
    const result = computeDisplayIterations(base, progress)
    expect(result).toHaveLength(1) // no duplication
    expect(result[0].iteration).toBe(1)
  })

  it('returns base when progress iteration is 0', () => {
    const progress: WsProgressPayload = {
      phase: 'thinking',
      iteration: 0,
      thinking: '',
      active_tools: [],
      completed_tools: [{ name: 'Shell', label: 'Run', status: 'done', elapsed_ms: 100 }],
    }
    expect(computeDisplayIterations([], progress)).toEqual([])
  })
})
