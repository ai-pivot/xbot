/**
 * Theme system types (Spec 1 设计系统基础).
 */
import type { Theme } from './shared'
import type { MarkdownThemeId } from './markdown-theme'

export type { Theme }
export type { MarkdownThemeId }

export interface ThemeContextValue {
  /** Current color scheme, derived from the selected Markdown theme's mode. */
  theme: Theme
  /** Accent color as a CSS hex string, e.g. '#3388BB'. */
  accentColor: string
  /** Set the accent color; updates --accent* CSS vars and persists. */
  setAccentColor: (color: string) => void
  /** Current markdown theme. */
  mdTheme: MarkdownThemeId
  /** Set the markdown theme; updates data-md-theme and persists. */
  setMdTheme: (id: MarkdownThemeId) => void
}

export const DEFAULT_ACCENT_COLOR = '#3388BB'
export const THEME_STORAGE_KEY = 'xbot-theme'
export const ACCENT_STORAGE_KEY = 'xbot-accent'
