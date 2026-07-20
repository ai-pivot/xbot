/**
 * SessionSidebar — the left session panel (Spec 3 §3.1).
 *
 * Replaces Spec 2's empty left-sidebar body for the "sessions" view.
 * Wires useSessionStore to the search box, category switcher, the list, and
 * the new-session dialog. Pure presentational composition on top of the store.
 */
import { useCallback, useMemo, useRef, useState } from 'react'
import { ChevronDown, Globe, LayoutGrid, Loader2, Plus, Terminal, MessageCircle, MessageSquare, Bot, Server, CheckSquare, X, Trash2 } from 'lucide-react'
import type { ComponentType, SVGProps } from 'react'
import { Button } from '@/components/ui/button'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { useI18n } from '@/providers/i18n'
import { useSessionStore } from '@/hooks/useSessionStore'
import { groupSessions, isSubAgentSession, parseAgentChatID, sameSession, sessionKey, sortSessions } from '@/lib/session-grouping'
import type { SessionCategory, SessionInfo, SessionSelector } from '@/types/shared'
import type { TabManager } from '@/hooks/useTabManager'
import { SessionSearch } from './SessionSearch'
import { SessionList } from './SessionList'
import { NewSessionDialog } from './NewSessionDialog'

type IconComponent = ComponentType<SVGProps<SVGSVGElement> & { size?: number | string }>

const CHANNEL_ICONS: Record<string, IconComponent> = {
  web: Globe,
  cli: Terminal,
  feishu: MessageCircle,
  qq: MessageSquare,
  napcat: Bot,
  system: Server,
}

/** All channels that should appear in the picker, in display order. */
const ALL_CHANNEL_ORDER = ['web', 'cli', 'feishu', 'qq', 'napcat']

const CATEGORIES = ['time', 'status', 'path'] as const

interface SessionSidebarProps {
  /** Tab manager for opening SubAgent conversation tabs (Child 5). */
  tabManager: TabManager
}

export function SessionSidebar({ tabManager }: SessionSidebarProps) {
  const { t } = useI18n()
  const store = useSessionStore()
  const [search, setSearch] = useState('')
  const [newOpen, setNewOpen] = useState(false)
  const [channelPickerOpen, setChannelPickerOpen] = useState(false)

  // Multi-select state
  const [multiSelectMode, setMultiSelectMode] = useState(false)
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set())
  const lastSelectedKey = useRef<string | null>(null)
  const [batchDeleteOpen, setBatchDeleteOpen] = useState(false)
  const [batchBusy, setBatchBusy] = useState(false)

  // Channel-filtered sessions
  const filteredSessions = useMemo(() => {
    if (!store.activeChannel) return store.sessions
    return store.sessions.filter((s) =>
      s.channel === store.activeChannel ||
      s.parentChannel === store.activeChannel ||
      (s.children || []).some((c) => c.parentChannel === store.activeChannel)
    )
  }, [store.sessions, store.activeChannel])

  // Re-derive groups and sortedSessions for filtered sessions
  const filteredGroups = useMemo(
    () => groupSessions(filteredSessions, store.category, store.starredIds),
    [filteredSessions, store.category, store.starredIds],
  )
  const filteredSorted = useMemo(
    () => sortSessions(filteredSessions, store.starredIds),
    [filteredSessions, store.starredIds],
  )

  // Unified select handler: SubAgent clicks open a new Agent tab; main session
  // clicks switch the active chatroom as before.
  const handleSelect = useCallback(
    (id: string, channel: string) => {
      const selector = { channel: channel || 'web', chatID: id }
      const matched = findSessionInTree(store.sessions, selector) ?? store.subAgents.find((sa) => sameSession(sa, selector))
      if (matched && isSubAgentSession(matched)) {
        const subAgent = withParsedAgentFields(matched)
        tabManager.openTab({
          type: 'agent',
          title: subAgentTitle(subAgent),
          icon: 'bot',
          closable: true,
          data: {
            subAgentRole: subAgent.role,
            subAgentInstance: subAgent.instance,
            parentChatID: subAgent.parentChatID,
            parentChannel: subAgent.parentChannel,
            agentChatID: subAgent.fullKey || subAgent.agentChatID,
          },
        })
      } else {
        void store.switchSession(id, channel)
      }
    },
    [store.sessions, store.subAgents, store.switchSession, tabManager],
  )

  // Multi-select toggle handler with Shift+click range support
  const handleToggleSelect = useCallback(
    (key: string, shiftKey: boolean) => {
      setSelectedIds((prev) => {
        // Shift+click: select range from lastSelected to current
        if (shiftKey && lastSelectedKey.current) {
          // Build ordered key list from visible main sessions
          const orderedKeys = filteredSessions
            .filter((s) => !isSubAgentSession(s) && !s.synthetic)
            .map((s) => sessionKey(s))
          const startIdx = orderedKeys.indexOf(lastSelectedKey.current)
          const endIdx = orderedKeys.indexOf(key)
          if (startIdx >= 0 && endIdx >= 0) {
            const [from, to] = startIdx <= endIdx ? [startIdx, endIdx] : [endIdx, startIdx]
            const rangeKeys = orderedKeys.slice(from, to + 1)
            const next = new Set(prev)
            const allInRange = rangeKeys.every((k) => next.has(k))
            if (allInRange) {
              rangeKeys.forEach((k) => next.delete(k))
            } else {
              rangeKeys.forEach((k) => next.add(k))
            }
            return next
          }
        }
        // Normal toggle
        const next = new Set(prev)
        if (next.has(key)) next.delete(key)
        else next.add(key)
        lastSelectedKey.current = key
        return next
      })
    },
    [filteredSessions],
  )

  const exitMultiSelect = useCallback(() => {
    setMultiSelectMode(false)
    setSelectedIds(new Set())
    lastSelectedKey.current = null
  }, [])

  // Batch delete: iterate selected sessions and delete each
  const handleBatchDelete = useCallback(async () => {
    setBatchBusy(true)
    const entries = Array.from(selectedIds)
    await Promise.all(
      entries.map((key) => {
        const [channel, ...chatIDParts] = key.split(':')
        const chatID = chatIDParts.join(':')
        return store.deleteSession(chatID, channel || 'web')
      }),
    )
    setBatchBusy(false)
    setBatchDeleteOpen(false)
    exitMultiSelect()
  }, [selectedIds, store, exitMultiSelect])

  // Select all visible main sessions
  const selectAll = useCallback(() => {
    const keys = filteredSessions
      .filter((s) => !isSubAgentSession(s) && !s.synthetic)
      .map((s) => sessionKey(s))
    setSelectedIds(new Set(keys))
  }, [filteredSessions])

  return (
    <div className="flex h-full w-full flex-col bg-bg-secondary">
      {/* Header: channel filter + new-session button */}
      <header
        className="flex h-9 shrink-0 items-center justify-between px-2"
        style={{ borderBottom: '1px solid var(--border)' }}
      >
        <div className="flex items-center gap-1">
          <Popover open={channelPickerOpen} onOpenChange={setChannelPickerOpen}>
            <PopoverTrigger asChild>
              <button
                type="button"
                className="flex items-center gap-1 rounded px-1.5 py-0.5 text-xs font-semibold uppercase tracking-wide text-text-secondary transition-colors hover:bg-bg-tertiary"
              >
                {store.activeChannel
                  ? t(`channel.${store.activeChannel}`) || store.activeChannel
                  : t('channel.all')}
                <ChevronDown className="size-3" />
              </button>
            </PopoverTrigger>
            <PopoverContent align="start" sideOffset={4} className="w-48 p-1">
              <button
                type="button"
                className={`flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-sm transition-colors hover:bg-accent/10 ${!store.activeChannel ? 'font-medium text-accent' : 'text-text-secondary'}`}
                onClick={() => { store.setActiveChannel(null); setChannelPickerOpen(false) }}
              >
                <LayoutGrid className="size-3.5 shrink-0" />
                {t('channel.all')}
              </button>
              {(() => {
                // Derive available channels from sessions list (including web).
                // 'agent' is internal — never shown as a filterable channel.
                const channels = new Set<string>()
                for (const s of store.sessions) {
                  if (s.channel && s.channel !== 'agent') channels.add(s.channel)
                  if (s.parentChannel && s.parentChannel !== 'agent') channels.add(s.parentChannel)
                }
                // Sort by predefined order, unknown channels at the end
                return Array.from(channels).sort((a, b) => {
                  const ia = ALL_CHANNEL_ORDER.indexOf(a)
                  const ib = ALL_CHANNEL_ORDER.indexOf(b)
                  return (ia === -1 ? 999 : ia) - (ib === -1 ? 999 : ib)
                })
              })()
                .map((ch: string) => {
                  const Icon = CHANNEL_ICONS[ch] || Globe
                  return (
                    <button
                      key={ch}
                      type="button"
                      className={`flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-sm transition-colors hover:bg-accent/10 ${store.activeChannel === ch ? 'font-medium text-accent' : 'text-text-secondary'}`}
                      onClick={() => { store.setActiveChannel(ch); setChannelPickerOpen(false) }}
                    >
                      <Icon className="size-3.5 shrink-0" />
                      {t(`channel.${ch}`) || ch}
                    </button>
                  )
                })}
            </PopoverContent>
          </Popover>
        </div>
        <div className="flex items-center gap-0.5">
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                variant="ghost"
                size="icon-xs"
                aria-label={t('session.multiSelect')}
                onClick={() => {
                  if (multiSelectMode) exitMultiSelect()
                  else setMultiSelectMode(true)
                }}
                style={multiSelectMode ? { color: 'var(--accent)', backgroundColor: 'var(--bg-tertiary)' } : undefined}
              >
                <CheckSquare />
              </Button>
            </TooltipTrigger>
            <TooltipContent side="bottom">{t('session.multiSelect')}</TooltipContent>
          </Tooltip>
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                variant="ghost"
                size="icon-xs"
                aria-label={t('session.newSession')}
                onClick={() => setNewOpen(true)}
              >
                <Plus />
              </Button>
            </TooltipTrigger>
            <TooltipContent side="bottom">{t('session.newSession')}</TooltipContent>
          </Tooltip>
        </div>
      </header>

      {/* Category switcher */}
      <div
        className="flex shrink-0 items-center gap-0.5 px-2 py-1"
        style={{ borderBottom: '1px solid var(--border)' }}
      >
        {CATEGORIES.map((c) => {
          const active = store.category === c
          return (
            <button
              key={c}
              type="button"
              onClick={() => store.setCategory(c)}
              className="flex-1 rounded px-2 py-1 text-[11px] font-medium transition-colors"
              style={{
                backgroundColor: active ? 'var(--bg-tertiary)' : 'transparent',
                color: active ? 'var(--text-primary)' : 'var(--text-secondary)',
              }}
            >
              {labelForCategory(c, t)}
            </button>
          )
        })}
      </div>

      {/* Search */}
      <div className="shrink-0">
        <SessionSearch value={search} onChange={setSearch} />
      </div>

      {/* List */}
      <div className="min-h-0 flex-1">
        {store.loading ? (
          <div className="flex h-full items-center justify-center gap-2 px-4 text-xs text-text-muted">
            <Loader2 className="size-3.5 animate-spin" />
            <span>{t('common.loading')}</span>
          </div>
        ) : filteredSessions.length === 0 && store.activeChannel ? (
          <div className="flex h-full items-center justify-center px-4 text-center text-xs text-text-muted">
            {t('session.noSessionsForChannel', { channel: store.activeChannel })}
          </div>
        ) : (
        <SessionList
          sessions={filteredSessions}
          groups={filteredGroups}
          sortedSessions={filteredSorted}
          category={store.category}
          starredIds={store.starredIds}
          unreadIds={store.unreadIds}
          activeSession={store.activeSession}
          search={search}
          subAgents={store.subAgents}
          onSelect={handleSelect}
          onToggleStar={store.toggleStar}
          onRename={store.renameSession}
          onDelete={store.deleteSession}
          multiSelectMode={multiSelectMode}
          selectedIds={selectedIds}
          onToggleSelect={handleToggleSelect}
        />
        )}
      </div>

      {/* Batch operation bar — shown when multi-select is active and items are selected */}
      {multiSelectMode && selectedIds.size > 0 && (
        <div
          className="flex shrink-0 items-center gap-2 px-3 py-2"
          style={{ borderTop: '1px solid var(--border)', backgroundColor: 'var(--bg-tertiary)' }}
        >
          <span className="text-xs font-medium" style={{ color: 'var(--text-secondary)' }}>
            {t('session.selectedCount', { n: selectedIds.size })}
          </span>
          <div className="flex-1" />
          <Button
            variant="ghost"
            size="sm"
            onClick={selectAll}
            className="h-7 text-xs"
          >
            {t('session.selectAll')}
          </Button>
          <Button
            variant="destructive"
            size="sm"
            onClick={() => setBatchDeleteOpen(true)}
            className="h-7 gap-1 text-xs"
          >
            <Trash2 className="size-3" />
            {t('common.delete')}
          </Button>
        </div>
      )}

      {/* Multi-select exit bar — shown when multi-select is active but nothing selected */}
      {multiSelectMode && selectedIds.size === 0 && (
        <div
          className="flex shrink-0 items-center justify-between px-3 py-2"
          style={{ borderTop: '1px solid var(--border)' }}
        >
          <span className="text-xs" style={{ color: 'var(--text-muted)' }}>
            {t('session.multiSelect')}
          </span>
          <Button
            variant="ghost"
            size="sm"
            onClick={exitMultiSelect}
            className="h-7 gap-1 text-xs"
          >
            <X className="size-3" />
            {t('session.exitMultiSelect')}
          </Button>
        </div>
      )}

      {/* Batch delete confirmation */}
      <AlertDialog open={batchDeleteOpen} onOpenChange={setBatchDeleteOpen}>
        <AlertDialogContent className="sm:max-w-sm">
          <AlertDialogHeader>
            <AlertDialogTitle>{t('session.batchDeleteTitle')}</AlertDialogTitle>
            <AlertDialogDescription>
              {t('session.batchDeleteConfirm', { n: selectedIds.size })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={batchBusy}>{t('common.cancel')}</AlertDialogCancel>
            <AlertDialogAction
              onClick={(e) => {
                e.preventDefault()
                void handleBatchDelete()
              }}
              disabled={batchBusy}
            >
              {t('common.delete')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <NewSessionDialog
        open={newOpen}
        onOpenChange={setNewOpen}
        onCreate={store.createSession}
      />
    </div>
  )
}

function findSessionInTree(sessions: SessionInfo[], selector: SessionSelector): SessionInfo | null {
  for (const session of sessions) {
    if (sameSession(session, selector)) return session
    const child = findSessionInTree(session.children || [], selector)
    if (child) return child
  }
  return null
}

function withParsedAgentFields(session: SessionInfo): SessionInfo {
  const fullKey = session.fullKey || session.agentChatID || session.chatID
  const parsed = parseAgentChatID(fullKey)
  if (!parsed) return session
  return {
    ...session,
    role: parsed.role || session.role,
    instance: parsed.instance || session.instance,
    parentChatID: parsed.parentChatID || session.parentChatID,
    parentChannel: parsed.parentChannel || session.parentChannel,
    fullKey,
    agentChatID: session.agentChatID || fullKey,
  }
}

function subAgentTitle(session: SessionInfo): string {
  if (session.role) return session.instance ? `${session.role}/${session.instance}` : session.role
  const raw = (session.label || '').trim()
  if (raw && raw !== 'default' && raw !== '默认会话') return session.label
  const parsed = parseAgentChatID(session.fullKey || session.agentChatID || session.chatID)
  if (parsed?.role) return parsed.instance ? `${parsed.role}/${parsed.instance}` : parsed.role
  return session.agentChatID || session.fullKey || session.chatID || 'SubAgent'
}

function labelForCategory(
  c: SessionCategory,
  t: (k: string) => string,
): string {
  switch (c) {
    case 'time':
      return t('session.byTime')
    case 'status':
      return t('session.byStatus')
    case 'path':
      return t('session.byPath')
  }
}
