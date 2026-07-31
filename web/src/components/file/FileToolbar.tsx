/**
 * FileToolbar — file panel header: file name + edit/preview toggle (Spec 5 §3.6).
 *
 * Layout: [fileIcon] [fileName] ............ [edit] [preview]
 *
 *   - The active mode button is highlighted with the accent color.
 *   - The toggle is only rendered for files that can switch views
 *     (markdown). Image files are preview-only, so no toggle is shown.
 *   - All visible text goes through i18n.
 */
import { Eye, Pencil } from 'lucide-react'
import { createElement, type ReactNode } from 'react'

import { fileIcon } from './FileIcon'
import { useI18n } from '@/providers/i18n'
import type { FileViewMode } from './fileTypes'

export interface FileToolbarProps {
  fileName: string
  /** Current view mode. */
  mode: FileViewMode
  /** Switch the view; undefined when the file has no toggle (images). */
  onModeChange?: (mode: FileViewMode) => void
  /** Whether to offer the toggle at all (false for images). */
  canToggle: boolean
}

export function FileToolbar({ fileName, mode, onModeChange, canToggle }: FileToolbarProps) {
  const { t } = useI18n()

  return (
    <div className="flex h-9 shrink-0 items-center gap-2 border-b bg-bg-secondary px-3">
      <FileGlyph name={fileName} />
      <span className="truncate text-[13px] text-text-primary" title={fileName}>
        {fileName}
      </span>

      {canToggle && onModeChange && (
        <div className="ml-auto flex items-center gap-1">
          <ModeButton
            active={mode === 'editor'}
            onClick={() => onModeChange('editor')}
            label={t('workspace.edit')}
            ariaLabel={t('file.editMode')}
          >
            <Pencil className="size-3.5" />
          </ModeButton>
          <ModeButton
            active={mode === 'preview'}
            onClick={() => onModeChange('preview')}
            label={t('workspace.preview')}
            ariaLabel={t('file.previewMode')}
          >
            <Eye className="size-3.5" />
          </ModeButton>
        </div>
      )}
    </div>
  )
}

interface ModeButtonProps {
  active: boolean
  onClick: () => void
  label: string
  ariaLabel: string
  children: ReactNode
}

function ModeButton({ active, onClick, label, ariaLabel, children }: ModeButtonProps) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-label={ariaLabel}
      aria-pressed={active}
      className="flex items-center gap-1.5 rounded-md px-2 py-1 text-xs transition-colors"
      style={{
        backgroundColor: active ? 'color-mix(in srgb, var(--accent) 15%, transparent)' : 'transparent',
        color: active ? 'var(--accent)' : 'var(--text-secondary)',
      }}
    >
      {children}
      <span>{label}</span>
    </button>
  )
}

/**
 * Module-level glyph wrapper so the dynamic Lucide icon is never "created" in
 * a component's render body (which the react-hooks/static-components rule
 * forbids). `fileIcon` resolves to an existing module-level component; we render
 * it via `createElement` so the lookup stays a plain function call.
 */
function FileGlyph({ name }: { name: string }) {
  const Glyph = fileIcon(name)
  return createElement(Glyph, { className: 'size-4 shrink-0 text-text-secondary' })
}

