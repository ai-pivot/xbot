import { createContext, useContext, useState, useCallback, useRef, useEffect, type ReactNode } from 'react'
import { IconX, IconCheck } from '../components/Icons'

export interface ToastItem {
  id: number
  message: string
  type: 'info' | 'error' | 'success'
}

interface ToastContextValue {
  toasts: ToastItem[]
  showToast: (message: string, type?: 'info' | 'error' | 'success') => void
  removeToast: (id: number) => void
}

const ToastContext = createContext<ToastContextValue | null>(null)

export function useToast(): ToastContextValue {
  const ctx = useContext(ToastContext)
  if (!ctx) throw new Error('useToast must be used within ToastProvider')
  return ctx
}

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<ToastItem[]>([])
  const timersRef = useRef<Map<number, ReturnType<typeof setTimeout>>>(new Map())

  // Cleanup all timers on unmount
  useEffect(() => {
    return () => {
      timersRef.current.forEach((timer) => clearTimeout(timer))
      timersRef.current.clear()
    }
  }, [])

  const removeToast = useCallback((id: number) => {
    setToasts(prev => prev.filter(t => t.id !== id))
    const timer = timersRef.current.get(id)
    if (timer) {
      clearTimeout(timer)
      timersRef.current.delete(id)
    }
  }, [])

  const showToast = useCallback((message: string, type: 'info' | 'error' | 'success' = 'info') => {
    const id = Date.now() + Math.random() // ensure unique id even for rapid calls
    setToasts(prev => [...prev, { id, message, type }])
    const timer = setTimeout(() => removeToast(id), 3000)
    timersRef.current.set(id, timer)
  }, [removeToast])

  return (
    <ToastContext.Provider value={{ toasts, showToast, removeToast }}>
      {children}
      {/* Unified toast rendering layer */}
      <div className="toast-container" aria-live="polite" aria-atomic="false">
        {toasts.map(toast => (
          <div
            key={toast.id}
            className={`toast-item toast-enter ${
              toast.type === 'error' ? 'toast-error' :
              toast.type === 'success' ? 'toast-success' :
              'toast-info'
            }`}
            onClick={() => removeToast(toast.id)}
            role="alert"
          >
            {toast.type === 'error' && <IconX className="inline" />}
            {toast.type === 'success' && <IconCheck className="inline" />}
            {toast.message}
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  )
}
