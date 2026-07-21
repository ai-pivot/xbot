/**
 * usePwaInstall — captures the BeforeInstallPrompt event and exposes
 * install/update state for the settings panel.
 *
 * - `canInstall`: true when the browser fired beforeinstallprompt (PWA
 *   meets installability criteria). False on iOS Safari (no prompt API)
 *   or when already installed.
 * - `isInstalled`: true when running in standalone mode (already installed).
 * - `install()`: triggers the native install prompt.
 * - `updateAvailable` + `refreshSW()`: from vite-plugin-pwa's useRegisterSW.
 */
import { useEffect, useState } from 'react'
import { useRegisterSW } from 'virtual:pwa-register/react'

interface BeforeInstallPromptEvent extends Event {
  prompt: () => Promise<void>
  userChoice: Promise<{ outcome: 'accepted' | 'dismissed' }>
}

export function usePwaInstall() {
  const [promptEvent, setPromptEvent] = useState<BeforeInstallPromptEvent | null>(null)
  const [error, setError] = useState<string | null>(null)
  const isInstalled = useState(() =>
    window.matchMedia('(display-mode: standalone)').matches ||
    // iOS Safari
    (window.navigator as unknown as { standalone?: boolean }).standalone === true,
  )[0]

  const {
    needRefresh: [needRefresh, setNeedRefresh],
    updateServiceWorker,
  } = useRegisterSW({
    onRegisterError(err) {
      setError(err?.message ?? 'Service Worker registration failed')
    },
  })

  useEffect(() => {
    const handler = (e: Event) => {
      e.preventDefault()
      setPromptEvent(e as BeforeInstallPromptEvent)
    }
    window.addEventListener('beforeinstallprompt', handler)
    return () => window.removeEventListener('beforeinstallprompt', handler)
  }, [])

  const install = async () => {
    if (!promptEvent) return
    await promptEvent.prompt()
    const choice = await promptEvent.userChoice
    if (choice.outcome === 'accepted') {
      setPromptEvent(null)
    }
  }

  const refreshSW = () => {
    updateServiceWorker(true).then(() => {
      setNeedRefresh(false)
      window.location.reload()
    })
  }

  return {
    canInstall: !!promptEvent && !isInstalled,
    isInstalled,
    install,
    error,
    updateAvailable: needRefresh,
    refreshSW,
  }
}
