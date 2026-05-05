import React, { useState, useEffect, useCallback } from 'react'

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
}

export default function ChatSidebar({ onSwitchChat, onNewChat: _onNewChat, currentChatID }: ChatSidebarProps) {
  const [chats, setChats] = useState<ChatInfo[]>([])
  const [loading, setLoading] = useState(false)
  const [collapsed, setCollapsed] = useState(() => window.innerWidth < 640)
  const [renamingId, setRenamingId] = useState<string | null>(null)
  const [renameValue, setRenameValue] = useState('')

  const fetchChats = useCallback(async () => {
    setLoading(true)
    try {
      const resp = await fetch('/api/chats')
      const data = await resp.json()
      if (data.ok) setChats(data.chats || [])
    } catch { /* ignored */ }
    setLoading(false)
  }, [])

  useEffect(() => { fetchChats() }, [fetchChats])

  const handleSwitch = async (chatID: string) => {
    try {
      await fetch(`/api/chats/${encodeURIComponent(chatID)}/switch`, { method: 'POST' })
      onSwitchChat(chatID)
      // Auto-collapse on mobile after switch
      if (window.innerWidth < 640) setCollapsed(true)
    } catch { /* ignored */ }
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
        if (window.innerWidth < 640) setCollapsed(true)
      }
    } catch { /* ignored */ }
  }

  const handleDelete = async (e: React.MouseEvent, chatID: string) => {
    e.stopPropagation()
    if (!confirm('确定要删除此会话吗？')) return
    try {
      await fetch(`/api/chats/${encodeURIComponent(chatID)}`, { method: 'DELETE' })
      // If deleting current chat, switch to first remaining
      if (chatID === currentChatID) {
        fetchChats()
        const remaining = chats.filter(c => c.chat_id !== chatID)
        if (remaining.length > 0) {
          await fetch(`/api/chats/${encodeURIComponent(remaining[0].chat_id)}/switch`, { method: 'POST' })
          onSwitchChat(remaining[0].chat_id)
        }
      } else {
        fetchChats()
      }
    } catch { /* ignored */ }
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
    } catch { /* ignored */ }
    setRenamingId(null)
  }

  // Mobile overlay mode
  const isMobile = typeof window !== 'undefined' && window.innerWidth < 640

  if (collapsed) {
    return (
      <button
        className="chat-sidebar-toggle"
        onClick={() => { setCollapsed(false); fetchChats() }}
        title="展开会话列表"
      >
        💬 <span className="sidebar-count">{chats.length}</span>
      </button>
    )
  }

  // Mobile: render as overlay
  if (isMobile) {
    return (
      <div className="chat-sidebar-overlay" onClick={(e) => { if (e.target === e.currentTarget) setCollapsed(true) }}>
        <div className="chat-sidebar-mobile">
          {/* Header */}
          <div className="flex items-center justify-between px-3 py-2 border-b border-slate-700/50">
            <span className="text-sm font-medium text-slate-300">💬 会话</span>
            <div className="flex items-center gap-1">
              <button onClick={handleCreate} className="sidebar-btn" title="新建会话">+</button>
              <button onClick={() => setCollapsed(true)} className="sidebar-btn" title="收起">✕</button>
            </div>
          </div>
          {/* List */}
          <div className="flex-1 overflow-y-auto py-1">
            {loading ? (
              <div className="text-center py-4 text-slate-500 text-xs">加载中...</div>
            ) : (
              chats.map((chat) => (
                <div
                  key={chat.chat_id}
                  className={`sidebar-item ${chat.is_current ? 'sidebar-item-active' : ''}`}
                  onClick={() => handleSwitch(chat.chat_id)}
                >
                  <div className="flex items-center gap-1">
                    {renamingId === chat.chat_id ? (
                      <input
                        className="sidebar-rename-input"
                        value={renameValue}
                        onChange={e => setRenameValue(e.target.value)}
                        onKeyDown={e => handleRename(e, chat.chat_id)}
                        onBlur={() => setRenamingId(null)}
                        autoFocus
                        onClick={e => e.stopPropagation()}
                      />
                    ) : (
                      <span
                        className="text-xs truncate flex-1 text-slate-300"
                        onDoubleClick={(e) => { e.stopPropagation(); setRenamingId(chat.chat_id); setRenameValue(chat.label) }}
                      >{chat.label || '未命名'}</span>
                    )}
                    {chat.is_current && <span className="text-[10px] text-indigo-400 shrink-0">●</span>}
                    {!chat.is_current && (
                      <button onClick={(e) => handleDelete(e, chat.chat_id)} className="sidebar-delete-btn">✕</button>
                    )}
                  </div>
                  {chat.preview && <div className="text-[10px] text-slate-500 mt-0.5 truncate">{chat.preview}</div>}
                </div>
              ))
            )}
          </div>
        </div>
      </div>
    )
  }

  // Desktop: inline sidebar
  return (
    <div className="flex flex-col w-56 bg-slate-900/80 border-r border-slate-700/50 shrink-0">
      {/* Header */}
      <div className="flex items-center justify-between px-3 py-2 border-b border-slate-700/50">
        <span className="text-sm font-medium text-slate-300">💬 会话</span>
        <div className="flex items-center gap-1">
          <button onClick={handleCreate} className="sidebar-btn" title="新建会话">+</button>
          <button onClick={() => setCollapsed(true)} className="sidebar-btn" title="收起">◀</button>
        </div>
      </div>

      {/* Chat List */}
      <div className="flex-1 overflow-y-auto py-1">
        {loading ? (
          <div className="text-center py-4 text-slate-500 text-xs">加载中...</div>
        ) : chats.length === 0 ? (
          <div className="text-center py-4 text-slate-500 text-xs">暂无会话</div>
        ) : (
          chats.map((chat) => (
            <div
              key={chat.chat_id}
              className={`sidebar-item group ${chat.is_current ? 'sidebar-item-active' : ''}`}
              onClick={() => handleSwitch(chat.chat_id)}
            >
              <div className="flex items-center gap-1">
                {renamingId === chat.chat_id ? (
                  <input
                    className="sidebar-rename-input"
                    value={renameValue}
                    onChange={e => setRenameValue(e.target.value)}
                    onKeyDown={e => handleRename(e, chat.chat_id)}
                    onBlur={() => setRenamingId(null)}
                    autoFocus
                    onClick={e => e.stopPropagation()}
                  />
                ) : (
                  <span
                    className="text-xs truncate flex-1 text-slate-300"
                    onDoubleClick={(e) => { e.stopPropagation(); setRenamingId(chat.chat_id); setRenameValue(chat.label) }}
                  >{chat.label || '未命名'}</span>
                )}
                {chat.is_current && (
                  <span className="text-[10px] text-indigo-400 shrink-0">当前</span>
                )}
                {!chat.is_current && (
                  <button
                    onClick={(e) => handleDelete(e, chat.chat_id)}
                    className="sidebar-delete-btn"
                  >✕</button>
                )}
              </div>
              {chat.preview && <div className="text-[10px] text-slate-500 mt-0.5 truncate">{chat.preview}</div>}
            </div>
          ))
        )}
      </div>

      {/* Refresh */}
      <div className="border-t border-slate-700/50 px-2 py-1">
        <button onClick={fetchChats} disabled={loading} className="sidebar-refresh-btn">
          {loading ? '...' : '🔄 刷新'}
        </button>
      </div>
    </div>
  )
}
