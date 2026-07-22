/**
 * registerSW — registers the service worker for PWA offline support.
 *
 * Update strategy: silent background download, no auto-reload.
 * - The browser checks for SW updates on navigation (built-in).
 * - When a new SW is downloaded, it enters "waiting" state.
 * - We notify the UI via a CustomEvent so a "Update" button can appear.
 * - The user clicks "Update" → SKIP_WAITING → controllerchange → reload.
 * - If the user ignores the prompt, the new SW activates automatically
 *   on the NEXT page load (browser default with skipWaiting in sw.js).
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
      // A SW may already be waiting (from a previous visit's background download).
      if (reg.waiting) {
        window.dispatchEvent(new CustomEvent('sw-update-available'))
      }

      // When the browser downloads a new SW in the background, notify the UI.
      reg.addEventListener('updatefound', () => {
        const newWorker = reg.installing
        if (!newWorker) return
        newWorker.addEventListener('statechange', () => {
          if (newWorker.state === 'installed' && navigator.serviceWorker.controller) {
            window.dispatchEvent(new CustomEvent('sw-update-available'))
          }
        })
      })
    }).catch(() => {
      // SW registration failed — PWA features unavailable, app still works.
    })
  })
}
