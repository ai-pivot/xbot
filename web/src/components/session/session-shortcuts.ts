import type { SessionInfo } from '@/types/shared'

export const SESSION_SHORTCUTS = {
  newSession: 'mod+alt+n',
  star: 'mod+alt+s',
  rename: 'f2',
  fork: 'mod+alt+f',
  export: 'mod+alt+e',
  delete: 'mod+alt+backspace',
} as const

export type SessionShortcutAction = keyof typeof SESSION_SHORTCUTS
export type SessionShortcutBindings = Record<SessionShortcutAction, string | null>
export const SESSION_SHORTCUT_ACTIONS = Object.keys(SESSION_SHORTCUTS) as SessionShortcutAction[]
export const SESSION_SHORTCUT_TITLES: Record<SessionShortcutAction, string> = {
  newSession: 'settings.shortcuts.newSession', star: 'session.star', rename: 'common.rename',
  fork: 'session.fork', export: 'session.export', delete: 'common.delete',
}
type ShortcutHandlers = Partial<Record<SessionShortcutAction, () => void>>

const MODIFIERS = ['mod', 'ctrl', 'meta', 'alt', 'shift']
const ALIASES: Record<string, string> = {
  control: 'ctrl', command: 'meta', cmd: 'meta', win: 'meta', super: 'meta', option: 'alt', 'ctrl/cmd': 'mod',
  esc: 'escape', bksp: 'backspace', del: 'delete', spacebar: 'space',
  arrowleft: 'left', arrowright: 'right', arrowup: 'up', arrowdown: 'down',
  '-': 'minus', '=': 'equal', '[': 'bracketleft', ']': 'bracketright', '\\': 'backslash',
  ';': 'semicolon', "'": 'quote', '`': 'backquote', ',': 'comma', '.': 'period', '/': 'slash',
}
const NAMED_KEYS = new Set([
  'backspace', 'delete', 'insert', 'home', 'end', 'pageup', 'pagedown',
  'left', 'right', 'up', 'down', 'space', 'enter', 'tab', 'escape',
  'minus', 'equal', 'bracketleft', 'bracketright', 'backslash', 'semicolon',
  'quote', 'backquote', 'comma', 'period', 'slash',
])

/** Canonical bindings keep Control and Command distinct; Mod accepts either. */
export function parseShortcutBinding(input: string): string | null | undefined {
  if (!input.trim()) return null
  const tokens = input.toLowerCase().split('+').map(part => part.trim()).map(part => ALIASES[part] ?? part)
  const key = tokens.pop()!
  const functionKey = /^f([1-9]|1\d|2[0-4])$/.test(key)
  if (!functionKey && !/^[a-z0-9]$/.test(key) && !NAMED_KEYS.has(key)) return
  if (tokens.some(part => !MODIFIERS.includes(part)) || new Set(tokens).size !== tokens.length) return
  if (tokens.includes('mod') && (tokens.includes('ctrl') || tokens.includes('meta'))) return
  if (!functionKey && !tokens.some(part => ['mod', 'ctrl', 'meta', 'alt'].includes(part))) return
  return [...MODIFIERS.filter(part => tokens.includes(part)), key].join('+')
}

export function shortcutBindingFromEvent(event: KeyboardEvent): string | undefined {
  if (event.isComposing || event.keyCode === 229 || event.repeat || event.getModifierState('AltGraph')) return
  // Physical codes survive Option-generated symbols and shifted punctuation.
  const key = /^Key[A-Z]$/.test(event.code) ? event.code.slice(3)
    : /^Digit[0-9]$/.test(event.code) ? event.code.slice(5)
      : NAMED_KEYS.has(event.code.toLowerCase()) ? event.code
        : event.key === ' ' ? 'space' : event.key
  return parseShortcutBinding([
    event.ctrlKey && 'ctrl', event.metaKey && 'meta', event.altKey && 'alt', event.shiftKey && 'shift', key,
  ].filter(Boolean).join('+')) ?? undefined
}

export function shortcutBindingsOverlap(a: string, b: string): boolean {
  const expand = (binding: string) => binding.startsWith('mod+')
    ? [binding.replace(/^mod\+/, 'ctrl+'), binding.replace(/^mod\+/, 'meta+')]
    : [binding]
  return expand(a).some(binding => expand(b).includes(binding))
}

export function sessionShortcutAction(event: KeyboardEvent, bindings: SessionShortcutBindings = SESSION_SHORTCUTS): SessionShortcutAction | undefined {
  if (event.defaultPrevented || event.isComposing || event.keyCode === 229 || event.repeat || event.getModifierState('AltGraph')) return
  if (event.target instanceof Element && event.target.closest(
    'input, textarea, select, [role="textbox"], [contenteditable]:not([contenteditable="false"]), .monaco-editor, .xterm',
  )) return
  const binding = shortcutBindingFromEvent(event)
  return binding ? SESSION_SHORTCUT_ACTIONS.find(action => bindings[action] && shortcutBindingsOverlap(binding, bindings[action]!)) : undefined
}

export function dispatchSessionShortcut(event: KeyboardEvent, handlers: ShortcutHandlers, bindings: SessionShortcutBindings = SESSION_SHORTCUTS): boolean {
  const action = sessionShortcutAction(event, bindings)
  const handler = action && handlers[action]
  if (!handler) return false
  event.preventDefault()
  event.stopPropagation()
  handler()
  return true
}

export function formatShortcutBinding(binding: string | null, platform = navigator.platform, compact = false): string {
  if (!binding) return ''
  const mac = /Mac|iPhone|iPad/.test(platform)
  const names: Record<string, string> = {
    mod: mac ? 'Cmd' : 'Ctrl', ctrl: 'Ctrl', meta: mac ? 'Cmd' : 'Win',
    alt: mac ? 'Option' : 'Alt', shift: 'Shift',
    backspace: compact ? 'Bksp' : 'Backspace', delete: 'Delete', space: 'Space',
  }
  if (compact && mac) Object.assign(names, {
    mod: '\u2318', ctrl: '\u2303', meta: '\u2318', alt: '\u2325', shift: '\u21e7', backspace: '\u232b',
  })
  return binding.split('+').map(part => names[part] ?? (part.length === 1 || /^f\d+$/.test(part) ? part.toUpperCase() : part[0].toUpperCase() + part.slice(1)))
    .join(compact && mac ? '' : '+')
}

export function sessionShortcutLabel(action: SessionShortcutAction, platform = navigator.platform, bindings: SessionShortcutBindings = SESSION_SHORTCUTS): string {
  return formatShortcutBinding(bindings[action], platform, true)
}

export function openSessionInBrowserTab(session: SessionInfo): void {
  const url = new URL('/', window.location.origin)
  url.searchParams.set('session', `${session.channel || 'web'}:${session.chatID}`)
  window.open(url.href, '_blank', 'noopener')
}
