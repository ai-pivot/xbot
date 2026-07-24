/**
 * registerSW — registers the service worker for PWA offline support.
 *
 * Update strategy: skipWaiting is enabled in the workbox config, so new SWs
 * activate automatically. We listen for `controllerchange` to know when a
 * new SW has taken over, then show a toast prompting the user to reload.
 *
 * On localhost, the SW is NOT registered — the workbox NavigationRoute
 * intercepts all navigation requests and serves cached index.html, which
 * breaks API calls and dev workflow. The "check for updates" button in
 * About panel fetches /sw.js directly (no SW needed) and compares hashes.
 */
export function registerSW() {
  if (!('serviceWorker' in navigator)) return

  const isLocalhost = location.hostname === 'localhost' || location.hostname === '127.0.0.1'

  // On localhost, unregister any existing SW to prevent stale SWs from
  // intercepting API calls (the workbox NavigationRoute breaks /api/).
  if (isLocalhost) {
    navigator.serviceWorker.getRegistrations().then((regs) => {
      regs.forEach((r) => r.unregister())
    }).catch(() => {})
    return
  }

  window.addEventListener('load', () => {
    navigator.serviceWorker.register('/sw.js', { scope: '/' }).then((reg) => {
      reg.update().catch(() => {})
    }).catch(() => {})
  })

  let reloaded = false
  navigator.serviceWorker.addEventListener('controllerchange', () => {
    if (reloaded) return
    reloaded = true
    window.dispatchEvent(new CustomEvent('sw-updated'))
  })
}
