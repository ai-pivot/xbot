/**
 * AuthProvider — global authentication state + login/register/logout methods.
 *
 * Initialization: `POST /api/settings` (200 → logged in, 401 → not logged in)
 * since the backend has no `/api/auth/me` endpoint. The username from a
 * successful login is persisted to localStorage so it survives reloads.
 *
 * `POST /api/auth/config` → `{ invite_only: boolean }` is fetched in parallel.
 *
 * Backend contracts (channel/web/web_auth.go):
 *   POST /api/auth/register  { username, password } → { ok, user_id? }
 *   POST /api/auth/login     { username, password } → { ok, user_id? }
 *   POST /api/auth/logout    → { ok }
 *   GET  /api/auth/config    → { invite_only: boolean }
 *   GET  /api/settings        → 200 (authed) | 401 (not authed)
 */
import {
  createContext,
  useCallback,
  useEffect,
  useState,
  type ReactNode,
} from 'react'
import { postAPI } from '@/lib/api'
import { clearWebCaches } from '@/lib/webCache'

/** Current authenticated user. `username` is empty on cold reload (no /me). */
export interface AuthUser {
  username: string
}

export interface AuthContextValue {
  user: AuthUser | null
  loading: boolean
  inviteOnly: boolean
  /** First-user bootstrap: invite-only AND no web account exists yet — the
   * register page shows the "create the operator account" wizard instead of
   * a hidden route (after the first account, registration closes again). */
  bootstrap: boolean
  login: (username: string, password: string) => Promise<boolean>
  register: (username: string, password: string) => Promise<boolean>
  logout: () => Promise<void>
}

const USERNAME_KEY = 'xbot-auth-username'

export const AuthContext = createContext<AuthContextValue | undefined>(undefined)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<AuthUser | null>(null)
  const [loading, setLoading] = useState(true)
  const [inviteOnly, setInviteOnly] = useState(false)
  const [bootstrap, setBootstrap] = useState(false)

  // --- Init: check auth status + fetch auth config ---
  useEffect(() => {
    let cancelled = false
    void (async () => {
      try {
        // Fetch auth config (public, no cookie needed) + settings (checks auth) in parallel.
        const [configRes, settingsRes] = await Promise.allSettled([
          postAPI<{ invite_only?: boolean; bootstrap?: boolean }>('/api/auth/config'),
          postAPI('/api/settings'),
        ])

        if (cancelled) return

        // invite_only config + first-user bootstrap flag
        if (configRes.status === 'fulfilled') {
          setInviteOnly(Boolean(configRes.value.invite_only))
          setBootstrap(Boolean(configRes.value.bootstrap))
        }

        // Auth status: 200 → logged in, 401 → not logged in
        if (settingsRes.status === 'fulfilled') {
          const username = localStorage.getItem(USERNAME_KEY) ?? ''
          setUser({ username })
        }
      } finally {
        if (!cancelled) setLoading(false)
      }
    })()
    return () => { cancelled = true }
  }, [])

  // --- login ---
  const login = useCallback(async (username: string, password: string): Promise<boolean> => {
    try {
      await postAPI('/api/auth/login', { username, password })
      clearWebCaches()
      localStorage.setItem(USERNAME_KEY, username)
      setUser({ username })
      return true
    } catch {
      return false
    }
  }, [])

  // --- register ---
  const register = useCallback(async (username: string, password: string): Promise<boolean> => {
    try {
      await postAPI('/api/auth/register', { username, password })
      // Auto-login after successful registration.
      return await login(username, password)
    } catch {
      return false
    }
  }, [login])

  // --- logout ---
  const logout = useCallback(async () => {
    try {
      await postAPI('/api/auth/logout')
    } finally {
      clearWebCaches()
      localStorage.removeItem(USERNAME_KEY)
      setUser(null)
    }
  }, [])

  return (
    <AuthContext.Provider value={{ user, loading, inviteOnly, bootstrap, login, register, logout }}>
      {children}
    </AuthContext.Provider>
  )
}
