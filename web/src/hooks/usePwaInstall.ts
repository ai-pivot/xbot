/**
 * usePwaInstall — exposes PWA install/update state for the settings panel.
 *
 * - `canInstall`: true when the browser fired beforeinstallprompt.
 * - `isInstalled`: true when running in standalone mode.
 * - `install()`: triggers the native install prompt.
 * - `updateAvailable` + `refreshSW()`: checks for SW updates and reloads.
 */
import { useEffect, useState } from 'react'

interface BeforeInstallPromptEvent extends Event {
  prompt: () => Promise<void>
  userChoice: Promise<{ outcome: 'accepted' | 'dismissed' }>
}

export function usePwaInstall() {
  const [promptEvent, setPromptEvent] = useState<BeforeInstallPromptEvent | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [updateAvailable, setUpdateAvailable] = useState(false)
  const isInstalled = useState(() =>
    window.matchMedia('(display-mode: standalone)').matches ||
    (window.navigator as unknown as { standalone?: boolean }).standalone === true,
  )[0]
  // setError is used for future SW registration error reporting.
  void setError

  useEffect(() => {
    const handler = (e: Event) => {
      e.preventDefault()
      setPromptEvent(e as BeforeInstallPromptEvent)
    }
    window.addEventListener('beforeinstallprompt', handler)
    return () => window.removeEventListener('beforeinstallprompt', handler)
  }, [])

  // Check for SW updates.
  useEffect(() => {
    if (!('serviceWorker' in navigator)) return
    navigator.serviceWorker.getRegistration('/').then((reg) => {
      if (!reg) return
      const onUpdate = () => {
        const newWorker = reg.installing
        if (newWorker) {
          newWorker.addEventListener('statechange', () => {
            if (newWorker.state === 'installed' && navigator.serviceWorker.controller) {
              setUpdateAvailable(true)
            }
          })
        }
      }
      reg.addEventListener('updatefound', onUpdate)
    }).catch(() => {})
  }, [])

  const install = async () => {
    if (!promptEvent) return
    await promptEvent.prompt()
    const choice = await promptEvent.userChoice
    if (choice.outcome === 'accepted') {
      setPromptEvent(null)
    }
  }

  const refreshSW = async () => {
    if (!('serviceWorker' in navigator)) return
    const reg = await navigator.serviceWorker.getRegistration('/')
    if (reg?.waiting) {
      reg.waiting.postMessage({ type: 'SKIP_WAITING' })
    } else {
      window.location.reload()
    }
  }

  return {
    canInstall: !!promptEvent && !isInstalled,
    isInstalled,
    install,
    error,
    updateAvailable,
    refreshSW,
  }
}
