import { describe, it, expect } from 'vitest'
import { interpolate } from '../i18n'

// Test the interpolate function directly
describe('interpolate', () => {
  it('returns template without params', () => {
    expect(interpolate('Hello World')).toBe('Hello World')
  })

  it('substitutes named parameters', () => {
    expect(interpolate('Hello {name}', { name: 'World' })).toBe('Hello World')
  })

  it('substitutes multiple parameters', () => {
    expect(interpolate('{a} + {b} = {c}', { a: '1', b: '2', c: '3' })).toBe('1 + 2 = 3')
  })

  it('handles numeric parameters', () => {
    expect(interpolate('{n} items', { n: 42 })).toBe('42 items')
  })

  it('preserves unmatched placeholders', () => {
    expect(interpolate('Hello {name}', {})).toBe('Hello {name}')
  })

  it('handles empty template', () => {
    expect(interpolate('')).toBe('')
  })
})

// Import the actual locale data to verify key coverage
import zhCN from '../i18n/zh-CN'
import en from '../i18n/en'

describe('i18n key coverage', () => {
  it('en.ts has all keys from zh-CN.ts', () => {
    const zhKeys = Object.keys(zhCN) as string[]
    const enKeys = Object.keys(en) as string[]
    const missing = zhKeys.filter(k => !enKeys.includes(k))
    expect(missing).toEqual([])
  })

  it('zh-CN.ts has all keys from en.ts', () => {
    const zhKeys = Object.keys(zhCN) as string[]
    const enKeys = Object.keys(en) as string[]
    const extra = enKeys.filter(k => !zhKeys.includes(k))
    expect(extra).toEqual([])
  })

  it('no empty values in zh-CN', () => {
    const emptyKeys = Object.entries(zhCN)
      .filter(([, v]) => !v || v.trim() === '')
      .map(([k]) => k)
    expect(emptyKeys).toEqual([])
  })

  it('no empty values in en', () => {
    const emptyKeys = Object.entries(en)
      .filter(([, v]) => !v || v.trim() === '')
      .map(([k]) => k)
    expect(emptyKeys).toEqual([])
  })
})
