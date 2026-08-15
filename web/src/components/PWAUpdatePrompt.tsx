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
    // SW 文件名带版本（sw2）—— 绕开旧 sw.js 的 HTTP 启发式缓存污染：
    // server 曾长期不为 sw.js 发 Cache-Control，浏览器 heuristic 缓存了旧
    // SW 脚本（24h fresh 窗口），更新检查永远命中缓存、新 SW 永不安装 →
    // 用户永远跑旧 bundle（"修复了但完全没用"）。新 URL 无缓存 → 直接回源
    // （server 已对 SW 发 no-store）→ 安装新 precache → skipWaiting 立即接管。
    navigator.serviceWorker.register('/sw2.js', { scope: '/' }).then((reg) => {
      reg.update().catch(() => {})
      // 顺手注销可能残留的旧 SW（不同 script URL 的注册会被替换，显式清理更稳）。
      navigator.serviceWorker.getRegistrations().then((regs) => {
        regs.forEach((r) => {
          if (!r.active || (r.active.scriptURL.endsWith('/sw.js'))) r.unregister().catch(() => {})
        })
      }).catch(() => {})
    }).catch(() => {})
  })

  let reloaded = false
  navigator.serviceWorker.addEventListener('controllerchange', () => {
    if (reloaded) return
    reloaded = true
    window.dispatchEvent(new CustomEvent('sw-updated'))
  })
}
