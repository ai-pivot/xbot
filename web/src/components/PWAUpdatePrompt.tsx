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
    //
    // ⚠️ 绝不在这里 unregister：sw.js 与 sw2.js 共享 scope '/'，注册 sw2 是
    // 【替换同一注册的脚本】——安装/激活窗口内 reg.active 可能还是旧 sw.js
    //（或 null），任何"清理旧 sw.js"的 unregister 都会把刚注册的 sw2 一起
    // 注销 → 下次加载重新注册激活 → controllerchange → 再提示 → 循环刷新
    // （用户报告：手机无强刷选项，页面循环刷新）。替换语义本身已保证旧脚本
    // 不再被服务，无需手动清理。
    navigator.serviceWorker.register('/sw2.js', { scope: '/' }).then((reg) => {
      reg.update().catch(() => {})
    }).catch(() => {})
  })

  // ⚠️ Auto-reload on controller takeover: without this, the page keeps
  // running the bundle it was loaded with while the new SW's precache sits
  // idle until the NEXT manual reload — with frequent deploys the user is
  // always exactly one build behind ("部署了但前端没更新，刷新 N 次都是旧代码").
  // skipWaiting activates the new SW immediately; reloading on
  // controllerchange is the standard finish-the-update step. Loop safety:
  // this fires only when the controller ACTUALLY changes (a stable SW never
  // re-fires it — no unregister here, see above), plus a sessionStorage
  // circuit-breaker (max 2 auto-reloads / 30s) so a deploy landing mid-session
  // can't bounce the page forever; beyond that the manual SWUpdateButton wins.
  let reloaded = false
  navigator.serviceWorker.addEventListener('controllerchange', () => {
    if (reloaded) return
    reloaded = true
    window.dispatchEvent(new CustomEvent('sw-updated'))
    try {
      const KEY = 'sw-auto-reload'
      const now = Date.now()
      const hist: number[] = (JSON.parse(sessionStorage.getItem(KEY) || '[]') as number[]).filter((t) => now - t < 30_000)
      if (hist.length >= 2) return // circuit breaker → manual SWUpdateButton
      hist.push(now)
      sessionStorage.setItem(KEY, JSON.stringify(hist))
    } catch { /* storage unavailable — proceed with reload */ }
    window.location.reload()
  })
}
