import { useState, useEffect, useCallback, useRef } from 'react'
import { useTranslation } from '../i18n'
import { IconCopy, IconRefresh, IconX, IconSave, IconDownload } from './Icons'

/* -------------------------------------------------------------------------- */
/*  Types                                                                     */
/* -------------------------------------------------------------------------- */

export interface ThemeEditorProps {
  open: boolean
  onClose: () => void
}

interface ColorEntry {
  variable: string
  label: string
}

interface ColorGroup {
  title: string
  colors: ColorEntry[]
}

/* -------------------------------------------------------------------------- */
/*  Color variable definitions                                                */
/* -------------------------------------------------------------------------- */

const COLOR_GROUPS: ColorGroup[] = [
  {
    title: 'background',
    colors: [
      { variable: '--xbot-bg-primary', label: 'Primary' },
      { variable: '--xbot-bg-secondary', label: 'Secondary' },
      { variable: '--xbot-bg-surface', label: 'Surface' },
      { variable: '--xbot-bg-elevated', label: 'Elevated' },
      { variable: '--xbot-bg-input', label: 'Input' },
      { variable: '--xbot-bg-code', label: 'Code' },
    ],
  },
  {
    title: 'text',
    colors: [
      { variable: '--xbot-text-primary', label: 'Primary' },
      { variable: '--xbot-text-secondary', label: 'Secondary' },
      { variable: '--xbot-text-muted', label: 'Muted' },
      { variable: '--xbot-text-code', label: 'Code' },
      { variable: '--xbot-text-link', label: 'Link' },
      { variable: '--xbot-text-danger', label: 'Danger' },
    ],
  },
  {
    title: 'borderAccent',
    colors: [
      { variable: '--xbot-border', label: 'Border' },
      { variable: '--xbot-border-light', label: 'Border Light' },
      { variable: '--xbot-accent', label: 'Accent' },
      { variable: '--xbot-accent-blue', label: 'Accent Blue' },
    ],
  },
  {
    title: 'status',
    colors: [
      { variable: '--xbot-color-success', label: 'Success' },
      { variable: '--xbot-color-error', label: 'Error' },
      { variable: '--xbot-color-warning', label: 'Warning' },
    ],
  },
]

const STORAGE_KEY = 'xbot-custom-theme'

/* -------------------------------------------------------------------------- */
/*  Helpers                                                                   */
/* -------------------------------------------------------------------------- */

/** Read a CSS custom property value from :root */
function getCSSVar(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
}

/** Set a CSS custom property on :root */
function setCSSVar(name: string, value: string): void {
  document.documentElement.style.setProperty(name, value)
}

/** Remove a custom property override from :root (reverts to stylesheet value) */
function removeCSSVar(name: string): void {
  document.documentElement.style.removeProperty(name)
}

/** All variable names used in the editor */
const ALL_VARIABLES = COLOR_GROUPS.flatMap(g => g.colors.map(c => c.variable))

/** Load saved theme from localStorage */
function loadSavedTheme(): Record<string, string> {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return {}
    const parsed = JSON.parse(raw)
    if (typeof parsed !== 'object' || Array.isArray(parsed)) return {}
    return parsed
  } catch {
    return {}
  }
}

/** Save theme to localStorage */
function saveThemeToStorage(theme: Record<string, string>): void {
  localStorage.setItem(STORAGE_KEY, JSON.stringify(theme))
}

/** Remove saved theme from localStorage */
function clearSavedTheme(): void {
  localStorage.removeItem(STORAGE_KEY)
}

/** Validate that a parsed JSON object looks like a valid theme */
function isValidTheme(obj: unknown): obj is Record<string, string> {
  if (typeof obj !== 'object' || obj === null || Array.isArray(obj)) return false
  const record = obj as Record<string, string>
  // At least one key must be a known CSS variable
  return Object.keys(record).some(k => ALL_VARIABLES.includes(k))
}

/** Convert any color string to a hex value for the color picker */
function toHex(color: string): string {
  if (!color) return '#000000'
  if (color.startsWith('#') && (color.length === 7 || color.length === 4)) return color
  // Use a temp element to parse the color
  const tmp = document.createElement('div')
  tmp.style.color = color
  document.body.appendChild(tmp)
  const computed = getComputedStyle(tmp).color
  document.body.removeChild(tmp)
  const match = computed.match(/rgba?\((\d+),\s*(\d+),\s*(\d+)/)
  if (!match) return '#000000'
  const r = parseInt(match[1], 10).toString(16).padStart(2, '0')
  const g = parseInt(match[2], 10).toString(16).padStart(2, '0')
  const b = parseInt(match[3], 10).toString(16).padStart(2, '0')
  return `#${r}${g}${b}`
}

/* -------------------------------------------------------------------------- */
/*  Component                                                                 */
/* -------------------------------------------------------------------------- */

export default function ThemeEditor({ open, onClose }: ThemeEditorProps) {
  const { t } = useTranslation()
  const [colors, setColors] = useState<Record<string, string>>({})
  const [toast, setToast] = useState<string | null>(null)
  const toastTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  // Initialize: read current values from CSS and load saved overrides
  useEffect(() => {
    if (!open) return
    const saved = loadSavedTheme()
    // Apply saved theme
    for (const [key, value] of Object.entries(saved)) {
      setCSSVar(key, value)
    }
    // Build current color state from live CSS
    const current: Record<string, string> = {}
    for (const v of ALL_VARIABLES) {
      current[v] = toHex(getCSSVar(v))
    }
    setColors(current)
  }, [open])

  // Cleanup toast timer
  useEffect(() => {
    return () => {
      if (toastTimerRef.current) clearTimeout(toastTimerRef.current)
    }
  }, [])

  const showToast = useCallback((msg: string) => {
    setToast(msg)
    if (toastTimerRef.current) clearTimeout(toastTimerRef.current)
    toastTimerRef.current = setTimeout(() => setToast(null), 2000)
  }, [])

  // Handle color change: update live preview
  const handleColorChange = useCallback((variable: string, value: string) => {
    setCSSVar(variable, value)
    setColors(prev => ({ ...prev, [variable]: value }))
  }, [])

  // Save current theme to localStorage
  const handleSave = useCallback(() => {
    const theme: Record<string, string> = {}
    for (const v of ALL_VARIABLES) {
      const val = getCSSVar(v)
      if (val) theme[v] = val
    }
    saveThemeToStorage(theme)
    showToast(t('settingsSaved'))
  }, [t, showToast])

  // Reset to default theme
  const handleReset = useCallback(() => {
    for (const v of ALL_VARIABLES) {
      removeCSSVar(v)
    }
    clearSavedTheme()
    // Re-read default values
    const defaults: Record<string, string> = {}
    for (const v of ALL_VARIABLES) {
      defaults[v] = toHex(getCSSVar(v))
    }
    setColors(defaults)
    showToast(t('settingsReset'))
  }, [t, showToast])

  // Export theme JSON to clipboard
  const handleExport = useCallback(async () => {
    const theme: Record<string, string> = {}
    for (const v of ALL_VARIABLES) {
      const val = getCSSVar(v)
      if (val) theme[v] = val
    }
    try {
      await navigator.clipboard.writeText(JSON.stringify(theme, null, 2))
      showToast(t('copied'))
    } catch {
      showToast(t('operationFailed'))
    }
  }, [t, showToast])

  // Import theme JSON from clipboard
  const handleImport = useCallback(async () => {
    try {
      const text = await navigator.clipboard.readText()
      const parsed = JSON.parse(text)
      if (!isValidTheme(parsed)) {
        showToast(t('importFailed'))
        return
      }
      for (const [key, value] of Object.entries(parsed)) {
        if (ALL_VARIABLES.includes(key)) {
          setCSSVar(key, value)
        }
      }
      // Update state
      const current: Record<string, string> = {}
      for (const v of ALL_VARIABLES) {
        current[v] = toHex(getCSSVar(v))
      }
      setColors(current)
      saveThemeToStorage(parsed)
      showToast(t('importSuccess'))
    } catch {
      showToast(t('importFailed'))
    }
  }, [t, showToast])

  if (!open) return null

  return (
    <div className="theme-editor-backdrop" onClick={onClose}>
      <div
        className="theme-editor-panel"
        onClick={e => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-label={t('themeEditor')}
      >
        {/* Header */}
        <div className="theme-editor-header">
          <h2 className="theme-editor-title">🎨 {t('themeEditor')}</h2>
          <button className="theme-editor-close" onClick={onClose} aria-label={t('closeSettings')}>
            <IconX className="inline" />
          </button>
        </div>

        {/* Color groups */}
        <div className="theme-editor-body">
          {COLOR_GROUPS.map(group => (
            <div key={group.title} className="theme-editor-group">
              <h3 className="theme-editor-group-title">{group.title}</h3>
              <div className="theme-editor-color-grid">
                {group.colors.map(entry => (
                  <div key={entry.variable} className="theme-editor-color-item">
                    <label className="theme-editor-color-label">{entry.label}</label>
                    <div className="theme-editor-color-input-wrap">
                      <input
                        type="color"
                        className="theme-editor-color-picker"
                        value={colors[entry.variable] || '#000000'}
                        onChange={e => handleColorChange(entry.variable, e.target.value)}
                      />
                      <span className="theme-editor-color-value">
                        {colors[entry.variable] || '—'}
                      </span>
                    </div>
                  </div>
                ))}
              </div>
            </div>
          ))}
        </div>

        {/* Actions */}
        <div className="theme-editor-actions">
          <button className="theme-editor-btn theme-editor-btn-primary" onClick={handleSave}>
            <IconSave className="inline" /> {t('save')}
          </button>
          <button className="theme-editor-btn" onClick={handleExport}>
            <IconCopy className="inline" /> {t('exportTheme')}
          </button>
          <button className="theme-editor-btn" onClick={handleImport}>
            <IconDownload className="inline" /> {t('importTheme')}
          </button>
          <button className="theme-editor-btn theme-editor-btn-danger" onClick={handleReset}>
            <IconRefresh className="inline" /> {t('resetTheme')}
          </button>
        </div>

        {/* Toast */}
        {toast && <div className="theme-editor-toast">{toast}</div>}
      </div>
    </div>
  )
}
