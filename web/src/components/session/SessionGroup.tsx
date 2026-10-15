/**
 * SessionGroup — a titled bucket of sessions within a category (Spec 3 §3.2).
 *
 * Renders the translated group header (time / status) and its
 * sorted SessionItem children. Collapsible so long lists stay scannable.
 */
import { ChevronRight, Plus, Pencil, GripVertical } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useI18n } from '@/providers/i18n'
import { sameSession, sessionKey } from '@/lib/session-grouping'
import type { SessionCategory, SessionInfo, SessionSelector } from '@/types/shared'
import type { SessionCustomGroup } from '@/lib/session-custom-groups'
import type { ExportFormat } from '@/components/agent/api'
import { SessionItem } from './SessionItem'
import { childrenForParent } from './session-tree'
import { AnimatedCollapse } from '@/components/ui/animated-collapse'

interface SessionGroupProps {
  customGroup?: SessionCustomGroup
  customGroups?: SessionCustomGroup[]
  groupBusy?: boolean
  onEditGroup?: () => void
  onMoveToGroup?: (session: SessionInfo, groupId: string | null) => void
  onDropGroup?: (event: React.DragEvent) => void
  onDragGroup?: (event: React.DragEvent) => void

  groupKey: string
  category: SessionCategory
  sessions: SessionInfo[]
  /** Controlled collapsed state — the store owns it so it survives remounts
   *  and is shared by every panel instance（「全部折叠/展开」也要能改它）。 */
  open: boolean
  onToggle: () => void
  onNewSession?: (workPath: string) => void
  starredIds: string[]
  unreadIds: string[]
  activeSession: SessionSelector | null
  onSelect: (id: string, channel: string) => void
  onToggleStar: (id: string) => void
  onRename: (session: SessionInfo) => void
  onDelete: (session: SessionInfo) => void
  onFork?: (session: SessionInfo) => void
  onExport?: (session: SessionInfo, format: ExportFormat) => void
  /** Multi-select mode props (passed through to SessionItem). */
  multiSelectMode?: boolean
  selectedIds?: Set<string>
  onToggleSelect?: (key: string, shiftKey: boolean) => void
  /** Drag-and-drop reorder. */
  onDragStartItem?: (key: string) => void
  onDropItem?: (targetKey: string) => void
  onDragEndItem?: () => void
}

export function SessionGroup({
  groupKey,
  customGroup, customGroups, groupBusy, onEditGroup, onMoveToGroup, onDropGroup, onDragGroup,
  category,
  sessions,
  open,
  onToggle,
  onNewSession,
  starredIds,
  unreadIds,
  activeSession,
  onSelect,
  onToggleStar,
  onRename,
  onDelete,
  onFork,
  onExport,
  multiSelectMode = false,
  selectedIds,
  onToggleSelect,
  onDragStartItem,
  onDropItem,
  onDragEndItem,
}: SessionGroupProps) {
  const { t } = useI18n()
  const title = customGroup?.name ?? groupTitle(groupKey, category, t)
  const starred = new Set(starredIds)
  const unreadSet = new Set(unreadIds)

  return (
    <section data-testid={`session-group-${groupKey}`} className="flex min-w-0 flex-col" onDragOver={event => { if (onDropGroup && !groupBusy) event.preventDefault() }} onDrop={onDropGroup}>
      {/* Independent sibling buttons keep creation separate from collapse. */}
      <div className="flex min-w-0 items-center gap-1">
        {customGroup && <button type="button" data-testid="group-drag" draggable={!groupBusy} disabled={groupBusy} onDragStart={onDragGroup} aria-label={t('session.reorderGroup')} title={t('session.reorderGroup')} className="size-6 shrink-0 cursor-grab text-text-secondary"><GripVertical className="mx-auto size-3.5" /></button>}
        <button
          type="button"
          onClick={onToggle}
          data-testid="group-header"
          aria-expanded={open}
          className="flex min-w-0 flex-1 items-center gap-1.5 rounded-md px-2 py-1.5 text-[10px] font-semibold uppercase tracking-wider transition-spring hover:bg-bg-tertiary/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-app-accent/50"
          style={{ color: 'var(--text-secondary)' }}
        >
          <ChevronRight className={cn('size-3.5 shrink-0 transition-transform duration-200', open && 'rotate-90')} />
          <span className="min-w-0 truncate" title={customGroup?.name ?? (category === 'path' && groupKey !== '__unset__' ? groupKey : undefined)}>{title}</span>
          <span
            className="shrink-0 rounded-full bg-bg-tertiary/70 px-1.5 py-px font-medium tabular-nums"
            style={{ color: 'var(--text-muted)' }}
          >
            {sessions.length}
          </span>
        </button>
        {onEditGroup && <button type="button" disabled={groupBusy} data-testid="group-edit" onClick={onEditGroup} aria-label={t('session.editGroup')} title={t('session.editGroup')} className="size-7 shrink-0 rounded-md text-text-secondary hover:bg-bg-tertiary/40 disabled:opacity-40"><Pencil className="mx-auto size-3.5" /></button>}
        {(customGroup || (category === 'path' && groupKey !== '__unset__')) && onNewSession && (
          <button
            type="button"
            disabled={groupBusy}
            aria-label={`${t('session.newSession')} — ${groupKey}`}
            title={`${t('session.newSession')} — ${groupKey}`}
            data-testid="group-new-session"
            onClick={() => onNewSession(groupKey)}
            className="flex size-7 shrink-0 items-center justify-center rounded-md text-text-secondary transition-spring hover:bg-bg-tertiary/40 hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-app-accent/50"
          >
            <Plus className="size-3.5" />
          </button>
        )}
      </div>
      <AnimatedCollapse open={open} lazy>
        <div className="flex flex-col gap-0.5">
          {sessions.map((s) => (
            <div key={sessionKey(s)} className="flex flex-col gap-0.5">
              <SessionItem
                session={s}
                customGroups={customGroups}
                groupBusy={groupBusy}
                onMoveToGroup={onMoveToGroup}
                starred={starred.has(sessionKey(s))}
                unread={unreadSet.has(sessionKey(s))}
                active={sameSession(activeSession, s)}
                onSelect={(id) => onSelect(id, s.channel)}
                onToggleStar={onToggleStar}
                onRename={onRename}
                onDelete={onDelete}
                onFork={onFork}
                onExport={onExport}
                multiSelectMode={multiSelectMode}
                selected={selectedIds?.has(sessionKey(s)) ?? false}
                onToggleSelect={onToggleSelect}
                onDragStartItem={onDragStartItem}
                onDropItem={onDropItem}
                onDragEndItem={onDragEndItem}
              />
              {/* Render SubAgent children (indented) for this parent session */}
              {childrenForParent(s).filter(isVisibleSubAgent).map((sa) => (
                <SubAgentTreeItem
                  key={sessionKey(sa)}
                  session={sa}
                  activeSession={activeSession}
                  depth={1}
                    onSelect={(id, channel) => onSelect(id, channel)}
                  onRename={onRename}
                  onDelete={onDelete}
                />
              ))}
            </div>
          ))}
        </div>
      </AnimatedCollapse>
    </section>
  )
}

function SubAgentTreeItem({
  session,
  activeSession,
  depth,
  onSelect,
  onRename,
  onDelete,
}: {
  session: SessionInfo
  activeSession: SessionSelector | null
  depth: number
  onSelect: (id: string, channel: string) => void
  onRename: (session: SessionInfo) => void
  onDelete: (session: SessionInfo) => void
}) {
  return (
    <>
      <SessionItem
        session={session}
        starred={false}
        unread={false}
        active={sameSession(activeSession, session)}
        isSubAgent
        depth={depth}
        onSelect={(id) => onSelect(id, session.channel)}
        onToggleStar={() => undefined}
        onRename={onRename}
        onDelete={onDelete}
      />
      {childrenForParent(session).filter(isVisibleSubAgent).map((child) => (
        <SubAgentTreeItem
          key={sessionKey(child)}
          session={child}
          activeSession={activeSession}
          depth={depth + 1}
          onSelect={onSelect}
          onRename={onRename}
          onDelete={onDelete}
        />
      ))}
    </>
  )
}

function isVisibleSubAgent(session: SessionInfo): boolean {
  // Always show sub-agents (running AND completed) so users can open and review
  // them. Previously this filtered to running/waiting_input/pending only — a
  // completed sub-agent vanished from the sidebar immediately, and with the
  // (now fixed) unreliable running flag even active ones could disappear.
  // Only historical legacy rows are hidden.
  return session.historical !== true
}

function groupTitle(
  key: string,
  category: SessionCategory,
  t: (k: string, p?: Record<string, string | number>) => string,
): string {
  switch (category) {
    case 'time':
      return t(`time.${key}`)
    case 'status':
      return t(`session.status.${statusKey(key)}`)
    case 'path':
      return key === '__unset__' ? t('session.unsetWorkPath') : basename(key)
  }
}

function basename(path: string): string {
  if (path === '/' || /^[A-Za-z]:[\\/]$/.test(path)) return path
  const slash = Math.max(path.lastIndexOf('/'), path.lastIndexOf('\\'))
  return slash >= 0 ? path.slice(slash + 1) : path
}

function statusKey(s: string): 'running' | 'waiting' | 'pending' | 'unread' | 'idle' | 'error' {
  if (s === 'waiting_input') return 'waiting'
  return s as 'running' | 'pending' | 'unread' | 'idle' | 'error'
}
