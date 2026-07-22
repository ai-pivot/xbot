import { createRoot } from 'react-dom/client'
import './index.css'
import '@/i18n' // initialize i18next (side-effect import)
import App from '@/App'
import { AuthProvider } from '@/providers/AuthProvider'
import { UserSettingsProvider } from '@/providers/UserSettingsProvider'
import { ThemeProvider } from '@/providers/theme'
import { I18nProvider } from '@/providers/i18n'
import { registerSW } from '@/components/PWAUpdatePrompt'
import { toast } from 'sonner'

// Register Service Worker (PWA auto-update).
registerSW()

// Show a toast when a new SW version is downloaded (like VSCode's "Restart to update").
window.addEventListener('sw-update-available', () => {
  toast.info('有新版本可用', {
    duration: Infinity,
    action: {
      label: '更新',
      onClick: () => {
        navigator.serviceWorker.getRegistration('/').then((reg) => {
          if (reg?.waiting) {
            reg.waiting.postMessage({ type: 'SKIP_WAITING' })
          }
          window.location.reload()
        })
      },
    },
    cancel: {
      label: '稍后',
      onClick: () => {},
    },
  })
})

createRoot(document.getElementById('root')!).render(
  <AuthProvider>
    <UserSettingsProvider>
      <ThemeProvider>
        <I18nProvider>
          <App />
        </I18nProvider>
      </ThemeProvider>
    </UserSettingsProvider>
  </AuthProvider>,
)
