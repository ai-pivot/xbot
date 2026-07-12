/**
 * FileExplorer — file browser for the agent's working directory.
 *
 * Renders the file tree from useFileTree (lazy loading: only top-level
 * is fetched initially, subdirectories loaded on expand). Click → openTab
 * in the shared workspace.
 *
 * Includes a path bar at the top to navigate to any directory on the server.
 */
import { useCallback, useEffect, useState } from 'react'
import { ChevronRight, ChevronDown, FolderOpen, Loader2, RotateCcw } from 'lucide-react'

import { useI18n } from '@/providers/i18n'
import { useCwd } from '@/providers/CwdProvider'
import { useFileTree } from '@/hooks/useFileTree'
import { ScrollArea } from '@/components/ui/scroll-area'
import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuTrigger,
} from '@/components/ui/context-menu'
import { toast } from 'sonner'
import { statFile, parentPath, invalidateFsCache } from '@/hooks/useFileSystem'
import type { TabManager } from '@/hooks/useTabManager'
import type { FileNode } from '@/types/file'
import { FileNodeIcon } from './FileNodeIcon'

interface FileExplorerProps {
  tabManager: TabManager
}

export function FileExplorer({ tabManager }: FileExplorerProps) {
  const { t } = useI18n()
  const { cwd } = useCwd()
  // browseRoot: null means "follow session CWD". Non-null means user navigated
  // to a custom path via the path bar.
  const [browseRoot, setBrowseRoot] = useState<string | null>(null)
  const { tree, loading, error, expandDir, expandingPath } = useFileTree(browseRoot)
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set())

  // Reset expanded set when browse root changes
  useEffect(() => {
    setExpanded(new Set())
  }, [browseRoot])

  const toggle = useCallback(async (path: string, hasChildren: boolean) => {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(path)) {
        next.delete(path)
      } else {
        next.add(path)
      }
      return next
    })
    if (!hasChildren) {
      await expandDir(path)
    }
  }, [expandDir])

  const openFile = useCallback(
    (node: FileNode) => {
      tabManager.openTab({
        type: 'file',
        title: node.name,
        icon: 'file',
        closable: true,
        data: { filePath: node.path, language: node.language },
      })
    },
    [tabManager],
  )

  const handleNavigate = useCallback(async (path: string) => {
    const trimmed = path.trim()
    if (!trimmed) return
    try {
      const stat = await statFile(trimmed)
      if (stat.isDir) {
        // Directory: switch browse root
        invalidateFsCache()
        setBrowseRoot(trimmed)
      } else {
        // File: open it in a tab + switch browse root to its parent dir
        const dir = parentPath(trimmed)
        invalidateFsCache()
        setBrowseRoot(dir)
        tabManager.openTab({
          type: 'file',
          title: stat.name,
          icon: 'file',
          closable: true,
          data: { filePath: trimmed },
        })
      }
    } catch {
      toast.error(t('sidebar.pathNotFound'))
    }
  }, [t, tabManager])

  const handleReset = useCallback(() => {
    invalidateFsCache()
    setBrowseRoot(null)
  }, [])

  const displayPath = browseRoot ?? cwd ?? ''

  return (
    <div className="flex h-full flex-col">
      {/* Path bar */}
      <PathBar
        path={displayPath}
        onNavigate={handleNavigate}
        onReset={browseRoot !== null ? handleReset : undefined}
      />

      {/* File tree */}
      {loading && tree.length === 0 ? (
        <div className="flex flex-1 items-center justify-center gap-2 text-text-secondary">
          <Loader2 className="size-4 animate-spin" />
          <span className="text-sm">{t('sidebar.loadingFiles')}</span>
        </div>
      ) : error ? (
        <div className="flex flex-1 flex-col items-center justify-center gap-2 px-4 text-center">
          <p className="text-sm text-text-secondary">{t('sidebar.loadFailed')}</p>
          <p className="text-xs text-text-muted">{error}</p>
        </div>
      ) : (
        <ScrollArea className="min-h-0 flex-1">
          <div className="py-1 text-sm">
            {tree.map((node) => (
              <FileTreeNode
                key={node.path}
                node={node}
                depth={0}
                expanded={expanded}
                onToggleDir={(path, hasChildren) => void toggle(path, hasChildren)}
                onOpenFile={openFile}
                expandingPath={expandingPath}
              />
            ))}
            {tree.length === 0 && !loading && (
              <div className="px-3 py-6 text-center text-xs text-text-muted">{t('sidebar.empty')}</div>
            )}
          </div>
        </ScrollArea>
      )}
    </div>
  )
}

// ── PathBar ────────────────────────────────────────────────────────────

interface PathBarProps {
  path: string
  onNavigate: (path: string) => void
  onReset?: (() => void) | undefined
}

function PathBar({ path, onNavigate, onReset }: PathBarProps) {
  const { t } = useI18n()
  const [value, setValue] = useState(path)

  // Sync the input when the display path changes externally (e.g. session switch)
  useEffect(() => {
    setValue(path)
  }, [path])

  return (
    <div
      className="flex h-8 shrink-0 items-center gap-1 border-b px-2"
      style={{ borderColor: 'var(--border)' }}
    >
      <input
        value={value}
        spellCheck={false}
        autoComplete="off"
        placeholder={t('sidebar.pathBarPlaceholder')}
        onChange={(e) => setValue(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter') {
            e.preventDefault()
            onNavigate(value)
          }
        }}
        className="min-w-0 flex-1 bg-transparent text-xs font-mono outline-none"
        style={{ color: 'var(--text-primary)' }}
      />
      {onReset && (
        <button
          type="button"
          aria-label={t('sidebar.pathBarReset')}
          title={t('sidebar.pathBarReset')}
          onClick={onReset}
          className="flex size-5 shrink-0 items-center justify-center rounded transition-colors hover:bg-bg-tertiary"
          style={{ color: 'var(--text-secondary)' }}
        >
          <RotateCcw className="size-3" />
        </button>
      )}
    </div>
  )
}

// ── FileTreeNode (unchanged) ───────────────────────────────────────────

interface FileTreeNodeProps {
  node: FileNode
  depth: number
  expanded: Set<string>
  onToggleDir: (path: string, hasChildren: boolean) => void
  onOpenFile: (node: FileNode) => void
  expandingPath: string | null
}

function FileTreeNode({ node, depth, expanded, onToggleDir, onOpenFile, expandingPath }: FileTreeNodeProps) {
  const { t } = useI18n()
  const isOpen = expanded.has(node.path)
  const isDir = node.type === 'directory'
  const isExpanding = expandingPath === node.path

  const row = (
    <button
      type="button"
      onClick={() => (isDir ? onToggleDir(node.path, !!node.children) : onOpenFile(node))}
      className="flex w-full items-center gap-1 py-[3px] pr-2 text-left transition-colors hover:bg-bg-tertiary"
      style={{ paddingLeft: depth * 12 + 4 }}
    >
      {isDir ? (
        <span className="flex size-4 shrink-0 items-center justify-center text-text-muted">
          {isExpanding ? (
            <Loader2 className="size-3.5 animate-spin" />
          ) : isOpen ? (
            <ChevronDown className="size-3.5" />
          ) : (
            <ChevronRight className="size-3.5" />
          )}
        </span>
      ) : (
        <span className="size-4 shrink-0" />
      )}
      {isDir ? (
        <FolderOpen className="size-4 shrink-0 text-text-secondary" />
      ) : (
        <FileNodeIcon node={node} />
      )}
      <span className="truncate text-text-primary">{node.name}</span>
    </button>
  )

  return (
    <div>
      <ContextMenu>
        <ContextMenuTrigger asChild>{row}</ContextMenuTrigger>
        <ContextMenuContent>
          <ContextMenuItem onSelect={() => onOpenFile(node)}>
            {t('sidebar.openInTab')}
          </ContextMenuItem>
          <ContextMenuItem
            onSelect={() => {
              void navigator.clipboard?.writeText(node.path).catch(() => {})
              toast.success(t('sidebar.pathCopied'))
            }}
          >
            {t('sidebar.copyPath')}
          </ContextMenuItem>
        </ContextMenuContent>
      </ContextMenu>

      {isDir && isOpen && node.children && (
        <div>
          {node.children.map((child) => (
            <FileTreeNode
              key={child.path}
              node={child}
              depth={depth + 1}
              expanded={expanded}
              onToggleDir={onToggleDir}
              onOpenFile={onOpenFile}
              expandingPath={expandingPath}
            />
          ))}
        </div>
      )}
    </div>
  )
}
