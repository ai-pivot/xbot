/**
 * useFileTree — fetches the file tree for the current working directory.
 *
 * Lazy loading: only fetches the top level on initial load. Subdirectories
 * are fetched on demand when the user expands them (via expandDir).
 */
import { useCallback, useEffect, useRef, useState } from 'react'

import { useCwd } from '@/providers/CwdProvider'
import { flattenFiles, type FileNode } from '@/types/file'
import { invalidateFsCache, listDir, joinPath } from '@/hooks/useFileSystem'

interface UseFileTreeResult {
  /** Nested file tree from the CWD root. */
  tree: FileNode[]
  /** Flattened file leaves (for search). */
  flatFiles: FileNode[]
  loading: boolean
  error: string | null
  /** Manually reload the tree. */
  reload: () => void
  /** Expand a directory node, fetching its children if not yet loaded. */
  expandDir: (path: string) => Promise<void>
  /** Whether a specific directory is currently loading. */
  expandingPath: string | null
}

/** Build top-level FileNode[] from a single listDir call (no recursion). */
async function buildTopLevel(cwd: string): Promise<FileNode[]> {
  const entries = await listDir(cwd)
  return entries.map((entry) => {
    const path = joinPath(cwd, entry.name)
    return {
      name: entry.name,
      path,
      type: entry.isDir ? 'directory' : 'file',
    } as FileNode
  })
}

/** Recursively find a node by path in the tree. */
function findNode(nodes: FileNode[], path: string): FileNode | null {
  for (const node of nodes) {
    if (node.path === path) return node
    if (node.children) {
      const found = findNode(node.children, path)
      if (found) return found
    }
  }
  return null
}

/** Deep clone the tree (shallow per-level) for immutable updates. */
function cloneTree(nodes: FileNode[]): FileNode[] {
  return nodes.map((n) => ({ ...n, children: n.children ? cloneTree(n.children) : undefined }))
}

export function useFileTree(rootPath?: string | null): UseFileTreeResult {
  const { cwd } = useCwd()
  // Use the explicit rootPath if provided, otherwise fall back to session CWD.
  const root = rootPath ?? cwd
  const [tree, setTree] = useState<FileNode[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [expandingPath, setExpandingPath] = useState<string | null>(null)
  const reloadTokenRef = useRef(0)

  const reload = useCallback(async () => {
    if (!root) return
    const token = ++reloadTokenRef.current
    setLoading(true)
    setError(null)
    try {
      invalidateFsCache()
      const result = await buildTopLevel(root)
      if (token !== reloadTokenRef.current) return // stale
      setTree(result)
    } catch (e) {
      if (token !== reloadTokenRef.current) return
      setError(e instanceof Error ? e.message : String(e))
      setTree([])
    } finally {
      if (token === reloadTokenRef.current) setLoading(false)
    }
  }, [root])

  // Re-fetch when the CWD changes.
  useEffect(() => {
    void reload()
  }, [reload])

  /** Expand a directory: fetch children if not yet loaded. */
  const expandDir = useCallback(async (path: string) => {
    setExpandingPath(path)
    try {
      const entries = await listDir(path)
      const children = entries.map((entry) => {
        const childPath = joinPath(path, entry.name)
        return {
          name: entry.name,
          path: childPath,
          type: entry.isDir ? 'directory' : 'file',
        } as FileNode
      })
      setTree((prev) => {
        const cloned = cloneTree(prev)
        const node = findNode(cloned, path)
        if (node) node.children = children
        return cloned
      })
    } catch {
      // Silently fail — node will just show no children
    } finally {
      setExpandingPath(null)
    }
  }, [])

  const flatFiles = useCallback(() => flattenFiles(tree), [tree])

  return {
    tree,
    flatFiles: flatFiles(),
    loading,
    error,
    reload,
    expandDir,
    expandingPath,
  }
}
