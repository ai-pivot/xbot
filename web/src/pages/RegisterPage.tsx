/**
 * RegisterPage — VSCode-style centered registration card.
 *
 * Username + password + confirm password. Frontend validation: password ≥ 6
 * chars, both passwords match. On success, auto-login + redirect to root.
 * Bottom shows a "login" link. Hidden when invite_only=true (route still
 * accessible but link is hidden from login page).
 */
import { useState, type FormEvent } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { Eye, EyeOff, Loader2 } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useAuth } from '@/hooks/useAuth'
import { useI18n } from '@/providers/i18n'

export function RegisterPage() {
  const { t } = useI18n()
  const { register } = useAuth()
  const navigate = useNavigate()

  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [confirmPassword, setConfirmPassword] = useState('')
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
    if (password.length < 6) {
      setError(t('auth.passwordTooShort'))
      return
    }
    if (password !== confirmPassword) {
      setError(t('auth.passwordMismatch'))
      return
    }

    setSubmitting(true)
    const ok = await register(username.trim(), password)
    setSubmitting(false)

    if (ok) {
      navigate('/', { replace: true })
    } else {
      setError(t('auth.registerFailed'))
    }
  }

  return (
    <div className="flex h-dvh w-full items-center justify-center bg-bg-primary">
      <div className="w-full max-w-sm rounded-lg border border-border bg-bg-secondary p-8 shadow-xl">
        {/* Header */}
        <div className="mb-6 text-center">
          <h1 className="text-2xl font-semibold text-text-primary">{t('auth.registerTitle')}</h1>
          <p className="mt-1 text-sm text-muted-foreground">{t('auth.registerSubtitle')}</p>
        </div>

        <form onSubmit={handleSubmit} className="flex flex-col gap-4">
          {/* Username */}
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="reg-username">{t('auth.username')}</Label>
            <Input
              id="reg-username"
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
            <Label htmlFor="reg-password">{t('auth.password')}</Label>
            <div className="relative">
              <Input
                id="reg-password"
                type={showPassword ? 'text' : 'password'}
                autoComplete="new-password"
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

          {/* Confirm Password */}
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="reg-confirm">{t('auth.confirmPassword')}</Label>
            <Input
              id="reg-confirm"
              type={showPassword ? 'text' : 'password'}
              autoComplete="new-password"
              value={confirmPassword}
              onChange={(e) => setConfirmPassword(e.target.value)}
              placeholder={t('auth.confirmPasswordPlaceholder')}
              disabled={submitting}
            />
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
              t('auth.registerButton')
            )}
          </Button>
        </form>

        {/* Login link */}
        <p className="mt-6 text-center text-sm text-muted-foreground">
          {t('auth.hasAccount')}{' '}
          <Link
            to="/login"
            className="font-medium text-accent underline-offset-4 hover:underline"
          >
            {t('auth.login')}
          </Link>
        </p>
      </div>
    </div>
  )
}
