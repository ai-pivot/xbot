/**
 * PathPicker — Remote-picker style path selector with autocomplete.
 *
 * Uses a plain Input + absolute-positioned dropdown (no Radix Popover) to
 * avoid focus/z-index conflicts when used inside a Dialog.
 *
 * VSCode-style folder navigation:
 *   - "/" shows all dirs under "/"
 *   - Selecting a dir appends "/" and lists its contents
 *   - Typing filters by name prefix within the parent dir
 *   - Enter on a highlighted entry navigates into it
 *   - Enter without highlight submits the current path
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import { ChevronRight, Folder, Loader2 } from 'lucide-react'

import { Input } from '@/components/ui/input'
import { useI18n } from '@/providers/i18n'
import { useDebounce } from '@/hooks/useDebounce'
import { listDir, type FsEntry } from '@/hooks/useFileSystem'
import { cn } from '@/lib/utils'

export interface PathPickerProps {
  value: string
  onChange: (path: string) => void
  placeholder?: string
  className?: string
  /** Compact mode: smaller height (for use in dialogs). */
  compact?: boolean
  /** External onKeyDown (e.g. Enter to submit a dialog). Called after internal handler. */
  onKeyDown?: (e: React.KeyboardEvent) => void
}

const DEBOUNCE_MS = 200

export function PathPicker({ value, onChange, placeholder, className, compact, onKeyDown: externalOnKeyDown }: PathPickerProps) {
  const { t } = useI18n()
  const [open, setOpen] = useState(false)
  const [entries, setEntries] = useState<FsEntry[]>([])
  const [loading, setLoading] = useState(false)
  const [highlightIdx, setHighlightIdx] = useState(-1)
  const wrapperRef = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLInputElement>(null)
  const abortRef = useRef<AbortController | null>(null)

  const debouncedValue = useDebounce(value, DEBOUNCE_MS)

  const { dirPath, prefix } = parseInput(debouncedValue)

  // Fetch subdirectories when the debounced value changes and dropdown is open.
  useEffect(() => {
    if (!open) return
    abortRef.current?.abort()
    const ac = new AbortController()
    abortRef.current = ac
    setLoading(true)
    listDir(dirPath, true, ac.signal)
      .then((all) => {
        const dirs = all.filter((e) => e.isDir)
        const filtered = prefix
          ? dirs.filter((e) => e.name.toLowerCase().startsWith(prefix.toLowerCase()))
          : dirs
        setEntries(filtered)
        setHighlightIdx(-1)
      })
      .catch(() => {
        if (!ac.signal.aborted) setEntries([])
      })
      .finally(() => {
        if (!ac.signal.aborted) setLoading(false)
      })
    return () => ac.abort()
  }, [dirPath, prefix, open])

  // Close dropdown on outside click.
  useEffect(() => {
    if (!open) return
    const handler = (e: MouseEvent) => {
      if (wrapperRef.current && !wrapperRef.current.contains(e.target as Node)) {
        setOpen(false)
      }
    }
    document.addEventListener('mousedown', handler)
    return () => document.removeEventListener('mousedown', handler)
  }, [open])

  // Navigate into a directory: set value to dir + "/" and keep dropdown open.
  const navigateInto = useCallback(
    (entry: FsEntry) => {
      const fullPath = entry.name === '/' ? '/' : `${dirPath === '/' ? '' : dirPath}/${entry.name}`
      onChange(fullPath + '/')
      setOpen(true)
      // Reset highlight for the new directory listing
      setHighlightIdx(-1)
    },
    [dirPath, onChange],
  )

  const onKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (e.key === 'ArrowDown' && open && entries.length > 0) {
        e.preventDefault()
        setHighlightIdx((prev) => (prev + 1) % entries.length)
      } else if (e.key === 'ArrowUp' && open && entries.length > 0) {
        e.preventDefault()
        setHighlightIdx((prev) => (prev <= 0 ? entries.length - 1 : prev - 1))
      } else if (e.key === 'Enter') {
        // If a dir is highlighted, navigate into it (don't submit the dialog)
        if (open && highlightIdx >= 0 && entries[highlightIdx]) {
          e.preventDefault()
          navigateInto(entries[highlightIdx])
          return
        }
        // If the current value doesn't end with '/', and there's exactly one
        // match, navigate into it too.
        if (open && entries.length === 1 && !value.endsWith('/')) {
          e.preventDefault()
          navigateInto(entries[0])
          return
        }
        // Otherwise fall through to the external onKeyDown (submit dialog)
      } else if (e.key === 'Escape') {
        setOpen(false)
      } else if (e.key === 'Tab') {
        // Tab on a highlighted entry navigates into it
        if (open && highlightIdx >= 0 && entries[highlightIdx]) {
          e.preventDefault()
          navigateInto(entries[highlightIdx])
        }
      }
    },
    [open, entries, highlightIdx, navigateInto, value],
  )

  return (
    <div ref={wrapperRef} className="relative">
      <Input
        ref={inputRef}
        value={value}
        onChange={(e) => {
          onChange(e.target.value)
          setOpen(true)
        }}
        onFocus={() => setOpen(true)}
        onKeyDown={(e) => {
          onKeyDown(e)
          // If the internal handler didn't preventDefault, call external
          if (!e.defaultPrevented) externalOnKeyDown?.(e)
        }}
        placeholder={placeholder ?? t('session.workPathPlaceholder')}
        className={cn(compact ? 'h-8' : 'h-9', 'text-sm', className)}
        aria-label={t('session.workPath')}
      />
      {open && (
        <div className="absolute left-0 right-0 top-full z-50 mt-1 overflow-hidden rounded-md border bg-bg-secondary shadow-lg">
          <div className="max-h-[240px] overflow-y-auto py-1 text-sm">
            {loading ? (
              <div className="flex items-center gap-2 px-3 py-2 text-text-muted">
                <Loader2 className="size-3.5 animate-spin" />
                <span>{t('common.loading')}</span>
              </div>
            ) : entries.length === 0 ? (
              <div className="px-3 py-2 text-xs text-text-muted">{t('sidebar.noResults')}</div>
            ) : (
              entries.map((entry, idx) => {
                const fullPath =
                  entry.name === '/' ? '/' : `${dirPath === '/' ? '' : dirPath}/${entry.name}`
                return (
                  <button
                    key={fullPath}
                    type="button"
                    onMouseEnter={() => setHighlightIdx(idx)}
                    onClick={() => navigateInto(entry)}
                    className={cn(
                      'flex w-full items-center gap-1.5 px-3 py-1.5 text-left transition-colors hover:bg-bg-tertiary',
                      highlightIdx === idx && 'bg-bg-tertiary',
                    )}
                  >
                    {idx === highlightIdx ? (
                      <ChevronRight className="size-3.5 shrink-0 text-text-muted" />
                    ) : (
                      <span className="size-3.5 shrink-0" />
                    )}
                    <Folder className="size-3.5 shrink-0 text-text-secondary" />
                    <span className="truncate text-text-primary">{entry.name}</span>
                  </button>
                )
              })
            )}
          </div>
        </div>
      )}
    </div>
  )
}

/**
 * Parse a path input into the directory to list and a name prefix to filter by.
 *
 * VSCode-style: the input always represents "where am I + what am I typing".
 *
 * Examples:
 *   "/"            → dirPath="/", prefix=""         → show all dirs under /
 *   "/r"           → dirPath="/", prefix="r"         → filter dirs under / starting with r
 *   "/root/"       → dirPath="/root", prefix=""     → show all dirs under /root
 *   "/root/Co"     → dirPath="/root", prefix="Co"   → filter dirs under /root
 *   "/root/Code/"  → dirPath="/root/Code", prefix="" → show all dirs under /root/Code
 */
function parseInput(input: string): { dirPath: string; prefix: string } {
  const trimmed = input.trim()
  if (!trimmed) return { dirPath: '/', prefix: '' }

  // If it ends with '/', the whole thing is a directory path → list its contents
  if (trimmed.endsWith('/')) {
    const clean = trimmed.replace(/\/+$/, '') || '/'
    return { dirPath: clean, prefix: '' }
  }

  const lastSlash = trimmed.lastIndexOf('/')
  if (lastSlash <= 0) {
    return { dirPath: '/', prefix: trimmed }
  }
  return { dirPath: trimmed.slice(0, lastSlash), prefix: trimmed.slice(lastSlash + 1) }
}
