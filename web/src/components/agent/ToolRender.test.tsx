/**
 * Tests for the fancy tool renderers (ToolRender + DiffView).
 *
 * Covers the pure parsers (they ARE the contract with the backend tool result
 * formats — see tools/shell.go, tools/grep.go, tools/glob.go, tools/edit.go)
 * plus render smoke tests for the dedicated views.
 */
import { describe, expect, it } from 'vitest'
import { screen } from '@testing-library/react'
import '@testing-library/jest-dom'

import { renderWithProviders } from '@/test-utils'
import { ToolRender, parseShell, parseRead, parseGrepResult, parseGlobResult, langFromPath } from '@/components/agent/ToolRender'
import { DiffView, extractDiffSource, parseUnifiedDiff } from '@/components/agent/DiffView'
import type { WebToolProgress } from '@/types/shared'

/** Helper: build a WebToolProgress with defaults. */
function makeTool(overrides: Partial<WebToolProgress> = {}): WebToolProgress {
  return {
    name: 'Shell',
    label: '',
    status: 'done',
    elapsedMs: 0,
    summary: '',
    detail: '',
    args: '',
    toolHints: '',
    ...overrides,
  }
}

// ── parseShell ──────────────────────────────────────────────────────────

describe('parseShell', () => {
  it('parses a successful run: command from label, output from summary', () => {
    const tool = makeTool({ label: 'Shell: ls -la' })
    const r = parseShell(tool, 'total 16\ndrwxr-xr-x', '')
    expect(r.command).toBe('ls -la')
    expect(r.output).toBe('total 16\ndrwxr-xr-x')
    expect(r.exitCode).toBeNull()
  })

  it('parses [EXIT N] prefix and strips the duplicated command line', () => {
    const tool = makeTool({ label: 'Shell: false' })
    const r = parseShell(tool, '[EXIT 1] false\nsome error', '')
    expect(r.exitCode).toBe(1)
    expect(r.output).toBe('some error')
  })

  it('parses [TIMEOUT] prefix (timeout flag, rest as output)', () => {
    const tool = makeTool({ label: 'Shell: sleep 100' })
    const r = parseShell(tool, '[TIMEOUT after 3m0s] Partial output:\nline1', '')
    expect(r.timeout).toBe(true)
    expect(r.output).toBe('line1')
  })

  it('prefers args.command when args are present (live streaming)', () => {
    const tool = makeTool({ args: '{"command":"echo hi"}' })
    const r = parseShell(tool, 'hi', '')
    expect(r.command).toBe('echo hi')
  })

  it('extracts the bg task id from the output', () => {
    const tool = makeTool({ label: 'Shell: x' })
    const r = parseShell(tool, 'Background task running: bg:3f8f492a', '')
    expect(r.bgTask).toBe('bg:3f8f492a')
  })
})

// ── parseRead ───────────────────────────────────────────────────────────

describe('parseRead', () => {
  it('strips the "N\\t" line-number prefix and keeps content', () => {
    const r = parseRead('1\tfoo\n2\tbar\n3\tbaz', '')
    expect(r?.code).toBe('foo\nbar\nbaz')
    expect(r?.startLine).toBe(1)
    expect(r?.lineCount).toBe(3)
  })

  it('tracks startLine from offset reads', () => {
    const r = parseRead('100\tfoo\n101\tbar', '')
    expect(r?.startLine).toBe(100)
  })

  it('captures the truncated notice line', () => {
    const r = parseRead('1\tfoo\n\n... [truncated: showing 1 of 50 lines, use max_lines parameter to see more]', '')
    expect(r?.code).toBe('foo')
    expect(r?.notice).toContain('truncated')
  })

  it('recognizes the engine 4000-rune cap notice "... (truncated)" (paren style) — no double line numbers', () => {
    // engine_run_tools.go appends "\n... (truncated)" to live detail. The old
    // parser only recognized the bracket style, so this tail line forced the
    // raw fallback → the embedded N\t numbers rendered next to the gutter.
    const r = parseRead('1\tfoo\n2\tbar\n... (truncated)', '')
    expect(r?.code).toBe('foo\nbar')
    expect(r?.notice).toBe('... (truncated)')
    expect(r?.startLine).toBe(1)
  })

  it('recognizes the offset-exceeds-EOF notice', () => {
    const r = parseRead('(offset 10 exceeds file length 3 — file has no content from this line)', '')
    expect(r?.code).toBe('(offset 10 exceeds file length 3 — file has no content from this line)')
    expect(r?.notice).toContain('offset')
  })

  it('falls back to raw content when no line numbers are present', () => {
    const r = parseRead('plain text\nno numbers', '')
    expect(r?.code).toBe('plain text\nno numbers')
    expect(r?.startLine).toBe(1)
  })

  it('returns null on empty input', () => {
    expect(parseRead('', '')).toBeNull()
  })
})

// ── parseGrepResult ─────────────────────────────────────────────────────

describe('parseGrepResult', () => {
  it('groups matches by file header (## path) and counts totals', () => {
    const summary = '## a.go\n12: foo()\n34: foo(x)\n\n## b.go\n7: foo(y)\n\n(Found 3 match(es))'
    const r = parseGrepResult(summary, '')
    expect(r.files).toHaveLength(2)
    expect(r.files[0]).toEqual({ path: 'a.go', matches: [{ line: 12, text: 'foo()' }, { line: 34, text: 'foo(x)' }] })
    expect(r.files[1].matches).toHaveLength(1)
    expect(r.total).toBe(3)
  })

  it('derives the total from matches when the footer is absent', () => {
    const r = parseGrepResult('## a.go\n1: x\n2: y', '')
    expect(r.total).toBe(2)
  })
})

// ── parseGlobResult ─────────────────────────────────────────────────────

describe('parseGlobResult', () => {
  it('parses the "Found N matching file(s)" header and file list', () => {
    const r = parseGlobResult('Found 2 matching file(s):\na/x.go\nb/y.ts')
    expect(r.files).toEqual(['a/x.go', 'b/y.ts'])
    expect(r.declaredCount).toBe(2)
  })
})

// ── langFromPath ────────────────────────────────────────────────────────

describe('langFromPath', () => {
  it('maps common extensions to hljs languages', () => {
    expect(langFromPath('a/b/c.ts')).toBe('typescript')
    expect(langFromPath('x.py')).toBe('python')
    expect(langFromPath('Dockerfile')).toBe('dockerfile')
    expect(langFromPath('Makefile')).toBe('bash')
  })
  it('returns undefined for unknown extensions', () => {
    expect(langFromPath('data.bin')).toBeUndefined()
  })
})

// ── DiffView parsers ────────────────────────────────────────────────────

describe('extractDiffSource', () => {
  it('extracts the diff body from a fenced ```diff hint', () => {
    const hints = '```diff\n--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-a\n+b\n```'
    expect(extractDiffSource(hints)).toBe('--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-a\n+b\n')
  })
  it('treats non-fenced input as raw diff', () => {
    expect(extractDiffSource('@@ -1 +1 @@')).toBe('@@ -1 +1 @@')
  })
})

describe('parseUnifiedDiff', () => {
  const diff = [
    '--- a/web/src/x.tsx',
    '+++ b/web/src/x.tsx',
    '@@ -10,4 +10,5 @@ function foo() {',
    ' context line',
    '-removed line',
    '+added line',
    ' more context',
  ].join('\n')

  it('parses the file path from the +++ line', () => {
    const files = parseUnifiedDiff(diff)
    expect(files).toHaveLength(1)
    expect(files[0].path).toBe('web/src/x.tsx')
  })

  it('assigns old/new line numbers from the hunk header', () => {
    const files = parseUnifiedDiff(diff)
    const lines = files[0].lines
    // hunk starts at old=10/new=10
    const ctx = lines.find((l) => l.kind === 'ctx')!
    expect(ctx.oldNum).toBe(10)
    expect(ctx.newNum).toBe(10)
    const del = lines.find((l) => l.kind === 'del')!
    expect(del.oldNum).toBe(11)
    expect(del.newNum).toBeUndefined()
    const add = lines.find((l) => l.kind === 'add')!
    expect(add.newNum).toBe(11)
    expect(add.oldNum).toBeUndefined()
  })

  it('counts adds/dels per file', () => {
    const files = parseUnifiedDiff(diff)
    expect(files[0].adds).toBe(1)
    expect(files[0].dels).toBe(1)
  })

  it('splits multi-file diffs', () => {
    const two = diff + '\n+++ b/other.go\n@@ -1 +1 @@\n-a\n+b'
    const files = parseUnifiedDiff(two)
    expect(files).toHaveLength(2)
    expect(files[1].path).toBe('other.go')
  })
})

// ── render smoke tests ──────────────────────────────────────────────────

describe('ToolRender render', () => {
  it('Shell renders the $ command echo and exit badge', () => {
    renderWithProviders(
      <ToolRender
        tool={makeTool({
          label: 'Shell: ls -la',
          summary: '[EXIT 1] ls -la\nnope',
          elapsedMs: 1500,
        })}
      />,
    )
    expect(screen.getByText('$')).toBeInTheDocument()
    // command appears in both the card header and the $ echo line
    expect(screen.getAllByText('ls -la').length).toBeGreaterThanOrEqual(2)
    expect(screen.getByText('exit 1')).toBeInTheDocument()
    expect(screen.getByText('1500ms')).toBeInTheDocument()
  })

  it('Grep renders per-file groups with line numbers', () => {
    renderWithProviders(
      <ToolRender
        tool={makeTool({
          name: 'Grep',
          label: 'Grep: "foo" in src',
          summary: '## a.go\n12: foo()\n\n(Found 1 match(es))',
        })}
      />,
    )
    expect(screen.getByText('a.go')).toBeInTheDocument()
    expect(screen.getByText('12')).toBeInTheDocument()
    expect(screen.getByText(/1 match/)).toBeInTheDocument()
  })

  it('Glob renders the file list with dir/base split', () => {
    renderWithProviders(
      <ToolRender
        tool={makeTool({
          name: 'Glob',
          label: 'Glob: **/*.go',
          summary: 'Found 1 matching file(s):\nweb/src/main.go',
        })}
      />,
    )
    expect(screen.getByText('main.go')).toBeInTheDocument()
  })

  it('FileReplace renders ONLY the DiffView header when toolHints carries a diff (no duplicate tool header)', () => {
    const diff = '--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-old\n+new'
    renderWithProviders(
      <ToolRender
        tool={makeTool({
          name: 'FileReplace',
          label: 'FileReplace: x.go',
          summary: 'Successfully replaced 1 occurrence(s) in x.go',
          toolHints: '```diff\n' + diff + '\n```',
        })}
      />,
    )
    // path appears ONCE (DiffView file header); the renderer no longer adds
    // its own header — and the occurrence badge (+1 −1) that contradicted
    // the real diff stats (+1 −1 vs the header) is gone.
    expect(screen.getByText('x.go')).toBeInTheDocument()
    expect(screen.getByText('+1')).toBeInTheDocument() // DiffView header stat badge
    expect(screen.getByText('old')).toBeInTheDocument()
    expect(screen.getByText('new')).toBeInTheDocument()
  })

  it('TodoWrite renders the checklist with progress', () => {
    renderWithProviders(
      <ToolRender
        tool={makeTool({
          name: 'TodoWrite',
          summary: 'TODO 列表已更新: 1/2 完成',
          args: '{"todos":[{"id":1,"text":"step one","done":true},{"id":2,"text":"step two","done":false}]}',
        })}
      />,
    )
    expect(screen.getByText('step one')).toBeInTheDocument()
    expect(screen.getByText('step two')).toBeInTheDocument()
    expect(screen.getByText('1/2')).toBeInTheDocument()
  })
})

describe('DiffView', () => {
  it('renders the file header and hunk badge', () => {
    renderWithProviders(
      <DiffView diff={'--- a/x.go\n+++ b/x.go\n@@ -1,2 +1,2 @@\n-a\n+b'} />,
    )
    expect(screen.getByText('x.go')).toBeInTheDocument()
    expect(screen.getByText('+1')).toBeInTheDocument()
    expect(screen.getByText('−1')).toBeInTheDocument()
  })

  it('scrolls the whole card as ONE unit — never a scrollbar per line', () => {
    // Long unbreakable line (tab-prefixed JSON) — the regression case where
    // every <pre> got its own overflow-x-auto (comically broken UX).
    const long = 'x'.repeat(300)
    renderWithProviders(
      <DiffView diff={`--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-${long}\n+short`} />,
    )
    const pres = document.querySelectorAll('pre')
    expect(pres.length).toBeGreaterThan(0)
    for (const pre of Array.from(pres)) {
      expect(pre.className).not.toContain('overflow-x-auto')
    }
    // The card root carries the single shared overflow-auto.
    const root = pres[0].closest('.overflow-auto')
    expect(root).not.toBeNull()
  })
})
