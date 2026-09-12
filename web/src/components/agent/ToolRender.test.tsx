/**
 * Tests for the fancy tool renderers (ToolRender + DiffView).
 *
 * Covers the pure parsers (they ARE the contract with the backend tool result
 * formats — see tools/shell.go, tools/grep.go, tools/glob.go, tools/edit.go)
 * plus render smoke tests for the dedicated views.
 */
import { describe, expect, it } from 'vitest'
import { fireEvent, screen } from '@testing-library/react'
import '@testing-library/jest-dom'

import { renderWithProviders } from '@/test-utils'
import { ToolRender, parseShell, parseRead, parseGrepResult, parseGlobResult, langFromPath, parseSyntheticHints } from '@/components/agent/ToolRender'
import { syntheticShortName, syntheticSubject } from '@/components/agent/SyntheticToolCard'
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

  it('extracts the task id from the promote/timeout/background result format [task_id: "xxx"]', () => {
    const tool = makeTool({ label: 'Shell: x' })
    const r = parseShell(tool, '[PROMOTED to background by user] Command moved to the background [task_id: "9adfa651"]\nPartial output so far:\nok', '')
    expect(r.promoted).toBe(true)
    expect(r.bgTask).toBe('9adfa651')
    expect(r.output).toContain('Partial output so far')
  })

  it('extracts the task id from timeout auto-promote results', () => {
    const tool = makeTool({ label: 'Shell: x' })
    const r = parseShell(tool, '[TIMEOUT after 2m0s] Command timed out. Auto-promoted to background task [task_id: "f6695704"]\nPartial output before timeout:\nbuild...', '')
    expect(r.timeout).toBe(true)
    expect(r.bgTask).toBe('f6695704')
  })

  it('extracts the task id from background-start results ("Background task started")', () => {
    const tool = makeTool({ label: 'Shell: x' })
    const r = parseShell(tool, 'Background task started [task_id: "1a2b3c4d"]\nCommand: npm run dev\n\nThe task is running in the background.', '')
    expect(r.bgTask).toBe('1a2b3c4d')
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

// ── injected (synthetic) notification tools ─────────────────────────────
//
// The backend injects bg-task / sub-agent completion, cron fires, etc. as fake
// tool-call pairs and ships a UI-only payload in toolHints
// (tools.SyntheticToolHints). These cards must show the ORIGINAL task, status,
// duration and a preview — and degrade to the summary text for history rows
// written before the payload existed.

describe('parseSyntheticHints', () => {
  it('parses the backend payload', () => {
    const h = parseSyntheticHints(
      JSON.stringify({ kind: 'bg_task', task_id: '3f8f492a', task: 'make build', status: 'done', exit_code: 0, elapsed_ms: 1234 }),
    )
    expect(h?.kind).toBe('bg_task')
    expect(h?.task).toBe('make build')
    expect(h?.exit_code).toBe(0)
  })

  it('returns null for empty / non-JSON / malformed payloads (legacy rows)', () => {
    expect(parseSyntheticHints('')).toBeNull()
    expect(parseSyntheticHints('Background task 3f8f492a completed.')).toBeNull()
    expect(parseSyntheticHints('{not json')).toBeNull()
  })
})

describe('SyntheticToolCard (fancy built-in notification cards)', () => {
  it('background task: original command, status, exit code, duration, output + section captions', () => {
    const tool = makeTool({
      name: 'background_task_result',
      label: 'bg:3f8f492a',
      status: 'done',
      summary: '背景任务 3f8f492a · done',
      toolHints: JSON.stringify({
        kind: 'bg_task', task_id: '3f8f492a', task: 'make build -j8',
        status: 'done', exit_code: 0, elapsed_ms: 1234, output: 'ok\nbuilt 3 targets',
      }),
    })
    renderWithProviders(<ToolRender tool={tool} />)
    // subject (task id) appears on the header chip AND in the meta footer
    expect(screen.getAllByText(/3f8f492a/).length).toBeGreaterThan(0)
    // the ORIGINAL task/command is the whole point of the card
    expect(screen.getByText('make build -j8')).toBeInTheDocument()
    // multi-line preview: match a substring (getByText normalizes whitespace)
    expect(screen.getByText(/built 3 targets/)).toBeInTheDocument()
    expect(screen.getByText(/done|完成/)).toBeInTheDocument()
    expect(screen.getByText(/(退出码|Exit code)\s*0/)).toBeInTheDocument()
    // rich card: section captions + duration chip + output stats + meta footer
    expect(screen.getByText(/Command|命令/)).toBeInTheDocument()
    expect(screen.getByText(/Output|输出/)).toBeInTheDocument()
    expect(screen.getByText('1.2s')).toBeInTheDocument()
    expect(screen.getByText(/2 (lines|行)/)).toBeInTheDocument()
    expect(screen.getByText(/Task ID|任务 ID/)).toBeInTheDocument()
  })

  it('sub-agent: role/instance, ORIGINAL task and result preview — never the internal name', () => {
    const tool = makeTool({
      name: 'bg_subagent_completed',
      label: 'bgsub:explore/mem-1',
      status: 'done',
      toolHints: JSON.stringify({
        kind: 'subagent', role: 'explore', instance: 'mem-1',
        task: '找出登录流程的入口', status: 'done', elapsed_ms: 42_000,
        output: '入口在 channel/web/web_auth.go',
      }),
    })
    renderWithProviders(<ToolRender tool={tool} />)
    expect(screen.getByText(/explore\/mem-1/)).toBeInTheDocument()
    expect(screen.getByText('找出登录流程的入口')).toBeInTheDocument()
    expect(screen.getByText('入口在 channel/web/web_auth.go')).toBeInTheDocument()
    expect(screen.getByText('42s')).toBeInTheDocument()
    // the raw snake_case tool name is never shown to users
    expect(screen.queryByText(/bg_subagent_completed/)).toBeNull()
  })

  it('falls back to summary/detail text when toolHints is absent (legacy history rows)', () => {
    const tool = makeTool({
      name: 'cron_fired',
      label: 'cron',
      status: 'done',
      summary: 'A scheduled cron job fired.\n\nMessage: nightly build',
    })
    renderWithProviders(<ToolRender tool={tool} />)
    expect(screen.getByText(/nightly build/)).toBeInTheDocument()
  })

  it('error cards surface the failure reason', () => {
    const tool = makeTool({
      name: 'background_task_result',
      status: 'error',
      toolHints: JSON.stringify({
        kind: 'bg_task', task: 'npm run build', status: 'error', exit_code: 1,
        elapsed_ms: 900, error: 'tsc: 3 errors',
      }),
    })
    renderWithProviders(<ToolRender tool={tool} />)
    expect(screen.getByText(/error|失败/)).toBeInTheDocument()
    expect(screen.getByText('tsc: 3 errors')).toBeInTheDocument()
    expect(screen.getByText(/(退出码|Exit code)\s*1/)).toBeInTheDocument()
  })

  it('renders a cancel marker card', () => {
    const tool = makeTool({ name: 'user_cancelled', label: 'cancelled by user', status: 'done' })
    renderWithProviders(<ToolRender tool={tool} />)
    // title + status chip both carry the cancelled wording
    expect(screen.getAllByText(/取消|cancelled/i).length).toBeGreaterThan(0)
  })

  it('user_interrupt renders a fancy interjection card with the message body', () => {
    const tool = makeTool({
      name: 'user_interrupt',
      status: 'done',
      toolHints: JSON.stringify({ kind: 'interrupt', message: '先别改前端，先把后端接口定下来' }),
    })
    renderWithProviders(<ToolRender tool={tool} />)
    expect(screen.getByTestId('interrupt-card')).toBeInTheDocument()
    expect(screen.getByText('先别改前端，先把后端接口定下来')).toBeInTheDocument()
    expect(screen.queryByText('user_interrupt')).toBeNull()
  })

  it('icons are lucide SVG — the cards contain no emoji glyphs', () => {
    const tool = makeTool({
      name: 'cron_fired',
      status: 'done',
      toolHints: JSON.stringify({ kind: 'cron', message: 'nightly build' }),
    })
    const { container } = renderWithProviders(<ToolRender tool={tool} />)
    const emoji = /[\u{1F300}-\u{1FAFF}\u{2600}-\u{27BF}]/u
    expect(emoji.test(container.textContent ?? '')).toBe(false)
  })
})

describe('syntheticShortName — friendly names for injected tools', () => {
  it('maps internal tool names to human labels (never snake_case), null for real tools', () => {
    expect(syntheticShortName(makeTool({ name: 'bg_subagent_completed' }))).toBe('Sub-agent')
    expect(syntheticShortName(makeTool({ name: 'background_task_result' }))).toBe('Background task')
    expect(syntheticShortName(makeTool({ name: 'user_cancelled' }))).toBe('Cancelled')
    expect(syntheticShortName(makeTool({ name: 'user_interrupt' }))).toBe('Interjection')
    expect(syntheticShortName(makeTool({ name: 'Shell' }))).toBeNull()
    // payload kind wins over the tool name (e.g. bg_subagent_failed → subagent)
    const failed = makeTool({
      name: 'bg_subagent_failed',
      toolHints: JSON.stringify({ kind: 'subagent', status: 'error' }),
    })
    expect(syntheticShortName(failed)).toBe('Sub-agent')
  })
})

describe('synthetic tools render their output as MARKDOWN from the unified fields', () => {
  it('sub-agent output is rendered as markdown (not raw asterisks / plain text)', () => {
    const tool = makeTool({
      name: 'bg_subagent_completed',
      status: 'done',
      toolHints: JSON.stringify({
        kind: 'subagent', role: 'explore', instance: 'mem-1',
        task: '查入口', output: '**入口**是 `channel/web/web_auth.go`\n\n- 步骤一\n- 步骤二',
      }),
    })
    const { container } = renderWithProviders(<ToolRender tool={tool} />)
    expect(container.querySelector('strong')?.textContent).toBe('入口')
    // inline code lives inside the rendered markdown output (the header also has
    // a <code> subject chip, so match by text)
    const codes = Array.from(container.querySelectorAll('code')).map((el) => el.textContent)
    expect(codes).toContain('channel/web/web_auth.go')
    expect(container.querySelectorAll('li')).toHaveLength(2)
    // raw markdown markers must NOT leak as text
    expect(container.textContent).not.toContain('**入口**')
  })

  it('falls back to the unified `detail` field when the payload is absent (legacy rows)', () => {
    const tool = makeTool({
      name: 'user_interrupt',
      status: 'done',
      detail: '## 插话\n\n性能必须好——**然后必须正确**',
    })
    const { container } = renderWithProviders(<ToolRender tool={tool} />)
    expect(container.querySelector('h2')?.textContent).toContain('插话')
    expect(container.querySelector('strong')?.textContent).toBe('然后必须正确')
  })

  it('bg_task keeps the terminal transcript style (raw stdout is not markdown)', () => {
    const tool = makeTool({
      name: 'background_task_result',
      status: 'done',
      toolHints: JSON.stringify({ kind: 'bg_task', task: 'npm run build', status: 'done', output: 'build ok\n3 targets' }),
    })
    const { container } = renderWithProviders(<ToolRender tool={tool} />)
    // the task block is also a <pre> — assert the transcript text is present in one of them
    const pres = Array.from(container.querySelectorAll('pre')).map((el) => el.textContent)
    expect(pres.some((t) => (t ?? '').includes('build ok'))).toBe(true)
  })
})

describe('synthetic card: the event and its continuity must be obvious at a glance', () => {
  it('bg-task card states the event ("finished") and that it came from the background', () => {
    const tool = makeTool({
      name: 'background_task_result',
      status: 'done',
      toolHints: JSON.stringify({
        kind: 'bg_task', task_id: '3f8f492a', task: 'npm run build', status: 'done',
        exit_code: 0, elapsed_ms: 1200, output: 'ok',
      }),
    })
    const { container } = renderWithProviders(<ToolRender tool={tool} />)
    // 事件标题（完成态，而不是一个光秃秃的名词）
    expect(container.textContent).toMatch(/Background task finished|后台任务已完成/)
    // 承接说明：这是"此前转后台、现在结束"的东西
    expect(container.textContent).toMatch(/moved to the background|此前转入了后台运行/)
    // 完成徽标（头像角上的 ✓）——"结束了"一眼可见
    expect(screen.getByTestId('synthetic-done-badge')).toBeInTheDocument()
  })

  it('sub-agent card says a previously dispatched sub-agent finished', () => {
    const tool = makeTool({
      name: 'bg_subagent_completed',
      status: 'done',
      toolHints: JSON.stringify({ kind: 'subagent', role: 'explore', instance: 'mem-1', status: 'done', task: '查入口' }),
    })
    const { container } = renderWithProviders(<ToolRender tool={tool} />)
    expect(container.textContent).toMatch(/Sub-agent finished|子代理已完成/)
    expect(container.textContent).toMatch(/dispatched earlier|此前派发出去的子代理/)
  })

  it('user_interrupt says it is an interjection that did NOT stop the task', () => {
    const tool = makeTool({
      name: 'user_interrupt',
      status: 'done',
      toolHints: JSON.stringify({ kind: 'interrupt', message: '顺便把文档也更新了' }),
    })
    const { container } = renderWithProviders(<ToolRender tool={tool} />)
    expect(container.textContent).toMatch(/User interjection received|收到用户插话/)
    expect(container.textContent).toMatch(/without stopping the task|未打断当前任务/)
    expect(container.textContent).toContain('顺便把文档也更新了')
  })
})

describe('synthetic card 呈现优先级 + 去重（用户插话反馈）', () => {
  it('user_interrupt：不再把「💬 插话」当 subject 重复渲染', () => {
    // label 就是显示名本身 → subject 必须为空，否则 pill/标题出现「插话 💬 插话」
    expect(syntheticSubject(makeTool({ name: 'user_interrupt', label: '💬 插话' }))).toBe('')
    expect(syntheticSubject(makeTool({ name: 'user_interrupt', label: '' }))).toBe('')
    // 有角色信息时仍然正常给 subject
    expect(
      syntheticSubject(
        makeTool({ name: 'bg_subagent_completed', toolHints: JSON.stringify({ kind: 'subagent', role: 'explore', instance: 'mem-1' }) }),
      ),
    ).toBe('explore/mem-1')
    // 去掉 emoji 前缀后的普通 label 仍可用
    expect(syntheticSubject(makeTool({ name: 'background_task_result', label: '💬 build now' }))).toBe('build now')
  })

  it('长命令默认折叠（带展开按钮），输出才是重点（800 字符不折叠）', () => {
    const longCmd = "ssh -o BatchMode=yes ubuntu@1.2.3.4 'pkill -9 -x serve; sleep 5; cd ~/ferrite && git fetch && git reset --hard origin/main && cd kernels/cuda && bash build.sh 103a 2>&1 | tail -1 && cd ~/ferrite && cargo build --release 2>&1 | tail -1 && ./target/release/ferrite-serve --serve --tp 8'"
    const tool = makeTool({
      name: 'background_task_result',
      status: 'done',
      toolHints: JSON.stringify({
        kind: 'bg_task', task_id: '5c4e1bfe', task: longCmd, status: 'done', exit_code: 0, elapsed_ms: 166200,
        output: 'x'.repeat(800),
      }),
    })
    renderWithProviders(<ToolRender tool={tool} />)

    // 命令：折叠 + 有展开按钮
    const cmd = screen.getByTestId('synthetic-command')
    expect(cmd.className).toContain('max-h-[3.75rem]')
    expect(screen.getByTestId('synthetic-command-toggle')).toBeInTheDocument()
    // 展开后命令不再被限高
    fireEvent.click(screen.getByTestId('synthetic-command-toggle'))
    expect(screen.getByTestId('synthetic-command').className).not.toContain('max-h-[3.75rem]')

    // 输出：800 字符属于重点内容，不应被折叠（旧阈值 600 会折叠它）
    expect(screen.queryByTestId('synthetic-output-toggle')).toBeNull()
  })

  it('超长输出（>1500 字符）才折叠，且给出行数统计', () => {
    const tool = makeTool({
      name: 'background_task_result',
      status: 'done',
      toolHints: JSON.stringify({
        kind: 'bg_task', task_id: 'x1', task: 'make', status: 'done', output: 'y'.repeat(2000) + '\nline2',
      }),
    })
    renderWithProviders(<ToolRender tool={tool} />)
    expect(screen.getByTestId('synthetic-output-toggle')).toBeInTheDocument()
  })
})
