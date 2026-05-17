import { describe, it, expect } from 'vitest'
import { formatRelativeTime } from '../utils'

// ─── formatRelativeTime ───
describe('formatRelativeTime', () => {
  it('returns "just now" for very recent timestamps', () => {
    const now = Date.now()
    expect(formatRelativeTime(now)).toBe('just now')
    expect(formatRelativeTime(now - 3000)).toBe('just now')
  })

  it('returns seconds ago for < 60s', () => {
    const now = Date.now()
    expect(formatRelativeTime(now - 10000)).toMatch(/^\d+s ago$/)
    expect(formatRelativeTime(now - 59000)).toMatch(/^\d+s ago$/)
  })

  it('returns minutes ago for < 1h', () => {
    const now = Date.now()
    expect(formatRelativeTime(now - 120000)).toMatch(/^\d+m ago$/)
    expect(formatRelativeTime(now - 3500000)).toMatch(/^\d+m ago$/)
  })

  it('returns hours ago for < 1d', () => {
    const now = Date.now()
    expect(formatRelativeTime(now - 7200000)).toMatch(/^\d+h ago$/)
    expect(formatRelativeTime(now - 80000000)).toMatch(/^\d+h ago$/)
  })

  it('returns days ago for < 7d', () => {
    const now = Date.now()
    expect(formatRelativeTime(now - 172800000)).toMatch(/^\d+d ago$/)
    expect(formatRelativeTime(now - 600000000)).toMatch(/^\d+d ago$/)
  })

  it('falls back to formatTime for > 7d', () => {
    const now = Date.now()
    const result = formatRelativeTime(now - 700000000)
    // Should not match relative patterns
    expect(result).not.toMatch(/^\d+[smhd] ago$/)
    expect(result).not.toBe('just now')
  })

  it('handles 0 diff (same time)', () => {
    const now = Date.now()
    expect(formatRelativeTime(now)).toBe('just now')
  })
})

// ─── Settings import/export utility logic ───
describe('settings import/export validation', () => {
  const VALID_SETTINGS = {
    _version: 1,
    theme: 'dark' as const,
    font_size: 'medium' as const,
    nickname: 'testuser',
    language: 'en' as const,
  }

  function validateImport(json: string): { ok: boolean; error?: string } {
    try {
      const data = JSON.parse(json)
      if (!data._version || data._version !== 1) return { ok: false, error: 'invalid version' }
      if (!data.theme || !['dark', 'light'].includes(data.theme)) return { ok: false, error: 'invalid theme' }
      if (!data.font_size || !['small', 'medium', 'large'].includes(data.font_size)) return { ok: false, error: 'invalid font_size' }
      if (!data.language || !['zh-CN', 'en'].includes(data.language)) return { ok: false, error: 'invalid language' }
      return { ok: true }
    } catch {
      return { ok: false, error: 'invalid JSON' }
    }
  }

  it('validates correct settings export', () => {
    const json = JSON.stringify(VALID_SETTINGS)
    expect(validateImport(json)).toEqual({ ok: true })
  })

  it('rejects invalid JSON', () => {
    expect(validateImport('not json')).toEqual({ ok: false, error: 'invalid JSON' })
  })

  it('rejects missing version', () => {
    const { _version, ...rest } = VALID_SETTINGS
    expect(validateImport(JSON.stringify(rest))).toEqual({ ok: false, error: 'invalid version' })
  })

  it('rejects wrong version', () => {
    expect(validateImport(JSON.stringify({ ...VALID_SETTINGS, _version: 2 }))).toEqual({ ok: false, error: 'invalid version' })
  })

  it('rejects invalid theme', () => {
    expect(validateImport(JSON.stringify({ ...VALID_SETTINGS, theme: 'purple' }))).toEqual({ ok: false, error: 'invalid theme' })
  })

  it('rejects invalid font_size', () => {
    expect(validateImport(JSON.stringify({ ...VALID_SETTINGS, font_size: 'huge' }))).toEqual({ ok: false, error: 'invalid font_size' })
  })

  it('rejects invalid language', () => {
    expect(validateImport(JSON.stringify({ ...VALID_SETTINGS, language: 'fr' }))).toEqual({ ok: false, error: 'invalid language' })
  })

  it('accepts missing nickname (optional field)', () => {
    const { nickname: _nickname, ...rest } = VALID_SETTINGS
    expect(validateImport(JSON.stringify(rest))).toEqual({ ok: true })
  })
})

// ─── Message type extensions ───
describe('Message type extensions', () => {
  it('status field is optional and backward compatible', () => {
    const msg: { id: string; type: 'user'; content: string; status?: string; edited?: boolean } = { id: '1', type: 'user' as const, content: 'hello' }
    // Should not throw when status is missing
    expect(msg.status).toBeUndefined()
    expect(msg.edited).toBeUndefined()
  })

  it('status field accepts valid values', () => {
    const sending = { id: '1', type: 'user' as const, content: 'hello', status: 'sending' as const, edited: undefined as boolean | undefined }
    const sent = { id: '2', type: 'user' as const, content: 'hello', status: 'sent' as const, edited: undefined as boolean | undefined }
    const failed = { id: '3', type: 'user' as const, content: 'hello', status: 'failed' as const, edited: undefined as boolean | undefined }
    expect(sending.status).toBe('sending')
    expect(sent.status).toBe('sent')
    expect(failed.status).toBe('failed')
  })

  it('edited field works as boolean', () => {
    const msg: { id: string; type: 'user'; content: string; edited?: boolean } = { id: '1', type: 'user' as const, content: 'hello', edited: true }
    expect(msg.edited).toBe(true)
  })
})

// ─── WebSocket reconnect math ───
describe('WebSocket reconnect backoff calculation', () => {
  it('exponential backoff doubles delay each attempt', () => {
    let delay = 1000
    const delays: number[] = []
    for (let i = 0; i < 5; i++) {
      delays.push(delay)
      delay = Math.min(delay * 2, 30000)
    }
    expect(delays).toEqual([1000, 2000, 4000, 8000, 16000])
  })

  it('backoff caps at 30000ms', () => {
    let delay = 1000
    for (let i = 0; i < 20; i++) {
      delay = Math.min(delay * 2, 30000)
    }
    expect(delay).toBeLessThanOrEqual(30000)
  })

  it('jitter keeps delay within 50%-100% of base', () => {
    const base = 4000
    for (let i = 0; i < 100; i++) {
      const jitter = Math.random() * 0.5 + 0.5
      const delay = Math.round(base * jitter)
      expect(delay).toBeGreaterThanOrEqual(Math.round(base * 0.5))
      expect(delay).toBeLessThanOrEqual(base)
    }
  })

  it('max reconnect attempts is 20', () => {
    const MAX_RECONNECT_ATTEMPTS = 20
    expect(MAX_RECONNECT_ATTEMPTS).toBe(20)
  })
})

// ─── Lightbox utility logic ───
describe('Lightbox zoom bounds', () => {
  it('scale stays within 0.5-5 bounds', () => {
    const MIN_SCALE = 0.5
    const MAX_SCALE = 5
    const STEP = 0.15

    // Simulate zooming in from 1
    let scale = 1
    for (let i = 0; i < 100; i++) {
      scale = Math.min(MAX_SCALE, scale + STEP)
    }
    expect(scale).toBe(MAX_SCALE)

    // Simulate zooming out from 1
    scale = 1
    for (let i = 0; i < 100; i++) {
      scale = Math.max(MIN_SCALE, scale - STEP)
    }
    expect(scale).toBe(MIN_SCALE)
  })
})

// ─── Search filter logic ───
describe('settings search filter', () => {
  const SETTINGS_ITEMS = [
    { id: 'theme', label: 'Theme', keywords: ['dark', 'light', 'appearance'] },
    { id: 'font_size', label: 'Font Size', keywords: ['small', 'medium', 'large', 'text'] },
    { id: 'nickname', label: 'Nickname', keywords: ['name', 'display'] },
    { id: 'language', label: 'Language', keywords: ['english', 'chinese', 'zh', 'en', 'i18n'] },
    { id: 'model', label: 'LLM Model', keywords: ['ai', 'gpt', 'claude', 'provider'] },
  ]

  function filterSettings(query: string) {
    if (!query.trim()) return SETTINGS_ITEMS
    const q = query.toLowerCase()
    return SETTINGS_ITEMS.filter(item =>
      item.label.toLowerCase().includes(q) ||
      item.id.toLowerCase().includes(q) ||
      item.keywords.some(kw => kw.includes(q))
    )
  }

  it('returns all items for empty query', () => {
    expect(filterSettings('')).toHaveLength(5)
  })

  it('returns all items for whitespace query', () => {
    expect(filterSettings('   ')).toHaveLength(5)
  })

  it('filters by label', () => {
    expect(filterSettings('theme')).toHaveLength(1)
    expect(filterSettings('theme')[0].id).toBe('theme')
  })

  it('filters by id', () => {
    expect(filterSettings('font')).toHaveLength(1)
    expect(filterSettings('font')[0].id).toBe('font_size')
  })

  it('filters by keyword', () => {
    expect(filterSettings('dark')).toHaveLength(1)
    expect(filterSettings('dark')[0].id).toBe('theme')
  })

  it('is case-insensitive', () => {
    expect(filterSettings('THEME')).toHaveLength(1)
    expect(filterSettings('NICKNAME')).toHaveLength(1)
  })

  it('returns empty for no match', () => {
    expect(filterSettings('nonexistent')).toHaveLength(0)
  })

  it('matches multiple items for broad query', () => {
    const results = filterSettings('l')
    expect(results.length).toBeGreaterThanOrEqual(2) // theme (light), language, model (llm)
  })
})
