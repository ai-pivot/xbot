/**
 * DiffView — GitHub-style unified diff renderer.
 *
 * Parses a unified diff (the ```diff block carried in ToolHints for
 * FileCreate/FileReplace) and renders it with:
 *   - file headers (--- a/… / +++ b/…) collapsed into a single path line
 *   - hunk headers (@@ -l,c +l,c @@) as accent badges
 *   - +/-/context lines with red/green backgrounds and old/new line numbers
 *
 * Pure/deterministic parsing — exported for unit tests.
 */
import { memo, useMemo } from 'react'

/** One parsed diff line. */
export interface DiffLine {
  kind: 'add' | 'del' | 'ctx' | 'hunk' | 'meta' | 'plain'
  /** Line content WITHOUT the leading +/-/space marker. */
  text: string
  /** 1-based old-file line number (del/ctx lines). */
  oldNum?: number
  /** 1-based new-file line number (add/ctx lines). */
  newNum?: number
}

interface DiffFile {
  /** Path shown in the header (from ---/+++ lines). */
  path: string
  lines: DiffLine[]
  adds: number
  dels: number
}

/** Extract the diff source from a ToolHints markdown string.
 *  ToolHints format: "```diff\n<diff>\n```" (see engine_run_tools.go).
 *  Non-fenced input is treated as raw diff text. */
export function extractDiffSource(hints: string): string {
  const fence = /```diff\n([\s\S]*?)```/.exec(hints)
  if (fence?.[1]) return fence[1]
  return hints
}

/** Parse a unified diff into per-file groups. */
export function parseUnifiedDiff(diff: string): DiffFile[] {
  const files: DiffFile[] = []
  let current: DiffFile | null = null
  let oldLine = 0
  let newLine = 0

  const pushLine = (line: DiffLine) => {
    if (!current) {
      current = { path: '', lines: [], adds: 0, dels: 0 }
      files.push(current)
    }
    current.lines.push(line)
    if (line.kind === 'add') current.adds++
    if (line.kind === 'del') current.dels++
  }

  for (const raw of diff.split('\n')) {
    if (raw.startsWith('+++ ')) {
      // Start of a new file section — "+++ b/path" (b/ prefix from our diff labels).
      const p = raw.slice(4).replace(/^b\//, '').trim()
      current = { path: p, lines: [], adds: 0, dels: 0 }
      files.push(current)
      continue
    }
    if (raw.startsWith('--- ') || raw.startsWith('diff ') || raw.startsWith('index ')) {
      // "--- a/path" and git metadata — path is taken from the +++ line.
      continue
    }
    if (raw.startsWith('@@')) {
      const m = /@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(raw)
      if (m) {
        oldLine = parseInt(m[1], 10)
        newLine = parseInt(m[2], 10)
      }
      pushLine({ kind: 'hunk', text: raw })
      continue
    }
    if (raw.startsWith('+')) {
      pushLine({ kind: 'add', text: raw.slice(1), newNum: newLine++ })
    } else if (raw.startsWith('-')) {
      pushLine({ kind: 'del', text: raw.slice(1), oldNum: oldLine++ })
    } else if (raw.startsWith(' ') || raw === '') {
      if (raw === '' && !current) continue // leading blank lines before the diff body
      pushLine({ kind: 'ctx', text: raw.slice(1), oldNum: oldLine++, newNum: newLine++ })
    } else {
      pushLine({ kind: 'plain', text: raw })
    }
  }
  return files.filter((f) => f.lines.length > 0)
}

const ADD_TEXT = 'text-green-700 dark:text-green-400'
const ADD_BG = 'bg-green-500/10'
const DEL_TEXT = 'text-red-700 dark:text-red-400'
const DEL_BG = 'bg-red-500/10'

function LineNumbers({ line }: { line: DiffLine }) {
  return (
    <>
      <span className="w-9 shrink-0 select-none pr-1.5 text-right font-mono text-[10px] leading-5 text-text-muted/70">
        {line.oldNum ?? ''}
      </span>
      <span className="w-9 shrink-0 select-none pr-1.5 text-right font-mono text-[10px] leading-5 text-text-muted/70">
        {line.newNum ?? ''}
      </span>
    </>
  )
}

function DiffLineRow({ line }: { line: DiffLine }) {
  if (line.kind === 'hunk') {
    return (
      <div className="flex items-center gap-2 bg-accent/10 px-2 py-0.5 font-mono text-[11px] text-accent">
        <span className="shrink-0">{line.text.slice(0, line.text.indexOf('@@', 2) + 2)}</span>
        {line.text.slice(line.text.indexOf('@@', 2) + 2).trim() && (
          <span className="truncate text-text-muted">{line.text.slice(line.text.indexOf('@@', 2) + 2).trim()}</span>
        )}
      </div>
    )
  }
  const marker =
    line.kind === 'add' ? '+' : line.kind === 'del' ? '-' : ' '
  const contentCls =
    line.kind === 'add' ? ADD_TEXT : line.kind === 'del' ? DEL_TEXT : 'text-text-secondary'
  const bgCls = line.kind === 'add' ? ADD_BG : line.kind === 'del' ? DEL_BG : ''
  return (
    <div className={`flex ${bgCls}`}>
      <LineNumbers line={line} />
      <span className={`w-3 shrink-0 select-none font-mono text-[12px] leading-5 ${contentCls}`}>{marker}</span>
      {/* No overflow/min-w-0 here: the line must NOT scroll on its own. Long
       *  lines stay unwrapped (whitespace-pre) and the flex item's default
       *  min-width:auto keeps the row at content width — the WHOLE diff card
       *  (overflow-auto on the root) scrolls horizontally as one unit. */}
      <pre className={`whitespace-pre font-mono text-[12px] leading-5 ${contentCls}`}>{line.text || ' '}</pre>
    </div>
  )
}

interface DiffViewProps {
  /** Raw unified diff text (NOT the full markdown hints — use extractDiffSource). */
  diff: string
  /** Max rendered body height before scrolling. */
  maxHeight?: number
}

export const DiffView = memo(function DiffView({ diff, maxHeight = 320 }: DiffViewProps) {
  const files = useMemo(() => parseUnifiedDiff(diff), [diff])
  if (files.length === 0) return null
  const totalAdds = files.reduce((s, f) => s + f.adds, 0)
  const totalDels = files.reduce((s, f) => s + f.dels, 0)

  return (
    <div
      className="overflow-auto rounded-md"
      style={{ border: '1px solid var(--border)', maxHeight }}
    >
      {files.map((f, fi) => (
        <div key={fi} className="min-w-max">
          {/* File header */}
          <div
            className="sticky top-0 z-[1] flex items-center gap-2 px-2 py-1"
            style={{ backgroundColor: 'var(--bg-secondary)', borderBottom: '1px solid var(--border)' }}
          >
            <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-text-primary">{f.path}</span>
            <span className={`shrink-0 font-mono text-[10px] ${ADD_TEXT}`}>+{f.adds}</span>
            <span className={`shrink-0 font-mono text-[10px] ${DEL_TEXT}`}>−{f.dels}</span>
          </div>
          {f.lines.map((line, li) => (
            <DiffLineRow key={li} line={line} />
          ))}
        </div>
      ))}
      {files.length > 1 && (
        <div
          className="sticky bottom-0 flex items-center gap-2 px-2 py-0.5 font-mono text-[10px]"
          style={{ backgroundColor: 'var(--bg-secondary)', borderTop: '1px solid var(--border)' }}
        >
          <span className="text-text-muted">{files.length} files</span>
          <span className={ADD_TEXT}>+{totalAdds}</span>
          <span className={DEL_TEXT}>−{totalDels}</span>
        </div>
      )}
    </div>
  )
})
