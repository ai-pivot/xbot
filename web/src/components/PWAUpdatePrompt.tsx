/**
 * registerSW — registers the service worker and auto-updates on new versions.
 * Does NOT depend on vite-plugin-pwa's registerSW.js (which Vite 8 doesn't
 * generate reliably). Instead uses the raw Service Worker API.
 *
 * Also unregisters any stale service workers from previous PWA versions
 * (e.g. the old registerSW.js-based one) to break out of cached old HTML.
 */
export function registerSW() {
  if (!('serviceWorker' in navigator)) return

  // Don't register SW on localhost / 127.0.0.1 — it causes navigation
  // requests to hang because the SW's NavigationRoute intercepts them
  // but the precache may not have the correct assets for a dev environment.
  const isLocalhost = location.hostname === 'localhost' || location.hostname === '127.0.0.1'

  window.addEventListener('load', () => {
    // On localhost, unregister any existing SW to clean up stale caches.
    if (isLocalhost) {
      navigator.serviceWorker.getRegistrations().then((regs) => {
        regs.forEach((r) => r.unregister())
      }).catch(() => {})
      return
    }

    // Production: register and auto-update.
    navigator.serviceWorker.getRegistrations().then((regs) => {
      const oldRegs = regs.filter((r) => !r.active?.scriptURL?.endsWith('/sw.js'))
      if (oldRegs.length > 0) {
        console.info('Unregistering stale service workers:', oldRegs.map((r) => r.scope))
      }
      return Promise.all(oldRegs.map((r) => r.unregister()))
    }).then(() => {
      return navigator.serviceWorker.register('/sw.js', { scope: '/' })
    }).then((reg) => {
      setInterval(() => reg.update().catch(() => {}), 60_000)

      reg.addEventListener('updatefound', () => {
        const newWorker = reg.installing
        if (!newWorker) return
        newWorker.addEventListener('statechange', () => {
          if (newWorker.state === 'installed' && navigator.serviceWorker.controller) {
            newWorker.postMessage({ type: 'SKIP_WAITING' })
          }
        })
      })
    }).catch((err) => {
      console.warn('SW registration failed:', err)
    })

    let refreshing = false
    navigator.serviceWorker.addEventListener('controllerchange', () => {
      if (refreshing) return
      refreshing = true
      window.location.reload()
    })
  })
}
