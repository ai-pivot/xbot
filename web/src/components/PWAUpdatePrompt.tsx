/**
 * registerSW — registers the service worker and auto-updates on new versions.
 * Does NOT depend on vite-plugin-pwa's registerSW.js (which Vite 8 doesn't
 * generate reliably). Instead uses the raw Service Worker API.
 */
export function registerSW() {
  if (!('serviceWorker' in navigator)) return

  window.addEventListener('load', () => {
    navigator.serviceWorker.register('/sw.js', { scope: '/' }).then((reg) => {
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
