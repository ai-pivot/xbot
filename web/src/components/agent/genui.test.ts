import { describe, expect, it } from 'vitest'
import { stripGenUIPrefix, genUICode } from './genui'

const TSX = `export default function App() {
  const [n, setN] = useState(0)
  return <div className="p-4">count: {n}</div>
}`

describe('stripGenUIPrefix', () => {
  it('strips the legacy Summary prefix from history-persisted display_html Detail', () => {
    const legacy = `🎨 UI rendered (1234 chars)\n${TSX}`
    expect(stripGenUIPrefix(legacy)).toBe(TSX)
  })

  it('keeps pure TSX unchanged (new backend writes Detail without prefix)', () => {
    expect(stripGenUIPrefix(TSX)).toBe(TSX)
  })

  it('keeps code starting with a code-ish first line unchanged', () => {
    const withImport = `import { useState } from 'react'\n${TSX.split('\n').slice(1).join('\n')}`
    expect(stripGenUIPrefix(withImport)).toBe(withImport)
  })

  it('returns empty for empty input', () => {
    expect(stripGenUIPrefix('')).toBe('')
    expect(stripGenUIPrefix(undefined as unknown as string)).toBe('')
  })
})

describe('genUICode', () => {
  it('returns tool.detail (prefix-stripped) for committed tools without args', () => {
    const tool = { name: 'display_html', label: '', status: 'done' as const, elapsedMs: 10, detail: `🎨 UI rendered (42 chars)\n${TSX}` }
    expect(genUICode(tool)).toBe(TSX)
  })

  it('returns empty for tools without detail', () => {
    expect(genUICode({ name: 'x', label: '', status: 'done' as const, elapsedMs: 0 })).toBe('')
    expect(genUICode(null)).toBe('')
  })
})
