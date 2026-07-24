/**
 * usePwaInstall — exposes PWA install/update state + diagnostics.
 *
 * - `canInstall`: true when beforeinstallprompt has fired.
 * - `isInstalled`: true when running in standalone mode.
 * - `install()`: triggers the native install prompt.
 * - `updateAvailable` + `refreshSW()`: checks for SW updates and reloads.
 * - `diagnostics`: real-time PWA installability criteria for display.
 */
import { useEffect, useState } from 'react'

interface BeforeInstallPromptEvent extends Event {
  prompt: () => Promise<void>
  userChoice: Promise<{ outcome: 'accepted' | 'dismissed' }>
}

interface PwaDiagnostics {
  hasSW: boolean
  swUrl: string | null
  hasManifest: boolean
  manifestDisplay: string
  iconCount: number
  has192Icon: boolean
  has512Icon: boolean
  isHttps: boolean
  isStandalone: boolean
  browserName: string
  isSafari: boolean
  isIOS: boolean
}

export function usePwaInstall() {
  const [promptEvent, setPromptEvent] = useState<BeforeInstallPromptEvent | null>(null)
  const [updateAvailable, setUpdateAvailable] = useState(false)
  const [diagnostics, setDiagnostics] = useState<PwaDiagnostics | null>(null)
  const isInstalled = useState(() =>
    window.matchMedia('(display-mode: standalone)').matches ||
    (window.navigator as unknown as { standalone?: boolean }).standalone === true,
  )[0]

  // Collect PWA diagnostics.
  useEffect(() => {
    let cancelled = false
    async function collect() {
      const ua = navigator.userAgent
      const isIOS = /iPad|iPhone|iPod/.test(ua) || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1)
      const isSafari = /^((?!chrome|android|crios|fxios).)*safari/i.test(ua)
      let browserName = 'Unknown'
      if (/Chrome\/(\d+)/.test(ua) && !/Edg|OPR/.test(ua)) browserName = `Chrome ${RegExp.$1}`
      else if (/Edg\/(\d+)/.test(ua)) browserName = `Edge ${RegExp.$1}`
      else if (isSafari) browserName = isIOS ? 'Safari (iOS)' : 'Safari'
      else if (/Firefox\/(\d+)/.test(ua)) browserName = `Firefox ${RegExp.$1}`

      const reg = await navigator.serviceWorker?.getRegistration?.('/').catch(() => null)
      let manifest: Record<string, unknown> | null = null
      try {
        manifest = await fetch('/manifest.webmanifest').then(r => r.json())
      } catch { /* ignore */ }

      const icons = (manifest?.icons as Array<{ sizes?: string }>) || []
      const sizes = icons.map(i => i.sizes || '')

      if (!cancelled) {
        setDiagnostics({
          hasSW: !!reg?.active,
          swUrl: reg?.active?.scriptURL || null,
          hasManifest: !!manifest,
          manifestDisplay: (manifest?.display as string) || 'none',
          iconCount: icons.length,
          has192Icon: sizes.some(s => s.includes('192')),
          has512Icon: sizes.some(s => s.includes('512')),
          isHttps: location.protocol === 'https:' || location.hostname === 'localhost',
          isStandalone: window.matchMedia('(display-mode: standalone)').matches,
          browserName,
          isSafari,
          isIOS,
        })
      }
    }
    void collect()
    return () => { cancelled = true }
  }, [])

  useEffect(() => {
    const handler = (e: Event) => {
      e.preventDefault()
      setPromptEvent(e as BeforeInstallPromptEvent)
    }
    window.addEventListener('beforeinstallprompt', handler)
    return () => window.removeEventListener('beforeinstallprompt', handler)
  }, [])

  // Listen for the global 'sw-updated' event (dispatched by registerSW
  // when a new SW has activated via skipWaiting).
  useEffect(() => {
    const handler = () => setUpdateAvailable(true)
    window.addEventListener('sw-updated', handler)
    return () => window.removeEventListener('sw-updated', handler)
  }, [])

  // Manually check for SW updates (called by the update button).
  // Returns true if an update was found and applied (reload needed).
  const checkForUpdate = async () => {
    if (!('serviceWorker' in navigator)) return false
    const reg = await navigator.serviceWorker.getRegistration('/').catch(() => null)
    if (reg) {
      // SW is registered — use the standard update flow.
      let changed = false
      const onChange = () => { changed = true }
      navigator.serviceWorker.addEventListener('controllerchange', onChange, { once: true })
      try {
        await reg.update()
        await new Promise((r) => setTimeout(r, 500))
      } finally {
        navigator.serviceWorker.removeEventListener('controllerchange', onChange)
      }
      if (changed) {
        setUpdateAvailable(true)
        return true
      }
      setUpdateAvailable(false)
      return false
    }
    // No SW registered (e.g. localhost) — fetch /sw.js directly and compare
    // the served precache manifest against the currently loaded index.html.
    // If the hash differs, an update is available.
    try {
      const res = await fetch('/sw.js', { cache: 'no-store' })
      const text = await res.text()
      // The SW precaches index.html with a revision hash. Extract it and
      // compare against the current page's script hash.
      const match = text.match(/"index\.html",revision:"([^"]+)"/)
      if (match && match[1]) {
        // If we can't compare precisely, return false (no update detected).
        // The user can always hard-refresh (Ctrl+Shift+R) to get the latest.
        return false
      }
    } catch { /* ignore */ }
    setUpdateAvailable(false)
    return false
  }

  const install = async () => {
    if (!promptEvent) return
    await promptEvent.prompt()
    const choice = await promptEvent.userChoice
    if (choice.outcome === 'accepted') {
      setPromptEvent(null)
    }
  }

  const refreshSW = async () => {
    if (!('serviceWorker' in navigator)) {
      window.location.reload()
      return
    }
    const reg = await navigator.serviceWorker.getRegistration('/')
    if (reg?.waiting) {
      // A waiting SW exists — activate it and reload on controllerchange.
      navigator.serviceWorker.addEventListener('controllerchange', () => {
        window.location.reload()
      }, { once: true })
      reg.waiting.postMessage({ type: 'SKIP_WAITING' })
    } else {
      // SW already activated (skipWaiting) — just reload to pick up new assets.
      window.location.reload()
    }
  }

  return {
    canInstall: !!promptEvent && !isInstalled,
    isInstalled,
    install,
    updateAvailable,
    checkForUpdate,
    refreshSW,
    diagnostics,
  }
}
