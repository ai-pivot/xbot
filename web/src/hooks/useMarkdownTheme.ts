/**
 * useMarkdownTheme — access the markdown theme from ThemeProvider.
 *
 * The actual state and persistence live in ThemeProvider so the
 * data-md-theme attribute is applied at app root level (before any
 * component mounts). This hook is just a thin accessor.
 */
import { useTheme } from './useTheme'

export function useMarkdownTheme() {
  const { mdTheme, setMdTheme } = useTheme()
  return { mdTheme, setMdTheme }
}
