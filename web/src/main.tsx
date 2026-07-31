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

// Disable browser scroll restoration — the app manages scroll position
// (virtualizer + scheduleFollow). Browser restoration fights our scroll,
// causing the view to jump from bottom to a stale mid-page position on refresh.
if ('scrollRestoration' in history) {
  history.scrollRestoration = 'manual'
}

// Register Service Worker (PWA auto-update).
registerSW()

// When a new SW activates (skipWaiting is on), prompt the user to reload.
// Apple-style: non-intrusive toast with a prominent "刷新" action.
// Auto-dismisses after 15s if the user ignores it — the update will still
// apply on the next page load (the new SW is already controlling).
//
// Dedup: swNotifiedKey tracks the SW scriptURL (changes per build) so the
// toast fires only once per SW version. On reload the module re-evaluates
// and swNotifiedKey resets to '' — but the SW is already the new version,
// so no spurious re-toast.
let swNotifiedKey = ''
window.addEventListener('sw-updated', () => {
  // Read the current SW's script URL — it changes with each build.
  // Use it as a per-version dedup key so we don't re-toast for the same SW.
  navigator.serviceWorker?.getRegistration?.('/').then((reg) => {
    const swUrl = reg?.active?.scriptURL || ''
    if (swUrl === swNotifiedKey) return // already notified for this SW version
    swNotifiedKey = swUrl
    toast.success('发现新版本', {
      duration: 15000,
      action: {
        label: '刷新',
        onClick: () => {
          window.location.reload()
        },
      },
    })
  }).catch(() => {})
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
