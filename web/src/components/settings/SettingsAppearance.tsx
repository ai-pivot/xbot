/**
 * SettingsAppearance — theme + accent color (Spec 7 §3.3).
 *
 *   - Theme: dark / light radio-style toggle wired to useTheme.setTheme.
 *   - Accent color: preset swatches + a custom hex input wired to
 *     useTheme.setAccentColor; the live preview chip reflects --accent.
 *
 * Both write through the ThemeProvider, which persists and updates the CSS
 * variables, so the rest of the UI updates live (no local state needed).
 */
import { Check } from 'lucide-react'
import { useState } from 'react'

import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useTheme } from '@/hooks/useTheme'
import { useI18n } from '@/providers/i18n'
import { DEFAULT_ACCENT_COLOR } from '@/types/theme'
import type { Theme } from '@/types/shared'
import { cn } from '@/lib/utils'

import { SettingsSection } from './SettingsSection'

/** Spec 7 §3.3 preset palette. */
const ACCENT_PRESETS = [
  '#3388BB',
  '#2563EB',
  '#7C3AED',
  '#DC2626',
  '#059669',
  '#EA580C',
]

const THEMES: Theme[] = ['light', 'dark']

/** Normalize a user-typed hex (#rgb / #rrggbb / no-hash) into '#RRGGBB' or null. */
function normalizeHex(input: string): string | null {
  let h = input.trim()
  if (!h) return null
  if (!h.startsWith('#')) h = `#${h}`
  if (/^#[0-9a-fA-F]{3}$/.test(h)) {
    // expand #abc → #aabbcc
    h = `#${h[1]}${h[1]}${h[2]}${h[2]}${h[3]}${h[3]}`
  }
  return /^#[0-9a-fA-F]{6}$/.test(h) ? h.toUpperCase() : null
}

export function SettingsAppearance() {
  const { t } = useI18n()
  const { theme, setTheme, accentColor, setAccentColor } = useTheme()

  // Local hex input state so the field stays editable until a valid color is
  // committed; out-of-range input shows an inline error without touching theme.
  const [hexInput, setHexInput] = useState(accentColor)
  const hexError = normalizeHex(hexInput) === null

  const commitHex = () => {
    const norm = normalizeHex(hexInput)
    if (norm) setAccentColor(norm)
    else setHexInput(accentColor) // revert invalid edit
  }

  return (
    <div className="flex flex-col">
      {/* Theme — dark / light */}
      <SettingsSection title={t('settings.theme')}>
        <div className="flex gap-2">
          {THEMES.map((value) => {
            const active = theme === value
            return (
              <button
                key={value}
                type="button"
                aria-pressed={active}
                onClick={() => setTheme(value)}
                className={cn(
                  'flex flex-1 items-center justify-center gap-2 rounded-md border px-3 py-2 text-sm transition-colors',
                  active
                    ? 'border-accent bg-accent/10 text-foreground'
                    : 'border-border bg-transparent text-muted-foreground hover:bg-muted',
                )}
              >
                <span
                  className={cn(
                    'flex size-4 items-center justify-center rounded-full border',
                    active ? 'border-accent text-accent' : 'border-border',
                  )}
                >
                  {active ? <span className="size-2 rounded-full bg-accent" /> : null}
                </span>
                {t(`settings.${value}`)}
              </button>
            )
          })}
        </div>
      </SettingsSection>

      {/* Accent color — presets + custom hex */}
      <SettingsSection title={t('settings.accentColor')}>
        <div className="flex flex-wrap gap-2">
          {ACCENT_PRESETS.map((color) => {
            const active = accentColor.toUpperCase() === color.toUpperCase()
            return (
              <button
                key={color}
                type="button"
                aria-label={color}
                aria-pressed={active}
                title={color}
                onClick={() => {
                  setAccentColor(color)
                  setHexInput(color)
                }}
                className={cn(
                  'relative size-8 rounded-md border-2 transition-transform hover:scale-105 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
                  active ? 'border-foreground' : 'border-transparent',
                )}
                style={{ backgroundColor: color }}
              >
                {active ? (
                  <Check
                    className="absolute inset-0 m-auto size-4"
                    // pick contrast text so the check is visible on any accent
                    style={{ color: 'var(--accent-foreground)' }}
                  />
                ) : null}
              </button>
            )
          })}
        </div>

        {/* Custom hex input */}
        <div className="flex flex-col gap-2 pt-1">
          <Label htmlFor="accent-hex" className="text-xs text-muted-foreground">
            {t('settings.accentCustom')}
          </Label>
          <div className="flex items-center gap-2">
            {/* live preview chip — reflects committed accent (var) */}
            <span
              className="size-8 shrink-0 rounded-md border border-border"
              style={{ backgroundColor: 'var(--accent)' }}
              aria-hidden
            />
            <Input
              id="accent-hex"
              value={hexInput}
              spellCheck={false}
              autoComplete="off"
              aria-invalid={hexError}
              onChange={(e) => setHexInput(e.target.value)}
              onBlur={commitHex}
              onKeyDown={(e) => {
                if (e.key === 'Enter') (e.target as HTMLInputElement).blur()
              }}
              className="max-w-[180px] font-mono"
              placeholder={DEFAULT_ACCENT_COLOR}
            />
          </div>
          {hexError ? (
            <p className="text-xs text-destructive">{t('settings.accentInvalid')}</p>
          ) : null}
        </div>
      </SettingsSection>
    </div>
  )
}
