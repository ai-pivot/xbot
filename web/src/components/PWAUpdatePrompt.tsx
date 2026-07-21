/**
 * PWAUpdatePrompt — auto-activates new Service Worker versions.
 *
 * When vite-plugin-pwa detects a new SW version, this component
 * automatically activates it and reloads the page. No user interaction
 * needed — ensures updates are always applied on next visit.
 */
import { useRegisterSW } from 'virtual:pwa-register/react'

export function PWAUpdatePrompt() {
  useRegisterSW({
    onRegisterError(error) {
      console.warn('SW registration failed:', error)
    },
    onOfflineReady() {
      // App cached and ready for offline use.
    },
    onNeedRefresh() {
      // Auto-activate new SW and reload.
      window.location.reload()
    },
  })
  return null
}
