import { useEffect, useState } from 'react'
import LoginPage from './LoginPage'
import ChatPage from './ChatPage'
import { ToastProvider } from './contexts/ToastContext'
import { NotificationProvider } from './contexts/NotificationContext'
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
  const { t, setLocale } = useTranslation()

  useEffect(() => {
    // Check if already logged in by trying to fetch history
    fetch('/api/history')
      .then((r) => {
        setAuthed(r.ok)
        // Sync theme from server so it matches even if localStorage is stale
        if (r.ok) {
          fetch('/api/settings')
            .then((sr) => sr.json())
            .then((data) => {
              if (data.ok && data.settings) {
                const s = data.settings
                if (s.theme && s.theme !== savedTheme) {
                  localStorage.setItem('xbot-theme', s.theme)
                  document.documentElement.setAttribute('data-theme', s.theme)
                }
                if (s.language) {
                  localStorage.setItem('xbot-language', s.language)
                  setLocale(s.language)
                }
                if (s.font_size) localStorage.setItem('xbot-font-size', s.font_size)
                if (s.image_brightness) localStorage.setItem('xbot-image-brightness', String(s.image_brightness))
              }
            })
            .catch(() => {/* ignore */})
        }
      })
      .catch(() => setAuthed(false))
  }, [setLocale])

  if (authed === null) {
    const theme = savedTheme
    const isDark = theme !== 'light'
    return (
      <ErrorBoundary>
        <MediaPlayerProvider>
          <NotificationProvider>
            <ToastProvider>
              <div className={`flex flex-col items-center justify-center min-h-screen gap-3 ${isDark ? 'bg-slate-900 text-slate-400' : 'bg-stone-100 text-stone-400'}`}>
                <div className="w-6 h-6 border-2 border-current border-t-transparent rounded-full animate-spin" />
                <span className="text-sm">{t('loading')}</span>
              </div>
            </ToastProvider>
          </NotificationProvider>
        </MediaPlayerProvider>
      </ErrorBoundary>
    )
  }

  return (
    <ErrorBoundary>
      <MediaPlayerProvider>
        <NotificationProvider>
          <ToastProvider>
            {authed ? (
              <ChatPage onLogout={() => setAuthed(false)} />
            ) : (
              <LoginPage onLogin={() => setAuthed(true)} />
            )}
          </ToastProvider>
        </NotificationProvider>
      </MediaPlayerProvider>
    </ErrorBoundary>
  )
}

export default App
