/**
 * useTheme — access the active ThemeProvider context.
 * Throws if used outside <ThemeProvider>.
 */
import { useContext } from 'react'
import { ThemeContext } from '@/providers/theme'
import type { ThemeContextValue } from '@/types/theme'

export function useTheme(): ThemeContextValue {
  const ctx = useContext(ThemeContext)
  if (!ctx) {
    throw new Error('useTheme must be used within a <ThemeProvider>')
  }
  return ctx
}

export { type ThemeContextValue }
