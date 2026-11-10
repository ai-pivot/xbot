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
  useRef,
  useState,
  type ReactNode,
} from 'react'
import { toast } from 'sonner'
import i18n from '@/i18n'
import { onAPIError, postAPI } from '@/lib/api'
import { clearWebCaches } from '@/lib/webCache'
import { registerCurrentWebPush, unregisterCurrentWebPush } from '@/lib/webpush'

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
  /** Ref mirror of `user` — the API error listener runs outside render and must
   *  know whether a session existed (未登录时的探活 401 不该弹「登录已失效」). */
  const userRef = useRef<AuthUser | null>(null)

  const applyUser = useCallback((next: AuthUser | null) => {
    userRef.current = next
    setUser(next)
  }, [])

  // --- 统一错误出口：401 会话失效 / 403 无权限（收敛点，不在调用点各写一遍） ---
  useEffect(() => onAPIError((error) => {
    if (error.status === 401) {
      // 只有「本来已登录」才谈得上会话失效；白名单探活的 401 已在 api 层静默。
      if (!userRef.current) return
      // 1) 清本地登录态 → AuthGuard 检测到 user=null 即 <Navigate to="/login">
      clearWebCaches()
      try { localStorage.removeItem(USERNAME_KEY) } catch { /* storage 不可用也不影响内存态 */ }
      applyUser(null)
      // 2) 明确告诉用户「登录已失效，请重新登录」（不是「操作失败」）
      toast.error(i18n.t('auth.sessionExpired') as string, { id: 'session-expired' })
      return
    }
    if (error.status === 403) {
      // 403 不许静默：给出可见提示 + 保留服务端原因。
      const detail = error.message && error.message !== 'invalid_response' ? `：${error.message}` : ''
      toast.error(`${i18n.t('auth.forbidden') as string}${detail}`, { id: 'forbidden-error' })
    }
  }), [applyUser])

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
          applyUser({ username })
          // Re-bind an existing browser subscription to this session's user.
          void registerCurrentWebPush().catch(() => {})
        }
      } finally {
        if (!cancelled) setLoading(false)
      }
    })()
    return () => { cancelled = true }
  }, [applyUser])

  // --- login ---
  const login = useCallback(async (username: string, password: string): Promise<boolean> => {
    try {
      await postAPI('/api/auth/login', { username, password })
      clearWebCaches()
      localStorage.setItem(USERNAME_KEY, username)
      applyUser({ username })
      // Bind the existing browser subscription to the newly logged-in user.
      void registerCurrentWebPush().catch(() => {})
      return true
    } catch {
      // 允许吞：`/api/auth/login` 在 401 白名单里 → 401 = 用户名或密码错误，
      // 由 LoginPage 显示 `auth.loginFailed`，不是「登录已失效」。403 仍会走统一出口。
      return false
    }
  }, [applyUser])

  // --- register ---
  const register = useCallback(async (username: string, password: string): Promise<boolean> => {
    try {
      await postAPI('/api/auth/register', { username, password })
      // Auto-login after successful registration.
      return await login(username, password)
    } catch {
      // 允许吞：同 login —— 注册失败（含 401 白名单）由 RegisterPage 显示；
      // 403（邀请制关闭）已由统一出口提示。
      return false
    }
  }, [login])

  // --- logout ---
  const logout = useCallback(async () => {
    try {
      // Must run before the auth cookie is cleared (the unregister API is authed).
      await unregisterCurrentWebPush().catch(() => {})
      await postAPI('/api/auth/logout')
    } finally {
      clearWebCaches()
      localStorage.removeItem(USERNAME_KEY)
      applyUser(null)
    }
  }, [applyUser])

  return (
    <AuthContext.Provider value={{ user, loading, inviteOnly, bootstrap, login, register, logout }}>
      {children}
    </AuthContext.Provider>
  )
}
