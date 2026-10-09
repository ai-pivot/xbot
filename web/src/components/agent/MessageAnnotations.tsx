import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { Dialog as DialogPrimitive, Popover as PopoverPrimitive, DropdownMenu as MenuPrimitive } from 'radix-ui'
import { createPortal } from 'react-dom'
import { Check, MessageSquareText, MoreHorizontal, Pencil, Trash2, X } from 'lucide-react'
import { toast } from 'sonner'

import { useI18n } from '@/providers/i18n'
import { useIsMobile, useIsTouch } from '@/hooks/useIsMobile'
import { useKeyboardInset } from '@/hooks/useKeyboardInset'
import { annotationStorageKey, loadAnnotations, validateAnnotations, type AnnotationSource, type MessageAnnotation } from '@/lib/messageAnnotations'
import { getWebCacheEpoch } from '@/lib/webCache'
import { frameScheduler } from '@/lib/frameScheduler'

export type AnnotationPosition = { x: number; y: number }
type EditingAnnotation = { id: string; source: AnnotationSource; quote: string; comment: string; position: AnnotationPosition }
type SelectedAnnotation = { source: AnnotationSource; quote: string; position: AnnotationPosition }
type CaptureComposer = () => (() => void)

interface AnnotationActions {
  registerComposer: (capture: CaptureComposer | null) => void
}
interface AnnotationDraft {
  items: MessageAnnotation[]
  editing: boolean
  openList: (position: AnnotationPosition) => void
  removeSent: (items: MessageAnnotation[]) => void
}
const ActionsContext = createContext<AnnotationActions | null>(null)
const DraftContext = createContext<AnnotationDraft | null>(null)
export function useAnnotationActions() { return useContext(ActionsContext) }
export function useAnnotationDraft() { return useContext(DraftContext) }

/** Keep the exact selection, but never mix completed bodies or session panels. */
export function annotationSelection(body: HTMLElement): { quote: string; startOffset: number; endOffset: number } | null {
  const selection = window.getSelection()
  if (!selection || selection.isCollapsed || selection.rangeCount !== 1) return null
  const range = selection.getRangeAt(0)
  if (!body.contains(range.startContainer) || !body.contains(range.endContainer)) return null
  const prefix = range.cloneRange()
  prefix.selectNodeContents(body)
  prefix.setEnd(range.startContainer, range.startOffset)
  const startOffset = prefix.toString().length
  return { quote: selection.toString(), startOffset, endOffset: startOffset + range.toString().length }
}

/** One provider per authenticated session panel, outside virtual message rows. */
export function MessageAnnotationsProvider({ username, sessionKey, visible, enabled = true, children }: {
  username: string; sessionKey: string; visible: boolean; enabled?: boolean; children: ReactNode
}) {
  const { t } = useI18n()
  const touch = useIsTouch()
  const storageKey = annotationStorageKey(username, sessionKey)
  const [authEpoch] = useState(getWebCacheEpoch)
  const [loaded] = useState(() => {
    try { return { items: loadAnnotations(storageKey), error: false } } catch { return { items: [], error: true } }
  })
  const [items, setItems] = useState<MessageAnnotation[]>(loaded.items)
  useEffect(() => {
    if (loaded.error) toast.error(t('agent.annotations.storageError'))
  }, [loaded, t])
  const [editing, setEditing] = useState<EditingAnnotation | null>(null)
  const [listPosition, setListPosition] = useState<AnnotationPosition | null>(null)
  const [selected, setSelected] = useState<SelectedAnnotation | null>(null)
  const rootRef = useRef<HTMLDivElement>(null)
  const captureRef = useRef<CaptureComposer | null>(null)
  const restoreRef = useRef<(() => void) | null>(null)

  const begin = (selection: SelectedAnnotation) => {
    restoreRef.current = captureRef.current?.() ?? null
    setSelected(null)
    setListPosition(null)
    setEditing({ id: crypto.randomUUID(), ...selection, comment: '' })
  }
  const registerComposer = useCallback((capture: CaptureComposer | null) => { captureRef.current = capture }, [])
  const actions = useMemo(() => enabled ? { registerComposer } : null, [enabled, registerComposer])

  useEffect(() => {
    if (!enabled || !visible || editing || listPosition) { setSelected(null); return }
    let dragging = false
    const update = () => {
      const selection = document.getSelection()
      const node = selection?.anchorNode
      const element = node?.nodeType === Node.ELEMENT_NODE ? node as Element : node?.parentElement
      const body = element?.closest<HTMLElement>('[data-annotation-body]')
      const target = body?.closest<HTMLElement>('[data-copy-target]')
      const source = target?.dataset.annotationSource
      const snapshot = body ? annotationSelection(body) : null
      if (dragging || !body || !rootRef.current?.contains(body) || !source || !snapshot?.quote.trim()) {
        setSelected(null)
        return
      }
      const range = selection!.getRangeAt(0)
      const viewport = window.visualViewport
      const left = viewport?.offsetLeft ?? 0
      const top = viewport?.offsetTop ?? 0
      const right = left + (viewport?.width ?? window.innerWidth)
      const bottom = top + (viewport?.height ?? window.innerHeight)
      const rects = Array.from(range.getClientRects()).filter((r) => r.width > 0 && r.height > 0 && r.bottom > top && r.top < bottom && r.right > left && r.left < right)
      const rect = selection!.focusNode === range.startContainer && selection!.focusOffset === range.startOffset ? rects[0] : rects.at(-1)
      if (!rect) { setSelected(null); return }
      setSelected({ source: { ...JSON.parse(source) as AnnotationSource, startOffset: snapshot.startOffset, endOffset: snapshot.endOffset }, quote: snapshot.quote, position: {
        x: Math.max(left + 8, Math.min(rect.right - 44, right - 52)),
        y: Math.max(top + 8, Math.min(rect.top - 52 >= top + 8 ? rect.top - 52 : rect.bottom + 8, bottom - 52)),
      } })
    }
    const schedule = () => frameScheduler.schedule(update)
    const down = (event: PointerEvent) => {
      if (event.button !== 0) return
      if ((event.target as HTMLElement)?.closest('[data-testid="annotation-selection-action"]')) return
      dragging = true
      setSelected(null)
    }
    const up = () => { dragging = false; schedule() }
    const dismiss = () => { frameScheduler.cancel(update); setSelected(null) }
    const contextMenu = (event: MouseEvent) => {
      const element = event.target as HTMLElement | null
      const body = element?.closest?.('[data-annotation-body]')
      if (touch && body && rootRef.current?.contains(body) && body.closest('[data-annotation-source]') && !element?.closest('a[href]')) {
        // Native touch selection may emit contextmenu after selectionchange.
        dragging = false
        schedule()
      } else dismiss()
    }
    const key = (event: KeyboardEvent) => { if (event.key === 'Escape') dismiss() }
    // One scoped observer per visible panel, not one listener per virtual row.
    document.addEventListener('selectionchange', schedule)
    document.addEventListener('pointerdown', down, true)
    document.addEventListener('pointerup', up)
    document.addEventListener('pointercancel', up)
    document.addEventListener('scroll', dismiss, true)
    document.addEventListener('contextmenu', contextMenu, true)
    document.addEventListener('keydown', key)
    window.visualViewport?.addEventListener('resize', dismiss)
    return () => {
      frameScheduler.cancel(update)
      document.removeEventListener('selectionchange', schedule)
      document.removeEventListener('pointerdown', down, true)
      document.removeEventListener('pointerup', up)
      document.removeEventListener('pointercancel', up)
      document.removeEventListener('scroll', dismiss, true)
      document.removeEventListener('contextmenu', contextMenu, true)
      document.removeEventListener('keydown', key)
      window.visualViewport?.removeEventListener('resize', dismiss)
    }
  }, [enabled, visible, editing, listPosition, touch])

  const persist = useCallback((next: MessageAnnotation[]): boolean => {
    if (getWebCacheEpoch() !== authEpoch) return false
    try {
      if (next.length) localStorage.setItem(storageKey, JSON.stringify(next))
      else localStorage.removeItem(storageKey)
    } catch {
      toast.error(t('agent.annotations.storageError'))
      return false
    }
    setItems(next)
    return true
  }, [authEpoch, storageKey, t])

  const closeEditor = () => {
    setEditing(null)
  }
  const restoreComposer = () => {
    restoreRef.current?.()
    restoreRef.current = null
  }
  const save = (annotation: MessageAnnotation): string | null => {
    const existing = items.some((item) => item.id === annotation.id)
    const next = existing ? items.map((item) => item.id === annotation.id ? annotation : item) : [...items, annotation]
    const error = validateAnnotations(next)
    if (error) return t(`agent.annotations.${error}`)
    if (!persist(next)) return t('agent.annotations.storageError')
    closeEditor()
    return null
  }
  const removeSent = useCallback((sent: MessageAnnotation[]) => {
    // A delayed acceptance must not clear comments added/edited after submission.
    if (getWebCacheEpoch() !== authEpoch) return
    try {
      const current = loadAnnotations(storageKey)
      const next = current.filter((item) => !sent.some((s) => s.id === item.id && s.quote === item.quote && s.comment === item.comment))
      if (next.length) localStorage.setItem(storageKey, JSON.stringify(next))
      else localStorage.removeItem(storageKey)
      setItems(next)
    } catch { toast.error(t('agent.annotations.cleanupError')) }
  }, [authEpoch, storageKey, t])
  const draft = useMemo(() => ({ items, editing: editing !== null, openList: setListPosition, removeSent }), [items, editing, removeSent])

  return (
    <ActionsContext.Provider value={actions}>
      <DraftContext.Provider value={draft}>
        <div ref={rootRef} className="contents">{children}</div>
        {selected && enabled && visible && !editing && !listPosition && createPortal(
          <button type="button" data-testid="annotation-selection-action" aria-label={t('agent.annotations.add')} title={t('agent.annotations.add')}
            className="fixed z-40 inline-flex size-11 select-none items-center justify-center rounded-md border border-border bg-bg-secondary text-text-primary shadow-lg hover:bg-bg-tertiary"
            style={{ left: selected.position.x, top: selected.position.y }}
            onPointerDown={(e) => e.preventDefault()} onMouseDown={(e) => e.preventDefault()}
            onClick={() => begin(selected)}><MessageSquareText className="size-4" /></button>, document.body)}
        {editing && <AnnotationEditor key={editing.id} editing={editing} visible={visible} onClose={closeEditor} onSave={save} onAfterClose={restoreComposer} />}
        {listPosition && <AnnotationSurface position={listPosition} visible={visible} title={t('agent.annotations.count', { count: items.length })} testID="annotation-list" onClose={() => setListPosition(null)}>
          <div className="min-h-0 overflow-y-auto overscroll-contain">
            {items.map((item, index) => (
              <div key={item.id} className="border-b border-border px-3 py-3 last:border-0">
                <div className="mb-2 flex items-center justify-between gap-2">
                  <span className="min-w-0 text-xs text-text-muted">{t('agent.annotations.item', { number: index + 1 })}</span>
                  <AnnotationItemMenu onEdit={() => {
                    restoreRef.current = captureRef.current?.() ?? null
                    setListPosition(null)
                    setEditing({ ...item, position: listPosition })
                  }} onDelete={() => {
                    if (persist(items.filter((i) => i.id !== item.id)) && items.length === 1) setListPosition(null)
                  }} />
                </div>
                <div className="mb-1 text-xs text-text-muted">{t('agent.annotations.quote')}</div>
                <blockquote className="max-h-40 overflow-y-auto border-l-2 border-accent/60 pl-2 text-sm whitespace-pre-wrap wrap-anywhere">{item.quote}</blockquote>
                <div className="mt-3 mb-1 text-xs text-text-muted">{t('agent.annotations.comment')}</div>
                <p className="text-sm whitespace-pre-wrap wrap-anywhere">{item.comment}</p>
              </div>
            ))}
          </div>
        </AnnotationSurface>}
      </DraftContext.Provider>
    </ActionsContext.Provider>
  )
}

export function AnnotationChip() {
  const draft = useAnnotationDraft()
  const { t } = useI18n()
  if (!draft?.items.length) return null
  return <button type="button" data-testid="annotation-chip" className="inline-flex min-h-9 max-w-full items-center gap-1.5 rounded-md bg-bg-tertiary px-2 text-xs text-text-secondary hover:text-text-primary" onClick={(e) => {
    const rect = e.currentTarget.getBoundingClientRect()
    draft.openList({ x: rect.left, y: rect.top })
  }}>
    <MessageSquareText className="size-3.5 shrink-0" />
    {t('agent.annotations.count', { count: draft.items.length })}
  </button>
}

const ICON_BUTTON = 'inline-flex size-11 shrink-0 items-center justify-center rounded-md text-text-secondary hover:bg-bg-tertiary hover:text-text-primary disabled:opacity-40'

function AnnotationItemMenu({ onEdit, onDelete }: { onEdit: () => void; onDelete: () => void }) {
  const { t } = useI18n()
  const touch = useIsTouch()
  if (!touch) return <div className="flex shrink-0">
    <button type="button" className={ICON_BUTTON} aria-label={t('agent.annotations.edit')} title={t('agent.annotations.edit')} onClick={onEdit}><Pencil className="size-3.5" /></button>
    <button type="button" className={ICON_BUTTON} aria-label={t('agent.annotations.delete')} title={t('agent.annotations.delete')} onClick={onDelete}><Trash2 className="size-3.5" /></button>
  </div>
  return <MenuPrimitive.Root>
    <MenuPrimitive.Trigger className={ICON_BUTTON} aria-label={t('agent.annotations.actions')}><MoreHorizontal className="size-4" /></MenuPrimitive.Trigger>
    <MenuPrimitive.Portal><MenuPrimitive.Content className="z-[70] rounded-md border border-border bg-bg-secondary p-1 shadow-xl" sideOffset={4} align="end">
      <MenuPrimitive.Item className="flex min-h-11 items-center gap-2 px-3 text-sm outline-none focus:bg-bg-tertiary" onSelect={onEdit}><Pencil className="size-4" />{t('agent.annotations.edit')}</MenuPrimitive.Item>
      <MenuPrimitive.Item className="flex min-h-11 items-center gap-2 px-3 text-sm text-destructive outline-none focus:bg-bg-tertiary" onSelect={onDelete}><Trash2 className="size-4" />{t('agent.annotations.delete')}</MenuPrimitive.Item>
    </MenuPrimitive.Content></MenuPrimitive.Portal>
  </MenuPrimitive.Root>
}

function AnnotationEditor({ editing, visible, onClose, onSave, onAfterClose }: {
  editing: EditingAnnotation; visible: boolean; onClose: () => void; onSave: (annotation: MessageAnnotation) => string | null; onAfterClose: () => void
}) {
  const { t } = useI18n()
  const [comment, setComment] = useState(editing.comment)
  const quote = editing.quote
  const [error, setError] = useState<string | null>(null)
  const canConfirm = !!comment.trim() && !!quote.trim()
  const confirm = () => {
    if (!canConfirm) return
    setError(onSave({ id: editing.id, source: editing.source, quote, comment }))
  }
  return <AnnotationSurface position={editing.position} visible={visible} title={t('agent.annotations.add')} testID="annotation-editor" onClose={onClose} onAfterClose={onAfterClose}>
    <div className="flex min-h-0 flex-col gap-2 overflow-y-auto overscroll-contain px-3 pb-3">
      <label htmlFor={`quote-${editing.id}`} className="text-xs text-text-muted">{t('agent.annotations.quote')}</label>
      <textarea id={`quote-${editing.id}`} readOnly aria-label={t('agent.annotations.quote')} value={quote}
        className="h-28 min-h-20 w-full shrink-0 resize-none rounded-md border border-border bg-bg-primary p-2 text-sm text-text-primary outline-none select-text"
      />
      <label htmlFor={`comment-${editing.id}`} className="text-xs text-text-muted">{t('agent.annotations.comment')}</label>
      <textarea id={`comment-${editing.id}`} aria-label={t('agent.annotations.comment')} autoFocus enterKeyHint="done" value={comment} onChange={(e) => setComment(e.target.value)}
        onKeyDown={(e) => {
          if (e.nativeEvent.isComposing || e.keyCode === 229) return
          if (e.key === 'Enter' && !e.shiftKey && !e.altKey) {
            e.preventDefault()
            e.stopPropagation()
            if (!e.repeat) confirm()
          }
        }}
        className="h-24 min-h-20 w-full shrink-0 resize-y rounded-md border border-border bg-bg-primary p-2 text-sm text-text-primary outline-none focus:border-accent"
      />
      {error && <p role="alert" className="text-xs text-destructive">{error}</p>}
    </div>
    <div className="flex shrink-0 items-center justify-end border-t border-border px-3 py-1">
      <button type="button" className={ICON_BUTTON + ' text-accent'} aria-label={t('agent.annotations.confirm')} aria-keyshortcuts="Enter" title={t('agent.annotations.confirm')} disabled={!canConfirm} onClick={confirm}><Check className="size-4" /></button>
    </div>
  </AnnotationSurface>
}

function AnnotationSurface({ position, visible, title, testID, onClose, onAfterClose, children }: {
  position: AnnotationPosition; visible: boolean; title: string; testID: string; onClose: () => void; onAfterClose?: () => void; children: ReactNode
}) {
  const { t } = useI18n()
  const mobile = useIsMobile()
  const touch = useIsTouch()
  const inset = useKeyboardInset()
  const anchorRef = useMemo(() => ({ current: { getBoundingClientRect: () => new DOMRect(position.x, position.y, 1, 1) } }), [position])
  const header = <div className="flex shrink-0 items-center justify-between gap-2 px-3 py-1">
    <span className="min-w-0 text-sm font-medium">{title}</span>
    <button type="button" className={ICON_BUTTON} onClick={onClose} aria-label={t('common.close')} title={t('common.close')}><X className="size-4" /></button>
  </div>
  const visibleRef = useRef(visible)
  visibleRef.current = visible
  const focusClose = (e: Event) => {
    e.preventDefault()
    if (visibleRef.current) onAfterClose?.()
  }
  if (mobile || touch) return <DialogPrimitive.Root open={visible} onOpenChange={(open) => { if (!open && visible) onClose() }}>
    <DialogPrimitive.Portal>
      <DialogPrimitive.Overlay className="fixed inset-0 z-50 bg-black/25" />
      <DialogPrimitive.Content data-testid={testID} aria-describedby={undefined} onCloseAutoFocus={focusClose}
        style={{ bottom: inset, maxHeight: `calc(100dvh - ${inset + 16}px)` }}
        className="fixed inset-x-0 z-[60] flex flex-col overflow-hidden rounded-t-lg border border-border bg-bg-secondary pb-[env(safe-area-inset-bottom)] text-text-primary shadow-xl outline-none">
        <DialogPrimitive.Title className="sr-only">{title}</DialogPrimitive.Title>
        {header}{children}
      </DialogPrimitive.Content>
    </DialogPrimitive.Portal>
  </DialogPrimitive.Root>
  return <PopoverPrimitive.Root open={visible} onOpenChange={(open) => { if (!open && visible) onClose() }}>
    <PopoverPrimitive.Anchor virtualRef={anchorRef} />
    <PopoverPrimitive.Portal><PopoverPrimitive.Content data-testid={testID} aria-label={title} onCloseAutoFocus={focusClose}
      side={position.y > window.innerHeight / 2 ? 'top' : 'bottom'} align="start" collisionPadding={8} sideOffset={8}
      className="z-[60] flex w-[380px] max-w-[calc(100vw-16px)] max-h-[calc(100dvh-16px)] flex-col overflow-hidden rounded-lg border border-border bg-bg-secondary text-text-primary shadow-xl outline-none">
      {header}{children}
    </PopoverPrimitive.Content></PopoverPrimitive.Portal>
  </PopoverPrimitive.Root>
}
