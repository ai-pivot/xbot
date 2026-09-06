import { describe, expect, it } from 'vitest'
import { normalizeHref } from './SelectionToolbar'

describe('normalizeHref — scheme whitelist (xbotgh CR: javascript:/data: must not pass through)', () => {
  it('allows http/https/mailto/tel schemes verbatim', () => {
    expect(normalizeHref('https://example.com/x')).toBe('https://example.com/x')
    expect(normalizeHref('http://example.com')).toBe('http://example.com')
    expect(normalizeHref('HTTPS://EXAMPLE.COM')).toBe('HTTPS://EXAMPLE.COM')
    expect(normalizeHref('mailto:a@b.com')).toBe('mailto:a@b.com')
    expect(normalizeHref('tel:+1234567890')).toBe('tel:+1234567890')
  })

  it('allows anchors and relative paths (incl. protocol-relative)', () => {
    expect(normalizeHref('#section-id')).toBe('#section-id')
    expect(normalizeHref('/abs/path')).toBe('/abs/path')
    expect(normalizeHref('//cdn.example.com/lib.js')).toBe('//cdn.example.com/lib.js')
  })

  it('prefixes bare domains with https:// (unchanged behavior)', () => {
    expect(normalizeHref('example.com')).toBe('https://example.com')
    expect(normalizeHref('www.example.com')).toBe('https://www.example.com')
    expect(normalizeHref('  example.com/path  ')).toBe('https://example.com/path')
  })

  it('neutralizes dangerous schemes — javascript:/data:/vbscript: get https:// prefixed (inert URLs, no scheme execution)', () => {
    // The old generic ^[a-z][a-z0-9+.-]*: passthrough let Ctrl+K set
    // javascript: links; the markdown [text](javascript:...) would persist to DB and
    // other render surfaces may not sanitize it like react-markdown does.
    expect(normalizeHref('javascript:alert(1)')).toBe('https://javascript:alert(1)')
    expect(normalizeHref('data:text/html,<script>alert(1)</script>')).toBe('https://data:text/html,<script>alert(1)</script>')
    expect(normalizeHref('vbscript:msgbox')).toBe('https://vbscript:msgbox')
    expect(normalizeHref('ftp://files.example.com')).toBe('https://ftp://files.example.com')
  })

  it('empty input returns empty string (apply treats empty as unlink)', () => {
    expect(normalizeHref('')).toBe('')
    expect(normalizeHref('   ')).toBe('')
  })
})
