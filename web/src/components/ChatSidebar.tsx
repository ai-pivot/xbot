import { useTranslation } from '../i18n'
import { useState, useEffect, useCallback, useRef, useMemo } from 'react'
import { IconRefresh, IconPlus, IconSearch, IconSidebarCollapse, IconSidebarExpand, IconGlobe, IconChevronDown, IconFolder } from './Icons'
import ConfirmDialog from './ConfirmDialog'

interface ChatInfo {
  chat_id: string
  channel?: string
  label: string
  last_active: string
  preview: string
  is_current: boolean
}

interface ChannelInfo {
  channel: string
  label: string
}

interface ChatSidebarProps {
  onSwitchChat: (chatID: string, channel: string) => void
  onNewChat: () => void
  currentChatID: string
  currentChannel?: string
  onExportMarkdown?: () => void
  onExportJSON?: () => void
  connected?: boolean
  reconnecting?: boolean
  sessionStates?: Record<string, { busy: boolean; lastAction: string }>
  unreadSessions?: Set<string>
}

export default function ChatSidebar({ onSwitchChat, onNewChat: _onNewChat, currentChatID, currentChannel, onExportMarkdown, onExportJSON, connected = true, reconnecting = false, sessionStates, unreadSessions }: ChatSidebarProps) {
  const [chats, setChats] = useState<ChatInfo[]>([])
  const [loading, setLoading] = useState(false)
  const [collapsed, setCollapsed] = useState(() => window.innerWidth < 768)
  const [renamingId, setRenamingId] = useState<string | null>(null)
  const [renameValue, setRenameValue] = useState('')
  const [searchQuery, setSearchQuery] = useState('')
  const [confirmDelete, setConfirmDelete] = useState<string | null>(null)
  const { t } = useTranslation()

  // Cross-channel browsing: channel selector state
  const [channels, setChannels] = useState<ChannelInfo[]>([{ channel: 'web', label: 'Web' }])
  const [activeChannel, setActiveChannel] = useState('web')
  const [channelDropdownOpen, setChannelDropdownOpen] = useState(false)
  const channelDropdownRef = useRef<HTMLDivElement>(null)

  // Close channel dropdown on outside click
  useEffect(() => {
    const handler = (e: MouseEvent) => {
      if (channelDropdownRef.current && !channelDropdownRef.current.contains(e.target as Node)) {
        setChannelDropdownOpen(false)
      }
    }
    document.addEventListener('mousedown', handler)
    return () => document.removeEventListener('mousedown', handler)
  }, [])

  const [isMobile, setIsMobile] = useState(() => window.innerWidth < 768)
  const isMobileRef = useRef(isMobile)
  useEffect(() => { isMobileRef.current = isMobile }, [isMobile])
  useEffect(() => {
    const mql = window.matchMedia('(max-width: 768px)')
    const handler = (e: MediaQueryListEvent) => setIsMobile(e.matches)
    mql.addEventListener('change', handler)
    return () => mql.removeEventListener('change', handler)
  }, [])

  // Fetch available channels (admin sees all; non-admin sees only web)
  const fetchChannels = useCallback(async () => {
    try {
      const resp = await fetch('/api/channels')
      const data = await resp.json()
      if (data.ok && data.channels?.length > 0) {
        setChannels(data.channels)
      }
    } catch { /* non-admin or error — default to web only */ }
  }, [])
  useEffect(() => { fetchChannels() }, [fetchChannels])

  // Sync activeChannel from parent (e.g. on page refresh after server-side switch)
  useEffect(() => {
    if (currentChannel && currentChannel !== activeChannel) {
      setActiveChannel(currentChannel)
    }
  }, [currentChannel]) // eslint-disable-line react-hooks/exhaustive-deps

  const fetchChats = useCallback(async (channel?: string) => {
    const ch = channel || activeChannel
    setLoading(true)
    try {
      const resp = await fetch(`/api/chats${ch && ch !== 'web' ? `?channel=${encodeURIComponent(ch)}` : ''}`)
      const data = await resp.json()
      if (data.ok) setChats(data.chats || [])
    } catch (err) { console.warn('[ChatSidebar] fetchChats failed:', err) }
    setLoading(false)
  }, [activeChannel])

  useEffect(() => { fetchChats() }, [fetchChats])

  const handleSwitch = async (chatID: string) => {
    try {
      const qs = activeChannel !== 'web' ? `?channel=${encodeURIComponent(activeChannel)}` : ''
      await fetch(`/api/chats/${encodeURIComponent(chatID)}/switch${qs}`, { method: 'POST' })
      onSwitchChat(chatID, activeChannel)
      fetchChats()
      if (isMobileRef.current) setCollapsed(true)
    } catch (err) { console.warn('[ChatSidebar] switchChat failed:', err) }
  }

  const handleCreate = async () => {
    try {
      const resp = await fetch('/api/chats', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ label: '' }),
      })
      const data = await resp.json()
      if (data.ok && data.chat_id) {
        await fetch(`/api/chats/${encodeURIComponent(data.chat_id)}/switch`, { method: 'POST' })
        onSwitchChat(data.chat_id, 'web')
        fetchChats()
        if (isMobileRef.current) setCollapsed(true)
      }
    } catch (err) { console.warn('[ChatSidebar] createChat failed:', err) }
  }

  const handleDelete = (e: React.MouseEvent, chatID: string) => {
    e.stopPropagation()
    setConfirmDelete(chatID)
  }

  const executeDelete = async (chatID: string) => {
    setConfirmDelete(null)
    try {
      await fetch(`/api/chats/${encodeURIComponent(chatID)}`, { method: 'DELETE' })
      fetchChats()
      // After deleting current chat, switch to remaining one
      if (chatID === currentChatID) {
        // fetchChats will update the list; we need to wait for it to pick the first remaining
        // For now, just call onNewChat as fallback
        _onNewChat()
      }
    } catch (err) { console.warn('[ChatSidebar] deleteChat failed:', err) }
  }

  const handleRename = async (e: React.KeyboardEvent, chatID: string) => {
    if (e.key !== 'Enter') return
    const label = renameValue.trim()
    if (!label) { setRenamingId(null); return }
    try {
      await fetch(`/api/chats/${encodeURIComponent(chatID)}/rename`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ label }),
      })
      fetchChats()
    } catch (err) { console.warn('[ChatSidebar] renameChat failed:', err) }
    setRenamingId(null)
  }

  const filteredChats = searchQuery.trim()
    ? chats.filter(c => (c.label || '').toLowerCase().includes(searchQuery.toLowerCase()) || (c.preview || '').toLowerCase().includes(searchQuery.toLowerCase()))
    : chats

  // Group CLI sessions by working directory (chat_id format: "/path/to/cwd" or "/path/to/cwd:Agent-xxx")
  const groupedChats = useMemo(() => {
    if (activeChannel !== 'cli') return null
    const groups: Record<string, ChatInfo[]> = {}
    for (const chat of filteredChats) {
      const cwd = chat.chat_id.includes(':') ? chat.chat_id.split(':')[0] : chat.chat_id
      if (!groups[cwd]) groups[cwd] = []
      groups[cwd].push(chat)
    }
    // Sort groups by latest last_active
    return Object.entries(groups).sort((a, b) => {
      const aLatest = Math.max(...a[1].map(c => new Date(c.last_active).getTime() || 0))
      const bLatest = Math.max(...b[1].map(c => new Date(c.last_active).getTime() || 0))
      return bLatest - aLatest
    })
  }, [filteredChats, activeChannel])

  const renderChatItem = (chat: ChatInfo) => {
    const isBusy = sessionStates?.[chat.chat_id]?.busy ?? false
    const isUnread = unreadSessions?.has(chat.chat_id) ?? false
    const isActive = chat.is_current
    // For CLI grouped sessions, show the session name after ":" (or "main" if no colon)
    let displayName = chat.label || t('unnamedSession')
    if (activeChannel === 'cli' && chat.chat_id.includes(':')) {
      displayName = chat.chat_id.split(':').slice(1).join(':') || chat.label
    }
    return (
      <div key={chat.chat_id} className={`sidebar-item ${isActive ? 'sidebar-item-active' : ''} ${isUnread ? 'sidebar-item-unread' : ''}`} onClick={() => handleSwitch(chat.chat_id)}>
        {renamingId === chat.chat_id ? (
          <input className="sidebar-rename-input" value={renameValue} onChange={e => setRenameValue(e.target.value)} onKeyDown={e => handleRename(e, chat.chat_id)} onBlur={() => setRenamingId(null)} autoFocus onClick={e => e.stopPropagation()} />
        ) : (
          <>
            <span className={
              isBusy ? 'sidebar-busy-dot'
              : isActive ? 'sidebar-current-dot'
              : isUnread ? 'sidebar-unread-dot'
              : 'sidebar-idle-dot'
            } />
            <span className="sidebar-chatname" onDoubleClick={(e) => { e.stopPropagation(); setRenamingId(chat.chat_id); setRenameValue(chat.label) }}>{displayName}</span>
            {!isActive && activeChannel === 'web' && <button onClick={(e) => handleDelete(e, chat.chat_id)} className="sidebar-delete-btn" aria-label={t("deleteSession")}>×</button>}
            {(isActive || activeChannel !== 'web') && <span className="sidebar-delete-spacer" />}
          </>
        )}
      </div>
    )
  }

  if (collapsed) {
    const status = connected ? 'connected' : reconnecting ? 'reconnecting' : 'disconnected'
    return (
      <div className="sidebar-floating-bar">
        <span className="sidebar-floating-brand">xbot</span>
        <span className="sidebar-status-dot" data-status={status} />
        <button className="sidebar-floating-btn" onClick={() => { setCollapsed(false); fetchChats() }} title={t("expandSidebar")} aria-label={t("expandSidebar")}>
          <IconSidebarExpand />
        </button>
        <button className="sidebar-floating-btn" onClick={handleCreate} title={t("newSession")} aria-label={t("newSession")}>
          <IconPlus />
        </button>
        <button className="sidebar-floating-btn" onClick={() => { setCollapsed(false); fetchChats() }} title={t("searchHistory")} aria-label={t("searchHistory")}>
          <IconSearch />
        </button>
      </div>
    )
  }

  const sidebarContent = (
    <>
    <div className="sidebar-panel" role="navigation" aria-label="会话列表" data-testid="sidebar">
      {/* Brand + Status */}
      <div className="sidebar-brand">
        <span className="sidebar-brand-name">xbot</span>
        <span className="sidebar-status-dot" data-status={connected ? 'connected' : reconnecting ? 'reconnecting' : 'disconnected'} />
      </div>
      {/* Header */}
      <div className="sidebar-header">
        <span className="sidebar-header-title">{t("chatSessions")}</span>
        <div className="sidebar-header-actions">
          <button onClick={() => setCollapsed(true)} className="sidebar-btn" title={t("collapseSidebar")}><IconSidebarCollapse /></button>
        </div>
      </div>

      {/* Channel Selector dropdown (only shown if more than web) */}
      {channels.length > 1 && (
        <div className="sidebar-channel-selector" ref={channelDropdownRef}>
          <button
            className="sidebar-channel-trigger"
            onClick={() => setChannelDropdownOpen(!channelDropdownOpen)}
          >
            <IconGlobe width={14} height={14} />
            <span className="sidebar-channel-label">{channels.find(c => c.channel === activeChannel)?.label || 'Web'}</span>
            <IconChevronDown width={12} height={12} className={`sidebar-channel-chevron ${channelDropdownOpen ? 'open' : ''}`} />
          </button>
          {channelDropdownOpen && (
            <div className="sidebar-channel-dropdown">
              {channels.map(ch => (
                <button
                  key={ch.channel}
                  className={`sidebar-channel-option ${activeChannel === ch.channel ? 'active' : ''}`}
                  onClick={() => {
                    setChannelDropdownOpen(false)
                    setActiveChannel(ch.channel)
                  }}
                >
                  {ch.label}
                </button>
              ))}
            </div>
          )}
        </div>
      )}

      {/* New Chat — only for web channel (can't create sessions for other channels) */}
      {activeChannel === 'web' && (
        <button className="sidebar-new-btn" onClick={handleCreate}>
          <span style={{ fontSize: 16 }}>+</span> {t("newSession")}
        </button>
      )}

      {/* Search */}
      <div className="sidebar-search-wrap">
        <input className="sidebar-search" placeholder={t('searchPlaceholder')} value={searchQuery} onChange={e => setSearchQuery(e.target.value)} />
      </div>

      {/* Chat List */}
      <div className="sidebar-list">
        {loading ? (
          <div style={{ textAlign: 'center', padding: '24px 0', color: 'var(--text-tertiary)', fontSize: 12 }}>{t('sidebarLoading')}</div>
        ) : chats.length === 0 ? (
          <div style={{ textAlign: 'center', padding: '24px 0', color: 'var(--text-tertiary)', fontSize: 12 }}>{t('noSessions')}</div>
        ) : groupedChats ? (
          // CLI: grouped by working directory
          groupedChats.map(([cwd, sessionList]) => {
            const cwdShort = cwd.split('/').pop() || cwd
            return (
              <div key={cwd} className="sidebar-group">
                <div className="sidebar-group-header" title={cwd}>
                  <IconFolder width={13} height={13} />
                  <span className="sidebar-group-name">{cwdShort}</span>
                  <span className="sidebar-group-count">{sessionList.length}</span>
                </div>
                {sessionList.map(chat => renderChatItem(chat))}
              </div>
            )
          })
        ) : (
          filteredChats.map(chat => renderChatItem(chat))
        )}
      </div>

      {/* Footer */}
      <div className="sidebar-footer">
        <button onClick={() => fetchChats()} disabled={loading} className="sidebar-footer-btn"><IconRefresh className="inline" /> {loading ? '...' : t('refreshSessions')}</button>
        {onExportMarkdown && <button onClick={onExportMarkdown} className="sidebar-footer-btn" title={t('exportMarkdown')}>↓ MD</button>}
        {onExportJSON && <button onClick={onExportJSON} className="sidebar-footer-btn" title={t('exportJSON')}>↓ JSON</button>}
      </div>
    </div>

    {/* ConfirmDialog rendered OUTSIDE sidebar to avoid backdrop-filter containment */}
    <ConfirmDialog open={confirmDelete !== null} message="确定要删除此会话吗？此操作不可撤销。" onConfirm={() => confirmDelete && executeDelete(confirmDelete)} onCancel={() => setConfirmDelete(null)} />
  </>
  )

  if (isMobile) {
    return <div className="chat-sidebar-overlay" onClick={(e) => { if (e.target === e.currentTarget) setCollapsed(true) }}>{sidebarContent}</div>
  }

  return sidebarContent
}
