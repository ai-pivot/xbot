import { useTranslation } from '../../i18n'
import { useEffect, useState, useCallback } from 'react'

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
      <p className="text-xs text-slate-500 mb-3">
        所有对话都是 ChatRoom — 人↔Agent、Agent↔Agent 统一管理。
      </p>

      <div className="flex gap-2 mb-3">
        <button
          className="settings-action-btn"
          onClick={fetchSessions}
          disabled={sessionsLoading}
        >
          {sessionsLoading ? '加载中...' : '🔄 刷新'}
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
          <div className="flex items-center gap-2 mb-3 p-2 rounded-lg bg-slate-800/50">
            <span className={`text-xs px-1.5 py-0.5 rounded ${
              selectedSession.type === 'main'
                ? 'bg-emerald-900/50 text-emerald-400'
                : 'bg-indigo-900/50 text-indigo-400'
            }`}>
              {selectedSession.type === 'main' ? '👤 主会话' : '🤖 Agent'}
            </span>
            <span className="text-sm font-medium text-slate-200">
              {selectedSession.label}
            </span>
            {selectedSession.members && (
              <span className="text-xs text-slate-500">
                {selectedSession.members}
              </span>
            )}
            {selectedSession.type !== 'main' && (
              <span className={`ml-auto text-xs px-1.5 py-0.5 rounded ${
                selectedSession.running
                  ? 'bg-green-900/50 text-green-400'
                  : 'bg-slate-700 text-slate-400'
              }`}>
                {selectedSession.running ? '运行中' : '已完成'}
              </span>
            )}
          </div>

          {sessionMessagesLoading ? (
            <div className="text-center py-4 text-slate-500 text-sm">{t('loadingDots')}</div>
          ) : sessionMessages.length === 0 ? (
            <div className="text-center py-4 text-slate-500 text-sm">{t('noMessages')}</div>
          ) : (
            <div className="space-y-2 max-h-[400px] overflow-y-auto">
              {sessionMessages.map((msg, i) => (
                <div
                  key={i}
                  className={`p-2 rounded-lg text-sm ${
                    msg.role === 'user'
                      ? 'bg-blue-900/20 border-l-2 border-blue-500 ml-4'
                      : msg.role === 'system'
                      ? 'bg-yellow-900/20 border-l-2 border-yellow-500 mr-4 text-xs'
                      : 'bg-slate-800/50 border-l-2 border-indigo-500 mr-4'
                  }`}
                >
                  <div className="text-xs text-slate-500 mb-1">
                    {msg.role === 'user' ? '👤 User' : msg.role === 'system' ? '⚙️ System' : '🤖 Assistant'}
                  </div>
                  <div className="text-slate-300 whitespace-pre-wrap break-words" style={{maxHeight: '200px', overflowY: 'auto'}}>
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
          <div className="text-center py-6 text-slate-500 text-sm">{t('loadingDots')}</div>
        ) : sessions.length === 0 ? (
          <div className="text-center py-6 text-slate-500">
            <p className="text-2xl mb-2">📭</p>
            <p className="text-sm">{t('noChatRooms')}</p>
          </div>
        ) : (
          <div className="space-y-2">
            {sessions.map((s) => (
              <div
                key={s.id}
                className="p-3 rounded-lg bg-slate-800/50 hover:bg-slate-700/50 cursor-pointer transition-colors"
                onClick={() => setSelectedSession(s)}
              >
                <div className="flex items-center gap-2">
                  <span className={`text-xs px-1.5 py-0.5 rounded ${
                    s.type === 'main'
                      ? 'bg-emerald-900/50 text-emerald-400'
                      : 'bg-indigo-900/50 text-indigo-400'
                  }`}>
                    {s.type === 'main' ? '👤' : '🤖'}
                  </span>
                  <span className="text-sm font-medium text-slate-200">{s.label}</span>
                  {s.members && (
                    <span className="text-xs text-slate-500">{s.members}</span>
                  )}
                  {s.type !== 'main' && (
                    <span className={`ml-auto text-xs px-1.5 py-0.5 rounded ${
                      s.running
                        ? 'bg-green-900/50 text-green-400'
                        : 'bg-slate-700 text-slate-400'
                    }`}>
                      {s.running ? '运行中' : '已完成'}
                    </span>
                  )}
                </div>
                {s.preview && (
                  <div className="text-xs text-slate-500 mt-1 truncate">{s.preview}</div>
                )}
              </div>
            ))}
          </div>
        )
      )}
    </div>
  )
}
