/**
 * SelectionToolbar — floating formatting toolbar for the tiptap composer.
 *
 * Zero chrome at rest: it ONLY appears while text is selected (or while the
 * link editor is open), keeping the input minimal/premium (Linear-style).
 *
 * Two modes:
 *  - "bar":   [B] [I] [S] [</>] [🔗]   — inline marks + link entry
 *  - "link":  URL input (Enter=apply, Esc=cancel, IME-safe) + apply/unlink
 *
 * Built on @tiptap/react/menus BubbleMenu (floating-ui): auto viewport flip
 * keeps it on screen on phones; preventHide semantics keep it open while the
 * URL input is focused. Ctrl/Cmd+K (wired in MessageInput) opens link mode for
 * the word at the cursor.
 *
 * Hidden while the completion popup (/ @ triggers) is open — completions win.
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import { BubbleMenu } from '@tiptap/react/menus'
import { useEditorState, type Editor } from '@tiptap/react'
import { Bold, Check, Code, Italic, Link2, Link2Off, Strikethrough, X } from 'lucide-react'

import { useI18n } from '@/providers/i18n'
import { cn } from '@/lib/utils'

interface SelectionToolbarProps {
  editor: Editor | null
  /** Hide while the completion popup (/ @ triggers) is open — it takes priority. */
  hidden?: boolean
  /** Increment to open link-edit mode (Ctrl/Cmd+K from MessageInput). */
  linkEditSignal?: number
}

/** Normalize a user-entered URL: pass through schemes/anchors/relative paths,
 *  auto-prefix bare domains with https:// (a bare href would be a relative link). */
export function normalizeHref(raw: string): string {
  const url = raw.trim()
  if (!url) return ''
  if (/^[a-z][a-z0-9+.-]*:/i.test(url) || url.startsWith('#') || url.startsWith('/') || url.startsWith('//')) {
    return url
  }
  return `https://${url}`
}

export function SelectionToolbar({ editor, hidden = false, linkEditSignal = 0 }: SelectionToolbarProps) {
  const { t } = useI18n()
  const [mode, setMode] = useState<'bar' | 'link'>('bar')
  const [url, setUrl] = useState('')
  const inputRef = useRef<HTMLInputElement>(null)
  const toolbarRef = useRef<HTMLDivElement>(null)

  // Refs read inside the stable shouldShow callback (BubbleMenu captures it once).
  const hiddenRef = useRef(hidden)
  hiddenRef.current = hidden
  const linkModeRef = useRef(mode === 'link')
  linkModeRef.current = mode === 'link'

  const markState = useEditorState({
    editor,
    selector: ({ editor: e }) =>
      e
        ? {
            bold: e.isActive('bold'),
            italic: e.isActive('italic'),
            strike: e.isActive('strike'),
            code: e.isActive('code'),
            link: e.isActive('link'),
            linkHref: (e.getAttributes('link').href as string | undefined) ?? '',
          }
        : null,
  }) ?? { bold: false, italic: false, strike: false, code: false, link: false, linkHref: '' }

  // Default BubbleMenu visibility + our two overrides:
  //  - completion popup takes priority (hidden)
  //  - stay mounted while the URL editor is open (editor blur must not dismiss)
  const shouldShow = useCallback(({ view, state, from, to }: { view: { hasFocus: () => boolean; editable: boolean }; state: { selection: { empty: boolean }; doc: { textBetween: (from: number, to: number) => string } }; from: number; to: number }) => {
    if (hiddenRef.current) return false
    if (linkModeRef.current) return true
    const { selection, doc } = state
    const { empty } = selection
    const isEmptyTextBlock = !doc.textBetween(from, to).length
    const isChildOfMenu = toolbarRef.current?.contains(document.activeElement) ?? false
    if (!(view.hasFocus() || isChildOfMenu) || empty || isEmptyTextBlock || !view.editable) return false
    return true
  }, [])

  // ── Link actions ──────────────────────────────────────────────────────────

  const openLinkMode = useCallback(() => {
    if (!editor) return
    setUrl(markState.linkHref || '')
    setMode('link')
    requestAnimationFrame(() => inputRef.current?.focus())
  }, [editor, markState.linkHref])

  const applyLink = useCallback(() => {
    if (!editor) return
    const href = normalizeHref(url)
    if (!href) {
      // Empty URL → treat as unlink (predictable, no-op on plain text)
      editor.chain().focus().extendMarkRange('link').unsetLink().run()
    } else {
      editor.chain().focus().extendMarkRange('link').setLink({ href }).run()
    }
    setMode('bar')
  }, [editor, url])

  const removeLink = useCallback(() => {
    if (!editor) return
    editor.chain().focus().extendMarkRange('link').unsetLink().run()
    setMode('bar')
  }, [editor])

  // Ctrl/Cmd+K signal from MessageInput (selection already prepared by caller)
  const linkEditSignalRef = useRef(0)
  useEffect(() => {
    if (!editor || linkEditSignal === linkEditSignalRef.current) return
    linkEditSignalRef.current = linkEditSignal
    if (linkEditSignal === 0) return
    setUrl(markState.linkHref || '')
    setMode('link')
    requestAnimationFrame(() => inputRef.current?.focus())
    // markState.linkHref intentionally read at open time (signal-driven, not reactive)
  }, [linkEditSignal, editor])

  // Collapse link mode back to bar when the user clicks into the editor and
  // collapses the selection (e.g. clicked elsewhere → edit mode is stale).
  useEffect(() => {
    if (!editor) return
    const onSelection = () => {
      if (linkModeRef.current && editor.state.selection.empty && editor.view.hasFocus()) {
        setMode('bar')
      }
    }
    editor.on('selectionUpdate', onSelection)
    return () => { editor.off('selectionUpdate', onSelection) }
  }, [editor])

  if (!editor) return null

  const marks: Array<{ name: 'bold' | 'italic' | 'strike' | 'code'; label: string; Icon: typeof Bold }> = [
    { name: 'bold', label: t('agent.editorBold'), Icon: Bold },
    { name: 'italic', label: t('agent.editorItalic'), Icon: Italic },
    { name: 'strike', label: t('agent.editorStrike'), Icon: Strikethrough },
    { name: 'code', label: t('agent.editorCode'), Icon: Code },
  ]

  return (
    <BubbleMenu
      editor={editor}
      shouldShow={shouldShow}
      updateDelay={100}
      options={{ placement: 'top', offset: 8, flip: {}, shift: {} }}
    >
      <div
        ref={toolbarRef}
        data-testid="selection-toolbar"
        data-mode={mode}
        className="pointer-events-auto flex select-none items-center gap-0.5 rounded-lg border border-border bg-bg-primary p-1 shadow-lg shadow-black/10 backdrop-blur-sm animate-in fade-in zoom-in-95 duration-100"
      >
        {mode === 'bar' ? (
          <>
            {marks.map(({ name, label, Icon }) => (
              <button
                key={name}
                type="button"
                data-testid={`st-${name}`}
                aria-label={label}
                title={label}
                onMouseDown={(e) => e.preventDefault()}
                className={cn(
                  'flex size-8 items-center justify-center rounded-md transition-colors active:scale-90',
                  markState[name]
                    ? 'bg-accent/15 text-accent'
                    : 'text-text-secondary hover:bg-bg-tertiary hover:text-text-primary',
                )}
                onClick={() => editor.chain().focus().toggleMark(name).run()}
              >
                <Icon className="size-4" />
              </button>
            ))}
            <span className="mx-0.5 h-5 w-px shrink-0 bg-border" aria-hidden />
            <button
              type="button"
              data-testid="st-link"
              aria-label={t('agent.editorLink')}
              title={t('agent.editorLink')}
              onMouseDown={(e) => e.preventDefault()}
              className={cn(
                'flex size-8 items-center justify-center rounded-md transition-colors active:scale-90',
                markState.link
                  ? 'bg-accent/15 text-accent'
                  : 'text-text-secondary hover:bg-bg-tertiary hover:text-text-primary',
              )}
              onClick={openLinkMode}
            >
              <Link2 className="size-4" />
            </button>
          </>
        ) : (
          <>
            <Link2 className="ml-1.5 size-3.5 shrink-0 text-text-muted" />
            <input
              ref={inputRef}
              data-testid="st-link-input"
              className="h-8 w-56 rounded-md bg-transparent px-1.5 text-sm text-text-primary placeholder:text-text-muted outline-none ring-1 ring-border focus:ring-accent/50"
              type="text"
              spellCheck={false}
              autoComplete="off"
              placeholder={t('agent.editorLinkPlaceholder')}
              value={url}
              onChange={(e) => setUrl(e.target.value)}
              // IME guard: Enter selects the candidate, must NOT submit
              onKeyDown={(e) => {
                if (e.key === 'Enter' && !e.nativeEvent.isComposing) {
                  e.preventDefault()
                  applyLink()
                } else if (e.key === 'Escape') {
                  e.preventDefault()
                  setMode('bar')
                  editor.commands.focus()
                }
              }}
              // onMouseDown keep default so the input focuses (blur toolbar →
              // BubbleMenu's relatedTarget logic keeps the menu mounted)
            />
            <button
              type="button"
              data-testid="st-link-apply"
              aria-label={t('agent.editorLinkApply')}
              title={t('agent.editorLinkApply')}
              disabled={!url.trim()}
              className="flex size-8 items-center justify-center rounded-md text-text-secondary transition-colors hover:bg-bg-tertiary hover:text-text-primary active:scale-90 disabled:opacity-40"
              onClick={applyLink}
            >
              <Check className="size-4" />
            </button>
            {markState.link && (
              <button
                type="button"
                data-testid="st-link-remove"
                aria-label={t('agent.editorLinkRemove')}
                title={t('agent.editorLinkRemove')}
                className="flex size-8 items-center justify-center rounded-md text-text-secondary transition-colors hover:bg-destructive/10 hover:text-destructive active:scale-90"
                onClick={removeLink}
              >
                <Link2Off className="size-4" />
              </button>
            )}
            <button
              type="button"
              aria-label={t('common.cancel')}
              className="flex size-8 items-center justify-center rounded-md text-text-muted transition-colors hover:bg-bg-tertiary hover:text-text-primary active:scale-90"
              onClick={() => {
                setMode('bar')
                editor.commands.focus()
              }}
            >
              <X className="size-4" />
            </button>
          </>
        )}
      </div>
    </BubbleMenu>
  )
}
