/**
 * FileExplorer — file browser for the agent's working directory.
 *
 * Renders the file tree from useFileTree (lazy loading: only top-level
 * is fetched initially, subdirectories loaded on expand). Click → openTab
 * in the shared workspace.
 *
 * Includes a path bar at the top to navigate to any directory on the server.
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { ChevronRight, ChevronDown, FolderOpen, FolderUp, Loader2, RotateCcw, Search, X } from 'lucide-react'

import { useI18n } from '@/providers/i18n'
import { useCwd } from '@/providers/CwdProvider'
import { useFileTree } from '@/hooks/useFileTree'
import { insertIntoChat } from '@/lib/chatInputBridge'
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
  const { tree, flatFiles, loading, error, expandDir, expandingPath } = useFileTree(browseRoot)
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set())
  // Inline file-name filter (toggled from the path bar search icon).
  const [searchMode, setSearchMode] = useState(false)
  const [searchQuery, setSearchQuery] = useState('')
  const searchInputRef = useRef<HTMLInputElement>(null)

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
  const canGoUp = parentPath(displayPath) !== displayPath

  // Temporarily browse the parent directory (display-only — does NOT change
  // the session CWD). browseRoot is separate from the session cwd.
  const handleGoUp = useCallback(() => {
    const parent = parentPath(displayPath)
    if (parent === displayPath) return // already at filesystem root
    invalidateFsCache()
    setBrowseRoot(parent)
  }, [displayPath])

  // Filter the flattened file tree by name/path (case-insensitive substring).
  const filteredFiles = useMemo(() => {
    const q = searchQuery.trim().toLowerCase()
    if (!q) return []
    return flatFiles.filter(
      (f) => f.name.toLowerCase().includes(q) || f.path.toLowerCase().includes(q),
    )
  }, [searchQuery, flatFiles])

  const exitSearch = useCallback(() => {
    setSearchQuery('')
    setSearchMode(false)
  }, [])

  return (
    <div className="flex h-full flex-col">
      {/* Path bar */}
      <PathBar
        path={displayPath}
        onNavigate={handleNavigate}
        onReset={browseRoot !== null ? handleReset : undefined}
        onToggleSearch={() => setSearchMode((v) => !v)}
        searchActive={searchMode}
      />

      {/* Inline file-name filter */}
      {searchMode && (
        <div className="relative shrink-0 border-b px-2 py-1.5" style={{ borderColor: 'var(--border)' }}>
          <Search className="pointer-events-none absolute left-4 top-1/2 size-3 -translate-y-1/2 text-text-muted" />
          <input
            ref={searchInputRef}
            value={searchQuery}
            spellCheck={false}
            autoComplete="off"
            autoFocus
            placeholder={t('sidebar.filterPlaceholder')}
            onChange={(e) => setSearchQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Escape') exitSearch()
            }}
            className="min-w-0 w-full bg-transparent pl-6 pr-6 text-xs outline-none"
            style={{ color: 'var(--text-primary)' }}
          />
          {searchQuery && (
            <button
              type="button"
              aria-label={t('common.close')}
              onClick={() => setSearchQuery('')}
              className="absolute right-3 top-1/2 -translate-y-1/2 text-text-muted hover:text-text-primary"
            >
              <X className="size-3" />
            </button>
          )}
        </div>
      )}

      {/* File tree / filtered results */}
      {searchMode && searchQuery.trim() ? (
        <ScrollArea className="min-h-0 flex-1">
          <ul className="py-1 text-sm">
            {filteredFiles.map((node) => (
              <li key={node.path}>
                <button
                  type="button"
                  onClick={() => openFile(node)}
                  className="flex w-full flex-col items-start gap-0.5 px-3 py-1.5 text-left transition-colors hover:bg-bg-tertiary"
                >
                  <span className="flex items-center gap-1.5">
                    <FileNodeIcon node={node} className="size-3.5 shrink-0 text-text-secondary" />
                    <span className="truncate text-text-primary">{node.name}</span>
                  </span>
                  <span className="truncate pl-5 text-[11px] text-text-muted">{node.path}</span>
                </button>
              </li>
            ))}
            {filteredFiles.length === 0 && (
              <div className="px-3 py-6 text-center text-xs text-text-muted">{t('sidebar.noResults')}</div>
            )}
          </ul>
        </ScrollArea>
      ) : loading && tree.length === 0 ? (
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
            {canGoUp && (
              <button
                type="button"
                onClick={handleGoUp}
                title={t('sidebar.goUp')}
                className="flex w-full items-center gap-1 py-[3px] pr-2 text-left transition-colors hover:bg-bg-tertiary"
                style={{ paddingLeft: 4 }}
              >
                <span className="flex size-4 shrink-0 items-center justify-center text-text-muted">
                  <ChevronRight className="size-3.5" />
                </span>
                <FolderUp className="size-4 shrink-0 text-text-secondary" />
                <span className="truncate text-text-secondary">..</span>
              </button>
            )}
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
  onToggleSearch?: (() => void) | undefined
  searchActive?: boolean
}

function PathBar({ path, onNavigate, onReset, onToggleSearch, searchActive }: PathBarProps) {
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
      {onToggleSearch && (
        <button
          type="button"
          aria-label={t('sidebar.search')}
          title={t('sidebar.search')}
          aria-pressed={searchActive}
          onClick={onToggleSearch}
          className="flex size-5 shrink-0 items-center justify-center rounded transition-colors hover:bg-bg-tertiary"
          style={{ color: searchActive ? 'var(--accent)' : 'var(--text-secondary)' }}
        >
          <Search className="size-3" />
        </button>
      )}
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
          <ContextMenuItem onSelect={() => insertIntoChat(`@${node.path}`)}>
            {t('sidebar.addToChat')}
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
