import { useTranslation } from '../i18n'
import { useState, useEffect, useCallback, useRef } from 'react'
import { IconRefresh, IconPlus, IconSearch, IconSidebarCollapse, IconSidebarExpand } from './Icons'
import ConfirmDialog from './ConfirmDialog'

interface ChatInfo {
  chat_id: string
  label: string
  last_active: string
  preview: string
  is_current: boolean
}

interface ChatSidebarProps {
  onSwitchChat: (chatID: string) => void
  onNewChat: () => void
  currentChatID: string
  onExportMarkdown?: () => void
  onExportJSON?: () => void
  connected?: boolean
  reconnecting?: boolean
}

export default function ChatSidebar({ onSwitchChat, onNewChat: _onNewChat, currentChatID, onExportMarkdown, onExportJSON, connected = true, reconnecting = false }: ChatSidebarProps) {
  const [chats, setChats] = useState<ChatInfo[]>([])
  const [loading, setLoading] = useState(false)
  const [collapsed, setCollapsed] = useState(() => window.innerWidth < 640)
  const [renamingId, setRenamingId] = useState<string | null>(null)
  const [renameValue, setRenameValue] = useState('')
  const [searchQuery, setSearchQuery] = useState('')
  const [confirmDelete, setConfirmDelete] = useState<string | null>(null)
  const { t } = useTranslation()

  const [isMobile, setIsMobile] = useState(() => window.innerWidth < 640)
  const isMobileRef = useRef(isMobile)
  useEffect(() => { isMobileRef.current = isMobile }, [isMobile])
  useEffect(() => {
    const mql = window.matchMedia('(max-width: 640px)')
    const handler = (e: MediaQueryListEvent) => setIsMobile(e.matches)
    mql.addEventListener('change', handler)
    return () => mql.removeEventListener('change', handler)
  }, [])

  const fetchChats = useCallback(async () => {
    setLoading(true)
    try {
      const resp = await fetch('/api/chats')
      const data = await resp.json()
      if (data.ok) setChats(data.chats || [])
    } catch (err) { console.warn('[ChatSidebar] fetchChats failed:', err) }
    setLoading(false)
  }, [])

  useEffect(() => { fetchChats() }, [fetchChats])

  const handleSwitch = async (chatID: string) => {
    try {
      await fetch(`/api/chats/${encodeURIComponent(chatID)}/switch`, { method: 'POST' })
      onSwitchChat(chatID)
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
        onSwitchChat(data.chat_id)
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
      const resp = await fetch('/api/chats')
      const data = await resp.json()
      if (data.ok) {
        const remaining: ChatInfo[] = (data.chats || []).filter((c: ChatInfo) => c.chat_id !== chatID)
        setChats(remaining)
        if (chatID === currentChatID) {
          if (remaining.length > 0) {
            await fetch(`/api/chats/${encodeURIComponent(remaining[0].chat_id)}/switch`, { method: 'POST' })
            onSwitchChat(remaining[0].chat_id)
          } else {
            _onNewChat()
          }
        }
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

      {/* New Chat */}
      <button className="sidebar-new-btn" onClick={handleCreate}>
        <span style={{ fontSize: 16 }}>+</span> {t("newSession")}
      </button>

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
        ) : (
          filteredChats.map((chat) => (
            <div key={chat.chat_id} className={`sidebar-item ${chat.is_current ? 'sidebar-item-active' : ''}`} onClick={() => handleSwitch(chat.chat_id)}>
              {renamingId === chat.chat_id ? (
                <input className="sidebar-rename-input" value={renameValue} onChange={e => setRenameValue(e.target.value)} onKeyDown={e => handleRename(e, chat.chat_id)} onBlur={() => setRenamingId(null)} autoFocus onClick={e => e.stopPropagation()} />
              ) : (
                <>
                  <span className="sidebar-chatname" onDoubleClick={(e) => { e.stopPropagation(); setRenamingId(chat.chat_id); setRenameValue(chat.label) }}>{chat.label || t('unnamedSession')}</span>
                  {chat.is_current && <span className="sidebar-current-dot" />}
                </>
              )}
              {!chat.is_current && <button onClick={(e) => handleDelete(e, chat.chat_id)} className="sidebar-delete-btn" aria-label={t("deleteSession")}>×</button>}
            </div>
          ))
        )}
      </div>

      {/* Footer */}
      <div className="sidebar-footer">
        <button onClick={fetchChats} disabled={loading} className="sidebar-footer-btn"><IconRefresh className="inline" /> {loading ? '...' : t('refreshSessions')}</button>
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
