/**
 * ThemeProvider — dark/light + brand accent color, CSS-variable driven.
 *
 * Spec 1 设计系统基础:
 *   - theme 'dark' | 'light' toggles <html class="dark">
 *   - accentColor (default '#3388BB') drives --accent / --accent-hover /
 *     --accent-foreground, so every accent element updates live
 *   - both persist to localStorage
 */
import { createContext, useCallback, useEffect, useMemo, useState, type ReactNode } from 'react'
import { type Theme } from '@/types/shared'
import {
  DEFAULT_ACCENT_COLOR,
  THEME_STORAGE_KEY,
  ACCENT_STORAGE_KEY,
  type ThemeContextValue,
} from '@/types/theme'

export { type ThemeContextValue }

const ThemeContext = createContext<ThemeContextValue | undefined>(undefined)

/** Read the persisted or system-preferred theme on first paint. */
function getInitialTheme(): Theme {
  try {
    const saved = localStorage.getItem(THEME_STORAGE_KEY)
    if (saved === 'dark' || saved === 'light') return saved
  } catch { /* ignore */ }
  if (typeof window !== 'undefined' && window.matchMedia) {
    return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
  }
  return 'dark'
}

function getInitialAccent(): string {
  try {
    const saved = localStorage.getItem(ACCENT_STORAGE_KEY)
    if (saved) return saved
  } catch { /* ignore */ }
  return DEFAULT_ACCENT_COLOR
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
  defaultTheme?: Theme
  defaultAccentColor?: string
  storageKey?: string
  accentStorageKey?: string
}

export function ThemeProvider({
  children,
  defaultTheme,
  defaultAccentColor,
  storageKey = THEME_STORAGE_KEY,
  accentStorageKey = ACCENT_STORAGE_KEY,
}: ThemeProviderProps) {
  const [theme, setThemeState] = useState<Theme>(() => {
    if (defaultTheme) return defaultTheme
    try {
      const saved = localStorage.getItem(storageKey)
      if (saved === 'dark' || saved === 'light') return saved
    } catch { /* ignore */ }
    return getInitialTheme()
  })

  const [accentColor, setAccentColorState] = useState<string>(() => {
    if (defaultAccentColor) return defaultAccentColor
    return getInitialAccent()
  })

  // Apply theme class to <html>.
  useEffect(() => {
    const root = document.documentElement
    root.classList.toggle('dark', theme === 'dark')
    try {
      localStorage.setItem(storageKey, theme)
    } catch { /* ignore */ }
  }, [theme, storageKey])

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
    } catch { /* ignore */ }
  }, [accentColor, accentStorageKey])

  const setTheme = useCallback((t: Theme) => setThemeState(t), [])
  const setAccentColor = useCallback((c: string) => setAccentColorState(c), [])

  const value = useMemo<ThemeContextValue>(
    () => ({ theme, setTheme, accentColor, setAccentColor }),
    [theme, setTheme, accentColor, setAccentColor],
  )

  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>
}

export { ThemeContext }
