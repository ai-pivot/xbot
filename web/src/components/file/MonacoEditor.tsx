/**
 * MonacoEditor — Monaco editor wrapper (Spec 5 §3.3).
 *
 * Wraps `@monaco-editor/react`'s `<Editor>` with the Spec's editor defaults:
 *   - font: JetBrains Mono → Menlo → Consolas → monospace
 *   - line numbers + code folding + syntax highlighting
 *   - minimap off by default
 *   - editable (readOnly off unless requested)
 *
 * Theme follows the global ThemeProvider:
 *   - We define two custom themes (`xbot-dark`, `xbot-light`) in `beforeMount`,
 *     reading the live CSS design tokens so the editor surface, gutter and
 *     selection match the VSCode palette and the accent color.
 *   - On theme switch we re-read the tokens and re-define the theme, then
 *     `setTheme` so the change is live (no remount needed).
 *
 * `monacoEnv.ts` (imported for side effects) pins Monaco to the local bundle
 * and wires the language web workers.
 */
import Editor, { type BeforeMount, type OnMount } from '@monaco-editor/react'
import { useEffect, useRef } from 'react'

import { useTheme } from '@/hooks/useTheme'
import type { Theme } from '@/types/shared'

import './monacoEnv'

const FONT_FAMILY =
  "'JetBrains Mono', 'Menlo', 'Consolas', 'Liberation Mono', 'DejaVu Sans Mono', monospace"

const THEME_ID: Record<Theme, string> = {
  dark: 'xbot-dark',
  light: 'xbot-light',
}

/** Read a CSS custom property from <html> (the design-token layer). */
function cssVar(name: string): string {
  if (typeof window === 'undefined') return ''
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
}

/**
 * Normalize a CSS hex color to a 6-digit form (`#rrggbb`).
 *
 * Production CSS is minified, so a design token like `--bg-primary: #ffffff`
 * is rewritten to the 3-digit `#fff`. Monaco's token-color ColorMap regex
 * only accepts 6-digit hex, so any color we feed into `editor.foreground` /
 * `editor.background` (which become token colors) must be expanded. Non-hex
 * inputs (rgb(), named colors) pass through untouched and are avoided by the
 * theme (design tokens are all hex).
 */
function normalizeHex(color: string): string {
  const m = /^#?([0-9a-fA-F]{3})$/.exec(color.trim())
  if (m) {
    const [, c] = m
    return `#${c[0]}${c[0]}${c[1]}${c[1]}${c[2]}${c[2]}`
  }
  return color
}

/** Read + normalize a design token so it is safe as a Monaco token color. */
function tokenColor(name: string): string {
  return normalizeHex(cssVar(name))
}

/** Build a Monaco theme data object from the live design tokens. */
function defineXbotTheme(monaco: typeof import('monaco-editor'), theme: Theme): void {
  const isDark = theme === 'dark'
  const bg = tokenColor('--bg-primary') || (isDark ? '#1e1e1e' : '#ffffff')
  const fg = tokenColor('--text-primary') || (isDark ? '#cccccc' : '#1e1e1e')
  const gutter = tokenColor('--editor-gutter') || (isDark ? '#1e1e1e' : '#f0f0f0')
  const accent = tokenColor('--accent') || '#3388bb'
  const border = tokenColor('--border') || (isDark ? '#3c3c3c' : '#e0e0e0')
  const selection = normalizeHex(isDark ? '#264f78' : '#add6ff')

  // `inherit: true` keeps the base theme's token rules so every language gets
  // full syntax highlighting; we only override the editor surface + accent.
  // (Monaco 0.55's ColorMap rejects 3-digit hex token colors, but the base
  // themes use 6-digit hex — design-token colors are normalized via tokenColor.)
  monaco.editor.defineTheme(THEME_ID[theme], {
    base: isDark ? 'vs-dark' : 'vs',
    inherit: true,
    rules: [
      { token: 'comment', foreground: isDark ? '6a9955' : '6a737d', fontStyle: 'italic' },
      { token: 'keyword', foreground: isDark ? '569cd6' : '0000ff' },
      { token: 'string', foreground: isDark ? 'ce9178' : 'a31515' },
      { token: 'number', foreground: isDark ? 'b5cea8' : '098658' },
      { token: 'type', foreground: isDark ? '4ec9b0' : '267f99' },
    ],
    colors: {
      'editor.background': bg,
      'editor.foreground': fg,
      'editorLineNumber.foreground': isDark ? '858585' : '6b6b6b',
      'editorLineNumber.activeForeground': accent,
      'editorGutter.background': gutter,
      'editor.selectionBackground': selection,
      'editor.lineHighlightBackground': isDark ? '#2a2d2e' : '#f0f0f0',
      'editorCursor.foreground': accent,
      'editorIndentGuide.background': isDark ? '#404040' : '#d0d0d0',
      'editorIndentGuide.activeBackground': border,
      'editorWidget.background': isDark ? '#252526' : '#f3f3f3',
      'editorWidget.border': border,
      'editorSuggestWidget.background': isDark ? '#252526' : '#f3f3f3',
      'editorSuggestWidget.selectedBackground': isDark ? '#094771' : '#e6f0fb',
    },
  })
}

export interface MonacoEditorProps {
  /** Current text content. */
  value: string
  /** Monaco language id (see fileTypes.languageOf). */
  language: string
  /** Called with the new text on every edit. */
  onChange?: (value: string) => void
  /** Disable editing (e.g. read-only preview of code). Default false. */
  readOnly?: boolean
  /** CSS height; defaults to 100% of the parent. */
  height?: string
  /** className for the host wrapper. */
  className?: string
}

export function MonacoEditor({
  value,
  language,
  onChange,
  readOnly = false,
  height = '100%',
  className,
}: MonacoEditorProps) {
  const { theme } = useTheme()
  // Hold the Monaco namespace so the theme effect can re-define/setTheme.
  const monacoRef = useRef<typeof import('monaco-editor') | null>(null)

  const handleBeforeMount: BeforeMount = (monaco) => {
    monacoRef.current = monaco
    defineXbotTheme(monaco, theme)
  }

  const handleMount: OnMount = (editor, monaco) => {
    monacoRef.current = monaco
    monaco.editor.setTheme(THEME_ID[theme])
    // Focus the editor so keyboard navigation works immediately on tab open.
    editor.focus()
  }

  // Re-apply theme when the global theme changes (live token re-read).
  useEffect(() => {
    const monaco = monacoRef.current
    if (!monaco) return
    defineXbotTheme(monaco, theme)
    monaco.editor.setTheme(THEME_ID[theme])
  }, [theme])

  return (
    <Editor
      className={className}
      height={height}
      value={value}
      language={language}
      theme={THEME_ID[theme]}
      beforeMount={handleBeforeMount}
      onMount={handleMount}
      onChange={(next) => onChange?.(next ?? '')}
      loading={<EditorLoading />}
      options={{
        fontFamily: FONT_FAMILY,
        fontSize: 13,
        fontLigatures: true,
        minimap: { enabled: false },
        lineNumbers: 'on',
        lineNumbersMinChars: 3,
        renderLineHighlight: 'all',
        folding: true,
        scrollBeyondLastLine: false,
        smoothScrolling: true,
        cursorBlinking: 'smooth',
        tabSize: 2,
        automaticLayout: true,
        readOnly,
        fixedOverflowWidgets: true,
      }}
    />
  )
}

function EditorLoading() {
  return (
    <div className="flex h-full items-center justify-center text-sm text-text-secondary">
      Loading editor…
    </div>
  )
}
