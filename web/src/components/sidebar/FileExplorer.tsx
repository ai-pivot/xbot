/**
 * FileExplorer — file browser for the agent's working directory.
 *
 * Renders the file tree from useFileTree (lazy loading: only top-level
 * is fetched initially, subdirectories loaded on expand). Click → openTab
 * in the shared workspace.
 */
import { useCallback, useState } from 'react'
import { ChevronRight, ChevronDown, FolderOpen, Loader2 } from 'lucide-react'

import { useI18n } from '@/providers/i18n'
import { useFileTree } from '@/hooks/useFileTree'
import { ScrollArea } from '@/components/ui/scroll-area'
import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuTrigger,
} from '@/components/ui/context-menu'
import { toast } from 'sonner'
import type { TabManager } from '@/hooks/useTabManager'
import type { FileNode } from '@/types/file'
import { FileNodeIcon } from './FileNodeIcon'

interface FileExplorerProps {
  tabManager: TabManager
}

export function FileExplorer({ tabManager }: FileExplorerProps) {
  const { t } = useI18n()
  const { tree, loading, error, expandDir, expandingPath } = useFileTree()
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set())

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
    // If children not loaded yet, fetch them on expand
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

  if (loading && tree.length === 0) {
    return (
      <div className="flex h-full items-center justify-center gap-2 text-text-secondary">
        <Loader2 className="size-4 animate-spin" />
        <span className="text-sm">{t('sidebar.loadingFiles')}</span>
      </div>
    )
  }

  if (error) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-2 px-4 text-center">
        <p className="text-sm text-text-secondary">{t('sidebar.loadFailed')}</p>
        <p className="text-xs text-text-muted">{error}</p>
      </div>
    )
  }

  return (
    <ScrollArea className="h-full">
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
  )
}

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
