/**
 * LoginPage — VSCode-style centered login card (dark background).
 *
 * Username + password (with show/hide toggle). On success, redirects back to
 * the original page (location.state.from) or root. If `!inviteOnly`, shows a
 * register link. Error messages shown inline below the form.
 */
import { useState, type FormEvent } from 'react'
import { Link, useLocation, useNavigate } from 'react-router-dom'
import { Eye, EyeOff, Loader2 } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useAuth } from '@/hooks/useAuth'
import { useI18n } from '@/providers/i18n'

export function LoginPage() {
  const { t } = useI18n()
  const { login, inviteOnly } = useAuth()
  const navigate = useNavigate()
  const location = useLocation()

  const from = (location.state as { from?: { pathname: string } } | null)?.from?.pathname ?? '/'

  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [showPassword, setShowPassword] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    setError('')

    if (!username.trim()) {
      setError(t('auth.usernameRequired'))
      return
    }
    if (!password) {
      setError(t('auth.passwordRequired'))
      return
    }

    setSubmitting(true)
    const ok = await login(username.trim(), password)
    setSubmitting(false)

    if (ok) {
      navigate(from, { replace: true })
    } else {
      setError(t('auth.loginFailed'))
    }
  }

  return (
    <div className="flex h-dvh w-full items-center justify-center bg-bg-primary">
      <div className="w-full max-w-sm rounded-lg border border-border bg-bg-secondary p-8 shadow-xl">
        {/* Header */}
        <div className="mb-6 text-center">
          <h1 className="text-2xl font-semibold text-text-primary">{t('auth.loginTitle')}</h1>
          <p className="mt-1 text-sm text-muted-foreground">{t('auth.loginSubtitle')}</p>
        </div>

        <form onSubmit={handleSubmit} className="flex flex-col gap-4">
          {/* Username */}
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="login-username">{t('auth.username')}</Label>
            <Input
              id="login-username"
              type="text"
              autoComplete="username"
              autoFocus
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              placeholder={t('auth.usernamePlaceholder')}
              disabled={submitting}
            />
          </div>

          {/* Password */}
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="login-password">{t('auth.password')}</Label>
            <div className="relative">
              <Input
                id="login-password"
                type={showPassword ? 'text' : 'password'}
                autoComplete="current-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder={t('auth.passwordPlaceholder')}
                disabled={submitting}
                className="pr-9"
              />
              <button
                type="button"
                aria-label={showPassword ? 'Hide password' : 'Show password'}
                onClick={() => setShowPassword((s) => !s)}
                className="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground transition-colors hover:text-text-primary"
                tabIndex={-1}
              >
                {showPassword ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
              </button>
            </div>
          </div>

          {/* Error */}
          {error ? (
            <p className="text-sm text-destructive">{error}</p>
          ) : null}

          {/* Submit */}
          <Button type="submit" disabled={submitting} className="w-full">
            {submitting ? (
              <>
                <Loader2 className="size-4 animate-spin" />
                {t('common.loading')}
              </>
            ) : (
              t('auth.loginButton')
            )}
          </Button>
        </form>

        {/* Register link */}
        {!inviteOnly ? (
          <p className="mt-6 text-center text-sm text-muted-foreground">
            {t('auth.noAccount')}{' '}
            <Link
              to="/register"
              className="font-medium text-accent underline-offset-4 hover:underline"
            >
              {t('auth.register')}
            </Link>
          </p>
        ) : null}
      </div>
    </div>
  )
}
