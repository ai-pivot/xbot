import { describe, it, expect } from 'vitest'
import { escapeRegex, splitByQuery, countMatches } from '../utils/highlight'

// ─── escapeRegex ───

describe('escapeRegex', () => {
  it('escapes special regex characters', () => {
    expect(escapeRegex('.*+?^${}()|[]\\')).toBe('\\.\\*\\+\\?\\^\\$\\{\\}\\(\\)\\|\\[\\]\\\\')
  })

  it('leaves normal text unchanged', () => {
    expect(escapeRegex('hello world 123')).toBe('hello world 123')
  })
})

// ─── splitByQuery ───

describe('splitByQuery', () => {
  it('returns single part for empty query', () => {
    const result = splitByQuery('hello world', '')
    expect(result).toEqual([{ text: 'hello world', isMatch: false }])
  })

  it('returns single part for whitespace-only query', () => {
    const result = splitByQuery('hello world', '   ')
    expect(result).toEqual([{ text: 'hello world', isMatch: false }])
  })

  it('splits text and marks matches', () => {
    const result = splitByQuery('hello world', 'world')
    // Regex split produces trailing empty string after match at end
    expect(result.filter(p => p.isMatch)).toEqual([{ text: 'world', isMatch: true }])
    expect(result.filter(p => !p.isMatch).some(p => p.text === 'hello ')).toBe(true)
  })

  it('handles case-insensitive matches', () => {
    const result = splitByQuery('Hello WORLD', 'world')
    // split is case-insensitive, match detection is also case-insensitive
    expect(result.filter(p => p.isMatch)).toEqual([{ text: 'WORLD', isMatch: true }])
    expect(result.filter(p => !p.isMatch).some(p => p.text === 'Hello ')).toBe(true)
  })

  it('handles no match — returns single unmarked part', () => {
    const result = splitByQuery('hello world', 'xyz')
    expect(result).toEqual([{ text: 'hello world', isMatch: false }])
  })

  it('handles multiple matches', () => {
    const result = splitByQuery('abc abc abc', 'abc')
    // Regex splits by all occurrences
    expect(result.filter(p => p.isMatch)).toHaveLength(3)
    expect(result.filter(p => !p.isMatch && p.text === ' ')).toHaveLength(2)
  })
})

// ─── countMatches ───

describe('countMatches', () => {
  it('returns 0 for empty query', () => {
    expect(countMatches('hello world', '')).toBe(0)
  })

  it('returns 0 for whitespace-only query', () => {
    expect(countMatches('hello world', '   ')).toBe(0)
  })

  it('counts matches correctly', () => {
    expect(countMatches('abc abc abc', 'abc')).toBe(3)
  })

  it('counts case-insensitively', () => {
    expect(countMatches('Hello HELLO hello', 'hello')).toBe(3)
  })

  it('returns 0 when no matches', () => {
    expect(countMatches('hello world', 'xyz')).toBe(0)
  })

  it('counts single match', () => {
    expect(countMatches('hello world', 'world')).toBe(1)
  })
})
