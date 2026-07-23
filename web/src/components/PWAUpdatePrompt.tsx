/**
 * registerSW — registers the service worker for PWA offline support.
 *
 * Update strategy: skipWaiting is enabled in the workbox config, so new SWs
 * activate automatically. We listen for `controllerchange` to know when a
 * new SW has taken over, then show a toast prompting the user to reload.
 *
 * On localhost, any existing SW is unregistered (dev environment must not
 * be intercepted by the SW's NavigationRoute).
 */
export function registerSW() {
  if (!('serviceWorker' in navigator)) return

  const isLocalhost = location.hostname === 'localhost' || location.hostname === '127.0.0.1'

  window.addEventListener('load', () => {
    if (isLocalhost) {
      navigator.serviceWorker.getRegistrations().then((regs) => {
        regs.forEach((r) => r.unregister())
      }).catch(() => {})
      return
    }

    navigator.serviceWorker.register('/sw.js', { scope: '/' }).then((reg) => {
      // Proactively check for updates on every page load — the browser's
      // built-in check only happens on navigation, so we force one here to
      // catch updates deployed since the last visit. With skipWaiting:true,
      // the new SW activates immediately, triggering controllerchange.
      reg.update().catch(() => {})
    }).catch(() => {
      // SW registration failed — PWA features unavailable, app still works.
    })
  })

  // When a new SW activates (skipWaiting is on), notify the UI.
  // The event fires once per activation — no dedup needed.
  let reloaded = false
  navigator.serviceWorker.addEventListener('controllerchange', () => {
    if (reloaded) return
    reloaded = true
    window.dispatchEvent(new CustomEvent('sw-updated'))
  })
}
