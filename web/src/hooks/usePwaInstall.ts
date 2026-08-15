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

    // Strategy: fetch the SW script that this page's registration ACTUALLY uses
    // (reg.active.scriptURL — e.g. /sw2.js) and check whether the currently
    // loaded JS bundle appears in its precache list. If not, the server has a
    // newer build → update available.
    //
    // ⚠️ Never hardcode '/sw.js': dist dirs carry a STALE sw.js from older
    // deploys (cp -r never deletes). Comparing the current bundle against the
    // stale manifest reports "update available" forever; combined with
    // refreshSW's old unregister+nuke path this produced an endless
    // update→reload→update loop (user report, mobile has no force-refresh).
    try {
      const reg = await navigator.serviceWorker.getRegistration('/').catch(() => null)
      // 1. Trigger an update check first — a waiting/installing worker is the
      //    authoritative signal (server bytes differ from active).
      if (reg) {
        let changed = false
        const onChange = () => { changed = true }
        navigator.serviceWorker.addEventListener('controllerchange', onChange, { once: true })
        try {
          await reg.update()
          await new Promise((r) => setTimeout(r, 500))
        } finally {
          navigator.serviceWorker.removeEventListener('controllerchange', onChange)
        }
        if (changed || reg.waiting) {
          setUpdateAvailable(true)
          return true
        }
      }

      // 2. No waiting worker — compare the server's CURRENT SW manifest against
      //    the bundle this page is running. Script URL from the registration
      //    (fallback: the registered filename), fetched no-store.
      const swUrl = reg?.active?.scriptURL || new URL('/sw2.js', location.origin).href
      const serverRes = await fetch(swUrl, { cache: 'no-store' })
      if (!serverRes.ok) {
        setUpdateAvailable(false)
        return false
      }
      const serverText = await serverRes.text()
      const currentScript = document.querySelector('script[src*="assets/index-"]')
      const currentJsName = currentScript?.getAttribute('src')?.split('/').pop() || ''
      if (currentJsName && !serverText.includes(currentJsName)) {
        setUpdateAvailable(true)
        return true
      }

      setUpdateAvailable(false)
      return false
    } catch {
      setUpdateAvailable(false)
      return false
    }
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
    const reg = await navigator.serviceWorker.getRegistration('/').catch(() => null)
    if (reg?.waiting) {
      // A waiting SW exists — activate it and reload on controllerchange.
      // Set up a timeout fallback: if controllerchange doesn't fire within 3s
      // (e.g. SW crashed during activation), force a reload anyway so the
      // user is never left hanging with a non-responsive button.
      let reloaded = false
      const doReload = () => {
        if (reloaded) return
        reloaded = true
        window.location.reload()
      }
      navigator.serviceWorker.addEventListener('controllerchange', doReload, { once: true })
      setTimeout(doReload, 3000)
      reg.waiting.postMessage({ type: 'SKIP_WAITING' })
      return
    }
    // With skipWaiting=true, the new SW activates immediately and there's
    // never a waiting state. DO NOT unregister + nuke caches here: assets are
    // content-hashed and immutable, index.html is no-cache, and unregistering
    // the registration makes the NEXT load re-register → re-activate →
    // controllerchange toast → update-check mismatch → user taps 更新 again →
    // infinite loop (user report: mobile, no force-refresh option). Caching is
    // what keeps weak-network loads fast — keep it.
    await reg?.update().catch(() => {})
    if (reg?.waiting) {
      let reloaded = false
      const doReload = () => {
        if (reloaded) return
        reloaded = true
        window.location.reload()
      }
      navigator.serviceWorker.addEventListener('controllerchange', doReload, { once: true })
      setTimeout(doReload, 3000)
      reg.waiting.postMessage({ type: 'SKIP_WAITING' })
      return
    }
    // Nothing waiting: the page simply reloads — index.html (no-cache) pulls
    // the latest bundle hashes from the server.
    window.location.reload()
  }

  // Auto-detect SW updates (Apple-style: check on load + when tab regains focus).
  // When a new SW activates via skipWaiting, set updateAvailable so the UI
  // can prompt the user — no manual "check for updates" needed.
  // Dedup: track the SW version (scriptURL) so we only notify once per version.
  useEffect(() => {
    if (!('serviceWorker' in navigator)) return

    let lastSwUrl = ''

    const handler = () => {
      navigator.serviceWorker.getRegistration('/').then((reg) => {
        const swUrl = reg?.active?.scriptURL || ''
        if (swUrl && swUrl !== lastSwUrl) {
          lastSwUrl = swUrl
          setUpdateAvailable(true)
        }
      }).catch(() => {})
    }
    navigator.serviceWorker.addEventListener('controllerchange', handler)

    // Check for updates when the tab regains focus (user switches back).
    const onFocus = () => {
      navigator.serviceWorker.getRegistration('/').then((reg) => {
        reg?.update().catch(() => {})
      }).catch(() => {})
    }
    window.addEventListener('focus', onFocus)

    return () => {
      navigator.serviceWorker.removeEventListener('controllerchange', handler)
      window.removeEventListener('focus', onFocus)
    }
  }, [])

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
