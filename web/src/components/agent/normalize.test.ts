/**
 * Tests for the Agent normalization helpers (Spec 4).
 *
 * Verifies that history `detail` JSON, progress tool payloads, and iteration
 * history are coerced into the clean domain types the components expect, and
 * that malformed input never throws.
 */
import { describe, expect, it } from 'vitest'

import {
  normalizeIteration,
  normalizeIterationTool,
  normalizeTool,
  parseIterations,
} from '@/components/agent/normalize'
import { defaultOpenForLevel } from '@/hooks/useCollapseLevel'
import { toolStatusKind } from '@/types/agent'

describe('parseIterations', () => {
  it('parses a valid iteration-history JSON array', () => {
    const json = JSON.stringify([
      {
        iteration: 1,
        thinking: 'plan',
        reasoning: 'why',
        tools: [
          { name: 'Read', label: 'Read', status: 'done', elapsed_ms: 12, summary: 'ok' },
          { name: 'Shell', status: 'error', summary: 'boom' },
        ],
      },
      { iteration: 2, tools: [] },
    ])
    const out = parseIterations(json)
    expect(out).toHaveLength(2)
    expect(out[0].iteration).toBe(1)
    expect(out[0].thinking).toBe('plan')
    expect(out[0].tools).toHaveLength(2)
    expect(out[0].tools[0]).toEqual({
      name: 'Read',
      label: 'Read',
      status: 'done',
      elapsedMs: 12,
      summary: 'ok',
    })
    expect(out[0].tools[1].status).toBe('error')
  })

  it('returns [] for malformed JSON', () => {
    expect(parseIterations('not json')).toEqual([])
    expect(parseIterations('{ "a": 1 }')).toEqual([])
    expect(parseIterations(null)).toEqual([])
    expect(parseIterations(undefined)).toEqual([])
  })

  it('skips non-object entries', () => {
    expect(parseIterations(JSON.stringify([null, 1, 'x', { iteration: 5, tools: [] }]))).toEqual([
      { iteration: 5, thinking: undefined, reasoning: undefined, tools: [] },
    ])
  })
})

describe('normalizeTool', () => {
  it('coerces a progress tool entry', () => {
    expect(normalizeTool({ name: 'Grep', status: 'running', elapsed_ms: 3, args: '{}' })).toEqual({
      name: 'Grep',
      label: undefined,
      status: 'running',
      elapsedMs: 3,
      iteration: undefined,
      summary: undefined,
      detail: undefined,
      args: '{}',
    })
  })
  it('returns null for non-object', () => {
    expect(normalizeTool(null)).toBeNull()
    expect(normalizeTool(undefined)).toBeNull()
    expect(normalizeTool(42)).toBeNull()
  })
})

describe('normalizeIterationTool', () => {
  it('defaults missing status to done', () => {
    expect(normalizeIterationTool({ name: 'Read' })).toEqual({
      name: 'Read',
      label: undefined,
      status: 'done',
      elapsedMs: undefined,
      summary: undefined,
    })
  })
})

describe('normalizeIteration', () => {
  it('handles missing tools array', () => {
    expect(normalizeIteration({ iteration: 3 })).toEqual({
      iteration: 3,
      thinking: undefined,
      reasoning: undefined,
      elapsedMs: undefined,
      tools: [],
    })
  })
  it('returns null for non-object', () => {
    expect(normalizeIteration(null)).toBeNull()
  })
  it('falls back to completed_tools when tools is absent (histIterSnapshot shape)', () => {
    const out = normalizeIteration({ iteration: 2, completed_tools: [{ name: 'Read', status: 'done' }] })
    expect(out).not.toBeNull()
    expect(out!.tools).toHaveLength(1)
    expect(out!.tools[0].name).toBe('Read')
  })
  it('reads elapsed_wall into elapsedMs', () => {
    const out = normalizeIteration({ iteration: 5, elapsed_wall: 12345, tools: [] })
    expect(out).not.toBeNull()
    expect(out!.elapsedMs).toBe(12345)
  })
})

describe('defaultOpenForLevel', () => {
  it('none expands tools and iterations, but reasoning stays collapsed (T always folded)', () => {
    expect(defaultOpenForLevel('none', 'tool')).toBe(true)
    expect(defaultOpenForLevel('none', 'reasoning')).toBe(false)
    expect(defaultOpenForLevel('none', 'iteration')).toBe(true)
  })
  it('all collapses everything', () => {
    expect(defaultOpenForLevel('all', 'tool')).toBe(false)
    expect(defaultOpenForLevel('all', 'iteration')).toBe(false)
  })
  it('minimal collapses bodies (header summaries still shown)', () => {
    expect(defaultOpenForLevel('minimal', 'tool')).toBe(false)
    expect(defaultOpenForLevel('minimal', 'reasoning')).toBe(false)
    expect(defaultOpenForLevel('minimal', 'iteration')).toBe(false)
  })
})

describe('toolStatusKind', () => {
  it.each([
    ['done', 'done'],
    ['error', 'error'],
    ['running', 'running'],
    ['pending', 'pending'],
    [undefined, 'pending'],
    ['unknown', 'pending'],
  ])('status %s → %s', (input, expected) => {
    expect(toolStatusKind(input)).toBe(expected)
  })
})
