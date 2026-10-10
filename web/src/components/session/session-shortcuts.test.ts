import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { SESSION_SHORTCUTS, parseShortcutBinding, shortcutBindingFromEvent, shortcutBindingsOverlap, formatShortcutBinding, sessionShortcutAction, sessionShortcutLabel } from './session-shortcuts'

describe('session shortcut bindings', () => {
  beforeEach(() => vi.spyOn(navigator, 'platform', 'get').mockReturnValue('MacIntel'))
  afterEach(() => vi.restoreAllMocks())

  it('opens an existing session with Control+N, not Command+N or the creation binding', () => {
    expect(sessionShortcutAction(new KeyboardEvent('keydown', { key: 'n', ctrlKey: true }))).toBe('openInBrowserTab')
    expect(sessionShortcutAction(new KeyboardEvent('keydown', { key: 'n', metaKey: true }))).toBeUndefined()
    expect(sessionShortcutAction(new KeyboardEvent('keydown', { key: 'n', ctrlKey: true, altKey: true }))).toBe('newSession')
    expect(sessionShortcutLabel('openInBrowserTab', 'MacIntel')).toBe('\u2303N')
    expect(sessionShortcutLabel('openInBrowserTab', 'Win32')).toBe('Ctrl+Shift+2')
  })

  it.each([
    ['Control+Shift+R', 'ctrl+shift+r'], ['Cmd+Option+O', 'meta+alt+o'],
    ['Shift+Control+9', 'ctrl+shift+9'], ['Mod+Alt+E', 'mod+alt+e'], ['F8', 'f8'], ['', null],
    ['s', undefined], ['Shift+S', undefined], ['Control', undefined], ['Ctrl+Ctrl+S', undefined],
    ['Mod+Cmd+S', undefined], ['Cmd+Nonsense', undefined], ['F25', undefined],
  ])('parses %s as %s', (input, expected) => {
    expect(parseShortcutBinding(input as string)).toBe(expected)
  })

  it('keeps Control and Command separate for custom bindings', () => {
    const bindings = { ...SESSION_SHORTCUTS, star: 'ctrl+shift+s' }
    expect(sessionShortcutAction(new KeyboardEvent('keydown', { key: 's', ctrlKey: true, shiftKey: true }), bindings)).toBe('star')
    expect(sessionShortcutAction(new KeyboardEvent('keydown', { key: 's', metaKey: true, shiftKey: true }), bindings)).toBeUndefined()
    expect(shortcutBindingsOverlap('ctrl+shift+s', 'meta+shift+s')).toBe(false)
    expect(shortcutBindingsOverlap('mod+shift+s', 'meta+shift+s')).toBe(true)
  })

  it('records physical digit and punctuation keys and displays custom modifiers', () => {
    expect(shortcutBindingFromEvent(new KeyboardEvent('keydown', { key: '&', code: 'Digit7', metaKey: true, shiftKey: true }))).toBe('meta+shift+7')
    expect(shortcutBindingFromEvent(new KeyboardEvent('keydown', { key: '+', code: 'Equal', ctrlKey: true, shiftKey: true }))).toBe('ctrl+shift+equal')
    expect(formatShortcutBinding('ctrl+shift+7', 'MacIntel', true)).toBe('\u2303\u21e77')
    expect(formatShortcutBinding(null, 'MacIntel')).toBe('')
  })

  it.each([
    { key: '\u02dc', code: 'KeyN', action: 'newSession' },
    { key: '\u00df', code: 'KeyS', action: 'star' },
    { key: '\u0192', code: 'KeyF', action: 'fork' },
    { key: 'Dead', code: 'KeyE', action: 'export' },
  ])('matches physical keys when Option changes key to $key', ({ key, code, action }) => {
    expect(sessionShortcutAction(new KeyboardEvent('keydown', { key, code, metaKey: true, altKey: true }))).toBe(action)
  })

  it.each([
    { key: 's', ctrlKey: true },
    { key: 's', altKey: true },
    { key: 's', ctrlKey: true, altKey: true, shiftKey: true },
    { key: 'Backspace' },
    { key: 'Delete' },
  ])('leaves unrelated bindings alone (%o)', (options) => {
    expect(sessionShortcutAction(new KeyboardEvent('keydown', options))).toBeUndefined()
  })

  it('renders platform-specific compact labels', () => {
    expect(sessionShortcutLabel('star', 'MacIntel')).toBe('\u2318\u2325S')
    expect(sessionShortcutLabel('delete', 'MacIntel')).toBe('\u2318\u2325\u232b')
    expect(sessionShortcutLabel('star', 'Win32')).toBe('Ctrl+Alt+S')
    expect(sessionShortcutLabel('delete', 'Linux')).toBe('Ctrl+Alt+Bksp')
    expect(sessionShortcutLabel('rename', 'MacIntel')).toBe('F2')
  })
})

describe('Windows session shortcuts', () => {
  afterEach(() => vi.restoreAllMocks())

  it.each([
    { key: 'n', code: 'KeyN', action: 'newSession', altKey: true },
    { key: '@', code: 'Digit2', action: 'openInBrowserTab', shiftKey: true },
    { key: 's', code: 'KeyS', action: 'star', altKey: true },
    { key: 'f', code: 'KeyF', action: 'fork', altKey: true },
    { key: 'e', code: 'KeyE', action: 'export', altKey: true },
    { key: 'Backspace', code: 'Backspace', action: 'delete', altKey: true },
  ])('matches the mapped Windows binding for $action', ({ key, code, action, ...modifiers }) => {
    vi.spyOn(navigator, 'platform', 'get').mockReturnValue('Win32')
    expect(sessionShortcutAction(new KeyboardEvent('keydown', { key, code, ctrlKey: true, ...modifiers }))).toBe(action)
    expect(sessionShortcutAction(new KeyboardEvent('keydown', { key, code, metaKey: true, ...modifiers }))).toBeUndefined()
  })

  it('keeps F2 and leaves Control+N to the browser on Windows', () => {
    vi.spyOn(navigator, 'platform', 'get').mockReturnValue('Win32')
    expect(sessionShortcutAction(new KeyboardEvent('keydown', { key: 'F2' }))).toBe('rename')
    expect(sessionShortcutAction(new KeyboardEvent('keydown', { key: 'n', ctrlKey: true }))).toBeUndefined()
  })

  it('resolves Mod to Control only on Windows, without changing explicit Meta', () => {
    vi.spyOn(navigator, 'platform', 'get').mockReturnValue('Win32')
    const bindings = { ...SESSION_SHORTCUTS, star: 'mod+shift+s' }
    expect(shortcutBindingsOverlap('mod+shift+s', 'ctrl+shift+s')).toBe(true)
    expect(shortcutBindingsOverlap('mod+shift+s', 'meta+shift+s')).toBe(false)
    expect(sessionShortcutAction(new KeyboardEvent('keydown', { key: 's', metaKey: true, shiftKey: true }), bindings)).toBeUndefined()
    expect(sessionShortcutAction(new KeyboardEvent('keydown', { key: 's', metaKey: true, shiftKey: true }), { ...bindings, star: 'meta+shift+s' })).toBe('star')
  })

  it('keeps Shift combinations matched by physical top-row digits', () => {
    vi.spyOn(navigator, 'platform', 'get').mockReturnValue('Win32')
    expect(sessionShortcutAction(new KeyboardEvent('keydown', { key: '@', code: 'Digit2', ctrlKey: true, shiftKey: true }))).toBe('openInBrowserTab')
  })

  it('distinguishes a mapped Ctrl+Alt binding from AltGraph input', () => {
    vi.spyOn(navigator, 'platform', 'get').mockReturnValue('Win32')
    const event = new KeyboardEvent('keydown', { key: 'Dead', code: 'KeyE', ctrlKey: true, altKey: true })
    expect(sessionShortcutAction(event)).toBe('export')
    vi.spyOn(event, 'getModifierState').mockImplementation(key => key === 'AltGraph')
    expect(shortcutBindingFromEvent(event)).toBeUndefined()
    expect(sessionShortcutAction(event)).toBeUndefined()
  })

  it.each([
    { key: 'n', ctrlKey: true }, { key: 'n', ctrlKey: true, shiftKey: true },
    { key: 's', ctrlKey: true }, { key: 'e', ctrlKey: true }, { key: 'Delete', ctrlKey: true, altKey: true },
  ])('does not collapse modifiers or consume reserved/default-unassigned keys (%o)', options => {
    vi.spyOn(navigator, 'platform', 'get').mockReturnValue('Win32')
    expect(sessionShortcutAction(new KeyboardEvent('keydown', options))).toBeUndefined()
  })

  it.each(['MacIntel', 'Linux x86_64'])('preserves all legacy defaults and matching on %s', platform => {
    vi.spyOn(navigator, 'platform', 'get').mockReturnValue(platform)
    expect(SESSION_SHORTCUTS).toEqual({
      newSession: 'mod+alt+n', openInBrowserTab: 'ctrl+n', star: 'mod+alt+s',
      rename: 'f2', fork: 'mod+alt+f', export: 'mod+alt+e', delete: 'mod+alt+backspace',
    })
    expect(sessionShortcutAction(new KeyboardEvent('keydown', { key: 'n', ctrlKey: true }))).toBe('openInBrowserTab')
    for (const modifier of [{ ctrlKey: true }, { metaKey: true }]) {
      expect(sessionShortcutAction(new KeyboardEvent('keydown', { key: 's', altKey: true, ...modifier }))).toBe('star')
    }
    expect(sessionShortcutAction(new KeyboardEvent('keydown', { key: '3', code: 'Digit3', ctrlKey: true, shiftKey: true }))).toBeUndefined()
  })
})
