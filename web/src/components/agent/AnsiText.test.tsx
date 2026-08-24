/**
 * Tests for AnsiText / parseAnsi — ANSI escape rendering in Shell output.
 *
 * The tsc --pretty output in the wild is the canonical input shape:
 * bright-cyan path, bright-yellow line:col, red "error", reverse-video line
 * numbers, dim summary lines — plus \r progress redraws from long-running
 * commands.
 */
import { describe, expect, it } from 'vitest'
import { screen } from '@testing-library/react'
import '@testing-library/jest-dom'

import { renderWithProviders } from '@/test-utils'
import { AnsiText, parseAnsi } from '@/components/agent/AnsiText'

describe('parseAnsi — colors', () => {
  it('plain text (no escapes) → single unstyled segment', () => {
    const segs = parseAnsi('hello world', false)
    expect(segs).toEqual([{ text: 'hello world' }])
  })

  it('renders basic fg colors (31 red) and resets (0)', () => {
    const segs = parseAnsi('\x1b[31merror\x1b[0m done', false)
    expect(segs).toEqual([
      { text: 'error', color: '#cf222e' },
      { text: ' done' },
    ])
  })

  it('bright fg (96 bright cyan — the tsc path color)', () => {
    const segs = parseAnsi('\x1b[96msrc/x.ts\x1b[0m', false)
    expect(segs[0].color).toBe('#3198a3')
  })

  it('dark theme selects the dark palette', () => {
    const segs = parseAnsi('\x1b[31mx', true)
    expect(segs[0].color).toBe('#ff7b72')
  })

  it('256-color (38;5;N) resolves through the xterm cube', () => {
    // 196 = cube index 180 → r=255 g=0 b=0
    expect(parseAnsi('\x1b[38;5;196mx', false)[0].color).toBe('rgb(255,0,0)')
    // 232 = grayscale start
    expect(parseAnsi('\x1b[38;5;232mx', false)[0].color).toBe('rgb(8,8,8)')
  })

  it('truecolor (38;2;r;g;b) renders literal rgb', () => {
    expect(parseAnsi('\x1b[38;2;10;20;30mx', false)[0].color).toBe('rgb(10,20,30)')
  })

  it('background colors (41/101) resolve like fg', () => {
    expect(parseAnsi('\x1b[41mx', false)[0].backgroundColor).toBe('#cf222e')
    expect(parseAnsi('\x1b[101mx', false)[0].backgroundColor).toBe('#a40e26')
  })
})

describe('parseAnsi — attributes', () => {
  it('bold / dim / italic / underline / strikethrough flags', () => {
    expect(parseAnsi('\x1b[1mx', false)[0].bold).toBe(true)
    expect(parseAnsi('\x1b[2mx', false)[0].dim).toBe(true)
    expect(parseAnsi('\x1b[3mx', false)[0].italic).toBe(true)
    expect(parseAnsi('\x1b[4mx', false)[0].underline).toBe(true)
    expect(parseAnsi('\x1b[9mx', false)[0].strikethrough).toBe(true)
  })

  it('reset (0) clears colors AND attributes; off-codes clear single flags', () => {
    const segs = parseAnsi('\x1b[31;1mx\x1b[0my\x1b[4mz', false)
    expect(segs[0]).toEqual({ text: 'x', color: '#cf222e', bold: true })
    expect(segs[1].color).toBeUndefined()
    expect(segs[1].bold).toBeUndefined()
    expect(segs[2].underline).toBe(true)
  })

  it('inverse (7) swaps fg/bg', () => {
    const seg = parseAnsi('\x1b[31;44;7mx', false)[0]
    expect(seg.color).toBe('#0550ae') // bg blue becomes fg
    expect(seg.backgroundColor).toBe('#cf222e') // fg red becomes bg
  })

  it('bold-off via 22 (21/39-style extended resets)', () => {
    const segs = parseAnsi('\x1b[1mb\x1b[22mx', false)
    expect(segs[0].bold).toBe(true)
    expect(segs[1].bold).toBeUndefined()
  })
})

describe('parseAnsi — escape stripping', () => {
  it('strips cursor movement / erase-screen sequences (2J, 2A, 1B)', () => {
    expect(parseAnsi('\x1b[2Jhello\x1b[1B', false)).toEqual([{ text: 'hello' }])
  })

  it('strips OSC window-title sequences (BEL and ESC-terminated)', () => {
    expect(parseAnsi('\x1b]0;title\x07hi', false)).toEqual([{ text: 'hi' }])
    expect(parseAnsi('\x1b]2;title\x1b\\hi', false)).toEqual([{ text: 'hi' }])
  })

  it('strips private-mode sequences (?25l)', () => {
    expect(parseAnsi('\x1b[?25lhi', false)).toEqual([{ text: 'hi' }])
  })
})

describe('parseAnsi — carriage returns (progress bars)', () => {
  it('later write wins on the same line (10% → 20%)', () => {
    expect(parseAnsi('prog 10%\rprog 20%', false)).toEqual([{ text: 'prog 20%' }])
  })

  it('handles bare \\r redraws with no color codes (fast-path guard)', () => {
    expect(parseAnsi('10%\r20%\r30%', false)).toEqual([{ text: '30%' }])
  })

  it('CRLF is a line ending, NOT an overwrite (no lines lost)', () => {
    expect(parseAnsi('line1\r\nline2\r\nline3', false)).toEqual([{ text: 'line1\nline2\nline3' }])
  })

  it('trailing lone \\r keeps the line', () => {
    expect(parseAnsi('abc\r', false)).toEqual([{ text: 'abc' }])
  })

  it('\\r + ESC[K clears the line before redraw', () => {
    expect(parseAnsi('prog 10%\r\x1b[Kprog 20%', false)).toEqual([{ text: 'prog 20%' }])
  })

  it('does not touch other lines', () => {
    const segs = parseAnsi('a\r\x1b[Kb\ncxx\rd', false)
    expect(segs).toEqual([{ text: 'b\nd' }])
  })
})

describe('AnsiText — rendering', () => {
  it('renders colored spans with inline styles (tsc --pretty shape)', () => {
    renderWithProviders(
      <AnsiText
        text={'\x1b[96msrc/x.ts\x1b[0m:\x1b[93m9\x1b[0m - \x1b[91merror\x1b[0m TS2307'}
      />,
    )
    const path = screen.getByText('src/x.ts')
    expect(path).toBeInTheDocument()
    expect(path).toHaveStyle({ color: '#3198a3' })
    // \x1b[91m = bright red (light palette #a40e26)
    expect(screen.getByText('error')).toHaveStyle({ color: '#a40e26' })
    // unstyled reset text has no inline color
    const tail = screen.getByText(/TS2307/)
    expect(tail.style.color).toBe('')
  })

  it('plain text renders verbatim without extra styling', () => {
    renderWithProviders(<AnsiText text="no escapes here" />)
    expect(screen.getByText('no escapes here')).toBeInTheDocument()
  })
})
