import { useState } from 'react'
import { useTranslation } from '../i18n'
import { useNotificationContext } from '../contexts/NotificationContext'
import type { NotificationItem } from '../types'

interface NotificationPanelProps {
  open: boolean
  onClose: () => void
}

export default function NotificationPanel({ open, onClose }: NotificationPanelProps) {
  const { notifications, unreadCount, markAsRead, markAllRead, clearNotifications } = useNotificationContext()
  const [filter, setFilter] = useState<'all' | 'unread'>('all')
  const { t } = useTranslation()

  if (!open) return null

  const filtered = filter === 'unread' ? notifications.filter(n => !n.read) : notifications

  const getIcon = (type: NotificationItem['type']) => {
    switch (type) {
      case 'message': return '💬'
      case 'reply': return '↩️'
      case 'mention': return '@'
      case 'ws_connected': return '🟢'
      case 'ws_disconnected': return '🔴'
      case 'ws_reconnecting': return '🟡'
      case 'system': return '⚙️'
      default: return '📌'
    }
  }

  return (
    <div className="notification-panel" data-testid="notification-panel" role="dialog" aria-label={t('notificationCenter')}>
      {/* Header */}
      <div className="notification-header">
        <h3 className="notification-title">{t('notificationCenter')}</h3>
        <div className="notification-header-actions">
          <button onClick={markAllRead} className="notification-action-btn" title={t('markAllRead')} data-testid="mark-all-read-btn">
            ✅
          </button>
          <button onClick={clearNotifications} className="notification-action-btn" title={t('clearNotifications')} data-testid="clear-notifications-btn">
            🗑️
          </button>
          <button onClick={onClose} className="notification-close-btn" data-testid="notification-close-btn">✕</button>
        </div>
      </div>

      {/* Filter tabs */}
      <div className="notification-filters">
        <button
          className={`notification-filter-btn${filter === 'all' ? ' active' : ''}`}
          onClick={() => setFilter('all')}
          data-testid="filter-all"
        >
          {t('searchFilterAll')}
        </button>
        <button
          className={`notification-filter-btn${filter === 'unread' ? ' active' : ''}`}
          onClick={() => setFilter('unread')}
          data-testid="filter-unread"
        >
          {t('unreadCount', { count: unreadCount })}
        </button>
      </div>

      {/* Notification list */}
      <div className="notification-list">
        {filtered.length === 0 && (
          <div className="notification-empty">{t('noNotifications')}</div>
        )}
        {filtered.map(n => (
          <div
            key={n.id}
            className={`notification-item${n.read ? ' notification-read' : ''}`}
            onClick={() => markAsRead(n.id)}
            data-testid={`notification-${n.id}`}
          >
            <span className="notification-item-icon">{getIcon(n.type)}</span>
            <div className="notification-item-body">
              <div className="notification-item-title">{n.title}</div>
              <div className="notification-item-text">{n.body}</div>
              <div className="notification-item-time">{new Date(n.ts).toLocaleTimeString()}</div>
            </div>
            {!n.read && <div className="notification-unread-dot" />}
          </div>
        ))}
      </div>
    </div>
  )
}
