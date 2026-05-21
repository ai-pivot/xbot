import { useEffect, useState } from 'react'
import { useTranslation } from './i18n'

interface LoginPageProps {
  onLogin: () => void
}

export default function LoginPage({ onLogin }: LoginPageProps) {
  const [isRegister, setIsRegister] = useState(false)
  const [showFeishu, setShowFeishu] = useState(false)
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [feishuUserId, setFeishuUserId] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const [inviteOnly, setInviteOnly] = useState(true)
  const { t } = useTranslation()

  useEffect(() => {
    fetch('/api/auth/config')
      .then(r => r.json())
      .then(data => { setInviteOnly(!!data.invite_only) })
      .catch((err) => { console.warn('[LoginPage] failed to fetch auth config:', err) })
  }, [])

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')
    setLoading(true)

    try {
      if (showFeishu) {
        const res = await fetch('/api/auth/feishu-login', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ feishu_user_id: feishuUserId, password }),
        })
        const data = await res.json()
        if (!data.ok) {
          setError(data.message || t('feishuLoginFailed'))
          return
        }
        onLogin()
        return
      }

      const url = isRegister ? '/api/auth/register' : '/api/auth/login'
      const res = await fetch(url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username, password }),
      })

      const data = await res.json()

      if (!data.ok) {
        setError(data.message || t('operationFailed'))
        return
      }

      if (isRegister) {
        const loginRes = await fetch('/api/auth/login', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ username, password }),
        })
        const loginData = await loginRes.json()
        if (!loginData.ok) {
          setError(t('registerFailed'))
          setIsRegister(false)
          return
        }
      }

      onLogin()
    } catch {
      setError(t('networkError'))
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="flex items-center justify-center min-h-screen px-4 login-container" style={{ background: 'var(--bg-base)' }}>
      <div className="w-full max-w-sm">
        <div className="text-center mb-8">
          <h1 className="text-3xl font-bold mb-2" style={{ color: 'var(--text-primary)', letterSpacing: '-0.02em' }}>🤖 {t('appName')}</h1>
          <p className="text-sm" style={{ color: 'var(--text-tertiary)' }}>{t('appSubtitle')}</p>
        </div>

        <form
          onSubmit={handleSubmit}
          className="rounded-2xl p-8 shadow-lg"
          style={{ background: 'var(--bg-secondary)', border: '0.5px solid var(--border)' }}
        >
          <h2 className="text-lg font-semibold mb-5" style={{ color: 'var(--text-primary)' }}>
            {showFeishu ? t('feishuAccountLogin') : isRegister ? t('createAccount') : t('login')}
          </h2>

          {error && (
            <div className="rounded-xl px-3 py-2 mb-4 text-sm" style={{ background: 'var(--xbot-bg-danger)', border: '0.5px solid var(--xbot-border-danger)', color: 'var(--xbot-text-danger)' }}>
              {error}
            </div>
          )}

          {showFeishu ? (
            <>
              <div className="mb-4">
                <label className="block text-sm mb-1.5" style={{ color: 'var(--text-secondary)' }}>{t('feishuUserId')}</label>
                <input
                  type="text"
                  value={feishuUserId}
                  onChange={(e) => setFeishuUserId(e.target.value)}
                  className="w-full rounded-xl px-3.5 py-3 text-sm focus:outline-none"
                  style={{ background: 'var(--xbot-bg-input)', border: '0.5px solid var(--border)', color: 'var(--text-primary)' }}
                  placeholder={t('feishuPlaceholder')}
                  required
                  maxLength={128}
                />
              </div>

              <div className="mb-6">
                <label className="block text-sm mb-1.5" style={{ color: 'var(--text-secondary)' }}>{t('feishuPasswordLabel')}</label>
                <input
                  type="password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  className="w-full rounded-xl px-3.5 py-3 text-sm focus:outline-none"
                  style={{ background: 'var(--xbot-bg-input)', border: '0.5px solid var(--border)', color: 'var(--text-primary)' }}
                  placeholder={t('feishuPassword')}
                  required
                  maxLength={128}
                />
              </div>

              <p className="text-xs mb-4" style={{ color: 'var(--text-tertiary)' }}>
                {t('feishuBindHint')}
              </p>
            </>
          ) : (
            <>
              <div className="mb-4">
                <label className="block text-sm mb-1.5" style={{ color: 'var(--text-secondary)' }}>{t('username')}</label>
                <input
                  type="text"
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  className="w-full rounded-xl px-3.5 py-3 text-sm focus:outline-none"
                  style={{ background: 'var(--xbot-bg-input)', border: '0.5px solid var(--border)', color: 'var(--text-primary)' }}
                  placeholder={t('enterUsername')}
                  required
                  maxLength={64}
                />
              </div>

              <div className="mb-6">
                <label className="block text-sm mb-1.5" style={{ color: 'var(--text-secondary)' }}>{t('password')}</label>
                <input
                  type="password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  className="w-full rounded-xl px-3.5 py-3 text-sm focus:outline-none"
                  style={{ background: 'var(--xbot-bg-input)', border: '0.5px solid var(--border)', color: 'var(--text-primary)' }}
                  placeholder={t('enterPassword')}
                  required
                  maxLength={128}
                />
              </div>
            </>
          )}

          <button
            type="submit"
            disabled={loading}
            className="w-full font-semibold py-3 rounded-xl transition-all"
            style={{ background: loading ? 'var(--text-placeholder)' : 'linear-gradient(180deg, #5bb8ff 0%, #0055b3 100%)', color: '#fff', boxShadow: loading ? 'none' : '0 1px 3px rgba(0,0,0,0.15), 0 4px 16px rgba(0,85,179,0.35)' }}
            aria-label={loading ? t('loading') : showFeishu ? t('feishuLogin') : isRegister ? t('register') : t('login')}
          >
            {loading ? '...' : showFeishu ? t('feishuLogin') : isRegister ? t('register') : t('login')}
          </button>

          {!showFeishu && !inviteOnly && (
            <div className="mt-5 text-center">
              <button
                type="button"
                onClick={() => { setIsRegister(!isRegister); setError('') }}
                className="text-sm transition-colors"
                style={{ color: 'var(--accent)' }}
              >
                {isRegister ? t('hasAccount') : t('noAccount')}
              </button>
            </div>
          )}

          <div className="mt-5 text-center">
            {showFeishu ? (
              <button
                type="button"
                onClick={() => { setShowFeishu(false); setError('') }}
                className="text-xs transition-colors"
                style={{ color: 'var(--text-tertiary)' }}
              >
                {t('backToPasswordLogin')}
              </button>
            ) : (
              <button
                type="button"
                onClick={() => { setShowFeishu(true); setError('') }}
                className="text-xs transition-colors"
                style={{ color: 'var(--text-tertiary)' }}
              >
                {t('loginViaFeishu')}
              </button>
            )}
          </div>
        </form>
      </div>
    </div>
  )
}
