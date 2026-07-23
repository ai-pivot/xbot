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

// When a new SW activates (skipWaiting is on), prompt the user to reload.
// The event fires once per SW activation — no duplicate toasts.
window.addEventListener('sw-updated', () => {
  toast.info('应用已更新，刷新以加载新版本', {
    duration: Infinity,
    action: {
      label: '刷新',
      onClick: () => {
        window.location.reload()
      },
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
