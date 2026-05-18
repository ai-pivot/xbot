import { createContext, useContext, useState, useCallback, useRef, type ReactNode } from 'react'
import type { NotificationItem } from '../types'

interface NotificationContextValue {
  notifications: NotificationItem[]
  unreadCount: number
  addNotification: (notification: Omit<NotificationItem, 'id' | 'ts' | 'read'>) => void
  markAsRead: (id: string) => void
  markAllRead: () => void
  clearNotifications: () => void
  removeNotification: (id: string) => void
}

const NotificationContext = createContext<NotificationContextValue | null>(null)

export function useNotificationContext(): NotificationContextValue {
  const ctx = useContext(NotificationContext)
  if (!ctx) throw new Error('useNotificationContext must be used within NotificationProvider')
  return ctx
}

export function NotificationProvider({ children }: { children: ReactNode }) {
  const [notifications, setNotifications] = useState<NotificationItem[]>([])
  const idCounterRef = useRef(0)

  const addNotification = useCallback((item: Omit<NotificationItem, 'id' | 'ts' | 'read'>) => {
    const id = `notif-${Date.now()}-${++idCounterRef.current}`
    const notification: NotificationItem = {
      ...item,
      id,
      ts: Date.now(),
      read: false,
    }
    setNotifications(prev => [notification, ...prev].slice(0, 100)) // keep last 100
  }, [])

  const markAsRead = useCallback((id: string) => {
    setNotifications(prev => prev.map(n => n.id === id ? { ...n, read: true } : n))
  }, [])

  const markAllRead = useCallback(() => {
    setNotifications(prev => prev.map(n => ({ ...n, read: true })))
  }, [])

  const clearNotifications = useCallback(() => {
    setNotifications([])
  }, [])

  const removeNotification = useCallback((id: string) => {
    setNotifications(prev => prev.filter(n => n.id !== id))
  }, [])

  const unreadCount = notifications.filter(n => !n.read).length

  return (
    <NotificationContext.Provider value={{ notifications, unreadCount, addNotification, markAsRead, markAllRead, clearNotifications, removeNotification }}>
      {children}
    </NotificationContext.Provider>
  )
}
