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

  window.addEventListener('load', () => {
    // First, unregister ALL existing service workers. The old SW (from
    // registerSW.js) caches the old index.html and blocks updates. By
    // unregistering first, the browser fetches the live index.html which
    // contains the new <script> tags. The new SW is registered immediately
    // after to re-enable caching for future visits.
    navigator.serviceWorker.getRegistrations().then((regs) => {
      const oldRegs = regs.filter((r) => !r.active?.scriptURL?.endsWith('/sw.js'))
      if (oldRegs.length > 0) {
        console.info('Unregistering stale service workers:', oldRegs.map((r) => r.scope))
      }
      return Promise.all(oldRegs.map((r) => r.unregister()))
    }).then(() => {
      // Register the new SW.
      return navigator.serviceWorker.register('/sw.js', { scope: '/' })
    }).then((reg) => {
      // Check for updates every 60s while the page is open.
      setInterval(() => reg.update().catch(() => {}), 60_000)

      reg.addEventListener('updatefound', () => {
        const newWorker = reg.installing
        if (!newWorker) return
        newWorker.addEventListener('statechange', () => {
          // New SW installed → skip waiting → it activates → reload page.
          if (newWorker.state === 'installed' && navigator.serviceWorker.controller) {
            newWorker.postMessage({ type: 'SKIP_WAITING' })
          }
        })
      })
    }).catch((err) => {
      console.warn('SW registration failed:', err)
    })

    // When the new SW takes control, reload to load the new cached assets.
    let refreshing = false
    navigator.serviceWorker.addEventListener('controllerchange', () => {
      if (refreshing) return
      refreshing = true
      window.location.reload()
    })
  })
}
