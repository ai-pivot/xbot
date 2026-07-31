/**
 * AuthGuard — route guard that redirects unauthenticated users to /login.
 *
 * Wraps protected routes. While AuthProvider is initializing (loading=true),
 * shows a centered spinner. Once loaded, if no user → redirect to /login with
 * `state.from` so the login page can redirect back after success.
 */
import { Navigate, useLocation } from 'react-router-dom'
import type { ReactNode } from 'react'
import { Loader2 } from 'lucide-react'
import { useAuth } from '@/hooks/useAuth'

export function AuthGuard({ children }: { children: ReactNode }) {
  const { user, loading } = useAuth()
  const location = useLocation()

  if (loading) {
    return (
      <div className="flex h-dvh w-full items-center justify-center bg-bg-primary">
        <Loader2 className="size-6 animate-spin text-muted-foreground" />
      </div>
    )
  }

  if (!user) {
    return <Navigate to="/login" state={{ from: location }} replace />
  }

  return <>{children}</>
}
