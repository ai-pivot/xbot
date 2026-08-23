/**
 * AnsiText — renders terminal output containing ANSI escape sequences with
 * proper colors (SGR 30-37/40-47/90-97/100-107, 256-color, truecolor).
 *
 * Scope: read-only pretty rendering of tool output (Shell etc.), NOT a full
 * terminal emulator:
 *   - SGR colors/attributes (bold/dim/italic/underline/strikethrough/inverse)
 *     rendered; unknown attributes ignored
 *   - `\r` handled per-line last-write-wins (progress bars); `\r` followed by
 *     ESC[K/2K clears the line (the common "clear then redraw" pattern)
 *   - cursor movement (ESC[C/A/J…), OSC (window titles) and other escapes
 *     stripped — they never reach the DOM as raw garbage
 *
 * Palettes: 16-color basic palette per light/dark theme (readability-tuned,
 * GitHub-style). 256-color cube and truecolor use literal colors
 * (theme-independent — matching how real terminals behave).
 */
import { memo, useContext, useMemo, type CSSProperties } from 'react'

import { ThemeContext } from '@/providers/theme'

/** One run of text with its resolved styling. */
export interface AnsiSegment {
  text: string
  color?: string
  backgroundColor?: string
  bold?: boolean
  dim?: boolean
  italic?: boolean
  underline?: boolean
  strikethrough?: boolean
}

/** Basic 16-color palettes (index 0-7 standard, 8-15 bright). */
const LIGHT_PALETTE = [
  '#24292f', '#cf222e', '#116329', '#4d2d00',
  '#0550ae', '#8250df', '#1b7c83', '#6e7781',
  '#6e7781', '#a40e26', '#1a7f37', '#9a6700',
  '#0969da', '#8250df', '#3198a3', '#8c959f',
]
const DARK_PALETTE = [
  '#484f58', '#ff7b72', '#3fb950', '#d29922',
  '#58a6ff', '#bc8cff', '#39c5cf', '#b1bac4',
  '#6e7681', '#ffa198', '#56d364', '#e3b341',
  '#79c0ff', '#d2a8ff', '#56d4dd', '#f0f6fc',
]

/** xterm 256-color cube (16-231) + grayscale ramp (232-255). */
const CUBE = [0, 95, 135, 175, 215, 255]
function color256(n: number, palette: string[]): string {
  if (n >= 0 && n < 16) return palette[n]
  if (n >= 16 && n < 232) {
    const i = n - 16
    return `rgb(${CUBE[Math.floor(i / 36)]},${CUBE[Math.floor(i / 6) % 6]},${CUBE[i % 6]})`
  }
  if (n >= 232 && n <= 255) {
    const v = 8 + (n - 232) * 10
    return `rgb(${v},${v},${v})`
  }
  return palette[7]
}

/** Apply `\r` semantics per line. CRLF is just a line ending (normalize first);
 *  a lone `\r` returns the cursor to column 0, so the LAST redraw is what the
 *  user saw — later chunks replace the line (progress bars). A leading
 *  ESC[K in a redraw chunk just makes the clear explicit. */
function applyCarriageReturns(input: string): string {
  const normalized = input.replace(/\r\n/g, '\n')
  if (!normalized.includes('\r')) return normalized
  return normalized
    .split('\n')
    .map((line) => {
      if (!line.includes('\r')) return line
      const chunks = line.split('\r')
      const last = chunks[chunks.length - 1]
      // trailing `\r` with nothing after it is not a redraw — keep the line
      if (last === '') return chunks[chunks.length - 2] ?? ''
      // eslint-disable-next-line no-control-regex -- \x1b 是 ANSI 转义符本身
      return last.replace(/^(?:\x1b\[[0-9]*K)+/, '')
    })
    .join('\n')
}

/** Matches OSC (…BEL / …ESC\), CSI sequences, and any other single-char ESC. */
// eslint-disable-next-line no-control-regex -- ANSI ESC/OSC/CSI 序列解析
const ESCAPE_RE = /\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b\[[0-9;?]*[A-Za-z]|\x1b./g
// eslint-disable-next-line no-control-regex -- ANSI CSI 序列解析
const CSI_RE = /^\x1b\[([0-9;?]*)([A-Za-z])$/

/** Parse ANSI text into styled segments. Pure — exported for unit tests.
 *  `dark` selects the 16-color palette; 256/truecolor are theme-independent. */
export function parseAnsi(input: string, dark: boolean): AnsiSegment[] {
  const palette = dark ? DARK_PALETTE : LIGHT_PALETTE
  // Fast path needs BOTH checks: no ESC AND no `\r` — progress-bar output with
  // bare \r redraws (no color codes) still needs carriage-return handling.
  if (!input.includes('\x1b') && !input.includes('\r')) return [{ text: input }]
  const cleaned = applyCarriageReturns(input)
  const segments: AnsiSegment[] = []
  let fg: string | undefined
  let bg: string | undefined
  let bold = false
  let dim = false
  let italic = false
  let underline = false
  let strike = false
  let inverse = false

  const push = (text: string) => {
    if (!text) return
    const seg: AnsiSegment = { text }
    const color = inverse ? bg : fg
    const bgColor = inverse ? fg : bg
    if (color) seg.color = color
    if (bgColor) seg.backgroundColor = bgColor
    if (bold) seg.bold = true
    if (dim) seg.dim = true
    if (italic) seg.italic = true
    if (underline) seg.underline = true
    if (strike) seg.strikethrough = true
    segments.push(seg)
  }

  const applySgr = (paramStr: string) => {
    const params = paramStr === '' ? [0] : paramStr.split(';').map((p) => parseInt(p, 10))
    let i = 0
    while (i < params.length) {
      const p = params[i]
      if (p === 0) {
        fg = undefined
        bg = undefined
        bold = dim = italic = underline = strike = inverse = false
      } else if (p === 1) bold = true
      else if (p === 2) dim = true
      else if (p === 3) italic = true
      else if (p === 4) underline = true
      else if (p === 7) inverse = true
      else if (p === 9) strike = true
      else if (p === 21 || p === 22) { bold = false; dim = false }
      else if (p === 23) italic = false
      else if (p === 24) underline = false
      else if (p === 27) inverse = false
      else if (p === 29) strike = false
      else if (p >= 30 && p <= 37) fg = palette[p - 30]
      else if (p === 39) fg = undefined
      else if (p >= 40 && p <= 47) bg = palette[p - 40]
      else if (p === 49) bg = undefined
      else if (p >= 90 && p <= 97) fg = palette[p - 90 + 8]
      else if (p >= 100 && p <= 107) bg = palette[p - 100 + 8]
      else if (p === 38 || p === 48) {
        const mode = params[i + 1]
        let consumed = 1
        let color: string | undefined
        if (mode === 5 && Number.isInteger(params[i + 2])) {
          color = color256(params[i + 2], palette)
          consumed = 3
        } else if (mode === 2 && Number.isInteger(params[i + 2]) && Number.isInteger(params[i + 3]) && Number.isInteger(params[i + 4])) {
          color = `rgb(${params[i + 2]},${params[i + 3]},${params[i + 4]})`
          consumed = 5
        }
        if (color !== undefined) {
          if (p === 38) fg = color
          else bg = color
          i += consumed
          continue
        }
      }
      i++
    }
  }

  let last = 0
  ESCAPE_RE.lastIndex = 0
  for (const m of cleaned.matchAll(ESCAPE_RE)) {
    const idx = m.index ?? 0
    if (idx > last) push(cleaned.slice(last, idx))
    const csi = CSI_RE.exec(m[0])
    if (csi && csi[2] === 'm') applySgr(csi[1].replace(/\?/g, ''))
    // every other escape (cursor movement, OSC, …) is dropped
    last = idx + m[0].length
  }
  push(cleaned.slice(last))
  return segments
}

function segmentStyle(s: AnsiSegment): CSSProperties {
  const st: CSSProperties = {}
  if (s.color) st.color = s.color
  if (s.backgroundColor) st.backgroundColor = s.backgroundColor
  if (s.bold) st.fontWeight = 600
  if (s.dim) st.opacity = 0.65
  if (s.italic) st.fontStyle = 'italic'
  if (s.underline || s.strikethrough) {
    st.textDecoration = [s.underline ? 'underline' : '', s.strikethrough ? 'line-through' : '']
      .filter(Boolean)
      .join(' ')
  }
  return st
}

/**
 * Inline renderer for ANSI-colored text. Place inside the caller's own
 * `<pre>` (whitespace handling stays with the parent). Falls back to plain
 * text when no escape sequences are present (single-segment fast path).
 */
export const AnsiText = memo(function AnsiText({ text }: { text: string }) {
  const themeCtx = useContext(ThemeContext)
  const dark = themeCtx?.theme === 'dark'
  const segments = useMemo(() => parseAnsi(text, dark), [text, dark])
  return (
    <>
      {segments.map((s, i) => (
        <span key={i} style={segmentStyle(s)}>{s.text}</span>
      ))}
    </>
  )
})
