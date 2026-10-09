import { describe, expect, it } from 'vitest'
import { SESSION_SHORTCUTS, parseShortcutBinding, shortcutBindingFromEvent, shortcutBindingsOverlap, formatShortcutBinding, sessionShortcutAction, sessionShortcutLabel } from './session-shortcuts'

describe('session shortcut bindings', () => {
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
