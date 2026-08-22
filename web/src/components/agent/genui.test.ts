import { describe, expect, it } from 'vitest'
import { stripGenUIPrefix, genUICode, isGenUITool } from './genui'

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
    const tool = { name: 'display_html', label: '', status: 'done' as const, elapsedMs: 10, summary: '', args: '', toolHints: '', detail: `🎨 UI rendered (42 chars)\n${TSX}` }
    expect(genUICode(tool)).toBe(TSX)
  })

  it('returns empty for tools without detail', () => {
    expect(genUICode({ name: 'x', label: '', status: 'done' as const, elapsedMs: 0, summary: '', args: '', toolHints: '' })).toBe('')
    expect(genUICode(null)).toBe('')
  })
})

describe('isGenUITool', () => {
  it('is metadata-driven: true only when ui.mode === genui', () => {
    expect(isGenUITool({ name: 'display_html', uiMode: 'genui' })).toBe(true)
    expect(isGenUITool({ name: 'custom-genui-tool', uiMode: 'genui' })).toBe(true)
    expect(isGenUITool({ name: 'shell', uiMode: undefined })).toBe(false)
    expect(isGenUITool({ name: 'read', uiMode: 'shell' })).toBe(false)
  })

  it('no longer falls back to the display_html tool name (hardcoding removed)', () => {
    // The legacy name fallback was removed — a tool WITHOUT ui.mode is no longer
    // treated as GenUI even if named display_html (pre-metadata history rows are
    // handled by builtinLegacyDisplayHtmlRenderer messageRenderer instead).
    expect(isGenUITool({ name: 'display_html' })).toBe(false)
  })

  it('handles null/undefined gracefully', () => {
    expect(isGenUITool(null)).toBe(false)
    expect(isGenUITool(undefined)).toBe(false)
  })
})
