import { useEffect, useState } from 'react'
import LoginPage from './LoginPage'
import ChatPage from './ChatPage'
import { ToastProvider } from './contexts/ToastContext'
import { MediaPlayerProvider } from './contexts/MediaPlayerContext'
import ErrorBoundary from './components/ErrorBoundary'
import { initWebVitals } from './webVitals'
import { useTranslation } from './i18n'

// Apply saved theme immediately before React renders (prevents flash)
const savedTheme = localStorage.getItem('xbot-theme') || 'dark'
document.documentElement.setAttribute('data-theme', savedTheme)

// Initialize Web Vitals collection (dev-only logging)
initWebVitals()

function App() {
  const [authed, setAuthed] = useState<boolean | null>(null)
  const { t } = useTranslation()

  useEffect(() => {
    // Check if already logged in by trying to fetch history
    fetch('/api/history')
      .then((r) => {
        setAuthed(r.ok)
      })
      .catch(() => setAuthed(false))
  }, [])

  if (authed === null) {
    const theme = savedTheme
    const isDark = theme !== 'light'
    return (
      <ErrorBoundary>
        <MediaPlayerProvider>
          <ToastProvider>
            <div className={`flex flex-col items-center justify-center min-h-screen gap-3 ${isDark ? 'bg-slate-900 text-slate-400' : 'bg-stone-100 text-stone-400'}`}>
              <div className="w-6 h-6 border-2 border-current border-t-transparent rounded-full animate-spin" />
              <span className="text-sm">{t('loading')}</span>
            </div>
          </ToastProvider>
        </MediaPlayerProvider>
      </ErrorBoundary>
    )
  }

  return (
    <ErrorBoundary>
      <MediaPlayerProvider>
        <ToastProvider>
          {authed ? (
            <ChatPage onLogout={() => setAuthed(false)} />
          ) : (
            <LoginPage onLogin={() => setAuthed(true)} />
          )}
        </ToastProvider>
      </MediaPlayerProvider>
    </ErrorBoundary>
  )
}

export default App
