/**
 * Markdown theme system types.
 *
 * Each theme declares a `mode` ('dark' | 'light') so consumers that need
 * a binary light/dark signal (Monaco, xterm, Sonner) can derive it
 * from the selected Markdown theme without a separate toggle.
 */
export type MarkdownThemeId =
  | 'vscode-dark'
  | 'github-dark'
  | 'github-light'
  | 'dracula'
  | 'one-dark'
  | 'monokai'
  | 'tokyo-night'
  | 'nord'
  | 'solarized-dark'
  | 'night-wolf-gray'
  | 'night-wolf-blue'
  | 'tui-midnight'
  | 'tui-ocean'
  | 'tui-forest'
  | 'tui-sunset'
  | 'tui-rose'
  | 'tui-mono'
  | 'tui-catppuccin'

export interface MarkdownTheme {
  id: MarkdownThemeId
  /** Display name key for i18n lookup: `settings.mdTheme.<id>`. */
  labelKey: string
  /** Whether this theme is dark or light — drives Monaco/xterm/Sonner. */
  mode: 'dark' | 'light'
}

/** All available markdown themes, in display order. */
export const MARKDOWN_THEMES: MarkdownTheme[] = [
  { id: 'vscode-dark', labelKey: 'settings.mdThemeVscodeDark', mode: 'dark' },
  { id: 'github-dark', labelKey: 'settings.mdThemeGithubDark', mode: 'dark' },
  { id: 'github-light', labelKey: 'settings.mdThemeGithubLight', mode: 'light' },
  { id: 'dracula', labelKey: 'settings.mdThemeDracula', mode: 'dark' },
  { id: 'one-dark', labelKey: 'settings.mdThemeOneDark', mode: 'dark' },
  { id: 'monokai', labelKey: 'settings.mdThemeMonokai', mode: 'dark' },
  { id: 'tokyo-night', labelKey: 'settings.mdThemeTokyoNight', mode: 'dark' },
  { id: 'nord', labelKey: 'settings.mdThemeNord', mode: 'dark' },
  { id: 'solarized-dark', labelKey: 'settings.mdThemeSolarizedDark', mode: 'dark' },
  { id: 'night-wolf-gray', labelKey: 'settings.mdThemeNightWolfGray', mode: 'dark' },
  { id: 'night-wolf-blue', labelKey: 'settings.mdThemeNightWolfBlue', mode: 'dark' },
  { id: 'tui-midnight', labelKey: 'settings.mdThemeTuiMidnight', mode: 'dark' },
  { id: 'tui-ocean', labelKey: 'settings.mdThemeTuiOcean', mode: 'dark' },
  { id: 'tui-forest', labelKey: 'settings.mdThemeTuiForest', mode: 'dark' },
  { id: 'tui-sunset', labelKey: 'settings.mdThemeTuiSunset', mode: 'dark' },
  { id: 'tui-rose', labelKey: 'settings.mdThemeTuiRose', mode: 'dark' },
  { id: 'tui-mono', labelKey: 'settings.mdThemeTuiMono', mode: 'dark' },
  { id: 'tui-catppuccin', labelKey: 'settings.mdThemeTuiCatppuccin', mode: 'dark' },
]

export const DEFAULT_MARKDOWN_THEME: MarkdownThemeId = 'vscode-dark'
export const MARKDOWN_THEME_STORAGE_KEY = 'xbot-md-theme'

/** Look up the mode for a given theme id; falls back to 'dark'. */
export function modeForTheme(id: MarkdownThemeId): 'dark' | 'light' {
  return MARKDOWN_THEMES.find((t) => t.id === id)?.mode ?? 'dark'
}
