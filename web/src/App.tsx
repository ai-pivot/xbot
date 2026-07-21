/**
 * App — application root with auth routing.
 *
 * AuthProvider (mounted in main.tsx) wraps everything. BrowserRouter routes:
 *   /login, /register — public pages
 *   /* — AuthGuard → WSProvider → AppShell (WS only connects when authenticated)
 *
 * Theme/i18n providers wrap in main.tsx; TooltipProvider + Toaster wrap here.
 */
import { BrowserRouter, Routes, Route } from 'react-router-dom'
import { TooltipProvider } from '@/components/ui/tooltip'
import { Toaster } from '@/components/ui/sonner'
import { WSProvider } from '@/providers/WSProvider'
import { CwdProvider } from '@/providers/CwdProvider'
import { SessionStoreProvider } from '@/hooks/useSessionStore'
import { AppShell } from '@/layouts/AppShell'
import { AuthGuard } from '@/components/auth/AuthGuard'
import { LoginPage } from '@/pages/LoginPage'
import { RegisterPage } from '@/pages/RegisterPage'
import { PWAUpdatePrompt } from '@/components/PWAUpdatePrompt'

export default function App() {
  return (
    <TooltipProvider delayDuration={200}>
      <BrowserRouter>
        <Routes>
          <Route path="/login" element={<LoginPage />} />
          <Route path="/register" element={<RegisterPage />} />
          <Route
            path="/*"
            element={
              <AuthGuard>
                <WSProvider>
                  <SessionStoreProvider>
                    <CwdProvider>
                      <AppShell />
                    </CwdProvider>
                  </SessionStoreProvider>
                </WSProvider>
              </AuthGuard>
            }
          />
        </Routes>
      </BrowserRouter>
      <Toaster />
      <PWAUpdatePrompt />
    </TooltipProvider>
  )
}
