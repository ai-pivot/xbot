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
    <div className="flex items-center justify-center min-h-screen bg-slate-900 px-4">
      <div className="w-full max-w-sm">
        <div className="text-center mb-8">
          <h1 className="text-3xl font-bold text-white mb-2">🤖 {t('appName')}</h1>
          <p className="text-slate-400 text-sm">{t('appSubtitle')}</p>
        </div>

        <form
          onSubmit={handleSubmit}
          className="bg-slate-800 rounded-xl p-6 shadow-lg border border-slate-700"
        >
          <h2 className="text-lg font-semibold text-white mb-4">
            {showFeishu ? t('feishuAccountLogin') : isRegister ? t('createAccount') : t('login')}
          </h2>

          {error && (
            <div className="bg-red-900/30 border border-red-800 text-red-300 text-sm rounded-lg px-3 py-2 mb-4">
              {error}
            </div>
          )}

          {showFeishu ? (
            <>
              <div className="mb-4">
                <label className="block text-sm text-slate-300 mb-1">{t('feishuUserId')}</label>
                <input
                  type="text"
                  value={feishuUserId}
                  onChange={(e) => setFeishuUserId(e.target.value)}
                  className="w-full bg-slate-700 border border-slate-600 rounded-lg px-3 py-2 text-white placeholder-slate-400 focus:outline-none focus:border-blue-500 focus:ring-1 focus:ring-blue-500"
                  placeholder={t('feishuPlaceholder')}
                  required
                  maxLength={128}
                />
              </div>

              <div className="mb-6">
                <label className="block text-sm text-slate-300 mb-1">{t('feishuPasswordLabel')}</label>
                <input
                  type="password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  className="w-full bg-slate-700 border border-slate-600 rounded-lg px-3 py-2 text-white placeholder-slate-400 focus:outline-none focus:border-blue-500 focus:ring-1 focus:ring-blue-500"
                  placeholder={t('feishuPassword')}
                  required
                  maxLength={128}
                />
              </div>

              <p className="text-xs text-slate-500 mb-4">
                {t('feishuBindHint')}
              </p>
            </>
          ) : (
            <>
              <div className="mb-4">
                <label className="block text-sm text-slate-300 mb-1">{t('username')}</label>
                <input
                  type="text"
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  className="w-full bg-slate-700 border border-slate-600 rounded-lg px-3 py-2 text-white placeholder-slate-400 focus:outline-none focus:border-blue-500 focus:ring-1 focus:ring-blue-500"
                  placeholder={t('enterUsername')}
                  required
                  maxLength={64}
                />
              </div>

              <div className="mb-6">
                <label className="block text-sm text-slate-300 mb-1">{t('password')}</label>
                <input
                  type="password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  className="w-full bg-slate-700 border border-slate-600 rounded-lg px-3 py-2 text-white placeholder-slate-400 focus:outline-none focus:border-blue-500 focus:ring-1 focus:ring-blue-500"
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
            className="w-full bg-blue-600 hover:bg-blue-700 disabled:bg-blue-800 text-white font-medium py-2 rounded-lg transition-colors"
            aria-label={loading ? t('loading') : showFeishu ? t('feishuLogin') : isRegister ? t('register') : t('login')}
          >
            {loading ? '...' : showFeishu ? t('feishuLogin') : isRegister ? t('register') : t('login')}
          </button>

          {!showFeishu && !inviteOnly && (
            <div className="mt-4 text-center">
              <button
                type="button"
                onClick={() => { setIsRegister(!isRegister); setError('') }}
                className="text-sm text-blue-400 hover:text-blue-300"
              >
                {isRegister ? t('hasAccount') : t('noAccount')}
              </button>
            </div>
          )}

          <div className="mt-4 text-center">
            {showFeishu ? (
              <button
                type="button"
                onClick={() => { setShowFeishu(false); setError('') }}
                className="text-xs text-slate-500 hover:text-slate-400"
              >
                {t('backToPasswordLogin')}
              </button>
            ) : (
              <button
                type="button"
                onClick={() => { setShowFeishu(true); setError('') }}
                className="text-xs text-slate-500 hover:text-slate-400"
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
