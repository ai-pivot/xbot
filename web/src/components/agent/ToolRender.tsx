/**
 * ToolRender — fancy rendering for built-in tools.
 *
 * Each renderer parses the structured information the backend already embeds
 * in `label` / `args` / `summary` / `detail` / `toolHints` and presents it in a
 * purpose-built view instead of a raw <pre>:
 *
 *   Shell        → terminal card (traffic-light header, $ command, EXIT badge)
 *   Read         → CodeView (line-number gutter + highlight.js by extension)
 *   FileCreate   → DiffView (unified diff from toolHints metadata)
 *   FileReplace  → DiffView (+/- fallback from live args)
 *   Grep         → per-file groups with line-number badges + pattern highlight
 *   Glob         → file list (dir muted / basename accent, extension badges)
 *   TodoWrite    → checklist with progress bar
 *
 * History-persisted tools carry `name`, `label`, `status`, `summary`,
 * `toolHints` (no `args` — transient during live execution), so history
 * renderers parse label+summary; live renderers prefer args/detail.
 */
import { memo, useMemo, type ReactNode } from 'react'
import {
  FileText, ChevronRight, CheckCircle2, Circle, Loader2,
} from 'lucide-react'
import type { WebToolProgress } from '@/types/shared'
import { ToolCallBlock } from './ToolCallBlock'
import { DiffView, extractDiffSource } from './DiffView'
import { CodeView } from './CodeView'
import { AnsiText } from './AnsiText'
import { useOptionalPluginRuntime } from '@/plugin-runtime'
import { GenUIPanel } from './GenUIPanel'

interface ToolRenderProps {
  tool: WebToolProgress
  /** 隐藏 fallback ToolCallBlock 的 args 块（统一参数渲染：浮窗已自行渲染 ArgsView）。 */
  hideArgs?: boolean
}

/** Try to parse the tool's args as JSON. Returns null on failure. */
export function parseArgs(tool: WebToolProgress): Record<string, unknown> | null {
  if (!tool.args) return null
  try {
    return JSON.parse(tool.args)
  } catch {
    return null
  }
}

/** Extract the command from a Shell label: "Shell: <command>" */
function shellCommand(tool: WebToolProgress): string {
  const args = parseArgs(tool)
  if (args?.command) return args.command as string
  const label = tool.label || ''
  if (label.startsWith('Shell: ')) return label.slice(7)
  return label
}

/** Extract the path from tool labels: "Read: <path>" etc. */
function filePathFromLabel(tool: WebToolProgress): string {
  const args = parseArgs(tool)
  if (args?.path) return args.path as string
  const label = tool.label || ''
  const prefixes = ['FileCreate: ', 'FileReplace: ', 'Read: ', 'Grep: ', 'Glob: ']
  for (const prefix of prefixes) {
    if (label.startsWith(prefix)) return label.slice(prefix.length)
  }
  return label
}

/** Extension → hljs language id (only languages registered in highlight.ts). */
const EXT_LANG: Record<string, string> = {
  ts: 'typescript', tsx: 'typescript', mts: 'typescript',
  js: 'javascript', jsx: 'javascript', mjs: 'javascript', cjs: 'javascript',
  go: 'go', py: 'python', rb: 'ruby', rs: 'rust', java: 'java', kt: 'kotlin',
  swift: 'swift', c: 'c', h: 'c', cpp: 'cpp', cc: 'cpp', cxx: 'cpp', hpp: 'cpp',
  cs: 'csharp', sh: 'bash', bash: 'bash', zsh: 'bash',
  json: 'json', yaml: 'yaml', yml: 'yaml', toml: 'ini', ini: 'ini',
  md: 'markdown', markdown: 'markdown', html: 'xml', xml: 'xml', svg: 'xml',
  css: 'css', sql: 'sql', php: 'php', pl: 'perl', lua: 'lua',
}

/** Map a file path to an hljs language id via its extension. */
export function langFromPath(path: string): string | undefined {
  const base = path.split('/').pop() || ''
  if (/^dockerfile/i.test(base)) return 'dockerfile'
  if (/^makefile/i.test(base)) return 'bash'
  const ext = base.includes('.') ? base.split('.').pop()!.toLowerCase() : ''
  return EXT_LANG[ext]
}

/** Truncate text to maxLines, appending ellipsis if cut. */
function truncate(text: string, maxLines: number): string {
  const lines = text.split('\n')
  if (lines.length <= maxLines) return text
  return lines.slice(0, maxLines).join('\n') + '\n…'
}

// ─── shared bits ────────────────────────────────────────────────────────

/** Small rounded count badge (e.g. "×12", "+3"). */
function Badge({ children, tone = 'muted' }: { children: ReactNode; tone?: 'muted' | 'green' | 'red' | 'accent' }) {
  const cls =
    tone === 'green' ? 'text-green-700 dark:text-green-400 bg-green-500/10'
    : tone === 'red' ? 'text-red-700 dark:text-red-400 bg-red-500/10'
    : tone === 'accent' ? 'text-accent bg-accent/10'
    : 'text-text-muted bg-bg-tertiary/60'
  return <span className={`shrink-0 rounded px-1.5 py-px font-mono text-[10px] leading-4 ${cls}`}>{children}</span>
}

/** Elapsed time in human form (ms → s). */
function elapsedBadge(ms: number): string | null {
  if (!ms || ms < 100) return null
  return ms < 10_000 ? `${Math.round(ms)}ms` : `${(ms / 1000).toFixed(1)}s`
}

export const ToolRender = memo(function ToolRender({ tool, hideArgs = false }: ToolRenderProps) {
  const runtime = useOptionalPluginRuntime()
  const name = tool.name || ''
  const summary = tool.summary || ''
  const detail = tool.detail || ''

  switch (name) {
    case 'Shell':
      return <ShellRender tool={tool} summary={summary} detail={detail} />
    case 'FileCreate':
      return <FileCreateRender tool={tool} summary={summary} />
    case 'FileReplace':
      return <FileReplaceRender tool={tool} summary={summary} />
    case 'Read':
      return <ReadRender tool={tool} summary={summary} detail={detail} />
    case 'Grep':
      return <GrepRender tool={tool} summary={summary} detail={detail} />
    case 'Glob':
      return <GlobRender tool={tool} summary={summary} />
    case 'TodoWrite':
      return <TodoWriteRender tool={tool} summary={summary} />
    default: {
      // messageRenderer 调度器：内置 GenUI renderer（matches uiMode='genui'）
      // 或插件声明的渲染器决定此工具的渲染，替代宿主硬编码的 display_html 特判。
      // 无匹配（或渲染器返回 null）→ 默认 ToolCallBlock。
      const rendered = runtime?.renderTool(tool, { chatID: '' }) ?? null
      if (rendered != null) {
        // surface=panel：顶层面板（fancy 标题栏 + 折叠 + 全屏），在工具被调用的
        // iteration 位置渲染，默认展开、不自动折叠。
        if (tool.surface?.kind === 'panel') {
          return (
            <GenUIPanel
              title={tool.surface.title || tool.summary}
              collapsible={tool.surface.collapsible ?? true}
              fullscreen={tool.surface.fullscreen ?? true}
              defaultOpen={tool.surface.defaultOpen ?? true}
            >
              {rendered}
            </GenUIPanel>
          )
        }
        return rendered
      }
      return <ToolCallBlock tool={tool} hideArgs={hideArgs} />
    }
  }
})

// ── Shell ──────────────────────────────────────────────────────────────

interface ShellParsed {
  command: string
  output: string
  exitCode: number | null
  timeout: boolean
  bgTask: string | null
}

/** Parse Shell summary/detail: "[EXIT N] cmd\n…", "[TIMEOUT after Xs] …",
 *  "Background task running: bg:xxx". */
export function parseShell(tool: WebToolProgress, summary: string, detail: string): ShellParsed {
  const command = shellCommand(tool)
  let text = detail || summary || ''
  let exitCode: number | null = null
  let timeout = false
  let bgTask: string | null = null

  const exitM = /^\[EXIT (-?\d+)\] /.exec(text)
  if (exitM) {
    exitCode = parseInt(exitM[1], 10)
    text = text.slice(exitM[0].length)
    // The command follows on the same line: "[EXIT 1] cmd\noutput…"
    const nl = text.indexOf('\n')
    if (nl >= 0 && text.slice(0, nl).trim() === command.trim()) text = text.slice(nl + 1)
  }
  if (text.startsWith('[TIMEOUT after ')) {
    timeout = true
    const nl = text.indexOf('\n')
    text = nl >= 0 ? text.slice(nl + 1) : ''
  }
  const bgM = /Background task running: (bg:[A-Za-z0-9-]+)/.exec(text)
  if (bgM) bgTask = bgM[1]

  return { command, output: text.trimEnd(), exitCode, timeout, bgTask }
}

function ShellRender({ tool, summary, detail }: { tool: WebToolProgress; summary: string; detail: string }) {
  const { command, output, exitCode, timeout, bgTask } = parseShell(tool, summary, detail)
  const elapsed = elapsedBadge(tool.elapsedMs)
  const isError = exitCode != null && exitCode !== 0

  return (
    <div className="flex flex-col gap-1.5 py-1 text-xs">
      {/* Terminal card */}
      <div className="overflow-hidden rounded-md" style={{ border: '1px solid var(--border)' }}>
        {/* Traffic-light header */}
        <div
          className="flex items-center gap-1.5 px-2.5 py-1"
          style={{ backgroundColor: 'var(--bg-secondary)', borderBottom: '1px solid var(--border)' }}
        >
          <span className="h-2 w-2 shrink-0 rounded-full bg-red-400/80" />
          <span className="h-2 w-2 shrink-0 rounded-full bg-yellow-400/80" />
          <span className="h-2 w-2 shrink-0 rounded-full bg-green-400/80" />
          <span className="ml-1.5 shrink-0 font-mono text-[10px] uppercase tracking-wide text-text-muted">bash</span>
          <span className="min-w-0 flex-1 truncate text-right font-mono text-[11px] text-text-muted">{command}</span>
        </div>
        {/* Body: $ command echo + output */}
        <div className="max-h-64 overflow-auto px-2.5 py-1.5" style={{ backgroundColor: 'var(--bg-primary)' }}>
          {command && (
            <div className="flex gap-1.5 font-mono text-[12px] leading-5">
              <span className="shrink-0 select-none text-accent">$</span>
              <pre className="min-w-0 flex-1 whitespace-pre-wrap break-all text-text-primary">{command}</pre>
            </div>
          )}
          {output && (
            <pre className={`mt-1 whitespace-pre-wrap break-all font-mono text-[12px] leading-5 ${isError || timeout ? 'text-red-600 dark:text-red-400' : 'text-text-secondary'}`}>
              <AnsiText text={output} />
            </pre>
          )}
          {!command && !output && <div className="py-0.5 font-mono text-[12px] text-text-muted">—</div>}
        </div>
      </div>
      {/* Status badges */}
      {(exitCode != null || timeout || bgTask || elapsed) && (
        <div className="flex flex-wrap items-center gap-1.5">
          {exitCode != null && <Badge tone={isError ? 'red' : 'green'}>exit {exitCode}</Badge>}
          {timeout && <Badge tone="red">timeout</Badge>}
          {bgTask && <Badge tone="accent">{bgTask}</Badge>}
          {elapsed && <Badge>{elapsed}</Badge>}
        </div>
      )}
    </div>
  )
}

// ── Read ───────────────────────────────────────────────────────────────

interface ReadParsed {
  code: string
  startLine: number
  notice: string | null
  lineCount: number
}

/** Parse Read summary (line-numbered "   N\tcontent" format) into raw code.
 *  Falls back to the raw text when no line numbers are present.
 *
 *  Truncation notices appear in BOTH bracket styles and must be recognized
 *  (otherwise a stray tail line forces the raw fallback, which renders the
 *  embedded N\t numbers next to the gutter — double line numbers):
 *    "... [truncated: showing N of M lines...]" (applyLineLimit, tools/read.go)
 *    "... (truncated)"                          (engine 4000-rune cap, engine_run_tools.go)
 *    "(offset N exceeds file length M — ...)"   (offset beyond EOF)
 */
export function parseRead(summary: string, detail: string): ReadParsed | null {
  const content = detail || summary
  if (!content) return null
  const lines = content.split('\n')
  const numbered = /^\s*(\d+)\t(.*)$/
  const noticeRe = /^\s*(?:\.\.\.?\s*[[(]\s*(truncated|offset)|\(offset)/i
  let startLine = -1
  let notice: string | null = null
  const out: string[] = []
  let sawNumbered = false
  for (const line of lines) {
    if (line === '') continue // blank separator lines (e.g. before a notice)
    if (noticeRe.test(line)) {
      notice = line.trim()
      continue
    }
    const m = numbered.exec(line)
    if (m) {
      if (startLine < 0) startLine = parseInt(m[1], 10)
      sawNumbered = true
      out.push(m[2])
    } else if (sawNumbered) {
      // Stray non-numbered line INSIDE numbered content (an unrecognized
      // truncation marker variant) — keep it verbatim as a code line. Falling
      // back to raw here would leave the N\t prefixes rendered next to the
      // gutter numbers.
      out.push(line)
    } else {
      // First meaningful line is not numbered → raw code, no stripping.
      // Keep an already-captured notice (e.g. offset-exceeds-EOF hint — the
      // whole result IS the notice).
      return { code: content, startLine: 1, notice, lineCount: lines.length }
    }
  }
  if (!sawNumbered) {
    return { code: content, startLine: 1, notice, lineCount: lines.length }
  }
  return { code: out.join('\n'), startLine: startLine < 0 ? 1 : startLine, notice, lineCount: out.length }
}

function ReadRender({ tool, summary, detail }: { tool: WebToolProgress; summary: string; detail: string }) {
  const path = filePathFromLabel(tool)
  const parsed = useMemo(() => parseRead(summary, detail), [summary, detail])
  const lang = useMemo(() => langFromPath(path), [path])

  return (
    <div className="flex flex-col gap-1.5 py-1 text-xs">
      <div className="flex min-w-0 items-center gap-1.5">
        {/* No icon — the ToolCard header above already shows the tool icon. */}
        <code className="min-w-0 flex-1 truncate font-mono text-text-primary">{path}</code>
        {parsed && <Badge tone="accent">{parsed.lineCount} lines</Badge>}
        {lang && <Badge>{lang}</Badge>}
      </div>
      {parsed && (
        <CodeView code={parsed.code} language={lang} startLine={parsed.startLine} notice={parsed.notice ?? undefined} />
      )}
      {!parsed && <div className="text-text-muted">—</div>}
    </div>
  )
}

// ── FileCreate ─────────────────────────────────────────────────────────

function FileCreateRender({ tool, summary }: { tool: WebToolProgress; summary: string }) {
  const path = filePathFromLabel(tool)
  const diff = tool.toolHints ? extractDiffSource(tool.toolHints) : ''
  const args = parseArgs(tool)
  const content = (args?.content as string) || ''
  const lang = useMemo(() => langFromPath(path), [path])

  // With a diff, DiffView's file header (path + authoritative +N/−M) is the
  // ONLY header — the ToolCard chrome above already shows the tool name+icon.
  if (diff) {
    return (
      <div className="py-1">
        <DiffView diff={diff} />
      </div>
    )
  }

  // No diff hint yet (live streaming) or legacy history: path + new-content
  // preview. No icon — ToolCard has it.
  return (
    <div className="flex flex-col gap-1.5 py-1 text-xs">
      <div className="flex min-w-0 items-center gap-1.5">
        <code className="min-w-0 flex-1 truncate font-mono text-text-primary">{path}</code>
        {lang && <Badge>{lang}</Badge>}
      </div>
      {content ? (
        <div className="overflow-hidden rounded-md" style={{ border: '1px solid var(--border)' }}>
          <div className="max-h-64 overflow-auto py-1.5 pl-2 pr-3" style={{ borderLeft: '3px solid var(--status-running)' }}>
            <pre className="whitespace-pre font-mono text-[12px] leading-5 text-green-700 dark:text-green-400">
              {truncate(content, 60)}
            </pre>
          </div>
        </div>
      ) : (
        summary && <div className="text-text-muted">{summary}</div>
      )}
      {!path && !content && !summary && <div className="text-text-muted">—</div>}
    </div>
  )
}

// ── FileReplace ────────────────────────────────────────────────────────

function FileReplaceRender({ tool, summary }: { tool: WebToolProgress; summary: string }) {
  const path = filePathFromLabel(tool)
  const diff = tool.toolHints ? extractDiffSource(tool.toolHints) : ''
  const args = parseArgs(tool)
  const oldStr = (args?.old_string as string) || ''
  const newStr = (args?.new_string as string) || ''

  // With a diff, DiffView's own file header (path + authoritative +N/−M) is
  // the ONLY header — the ToolCard chrome above already shows the tool name
  // and icon. A second header here duplicated the path and showed occurrence
  // counts (+1 −1) that contradicted the real diff stats (+37 −0).
  if (diff) {
    return (
      <div className="py-1">
        <DiffView diff={diff} />
      </div>
    )
  }

  // No diff hint (live streaming before hints arrive, or legacy history):
  // path line + simple -/+ fallback. No icon — ToolCard has it.
  return (
    <div className="flex flex-col gap-1.5 py-1 text-xs">
      {path && <code className="min-w-0 truncate font-mono text-text-primary">{path}</code>}
      {oldStr && newStr ? (
        <div className="overflow-hidden rounded-md" style={{ border: '1px solid var(--border)' }}>
          <pre className="overflow-auto whitespace-pre px-2 py-1 font-mono text-[12px] leading-5 text-red-700 dark:text-red-400 bg-red-500/10">
            {truncate('- ' + oldStr, 8)}
          </pre>
          <pre className="overflow-auto whitespace-pre px-2 py-1 font-mono text-[12px] leading-5 text-green-700 dark:text-green-400 bg-green-500/10">
            {truncate('+ ' + newStr, 8)}
          </pre>
        </div>
      ) : (
        summary && <div className="text-text-muted">{summary}</div>
      )}
      {!path && !oldStr && !summary && <div className="text-text-muted">—</div>}
    </div>
  )
}

// ── Grep ───────────────────────────────────────────────────────────────

interface GrepMatch {
  line: number
  text: string
}
interface GrepFile {
  path: string
  matches: GrepMatch[]
}

/** Parse Grep summary: "## <file>\nN: content…\n(Found N match(es))". */
export function parseGrepResult(summary: string, detail: string): { files: GrepFile[]; total: number } {
  const text = detail || summary
  const files: GrepFile[] = []
  let total = 0
  let current: GrepFile | null = null
  for (const line of text.split('\n')) {
    if (!line) continue
    const head = /^## (.*)$/.exec(line)
    if (head) {
      current = { path: head[1], matches: [] }
      files.push(current)
      continue
    }
    const found = /^\(Found (\d+) match/.exec(line)
    if (found) {
      total = parseInt(found[1], 10)
      continue
    }
    const m = /^(\d+): (.*)$/.exec(line)
    if (m && current) {
      current.matches.push({ line: parseInt(m[1], 10), text: m[2] })
    }
  }
  if (total === 0) total = files.reduce((s, f) => s + f.matches.length, 0)
  return { files, total }
}

/** Extract the pattern from a Grep label: Grep: "pattern" in path / Grep: "pattern". */
function grepPattern(tool: WebToolProgress): string {
  const args = parseArgs(tool)
  if (args?.pattern) return args.pattern as string
  const label = tool.label || ''
  const m = /^Grep: "([^"]*)"/.exec(label)
  return m ? m[1] : ''
}

/** Split a match line into [before, hit, after] segments for highlighting. */
function splitHighlight(text: string, pattern: string, isRegex: boolean): { text: string; hit: boolean }[] {
  if (!pattern) return [{ text, hit: false }]
  try {
    const re = isRegex ? new RegExp(pattern, 'g') : new RegExp(pattern.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'), 'g')
    const segments: { text: string; hit: boolean }[] = []
    let last = 0
    for (const m of text.matchAll(re)) {
      const idx = m.index ?? 0
      if (idx > last) segments.push({ text: text.slice(last, idx), hit: false })
      segments.push({ text: m[0], hit: true })
      last = idx + m[0].length
      if (m[0] === '') break // zero-length match guard
    }
    if (last < text.length) segments.push({ text: text.slice(last), hit: false })
    return segments.length ? segments : [{ text, hit: false }]
  } catch {
    return [{ text, hit: false }]
  }
}

function GrepRender({ tool, summary, detail }: { tool: WebToolProgress; summary: string; detail: string }) {
  const { files, total } = useMemo(() => parseGrepResult(summary, detail), [summary, detail])
  const pattern = grepPattern(tool)
  const isRegex = parseArgs(tool)?.is_regex === true

  return (
    <div className="flex flex-col gap-1.5 py-1 text-xs">
      <div className="flex min-w-0 items-center gap-1.5">
        {/* No icon — the ToolCard header above already shows the tool icon. */}
        <code className="min-w-0 flex-1 truncate font-mono text-text-primary">{pattern}</code>
        {total > 0 && <Badge tone="accent">{total} matches</Badge>}
      </div>
      {files.length === 0 && <div className="text-text-muted">{summary || 'No matches'}</div>}
      {files.map((f, i) => (
        <div key={i} className="overflow-hidden rounded-md" style={{ border: '1px solid var(--border)' }}>
          <div
            className="flex items-center gap-1.5 px-2 py-1"
            style={{ backgroundColor: 'var(--bg-secondary)', borderBottom: '1px solid var(--border)' }}
          >
            <FileText className="h-3 w-3 shrink-0 text-text-muted" />
            <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-text-primary">{f.path}</span>
            <Badge tone="accent">{f.matches.length}</Badge>
          </div>
          <div className="max-h-56 overflow-auto px-2 py-1" style={{ backgroundColor: 'var(--bg-primary)' }}>
            {f.matches.map((m, j) => (
              <div key={j} className="flex items-start gap-2 font-mono text-[11px] leading-5">
                <span className="w-9 shrink-0 select-none text-right text-text-muted">{m.line}</span>
                <span className="min-w-0 flex-1 whitespace-pre-wrap break-all text-text-secondary">
                  {splitHighlight(m.text, pattern, isRegex).map((seg, k) =>
                    seg.hit ? (
                      <mark key={k} className="rounded-sm bg-yellow-300/40 px-px text-text-primary dark:bg-yellow-500/30">{seg.text}</mark>
                    ) : (
                      <span key={k}>{seg.text}</span>
                    ),
                  )}
                </span>
              </div>
            ))}
          </div>
        </div>
      ))}
    </div>
  )
}

// ── Glob ───────────────────────────────────────────────────────────────

/** Parse Glob summary: "Found N matching file(s):\npath\npath…". */
export function parseGlobResult(summary: string): { files: string[]; declaredCount: number | null } {
  const lines = summary.split('\n').filter((l) => l !== '')
  let declaredCount: number | null = null
  const files: string[] = []
  for (const line of lines) {
    const head = /^Found (\d+) matching file/.exec(line)
    if (head) {
      declaredCount = parseInt(head[1], 10)
      continue
    }
    files.push(line)
  }
  return { files, declaredCount }
}

function GlobRender({ tool, summary }: { tool: WebToolProgress; summary: string }) {
  const label = tool.label || ''
  const pattern = label.startsWith('Glob: ')
    ? label.slice(6)
    : ((parseArgs(tool)?.pattern as string) || '')
  const { files } = useMemo(() => parseGlobResult(summary), [summary])

  return (
    <div className="flex flex-col gap-1.5 py-1 text-xs">
      <div className="flex min-w-0 items-center gap-1.5">
        {/* No icon — the ToolCard header above already shows the tool icon. */}
        <code className="min-w-0 flex-1 truncate font-mono text-text-primary">{pattern}</code>
        {files.length > 0 && <Badge tone="accent">{files.length} files</Badge>}
      </div>
      {files.length > 0 && (
        <div className="overflow-hidden rounded-md" style={{ border: '1px solid var(--border)' }}>
          <div className="max-h-56 overflow-auto px-2 py-1" style={{ backgroundColor: 'var(--bg-primary)' }}>
            {files.map((f, i) => {
              const slash = f.lastIndexOf('/')
              const dir = slash >= 0 ? f.slice(0, slash + 1) : ''
              const base = slash >= 0 ? f.slice(slash + 1) : f
              return (
                <div key={i} className="flex items-center gap-1 font-mono text-[11px] leading-5">
                  <FileText className="h-3 w-3 shrink-0 text-text-muted" />
                  {dir && <span className="max-w-[55%] shrink-0 truncate text-text-muted" title={dir}>{dir}</span>}
                  {dir && <ChevronRight className="h-3 w-3 shrink-0 text-text-muted/60" />}
                  <span className="min-w-0 flex-1 truncate text-text-primary" title={f}>{base}</span>
                </div>
              )
            })}
          </div>
        </div>
      )}
      {files.length === 0 && <div className="text-text-muted">{summary || 'No files matched'}</div>}
    </div>
  )
}

// ── TodoWrite ──────────────────────────────────────────────────────────

interface TodoEntry {
  text: string
  status: string // "pending" | "doing" | "done"
}

function TodoWriteRender({ tool, summary }: { tool: WebToolProgress; summary: string }) {
  const args = parseArgs(tool)
  const todos = useMemo<TodoEntry[]>(() => {
    const raw = args?.todos
    if (!Array.isArray(raw)) return []
    return raw.map((t) => {
      const r = t as Record<string, unknown>
      // 新格式（v2）：status 必填。老数据兼容：done: true → status: "done"。
      let status = typeof r.status === 'string' && r.status ? r.status : ''
      if (!status) {
        // 老格式 fallback：done boolean → status
        if (r.done === true) status = 'done'
        else status = 'pending'
      }
      return { text: typeof r.text === 'string' ? r.text : '', status }
    })
  }, [tool.args])

  if (todos.length === 0) {
    return (
      <div className="py-1 text-xs">
        <div className="flex items-center gap-1.5">
          <span className="text-text-muted">{summary || '—'}</span>
        </div>
      </div>
    )
  }

  const done = todos.filter((t) => t.status === 'done').length
  const doing = todos.filter((t) => t.status === 'doing').length
  const pct = Math.round((done / todos.length) * 100)

  return (
    <div className="flex flex-col gap-1.5 py-1 text-xs">
      <div className="flex items-center gap-1.5">
        <span className="text-text-muted">{done}/{todos.length}</span>
        {doing > 0 && <Badge tone="accent">{doing} doing</Badge>}
        <Badge tone={done === todos.length ? 'green' : 'accent'}>{pct}%</Badge>
      </div>
      {/* Progress bar */}
      <div className="h-1 overflow-hidden rounded-full bg-bg-tertiary">
        <div
          className="h-full rounded-full transition-all"
          style={{ width: `${pct}%`, backgroundColor: done === todos.length ? 'var(--status-running)' : 'var(--accent)' }}
        />
      </div>
      <div className="flex flex-col gap-0.5">
        {todos.map((t, i) => (
          <div key={i} className="flex items-start gap-1.5">
            {t.status === 'done'
              ? <CheckCircle2 className="mt-0.5 h-3.5 w-3.5 shrink-0" style={{ color: 'var(--status-running)' }} />
              : t.status === 'doing'
                ? <Loader2 className="mt-0.5 h-3.5 w-3.5 shrink-0 animate-spin" style={{ color: 'var(--accent)' }} />
                : <Circle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-text-muted" />}
            <span className={`min-w-0 flex-1 break-words leading-5 ${
              t.status === 'done'
                ? 'text-text-muted line-through'
                : t.status === 'doing'
                  ? 'font-medium text-text-primary'
                  : 'text-text-primary'
            }`}>
              {t.status === 'doing' && <span className="mr-1 text-[9px] font-bold uppercase" style={{ color: 'var(--accent)' }}>▶</span>}
              {t.text}
            </span>
          </div>
        ))}
      </div>
    </div>
  )
}
