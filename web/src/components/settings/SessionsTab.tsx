import { useTranslation } from '../../i18n'
import { useEffect, useState, useCallback } from 'react'
import { IconRefresh, IconUser, IconBot, IconInbox, IconSettings } from '../Icons'

import type { SessionInfo, SessionMessage } from './shared'

export default function SessionsTab() {
  const [sessions, setSessions] = useState<SessionInfo[]>([])
  const [sessionsLoading, setSessionsLoading] = useState(false)
  const [selectedSession, setSelectedSession] = useState<SessionInfo | null>(null)
  const [sessionMessages, setSessionMessages] = useState<SessionMessage[]>([])
  const [sessionMessagesLoading, setSessionMessagesLoading] = useState(false)

  const fetchSessions = useCallback(async () => {
    setSessionsLoading(true)
    setSelectedSession(null)
    setSessionMessages([])
    try {
      const resp = await fetch('/api/sessions')
      const data = await resp.json()
      if (data.ok) setSessions(data.rooms || [])
    } catch (err) { console.warn('[SessionsTab] fetchSessions failed:', err) }
    setSessionsLoading(false)
  }, [])

  // Load sessions on mount
  useEffect(() => {
    fetchSessions()
  }, [fetchSessions])

  const fetchSessionMessages = useCallback(async (session: SessionInfo) => {
    setSessionMessagesLoading(true)
    setSessionMessages([])
    try {
      const resp = await fetch(`/api/sessions/messages?id=${encodeURIComponent(session.id)}`)
      const data = await resp.json()
      if (data.ok) setSessionMessages(data.messages || [])
    } catch (err) { console.warn('[SessionsTab] fetchSessionMessages failed:', err) }
    setSessionMessagesLoading(false)
  }, [])

  // Load session messages when a session is selected
  useEffect(() => {
    if (selectedSession) fetchSessionMessages(selectedSession)
  }, [selectedSession, fetchSessionMessages])

  const { t } = useTranslation()
  const sectionClass = 'settings-section'
  const sectionTitleClass = 'settings-section-title'

  return (
    <div className={sectionClass}>
      <div className={sectionTitleClass}>{t("chatRooms")}</div>
      <p className="settings-desc mb-3">
        所有对话都是 ChatRoom — 人↔Agent、Agent↔Agent 统一管理。
      </p>

      <div className="flex gap-2 mb-3">
        <button
          className="settings-action-btn"
          onClick={fetchSessions}
          disabled={sessionsLoading}
        >
          {sessionsLoading ? '加载中...' : <><IconRefresh className="inline" /> 刷新</>}
        </button>
        {selectedSession && (
          <button
            className="settings-action-btn"
            onClick={() => { setSelectedSession(null); setSessionMessages([]) }}
          >
            ← 返回列表
          </button>
        )}
      </div>

      {selectedSession ? (
        /* ── 查看 ChatRoom 消息 ── */
        <div>
          <div className="settings-session-card">
            <span className="settings-badge" style={selectedSession.type === 'main' ? { background: 'rgba(34,197,94,0.15)', color: '#16a34a' } : { background: 'rgba(99,102,241,0.15)', color: '#6366f1' }}>
              {selectedSession.type === 'main' ? <><IconUser className="inline" /> 主会话</> : <><IconBot className="inline" /> Agent</>}
            </span>
            <span className="settings-value" style={{ fontSize: 14 }}>
              {selectedSession.label}
            </span>
            {selectedSession.members && (
              <span className="settings-muted">
                {selectedSession.members}
              </span>
            )}
            {selectedSession.type !== 'main' && (
              <span className={`settings-badge ml-auto ${selectedSession.running ? 'settings-badge-active' : 'settings-badge-inactive'}`}>
                {selectedSession.running ? '运行中' : '已完成'}
              </span>
            )}
          </div>

          {sessionMessagesLoading ? (
            <div className="settings-loading" style={{ padding: '16px 0' }}>{t('loadingDots')}</div>
          ) : sessionMessages.length === 0 ? (
            <div className="settings-loading" style={{ padding: '16px 0' }}>{t('noMessages')}</div>
          ) : (
            <div className="space-y-2 max-h-[400px] overflow-y-auto">
              {sessionMessages.map((msg, i) => (
                <div
                  key={i}
                  className="settings-session-item"
                  style={{
                    borderLeftColor: msg.role === 'user' ? '#3b82f6' : msg.role === 'system' ? '#eab308' : '#6366f1',
                    marginLeft: msg.role === 'user' ? '16px' : '0',
                    marginRight: msg.role !== 'user' ? '16px' : '0',
                  }}
                >
                  <div className="settings-muted mb-1">
                    {msg.role === 'user' ? <><IconUser className="inline" /> User</> : msg.role === 'system' ? <><IconSettings className="inline" /> System</> : <><IconBot className="inline" /> Assistant</>}
                  </div>
                  <div className="settings-value" style={{ fontSize: 13, whiteSpace: 'pre-wrap', wordBreak: 'break-word', maxHeight: '200px', overflowY: 'auto' }}>
                    {msg.content}
                  </div>
                </div>
              ))}
            </div>
          )}
        </div>
      ) : (
        /* ── ChatRoom 列表 ── */
        sessionsLoading ? (
           <div className="settings-loading">{t('loadingDots')}</div>
          ) : sessions.length === 0 ? (
           <div className="settings-loading">
             <p className="text-2xl mb-2"><IconInbox className="inline" style={{width:28,height:28}} /></p>
             <p style={{ fontSize: 14 }}>{t('noChatRooms')}</p>
           </div>
          ) : (
           <div className="space-y-2">
             {sessions.map((s) => (
              <div
                key={s.id}
                className="settings-list-item"
                onClick={() => setSelectedSession(s)}
              >
                <div className="flex items-center gap-2">
                  <span className="settings-badge" style={s.type === 'main' ? { background: 'rgba(34,197,94,0.15)', color: '#16a34a' } : { background: 'rgba(99,102,241,0.15)', color: '#6366f1' }}>
                   {s.type === 'main' ? <IconUser className="inline" /> : <IconBot className="inline" />}
                  </span>
                  <span className="settings-value" style={{ fontSize: 14 }}>{s.label}</span>
                  {s.members && (
                   <span className="settings-muted">{s.members}</span>
                  )}
                  {s.type !== 'main' && (
                   <span className={`settings-badge ml-auto ${s.running ? 'settings-badge-active' : 'settings-badge-inactive'}`}>
                     {s.running ? '运行中' : '已完成'}
                   </span>
                  )}
                </div>
                {s.preview && (
                  <div className="settings-muted mt-1 truncate">{s.preview}</div>
                )}
              </div>
             ))}
           </div>
        )
      )}
    </div>
  )
}
