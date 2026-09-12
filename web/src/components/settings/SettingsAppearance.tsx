/**
 * SettingsAppearance — Markdown theme + accent color (Spec 7 §3.3).
 *
 *   - Markdown theme: pluggable palette selector (also drives dark/light).
 *   - Accent color: preset swatches + a custom hex input wired to
 *     useTheme.setAccentColor; the live preview chip reflects --accent.
 *
 * Both write through the ThemeProvider, which persists and updates the CSS
 * variables, so the rest of the UI updates live (no local state needed).
 */
import { Check, Monitor, Smartphone, Sparkles } from 'lucide-react'
import { useState } from 'react'

import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useTheme } from '@/hooks/useTheme'
import { UI_MODES, useUIMode, type UIMode } from '@/hooks/useUIMode'
import { useI18n } from '@/providers/i18n'
import { DEFAULT_ACCENT_COLOR } from '@/types/theme'
import { MARKDOWN_THEMES } from '@/types/markdown-theme'
import { cn } from '@/lib/utils'

import { SettingsSection } from './SettingsSection'

/** UI 外壳模式选项元数据（Record 保证穷尽所有模式）。 */
const UI_MODE_META: Record<UIMode, { labelKey: string; icon: typeof Monitor }> = {
  auto: { labelKey: 'settings.uiModeAuto', icon: Sparkles },
  desktop: { labelKey: 'settings.uiModeDesktop', icon: Monitor },
  mobile: { labelKey: 'settings.uiModeMobile', icon: Smartphone },
}

/** Spec 7 §3.3 preset palette. */
const ACCENT_PRESETS = [
  '#3388BB',
  '#2563EB',
  '#7C3AED',
  '#DC2626',
  '#059669',
  '#EA580C',
]

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
  const { accentColor, setAccentColor, mdTheme, setMdTheme } = useTheme()
  const { mode, effective, setMode } = useUIMode()

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
    <div className="flex flex-col gap-2.5 p-4">
      {/* UI 外壳模式（自动 / 桌面 / 移动端）——影响整个应用的外壳选择 */}
      <SettingsSection title={t('settings.uiMode')}>
        <p className="mb-2 text-xs text-text-muted">{t('settings.uiModeDesc')}</p>
        <div className="flex flex-wrap gap-2">
          {UI_MODES.map((id) => {
            const { labelKey, icon: Icon } = UI_MODE_META[id]
            const active = mode === id
            return (
              <button
                key={id}
                type="button"
                aria-pressed={active}
                data-testid={`ui-mode-${id}`}
                onClick={() => setMode(id)}
                className={cn(
                  'flex items-center gap-1.5 rounded-lg border px-3 py-1.5 text-xs transition-colors',
                  active
                    ? 'border-accent/40 bg-accent/14 text-accent'
                    : 'border-border bg-bg-secondary text-text-muted hover:bg-bg-tertiary hover:text-text-primary',
                )}
              >
                <Icon className="size-3.5" />
                {t(labelKey)}
              </button>
            )
          })}
        </div>
        <p className="mt-2 text-xs text-text-muted">
          {t('settings.uiModeEffective', {
            mode: t(effective === 'mobile' ? 'settings.uiModeMobile' : 'settings.uiModeDesktop'),
          })}
        </p>
      </SettingsSection>

      <SettingsSection title={t('settings.mdTheme')}>
        <p className="mb-2 text-xs text-text-muted">{t('settings.mdThemeDesc')}</p>
        <div className="flex flex-wrap gap-2">
          {MARKDOWN_THEMES.map((md) => {
            const active = mdTheme === md.id
            return (
              <button
                key={md.id}
                type="button"
                aria-pressed={active}
                onClick={() => setMdTheme(md.id)}
                className={cn(
                  'rounded-lg border px-3 py-1.5 text-xs transition-colors',
                  active
                    ? 'border-accent/40 bg-accent/14 text-accent'
                    : 'border-border bg-bg-secondary text-text-muted hover:bg-bg-tertiary hover:text-text-primary',
                )}
              >
                {t(md.labelKey)}
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
          <Label htmlFor="accent-hex" className="text-xs text-text-muted">
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
              className="max-w-[180px] rounded-lg border-border bg-bg-secondary font-mono focus-visible:border-accent/40 focus-visible:ring-accent/25"
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

