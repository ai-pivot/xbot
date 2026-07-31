/**
 * FileSearch — fuzzy file search over the workspace file tree.
 *
 * Search box + debounced (200ms) result list. Matching is case-insensitive
 * substring over file name and path; results sort by whether the match is in
 * the name (preferred) then by path. Matched substring is highlighted. Click a
 * result to open the file as a tab in the shared workspace.
 */
import { useCallback, useEffect, useMemo, useState } from 'react'
import { Search, X } from 'lucide-react'

import { useI18n } from '@/providers/i18n'
import { useFileTree } from '@/hooks/useFileTree'
import { Input } from '@/components/ui/input'
import { ScrollArea } from '@/components/ui/scroll-area'
import type { TabManager } from '@/hooks/useTabManager'
import type { FileNode } from '@/types/file'
import { FileNodeIcon } from './FileNodeIcon'

interface FileSearchProps {
  tabManager: TabManager
}

const DEBOUNCE_MS = 200

export function FileSearch({ tabManager }: FileSearchProps) {
  const { t } = useI18n()
  const { flatFiles } = useFileTree()
  const [query, setQuery] = useState('')
  const [debounced, setDebounced] = useState('')

  // Debounce the query → debounced snapshot drives the search.
  useEffect(() => {
    const id = setTimeout(() => setDebounced(query), DEBOUNCE_MS)
    return () => clearTimeout(id)
  }, [query])

  const results = useMemo<FileNode[]>(() => {
    const q = debounced.trim().toLowerCase()
    if (!q) return []
    const matched = flatFiles.filter(
      (f) => f.name.toLowerCase().includes(q) || f.path.toLowerCase().includes(q),
    )
    // Sort: name-match first, then path asc.
    return matched.sort((a, b) => {
      const an = a.name.toLowerCase().includes(q) ? 0 : 1
      const bn = b.name.toLowerCase().includes(q) ? 0 : 1
      if (an !== bn) return an - bn
      return a.path.localeCompare(b.path)
    })
  }, [debounced, flatFiles])

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

  return (
    <div className="flex h-full flex-col">
      <div className="relative px-2 py-2">
        <Search className="pointer-events-none absolute left-4 top-1/2 size-3.5 -translate-y-1/2 text-text-muted" />
        <Input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder={t('sidebar.searchPlaceholder')}
          className="h-7 pl-8 pr-7 text-xs"
          aria-label={t('sidebar.search')}
          autoFocus
        />
        {query && (
          <button
            type="button"
            aria-label={t('common.close')}
            onClick={() => setQuery('')}
            className="absolute right-3 top-1/2 -translate-y-1/2 text-text-muted hover:text-text-primary"
          >
            <X className="size-3.5" />
          </button>
        )}
      </div>

      <ScrollArea className="min-h-0 flex-1">
        {debounced.trim() === '' ? (
          <div className="px-3 py-6 text-center text-xs text-text-muted">
            {t('sidebar.searchHint')}
          </div>
        ) : results.length === 0 ? (
          <div className="px-3 py-6 text-center text-xs text-text-muted">
            {t('sidebar.noResults')}
          </div>
        ) : (
          <ul className="py-1 text-sm">
            {results.map((node) => (
              <li key={node.path}>
                <button
                  type="button"
                  onClick={() => openFile(node)}
                  className="flex w-full flex-col items-start gap-0.5 px-3 py-1.5 text-left transition-colors hover:bg-bg-tertiary"
                >
                  <span className="flex items-center gap-1.5">
                    <FileNodeIcon
                      node={node}
                      className="size-3.5 shrink-0 text-text-secondary"
                    />
                    <span className="truncate text-text-primary">
                      {highlight(node.name, debounced)}
                    </span>
                  </span>
                  <span className="truncate pl-5 text-[11px] text-text-muted">
                    {highlight(node.path, debounced)}
                  </span>
                </button>
              </li>
            ))}
          </ul>
        )}
      </ScrollArea>
    </div>
  )
}

/** Highlight the first case-insensitive match of `query` in `text`. */
function highlight(text: string, query: string) {
  const q = query.trim()
  if (!q) return text
  const idx = text.toLowerCase().indexOf(q.toLowerCase())
  if (idx < 0) return text
  const before = text.slice(0, idx)
  const match = text.slice(idx, idx + q.length)
  const after = text.slice(idx + q.length)
  return (
    <>
      {before}
      <mark className="rounded-sm bg-app-accent/30 text-text-primary">{match}</mark>
      {after}
    </>
  )
}
