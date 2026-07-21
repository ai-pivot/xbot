/**
 * PWAUpdatePrompt — listens for service worker updates and prompts the user.
 *
 * When a new version is available, shows a toast with a "Refresh" button.
 * Uses vite-plugin-pwa's `useRegisterSW` hook for auto-update lifecycle.
 */
import { useRegisterSW } from 'virtual:pwa-register/react'
import { toast } from 'sonner'

export function PWAUpdatePrompt() {
  const {
    needRefresh: [needRefresh, setNeedRefresh],
    updateServiceWorker,
  } = useRegisterSW({
    onRegisterError(error) {
      // Non-fatal — PWA is progressive enhancement, not a hard dependency.
      console.warn('SW registration failed:', error)
    },
    onOfflineReady() {
      // App is cached and ready for offline use — no action needed.
    },
  })

  if (!needRefresh) return null

  // Show a toast prompting the user to refresh.
  toast.info('有新版本可用', {
    duration: Infinity,
    action: {
      label: '刷新',
      onClick: () => {
        updateServiceWorker(true).then(() => {
          setNeedRefresh(false)
          window.location.reload()
        })
      },
    },
    cancel: {
      label: '稍后',
      onClick: () => setNeedRefresh(false),
    },
  })

  return null
}
