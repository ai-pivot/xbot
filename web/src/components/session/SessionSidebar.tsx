/**
 * SessionSidebar — the left session panel (Spec 3 §3.1).
 *
 * ⚠️ 布局 v4（一切皆面板）桌面端已废弃：AppShell 左栏改用 PanelDock
 * （core.sessions 面板渲染 SessionList+SessionSearch 主体）。本容器仅剩
 * MobileAppShell（手机端抽屉）消费——手机端渠道筛选/新会话/多选批量删除
 * 等容器级功能仍在使用，勿删；子组件 SessionItem/SessionSearch/
 * SessionGroup/SessionList 已由 core.sessions 直接复用。
 *
 * 布局 v2 的「会话|面板」segmented 与面板网格视图已删除：面板在 v4 是
 * PanelDock/浮动实体（不再是 dockview tab），该视图在手机抽屉里是无回调
 * 的死 UI（MobileAppShell 从未传 onOpenPanel/pluginViews 等 props）。

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
import type { ExportFormat } from '@/components/agent/api'
import { downloadSession } from '@/components/agent/api'
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
  /** Called after a session is selected. MobileAppShell uses this to close
   * the drawer automatically after switching sessions on mobile. */
  onSessionSelected?: () => void
  /** Optional callback for SubAgent selection on clients without a Dockview
   * tab container (mobile). When provided it REPLACES tabManager.openTab for
   * SubAgent rows; the caller renders the SubAgent view itself. */
  onSubAgentSelect?: (subAgent: SessionInfo) => void
}

export function SessionSidebar({ tabManager, onSessionSelected, onSubAgentSelect }: SessionSidebarProps) {
  const { t } = useI18n()
  const store = useSessionStore()
  const [search, setSearch] = useState('')
  const [newOpen, setNewOpen] = useState(false)
  const [channelPickerOpen, setChannelPickerOpen] = useState(false)
  const [groupPickerOpen, setGroupPickerOpen] = useState(false)

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
  // clicks open/focus an agent tab for that session (desktop) or switch session
  // (mobile, no dockview tab container).
  const handleSelect = useCallback(
    (id: string, channel: string) => {
      const selector = { channel: channel || 'web', chatID: id }
      const matched = findSessionInTree(store.sessions, selector) ?? store.subAgents.find((sa) => sameSession(sa, selector))
      if (matched && isSubAgentSession(matched)) {
        const subAgent = withParsedAgentFields(matched)
        if (onSubAgentSelect) {
          // Clients without a Dockview tab container (mobile) render the
          // SubAgent view themselves.
          onSubAgentSelect(subAgent)
        } else {
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
        }
      } else {
        // Main session: desktop opens/focuses an agent tab (session-per-tab);
        // mobile (onSubAgentSelect provided) switches the single AgentPanel.
        if (onSubAgentSelect) {
          void store.switchSession(id, channel)
        } else {
          const session = matched ?? store.sessions.find((s) => s.chatID === id && s.channel === (channel || 'web'))
          tabManager.openTab({
            type: 'agent',
            title: session?.label ?? id,
            icon: 'bot',
            closable: true,
            data: {
              filePath: id,
              channel: channel || 'web',
            },
          })
          // Update sidebar highlight + backend tracking (lightweight, no cache clearing).
          store.activateSession(id, channel)
        }
      }
      onSessionSelected?.()
    },
    [store.sessions, store.subAgents, store.switchSession, store.activateSession, tabManager, onSessionSelected, onSubAgentSelect],
  )

  // Export handler: download a session in the specified format
  const handleExport = useCallback(
    async (session: SessionInfo, format: ExportFormat) => {
      const selector: SessionSelector = {
        channel: session.channel || 'web',
        chatID: session.chatID,
      }
      try {
        await downloadSession(selector, format)
      } catch (err) {
        console.error('export session failed:', err)
      }
    },
    [],
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
    try {
      const entries = Array.from(selectedIds)
      await Promise.allSettled(
        entries.map((key) => {
          const [channel, ...chatIDParts] = key.split(':')
          const chatID = chatIDParts.join(':')
          return store.deleteSession(chatID, channel || 'web')
        }),
      )
    } finally {
      setBatchBusy(false)
      setBatchDeleteOpen(false)
      exitMultiSelect()
    }
  }, [selectedIds, store, exitMultiSelect])

  // Select all visible main sessions
  const selectAll = useCallback(() => {
    const keys = filteredSessions
      .filter((s) => !isSubAgentSession(s) && !s.synthetic)
      .map((s) => sessionKey(s))
    setSelectedIds(new Set(keys))
  }, [filteredSessions])

  return (
    <div className="flex h-full w-full flex-col bg-sidebar-bg">
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
                className="flex items-center gap-1 rounded px-1.5 py-0.5 text-xs font-semibold uppercase tracking-wide text-text-secondary transition-colors hover:bg-surface-bg"
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

          {/* 分组依据下拉（时间/状态/路径）——原独立切换行收进 header，
              紧跟全部渠道选择器（VS Code 列表工具栏风格：筛选器并排） */}
          <Popover open={groupPickerOpen} onOpenChange={setGroupPickerOpen}>
            <PopoverTrigger asChild>
              <button
                type="button"
                className="flex items-center gap-1 rounded px-1.5 py-0.5 text-xs font-semibold uppercase tracking-wide text-text-secondary transition-colors hover:bg-surface-bg"
              >
                {labelForCategory(store.category, t)}
                <ChevronDown className="size-3" />
              </button>
            </PopoverTrigger>
            <PopoverContent align="start" sideOffset={4} className="w-36 p-1">
              {CATEGORIES.map((c) => (
                <button
                  key={c}
                  type="button"
                  className={`flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-sm transition-colors hover:bg-accent/10 ${store.category === c ? 'font-medium text-accent' : 'text-text-secondary'}`}
                  onClick={() => { store.setCategory(c); setGroupPickerOpen(false) }}
                >
                  {labelForCategory(c, t)}
                </button>
              ))}
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
                style={multiSelectMode ? { color: 'var(--accent)', backgroundColor: 'var(--surface-bg)' } : undefined}
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

      {/* 新建会话 + 搜索框同行（按钮左、搜索右填充剩余宽度）；
          分组依据（时间/状态/路径）已收进 header 的下拉选择 */}
      <div className="flex shrink-0 items-center gap-2 px-2.5 pt-2.5">
        <button
          type="button"
          onClick={() => setNewOpen(true)}
          className="flex shrink-0 items-center gap-1.5 rounded-xl px-3 py-2 text-[12px] font-semibold text-white transition-opacity hover:opacity-90"
          style={{ background: 'var(--accent)' }}
        >
          <Plus className="size-3.5" />
          {t('session.newSession')}
        </button>
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
          onExport={handleExport}
          onReorder={store.reorderSessions}
          multiSelectMode={multiSelectMode}
          selectedIds={selectedIds}
          onToggleSelect={handleToggleSelect}
          hasMore={store.hasMore}
          onLoadMore={store.loadMore}
        />
        )}
      </div>


      {/* Batch operation bar — shown when multi-select is active and items are selected */}
      {multiSelectMode && selectedIds.size > 0 && (
        <div
          className="flex shrink-0 items-center gap-2 px-3 py-2"
          style={{ borderTop: '1px solid var(--border)', backgroundColor: 'var(--surface-bg)' }}
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
