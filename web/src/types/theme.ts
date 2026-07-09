/**
 * Theme system types (Spec 1 设计系统基础).
 */
import type { Theme } from './shared'

export type { Theme }

export interface ThemeContextValue {
  /** Current color scheme. */
  theme: Theme
  /** Switch the color scheme; persists to localStorage. */
  setTheme: (theme: Theme) => void
  /** Accent color as a CSS hex string, e.g. '#3388BB'. */
  accentColor: string
  /** Set the accent color; updates --accent* CSS vars and persists. */
  setAccentColor: (color: string) => void
}

export const DEFAULT_ACCENT_COLOR = '#3388BB'
export const THEME_STORAGE_KEY = 'xbot-theme'
export const ACCENT_STORAGE_KEY = 'xbot-accent'
