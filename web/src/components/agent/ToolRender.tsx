/**
 * ToolRender — special rendering for built-in tools.
 *
 * History-persisted tools only carry `name`, `label`, `status`, `summary`
 * (no `args` or `detail` — those are transient during live execution).
 * So we parse `label` (which embeds the command/path) and use `summary`
 * as the output preview.
 *
 * During live streaming, `args` and `detail` ARE populated, so we try
 * those first and fall back to label/summary parsing.
 */
import { memo } from 'react'
import {
  Terminal, FilePlus, FilePen, FileText, Search, FolderSearch,
} from 'lucide-react'
import type { WebToolProgress } from '@/types/shared'
import { ToolCallBlock } from './ToolCallBlock'

interface ToolRenderProps {
  tool: WebToolProgress
}

/** Try to parse the tool's args as JSON. Returns null on failure. */
function parseArgs(tool: WebToolProgress): Record<string, unknown> | null {
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
  // Parse from label: "Shell: actual command"
  const label = tool.label || ''
  const idx = label.indexOf(': ')
  return idx >= 0 ? label.slice(idx + 2) : label
}

/** Extract the path from a FileCreate/FileReplace label: "FileCreate: <path>" */
function filePathFromLabel(tool: WebToolProgress): string {
  const args = parseArgs(tool)
  if (args?.path) return args.path as string
  const label = tool.label || ''
  const idx = label.indexOf(': ')
  return idx >= 0 ? label.slice(idx + 2) : label
}

/** Truncate text to maxLines, appending ellipsis if cut. */
function truncate(text: string, maxLines: number): string {
  const lines = text.split('\n')
  if (lines.length <= maxLines) return text
  return lines.slice(0, maxLines).join('\n') + '\n…'
}

export const ToolRender = memo(function ToolRender({ tool }: ToolRenderProps) {
  const name = tool.name || ''
  const summary = tool.summary || ''
  const detail = tool.detail || ''

  // If we have args (live streaming), use them. Otherwise parse from label.
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
    default:
      return <ToolCallBlock tool={tool} />
  }
})

// ── Shell ──────────────────────────────────────────────────────────────

function ShellRender({ tool, summary, detail }: { tool: WebToolProgress; summary: string; detail: string }) {
  const command = shellCommand(tool)
  const output = detail || summary || ''
  const lines = output.split('\n').filter(Boolean)
  const isShort = lines.length <= 5
  return (
    <div className="flex flex-col gap-1.5 py-1 text-xs">
      {command && (
        <div className="flex items-start gap-2">
          <Terminal className="tool-icon mt-0.5" />
          <code className="font-mono text-text-primary">{command}</code>
        </div>
      )}
      {output && !isShort && (
        <div>
          <pre className="max-h-60 overflow-auto whitespace-pre-wrap rounded bg-bg-tertiary/60 p-2 font-mono text-[12px] text-text-secondary">
            {truncate(output, 15)}
          </pre>
        </div>
      )}
      {output && isShort && (
        <pre className="whitespace-pre-wrap rounded bg-bg-tertiary/60 p-2 font-mono text-[12px] text-text-secondary">
          {output}
        </pre>
      )}
      {!command && !output && (
        <div className="text-text-muted">—</div>
      )}
    </div>
  )
}

// ── FileCreate ─────────────────────────────────────────────────────────

function FileCreateRender({ tool, summary }: { tool: WebToolProgress; summary: string }) {
  const path = filePathFromLabel(tool)
  return (
    <div className="py-1 text-xs">
      {path && (
        <div className="flex items-center gap-1.5">
          <FilePlus className="tool-icon" />
          <code className="font-mono text-text-primary">{path}</code>
        </div>
      )}
      {summary && <div className="mt-1 text-text-muted">{summary}</div>}
    </div>
  )
}

// ── FileReplace ───────────────────────────────────────────────────────

function FileReplaceRender({ tool, summary }: { tool: WebToolProgress; summary: string }) {
  const path = filePathFromLabel(tool)
  const args = parseArgs(tool)
  const oldStr = (args?.old_string as string) || ''
  const newStr = (args?.new_string as string) || ''
  return (
    <div className="flex flex-col gap-1.5 py-1 text-xs">
      {path && (
        <div className="flex items-center gap-1.5">
          <FilePen className="tool-icon" />
          <code className="font-mono text-text-primary">{path}</code>
        </div>
      )}
      {oldStr && newStr && (
        <div className="rounded bg-bg-tertiary/60 p-2 font-mono text-[12px]">
          <div style={{ color: 'var(--status-error)' }}>- {truncate(oldStr, 3)}</div>
          <div style={{ color: 'var(--status-running)' }}>+ {truncate(newStr, 3)}</div>
        </div>
      )}
      {!oldStr && !newStr && summary && (
        <div className="text-text-muted">{summary}</div>
      )}
      {!path && <div className="text-text-muted">—</div>}
    </div>
  )
}

// ── Read ───────────────────────────────────────────────────────────────

function ReadRender({ tool, summary, detail }: { tool: WebToolProgress; summary: string; detail: string }) {
  const path = filePathFromLabel(tool)
  const content = detail || summary || ''
  const lines = content.split('\n').filter(Boolean)
  return (
    <div className="flex flex-col gap-1 py-1 text-xs">
      {path && (
        <div className="flex items-center gap-1.5">
          <FileText className="tool-icon" />
          <code className="font-mono text-text-primary">{path}</code>
          {lines.length > 0 && <span className="text-text-muted">({lines.length} lines)</span>}
        </div>
      )}
      {lines.length > 0 && lines.length <= 5 && (
        <pre className="whitespace-pre-wrap rounded bg-bg-tertiary/60 p-2 font-mono text-[12px] text-text-secondary">
          {truncate(content, 5)}
        </pre>
      )}
      {lines.length > 5 && (
        <div className="text-text-muted">{lines.length} lines read</div>
      )}
      {!path && !content && <div className="text-text-muted">—</div>}
    </div>
  )
}

// ── Grep ──────────────────────────────────────────────────────────────

function GrepRender({ tool, summary, detail }: { tool: WebToolProgress; summary: string; detail: string }) {
  const label = tool.label || ''
  // label format: "Grep: <pattern>" or "Grep: <pattern> in <path>"
  const labelContent = label.includes(': ') ? label.slice(label.indexOf(': ') + 2) : ''
  const output = detail || summary || ''
  const matches = output.split('\n').filter(Boolean)
  return (
    <div className="flex flex-col gap-1 py-1 text-xs">
      <div className="flex items-center gap-1.5">
        <Search className="tool-icon" />
        {labelContent && <code className="font-mono text-text-primary">{labelContent}</code>}
        {matches.length > 0 && <span className="text-text-muted">({matches.length} matches)</span>}
      </div>
      {matches.length > 0 && matches.length <= 5 && (
        <pre className="whitespace-pre-wrap rounded bg-bg-tertiary/60 p-2 font-mono text-[12px] text-text-secondary">
          {truncate(output, 5)}
        </pre>
      )}
      {matches.length > 5 && <div className="text-text-muted">{matches.length} matches found</div>}
      {!output && <div className="text-text-muted">No matches</div>}
    </div>
  )
}

// ── Glob ──────────────────────────────────────────────────────────────

function GlobRender({ tool, summary }: { tool: WebToolProgress; summary: string }) {
  const label = tool.label || ''
  const pattern = label.includes(': ') ? label.slice(label.indexOf(': ') + 2) : ''
  const files = summary.split('\n').filter(Boolean)
  return (
    <div className="flex flex-col gap-1 py-1 text-xs">
      <div className="flex items-center gap-1.5">
        <FolderSearch className="tool-icon" />
        {pattern && <code className="font-mono text-text-primary">{pattern}</code>}
        {files.length > 0 && <span className="text-text-muted">({files.length} files)</span>}
      </div>
      {files.length > 0 && files.length <= 8 && (
        <div className="flex flex-col gap-0.5">
          {files.map((f, i) => (
            <code key={i} className="font-mono text-[12px] text-text-secondary">{f}</code>
          ))}
        </div>
      )}
      {files.length > 8 && <div className="text-text-muted">{files.length} files matched</div>}
      {files.length === 0 && <div className="text-text-muted">No files matched</div>}
    </div>
  )
}
