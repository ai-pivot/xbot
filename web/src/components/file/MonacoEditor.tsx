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
import Editor, { DiffEditor, type BeforeMount, type DiffOnMount, type OnMount } from '@monaco-editor/react'
import { forwardRef, useEffect, useImperativeHandle, useRef } from 'react'

import { useTheme } from '@/hooks/useTheme'
import type { Theme } from '@/types/shared'

import './monacoEnv'

const FONT_FAMILY =
  "'JetBrains Mono', 'Menlo', 'Consolas', 'Liberation Mono', 'DejaVu Sans Mono', monospace"

const THEME_ID: Record<Theme, string> = {
  dark: 'xbot-dark',
  light: 'xbot-light',
}

/**
 * Normalize a CSS hex color to a 6-digit form (`#rrggbb`).
 *
 * Production CSS is minified, so a design token like `--app-bg: #ffffff`
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

/** Build a Monaco theme data object from the live design tokens. */
function defineXbotTheme(monaco: typeof import('monaco-editor'), theme: Theme): void {
  const isDark = theme === 'dark'
  // 编辑器表面色全部写死——不读 CSS token（用户主题的 accent/gutter 可能是
  // 红粉色，把行号区域和光标染红，视觉噪音）。Monaco 编辑器有自己独立的
  // 调色体系，与全局 UI 主题解耦。
  const bg = isDark ? '#1e1e1e' : '#ffffff'
  const fg = isDark ? '#cccccc' : '#1e1e1e'
  const gutter = isDark ? '#1e1e1e' : '#f0f0f0'
  const cursor = isDark ? '#aeafad' : '#000000'
  const border = isDark ? '#3c3c3c' : '#e0e0e0'
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
      'editorLineNumber.foreground': isDark ? '565656' : '8c959f',
      // 行号 active 色写死（不用 accent token——Dracula 粉红 accent 会把当前行
      // 行号染成红色，视觉噪音；固定中性亮灰保证任何全局主题下都是 dim 调）。
      'editorLineNumber.activeForeground': isDark ? 'c6c6c6' : '24292f',
      'editorGutter.background': gutter,
      'editor.selectionBackground': selection,
      'editor.lineHighlightBackground': isDark ? '#2a2d2e' : '#f0f0f0',
      'editorCursor.foreground': cursor,
      'editorIndentGuide.background': isDark ? '#404040' : '#d0d0d0',
      'editorIndentGuide.activeBackground': border,
      'editorWidget.background': isDark ? '#252526' : '#f3f3f3',
      'editorWidget.border': border,
      'editorSuggestWidget.background': isDark ? '#252526' : '#f3f3f3',
      'editorSuggestWidget.selectedBackground': isDark ? '#094771' : '#e6f0fb',
      // ---- DiffEditor palette（预混 6 位 hex；GitHub PR 官方配色值）----
      // 行级淡染 + 词级加深，gutter 同步行色；不覆盖通用编辑器任何颜色。
      'diffEditor.insertedLineBackground': isDark ? '#173620' : '#e6ffec',
      'diffEditor.removedLineBackground': isDark ? '#3a2124' : '#ffebe9',
      'diffEditor.insertedTextBackground': isDark ? '#1f4d2e' : '#abf2bd',
      'diffEditor.removedTextBackground': isDark ? '#5c2b30' : '#ffc0bd',
      'diffEditorGutter.insertedLineBackground': isDark ? '#173620' : '#e6ffec',
      'diffEditorGutter.removedLineBackground': isDark ? '#3a2124' : '#ffebe9',
      'diffEditor.border': border,
      'diffEditor.diagonalFill': isDark ? '#2a2a2a' : '#ececec',
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
  /**
   * 编辑器实例挂载回调（宿主面板用它注册插件可控制的 EditorController：
   * 跳行/高亮/选区等，见 plugin-runtime/editorRegistry.ts）。monaco 命名空间
   * 一并透传——controller 需要 Range 等运行时构造器（panel 顶层只类型导入
   * monaco-editor，避免把完整 bundle 拉进测试环境）。
   */
  onEditorMount?: (
    editor: import('monaco-editor').editor.IStandaloneCodeEditor,
    monaco: typeof import('monaco-editor'),
  ) => void
}

export function MonacoEditor({
  value,
  language,
  onChange,
  readOnly = false,
  height = '100%',
  className,
  onEditorMount,
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
    onEditorMount?.(editor, monaco)
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

export interface MonacoDiffEditorProps {
  /** 旧内容（左/上侧）。 */
  original: string
  /** 新内容（右/下侧）。 */
  modified: string
  /** Monaco language id（见 fileTypes.languageOf）。 */
  language: string
  /** CSS height；默认占满父容器。 */
  height?: string
  /** 宿主 wrapper 的 className。 */
  className?: string
  /** 初始渲染模式：side-by-side（VSCode 默认）或 inline（行内）。 */
  renderSideBySide?: boolean
  /** diff 计算完成后自动跳到第一个差异位置。默认开启。 */
  autoFocusFirstDiff?: boolean
}

/**
 * MonacoDiffEditor —— 原生 diff 编辑器（VSCode DiffEditor 同款）。
 *
 * 只读两侧内容对比。导航基于 monaco 内置 diff action：
 *  - `editor.action.diffEditor.nextDiff` / `prevDiff`（注册在 modified editor）
 *  - 跳转前先 focus modified editor（action.run 需要 focus 才生效）
 *  - autoFocusFirstDiff 监听 `onDidUpdateDiff`（diff 计算完成事件）再跳——
 *    死等固定延时在 diff 计算未完成时是 no-op（"按钮没用"的根因）。
 * 主题复用 defineXbotTheme（原版基调 + diff 配色追加）。
 */
/** MonacoDiffEditor 暴露给宿主的命令式 API（导航按钮放编辑器外的 header 里调用）。 */
export interface MonacoDiffEditorHandle {
  /** 跳到下一个修改行（环绕）。 */
  goDiff: (dir: 1 | -1) => void
  /** 切换并排（side-by-side）/行内（inline）渲染。 */
  setRenderSideBySide: (sideBySide: boolean) => void
}

export const MonacoDiffEditor = forwardRef<MonacoDiffEditorHandle, MonacoDiffEditorProps>(
  function MonacoDiffEditor(
    {
      original,
      modified: modifiedContent,
      language,
      height = '100%',
      className,
      renderSideBySide = true,
      autoFocusFirstDiff = true,
    }: MonacoDiffEditorProps,
    ref,
  ) {
  const { theme } = useTheme()
  const monacoRef = useRef<typeof import('monaco-editor') | null>(null)
  const editorRef = useRef<import('monaco-editor').editor.IStandaloneDiffEditor | null>(null)

  useImperativeHandle(ref, () => ({
    goDiff,
    setRenderSideBySide: (sideBySide: boolean) => {
      editorRef.current?.updateOptions({ renderSideBySide: sideBySide })
    },
  }), [])

  const handleBeforeMount: BeforeMount = (monaco) => {
    monacoRef.current = monaco
    defineXbotTheme(monaco, theme)
  }

  /**
   * 自实现 diff 跳转（monaco-editor 浏览器版没有 editor.action.diffEditor.
   * nextDiff action——那是 VSCode 桌面版功能；也不存在 getDiffLineInformation
   * 公开 API）。从 modified editor 的 model decorations 提取 diff 行：
   * DiffEditorWidget 用 `line-insert` / `line-delete` 类名的整行装饰标记
   * 修改行（修改行在 modified 侧必有 line-insert；纯删除行只在 original 侧，
   * 跳转聚焦 modified 侧的修改行——已覆盖"定位改动"的语义）。
   */
  const getDiffLines = (): number[] => {
    const ed = editorRef.current
    if (!ed) return []
    const model = ed.getModifiedEditor().getModel()
    if (!model) return []
    const lines = new Set<number>()
    for (const d of model.getAllDecorations()) {
      const cls = (d.options as { className?: string }).className
      if (cls === 'line-insert' || cls === 'line-delete') {
        lines.add(d.range.startLineNumber)
      }
    }
    return [...lines].sort((a, b) => a - b)
  }

  /** 跳到上/下一个修改行（环绕；reveal 到视口中央）。不 focus 编辑器——
   * 移动端 focus 会弹出软键盘（按钮点击不应抢焦点；F7 键位在用户主动
   * 点进编辑器后仍可用）。 */
  const goDiff = (dir: 1 | -1) => {
    const ed = editorRef.current
    if (!ed) return
    const modified = ed.getModifiedEditor()
    const lines = getDiffLines()
    if (lines.length === 0) return
    const cur = modified.getPosition()?.lineNumber ?? 0
    let target: number
    if (dir > 0) {
      target = lines.find((l) => l > cur) ?? lines[0]
    } else {
      target = [...lines].reverse().find((l) => l < cur) ?? lines[lines.length - 1]
    }
    modified.revealLineInCenter(target)
    modified.setPosition({ lineNumber: target, column: 1 })
  }

  /** diff 行装饰可能晚于 onDidUpdateDiff 应用——重试直到拿到装饰为止。 */
  const goFirstDiffWithRetry = (attempt = 0) => {
    const lines = getDiffLines()
    if (lines.length > 0) {
      const modified = editorRef.current?.getModifiedEditor()
      if (!modified) return
      modified.revealLineInCenter(lines[0])
      modified.setPosition({ lineNumber: lines[0], column: 1 })
      return
    }
    if (attempt < 10) {
      window.setTimeout(() => goFirstDiffWithRetry(attempt + 1), 120)
    }
  }

  const handleMount: DiffOnMount = (editor, monaco) => {
    monacoRef.current = monaco
    editorRef.current = editor
    monaco.editor.setTheme(THEME_ID[theme])
    // F7 / Shift+F7 跳转（VSCode 惯例键位）
    const modified = editor.getModifiedEditor()
    modified.addCommand(monaco.KeyCode.F7, () => goDiff(1))
    modified.addCommand(monaco.KeyMod.Shift | monaco.KeyCode.F7, () => goDiff(-1))
    if (autoFocusFirstDiff) {
      const d = editor.onDidUpdateDiff(() => {
        d.dispose()
        window.setTimeout(() => goFirstDiffWithRetry(), 100)
      })
    }
  }

  useEffect(() => {
    const monaco = monacoRef.current
    if (!monaco) return
    defineXbotTheme(monaco, theme)
    monaco.editor.setTheme(THEME_ID[theme])
  }, [theme])

  return (
    <div className={className} style={{ height, position: 'relative' }}>
      <DiffEditor
        height="100%"
        original={original}
        modified={modifiedContent}
        language={language}
        theme={THEME_ID[theme]}
        beforeMount={handleBeforeMount}
        onMount={handleMount}
        loading={<EditorLoading />}
        // dispose 竞态修复（@monaco-editor/react DiffEditor 已知问题）：组件
        // 卸载时 SDK dispose TextModel 早于 DiffEditorWidget reset model →
        // "TextModel got disposed before DiffEditorWidget model got reset"。
        keepCurrentOriginalModel
        keepCurrentModifiedModel
        options={{
          fontFamily: FONT_FAMILY,
          fontSize: 13,
          fontLigatures: true,
          minimap: { enabled: false },
          lineNumbers: 'on',
          lineNumbersMinChars: 3,
          folding: true,
          scrollBeyondLastLine: false,
          smoothScrolling: true,
          automaticLayout: true,
          readOnly: true,
          renderSideBySide,
          renderOverviewRuler: false,
          fixedOverflowWidgets: true,
          diffWordWrap: 'off',
          ignoreTrimWhitespace: false,
          renderLineHighlight: 'none',
          glyphMargin: false,
          scrollbar: {
            verticalScrollbarSize: 10,
            horizontalScrollbarSize: 10,
            useShadows: false,
          },
        }}
      />
    </div>
  )
  },
)
