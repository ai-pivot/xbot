import { Component, type ReactNode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import '@/i18n' // initialize i18next (side-effect import)
import App from '@/App'
import { AuthProvider } from '@/providers/AuthProvider'
import { UserSettingsProvider } from '@/providers/UserSettingsProvider'
import { ThemeProvider } from '@/providers/theme'
import { I18nProvider } from '@/providers/i18n'
import { registerSW } from '@/components/PWAUpdatePrompt'
import { toast } from 'sonner'

// Disable browser scroll restoration — the app manages scroll position
// (virtualizer + scheduleFollow). Browser restoration fights our scroll,
// causing the view to jump from bottom to a stale mid-page position on refresh.
if ('scrollRestoration' in history) {
  history.scrollRestoration = 'manual'
}

// Register Service Worker (PWA auto-update). Dev build has no SW (PWA disabled),
// so skip registration to avoid a 404 noise on /sw2.js.
if (import.meta.env.PROD) {
  registerSW()
}

// When a new SW activates (skipWaiting is on), prompt the user to reload.
// Apple-style: non-intrusive toast with a prominent "刷新" action.
// Auto-dismisses after 15s if the user ignores it — the update will still
// apply on the next page load (the new SW is already controlling).
//
// Dedup: swNotifiedKey tracks the SW scriptURL (changes per build) so the
// toast fires only once per SW version. On reload the module re-evaluates
// and swNotifiedKey resets to '' — but the SW is already the new version,
// so no spurious re-toast.
let swNotifiedKey = ''
window.addEventListener('sw-updated', () => {
  // Read the current SW's script URL — it changes with each build.
  // Use it as a per-version dedup key so we don't re-toast for the same SW.
  navigator.serviceWorker?.getRegistration?.('/').then((reg) => {
    const swUrl = reg?.active?.scriptURL || ''
    if (swUrl === swNotifiedKey) return // already notified for this SW version
    swNotifiedKey = swUrl
    toast.success('发现新版本', {
      duration: 15000,
      action: {
        label: '刷新',
        onClick: () => {
          window.location.reload()
        },
      },
    })
  }).catch(() => {})
})

// ─────────────────────────────────────────────────────────────
// 全局崩溃覆盖层：手机上看不到 console 时，把未捕获错误直接渲染到屏幕。
// 两类来源：
//   1. React render 阶段崩溃（hook 错误 #311 等）→ ErrorBoundary 捕获
//   2. window error / unhandled promise rejection → 注入 DOM
// 任何一类触发，都会在页面顶部渲染一个醒目的红底错误面板，替代黑屏。
// ─────────────────────────────────────────────────────────────

/** 把错误文本写入一个 fixed 覆盖层（window 错误来源，React 可能已 unmount）。 */
function showGlobalErrorOverlay(message: string) {
  let el = document.getElementById('crash-overlay')
  if (!el) {
    el = document.createElement('div')
    el.id = 'crash-overlay'
    el.style.cssText =
      'position:fixed;inset:0;background:#1e1e1e;color:#ff6b6b;padding:12px;' +
      'font-family:ui-monospace,Menlo,monospace;font-size:12px;line-height:1.5;' +
      'overflow:auto;white-space:pre-wrap;z-index:2147483647;'
    const title = document.createElement('div')
    title.textContent = '⚠ 应用崩溃（错误已捕获，滚动查看详情）'
    title.style.cssText = 'font-size:16px;font-weight:700;margin-bottom:10px;color:#fff;'
    el.appendChild(title)
    document.body.appendChild(el)
  }
  const line = document.createElement('div')
  line.textContent = message
  line.style.cssText = 'margin-bottom:16px;border-bottom:1px dashed #555;padding-bottom:16px;'
  el.appendChild(line)
}

// window 层错误（React 之外的异步错误、未捕获异常）。
window.addEventListener('error', (e) => {
  const msg = e.error instanceof Error
    ? `${e.error.message}\n\n${e.error.stack || '(no stack)'}`
    : `${e.message} @ ${e.filename}:${e.lineno}:${e.colno}`
  showGlobalErrorOverlay(`[window.onerror]\n${msg}`)
})
window.addEventListener('unhandledrejection', (e) => {
  const r = e.reason
  const msg = r instanceof Error ? `${r.message}\n\n${r.stack || '(no stack)'}` : String(r)
  showGlobalErrorOverlay(`[unhandledrejection]\n${msg}`)
})

/** React render 阶段崩溃的 ErrorBoundary。 */
class CrashBoundary extends Component<{ children: ReactNode }, { error: Error | null }> {
  state = { error: null as Error | null }

  static getDerivedStateFromError(error: Error) {
    return { error }
  }

  componentDidCatch(error: Error, info: { componentStack?: string }) {
    console.error('[CrashBoundary]', error, info)
    const msg = `${error.message}\n\n${error.stack || '(no stack)'}`
    showGlobalErrorOverlay(`[CrashBoundary]\n${msg}`)
  }

  render() {
    if (this.state.error) return null
    return this.props.children
  }
}

createRoot(document.getElementById('root')!).render(
  <CrashBoundary>
    <AuthProvider>
      <UserSettingsProvider>
        <ThemeProvider>
          <I18nProvider>
            <App />
          </I18nProvider>
        </ThemeProvider>
      </UserSettingsProvider>
    </AuthProvider>
  </CrashBoundary>,
)
