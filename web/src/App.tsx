/**
 * App — application root with auth routing.
 *
 * AuthProvider (mounted in main.tsx) wraps everything. BrowserRouter routes:
 *   /login, /register — public pages
 *   /* — AuthGuard → WSProvider → AppShell (WS only connects when authenticated)
 *
 * Theme/i18n providers wrap in main.tsx; TooltipProvider + Toaster wrap here.
 */
import { BrowserRouter, Routes, Route, useParams } from 'react-router-dom'
import { TooltipProvider } from '@/components/ui/tooltip'
import { Toaster } from '@/components/ui/sonner'
import { WSProvider } from '@/providers/WSProvider'
import { CwdProvider } from '@/providers/CwdProvider'
import { SessionStoreProvider } from '@/hooks/useSessionStore'
import { PluginWidgetProvider } from '@/plugins/PluginWidgetProvider'
import { PluginRuntimeRoot } from '@/plugin-runtime/usePluginRuntimeHost'
import { AppShell } from '@/layouts/AppShell'
import { EmbedPanelShell } from '@/layouts/EmbedPanelShell'
import { parseEmbedQuery, parsePanelQuery } from '@/workspace/panelRequest'
import { AuthGuard } from '@/components/auth/AuthGuard'
import { LoginPage } from '@/pages/LoginPage'
import { RegisterPage } from '@/pages/RegisterPage'
import { SharePage } from '@/pages/SharePage'
import { ImageLightboxHost } from '@/components/agent/Lightbox'
import { registerBuiltinLayoutItems } from '@/plugin-runtime/layoutRegistry'

// Register built-in layout items once at app startup (session/view buttons etc).
registerBuiltinLayoutItems()

/** 从路由参数取 token 交给 SharePage（公开页，不经 AuthGuard）。 */
function SharePageRoute() {
  const { token } = useParams<{ token: string }>()
  if (!token) return null
  return <SharePage token={token} />
}

export default function App() {
  // 宿主嵌入模式（原生壳内嵌单个面板）：`?xbot_panel=<id>&xbot_embed=1`。
  // 判定一次即可（URL 在 WebView 生命周期内不变，⛔ 不订阅 location 以免无谓重渲）。
  // 两条都得成立 ⇒ 才走嵌入；`xbot_embed=1` 但面板 id 非法 ⇒ 回落完整应用
  // （fail-safe：宁可多渲 chrome，也不给宿主渲一个空白壳）。
  const embedPanelId = (() => {
    if (typeof window === 'undefined') return null
    const search = window.location.search
    return parseEmbedQuery(search) ? parsePanelQuery(search) : null
  })()

  return (
    <TooltipProvider delayDuration={200}>
      <BrowserRouter>
        <Routes>
          <Route path="/login" element={<LoginPage />} />
          <Route path="/register" element={<RegisterPage />} />
          {/* 公开分享页：token 即凭据，**必须**在 AuthGuard 之外（打开链接的朋友没有账号）。
              未分享的内容在这条路径上不可达 —— 只有主动创建过的 artifact 才有 token。 */}
          <Route path="/s/:token" element={<SharePageRoute />} />
          <Route
            path="/*"
            element={
              <AuthGuard>
                <WSProvider>
                  <SessionStoreProvider>
                    <CwdProvider>
                      <PluginWidgetProvider>
                        <PluginRuntimeRoot>
                          {embedPanelId ? (
                            <EmbedPanelShell panelId={embedPanelId} />
                          ) : (
                            <AppShell />
                          )}
                        </PluginRuntimeRoot>
                      </PluginWidgetProvider>
                    </CwdProvider>
                  </SessionStoreProvider>
                </WSProvider>
              </AuthGuard>
            }
          />
        </Routes>
      </BrowserRouter>
      {/* Markdown image lightbox (module-level openLightbox trigger from
          MarkdownRenderer's img) — global singleton, portal to document.body. */}
      <ImageLightboxHost />
      <Toaster />
    </TooltipProvider>
  )
}
