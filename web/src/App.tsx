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
import { parseEmbedTarget } from '@/workspace/panelRequest'
import { parsePluginViewQuery } from '@/plugin-runtime/pluginViewRequest'
import { AuthGuard } from '@/components/auth/AuthGuard'
import { LoginPage } from '@/pages/LoginPage'
import { RegisterPage } from '@/pages/RegisterPage'
import { SharePage } from '@/pages/SharePage'
import { CourtPage } from '@/pages/CourtPage'
import { StructuredPage } from '@/pages/StructuredPage'
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
  // 宿主嵌入模式（原生壳内嵌单个面板 / 单个插件视图）：
  //   · 内置面板：`?xbot_panel=<id>&xbot_embed=1`；
  //   · 插件视图：`?xbot_view=<viewId>&xbot_view_title=…&xbot_embed=1`。
  // 判定一次即可（URL 在 WebView 生命周期内不变，⛔ 不订阅 location 以免无谓重渲）。
  // ⛔ 两条**都**走嵌入（`parseEmbedTarget`，纯函数）—— 此前只认 `xbot_panel` ⇒ 插件视图
  //   落到 `<AppShell />`（整个 SPA）⇒ 原生 chrome 之下又出现 web 自己的头/底栏 = "套娃"。
  // 两条都无 ⇒ 回落完整应用（fail-safe：宁可多渲 chrome，也不给宿主渲一个空白壳）。
  const embedTarget = (() => {
    if (typeof window === 'undefined') return null
    const search = window.location.search
    const t = parseEmbedTarget(search)
    if (!t) return null
    if (t.kind === 'view') {
      // 细节（key/params/title）解析归 `pluginViewRequest.ts` 唯一真源（本文件只消费）。
      const req = parsePluginViewQuery(search)
      return { kind: 'view' as const, viewId: t.viewId, params: req?.params }
    }
    return t
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
          {/* 指挥室全屏路由：与侧边栏面板共用 CourtConsole。 */}
          <Route path="/court" element={<AuthGuard><CourtPage /></AuthGuard>} />
          {/* 结构化消息页依赖 SessionStore（activeSession）与 WSProvider。 */}
          <Route
            path="/structured"
            element={
              <AuthGuard>
                <WSProvider>
                  <SessionStoreProvider>
                    <StructuredPage />
                  </SessionStoreProvider>
                </WSProvider>
              </AuthGuard>
            }
          />
          {/* 指挥室演示路由：只用内置 seed 数据、不发起任何认证请求。
              供 e2e 截图与「未登录预览」使用；真实数据请走 /court。 */}
          <Route path="/court-demo" element={<CourtPage demo />} />
          <Route
            path="/*"
            element={
              <AuthGuard>
                <WSProvider>
                  <SessionStoreProvider>
                    <CwdProvider>
                      <PluginWidgetProvider>
                        <PluginRuntimeRoot>
                          {embedTarget ? (
                            <EmbedPanelShell target={embedTarget} />
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
