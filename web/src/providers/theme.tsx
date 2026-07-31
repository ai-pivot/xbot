/**
 * ThemeProvider — unified theme system driven by Markdown theme selection.
 *
 * The Markdown theme is the single source of truth. Each theme declares
 * `mode: 'dark' | 'light'`, from which the legacy `.dark` class on <html>
 * is derived for consumers that need a binary signal (Monaco, xterm, Sonner).
 *
 *   - mdTheme (e.g. 'dracula') → sets data-md-theme attribute on <html>
 *   - theme ('dark' | 'light') → derived from mdTheme's mode, toggles .dark class
 *   - accentColor → drives --accent / --accent-hover / --accent-foreground
 *   - all three persist to localStorage
 */
import { createContext, useCallback, useEffect, useMemo, useState, type ReactNode } from 'react'
import { type Theme } from '@/types/shared'
import {
  DEFAULT_ACCENT_COLOR,
  ACCENT_STORAGE_KEY,
  type ThemeContextValue,
} from '@/types/theme'
import {
  DEFAULT_MARKDOWN_THEME,
  MARKDOWN_THEME_STORAGE_KEY,
  modeForTheme,
  type MarkdownThemeId,
} from '@/types/markdown-theme'
import { syncSettingToServer, SETTINGS_SYNCED_EVENT } from '@/lib/userSettings'

export { type ThemeContextValue }

const ThemeContext = createContext<ThemeContextValue | undefined>(undefined)

function getInitialAccent(): string {
  try {
    const saved = localStorage.getItem(ACCENT_STORAGE_KEY)
    if (saved) return saved
  } catch { /* ignore */ }
  return DEFAULT_ACCENT_COLOR
}

function getInitialMdTheme(): MarkdownThemeId {
  try {
    const saved = localStorage.getItem(MARKDOWN_THEME_STORAGE_KEY)
    if (saved) return saved as MarkdownThemeId
  } catch { /* ignore */ }
  return DEFAULT_MARKDOWN_THEME
}

/** Darken a #RRGGBB hex by `amount` (0..1). Returns the same hex on parse error. */
function darken(hex: string, amount: number): string {
  const { r, g, b, ok } = parseHex(hex)
  if (!ok) return hex
  const d = (v: number) => Math.max(0, Math.round(v * (1 - amount)))
  return toHex(d(r), d(g), d(b))
}

/** Lighten a #RRGGBB hex by `amount` (0..1). */
function lighten(hex: string, amount: number): string {
  const { r, g, b, ok } = parseHex(hex)
  if (!ok) return hex
  const l = (v: number) => Math.min(255, Math.round(v + (255 - v) * amount))
  return toHex(l(r), l(g), l(b))
}

/** Relative luminance; pick black or white text for contrast. */
function contrastForeground(hex: string): string {
  const { r, g, b, ok } = parseHex(hex)
  if (!ok) return '#ffffff'
  const linear = (v: number) => {
    const c = v / 255
    return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4)
  }
  const lum = 0.2126 * linear(r) + 0.7152 * linear(g) + 0.0722 * linear(b)
  return lum > 0.45 ? '#1e1e1e' : '#ffffff'
}

function parseHex(hex: string): { r: number; g: number; b: number; ok: boolean } {
  const m = /^#?([0-9a-fA-F]{6})$/.exec(hex.trim())
  if (!m) return { r: 0, g: 0, b: 0, ok: false }
  const n = parseInt(m[1], 16)
  return { r: (n >> 16) & 255, g: (n >> 8) & 255, b: n & 255, ok: true }
}

function toHex(r: number, g: number, b: number): string {
  const h = (v: number) => v.toString(16).padStart(2, '0')
  return `#${h(r)}${h(g)}${h(b)}`
}

interface ThemeProviderProps {
  children: ReactNode
  defaultAccentColor?: string
  accentStorageKey?: string
}

export function ThemeProvider({
  children,
  defaultAccentColor,
  accentStorageKey = ACCENT_STORAGE_KEY,
}: ThemeProviderProps) {
  const [mdTheme, setMdThemeState] = useState<MarkdownThemeId>(getInitialMdTheme)

  // Derive theme ('dark' | 'light') from the selected Markdown theme's mode.
  const theme: Theme = modeForTheme(mdTheme)

  // Apply data-md-theme attribute + .dark class to <html>.
  useEffect(() => {
    const root = document.documentElement
    root.setAttribute('data-md-theme', mdTheme)
    root.classList.toggle('dark', theme === 'dark')
    try {
      localStorage.setItem(MARKDOWN_THEME_STORAGE_KEY, mdTheme)
      syncSettingToServer(MARKDOWN_THEME_STORAGE_KEY, mdTheme)
    } catch { /* ignore */ }
  }, [mdTheme, theme])

  const [accentColor, setAccentColorState] = useState<string>(() => {
    if (defaultAccentColor) return defaultAccentColor
    return getInitialAccent()
  })

  // Apply accent CSS variables to <html>.
  useEffect(() => {
    const root = document.documentElement
    const isDark = root.classList.contains('dark')
    const hover = isDark ? lighten(accentColor, 0.12) : darken(accentColor, 0.1)
    root.style.setProperty('--accent', accentColor)
    root.style.setProperty('--accent-hover', hover)
    root.style.setProperty('--accent-foreground', contrastForeground(accentColor))
    try {
      localStorage.setItem(accentStorageKey, accentColor)
      syncSettingToServer(accentStorageKey, accentColor)
    } catch { /* ignore */ }
  }, [accentColor, accentStorageKey])

  const setMdTheme = useCallback((id: MarkdownThemeId) => setMdThemeState(id), [])
  const setAccentColor = useCallback((c: string) => setAccentColorState(c), [])

  // Re-read from localStorage when server sync updates values.
  useEffect(() => {
    const handler = () => {
      setMdThemeState(getInitialMdTheme())
      setAccentColorState(getInitialAccent())
    }
    window.addEventListener(SETTINGS_SYNCED_EVENT, handler)
    return () => window.removeEventListener(SETTINGS_SYNCED_EVENT, handler)
  }, [])

  const value = useMemo<ThemeContextValue>(
    () => ({ theme, accentColor, setAccentColor, mdTheme, setMdTheme }),
    [theme, accentColor, setAccentColor, mdTheme, setMdTheme],
  )

  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>
}

export { ThemeContext }
