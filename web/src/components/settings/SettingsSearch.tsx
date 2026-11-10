/**
 * SettingsSearch — 设置 → 联网搜索（Tavily）。
 *
 * WebSearch 工具每次 Execute 都通过 ToolContext.ConfigGet 解析 tavily_api_key
 * （后端把它固定到 canonical channel 的同一行），所以这里保存后**下一轮搜索立即
 * 生效**，无需重启；Web / CLI / 飞书所有渠道共享同一个密钥。
 *
 * 安全契约：`get_settings` 对敏感项只返回掩码占位符（`***`）。输入框留空保存不会
 * 覆盖现有密钥（后端同样拒绝持久化掩码值）；「清除」写空串回退到 config.json 的
 * 全局默认值（如有）。
 */
import { useCallback, useEffect, useState } from 'react'

import { getSettings, setSetting } from '@/components/agent/api'
import { useWSConnection } from '@/hooks/useWSConnection'
import { useI18n } from '@/providers/i18n'

import { SettingsSection } from './SettingsSection'

const TAVILY_KEY = 'tavily_api_key'
/** Canonical namespace used by every Web settings panel (see SettingsAgent). */
const SETTINGS_NAMESPACE = 'cli'

type Flash = 'saved' | 'cleared' | null
type Busy = 'save' | 'clear' | null

export function SettingsSearch() {
  const { t } = useI18n()
  const conn = useWSConnection()
  const [configured, setConfigured] = useState(false)
  const [loaded, setLoaded] = useState(false)
  const [draft, setDraft] = useState('')
  const [busy, setBusy] = useState<Busy>(null)
  const [flash, setFlash] = useState<Flash>(null)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async () => {
    try {
      const settings = await getSettings(conn, SETTINGS_NAMESPACE)
      // Masked ("***") and a real value both mean "configured".
      setConfigured(Boolean(settings[TAVILY_KEY]))
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setLoaded(true)
    }
  }, [conn])

  useEffect(() => {
    if (!conn.connected) return
    void load()
  }, [conn.connected, load])

  const save = useCallback(async () => {
    const value = draft.trim()
    if (!value) return
    setBusy('save')
    setFlash(null)
    setError(null)
    try {
      await setSetting(conn, SETTINGS_NAMESPACE, TAVILY_KEY, value)
      setDraft('')
      await load()
      setFlash('saved')
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(null)
    }
  }, [conn, draft, load])

  const clear = useCallback(async () => {
    setBusy('clear')
    setFlash(null)
    setError(null)
    try {
      await setSetting(conn, SETTINGS_NAMESPACE, TAVILY_KEY, '')
      await load()
      setFlash('cleared')
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(null)
    }
  }, [conn, load])

  const disabled = !loaded || busy !== null

  return (
    <div className="flex flex-col gap-4 p-5" data-testid="search-settings">
      <SettingsSection title={t('settings.search.title')} description={t('settings.search.description')}>
        <div className="flex items-center justify-between gap-3">
          <div className="min-w-0 flex-1">
            <p className="text-sm text-text-primary">{t('settings.search.apiKey')}</p>
            <p className="mt-0.5 text-xs text-text-muted">{t('settings.search.apiKeyDesc')}</p>
          </div>
          <span
            data-testid="search-key-status"
            data-configured={configured ? 'true' : 'false'}
            className={
              configured
                ? 'shrink-0 rounded-full bg-emerald-500/12 px-2 py-0.5 text-[11px] font-medium text-emerald-500'
                : 'shrink-0 rounded-full bg-bg-tertiary px-2 py-0.5 text-[11px] font-medium text-text-muted'
            }
          >
            {configured ? t('settings.search.statusConfigured') : t('settings.search.statusMissing')}
          </span>
        </div>

        <form
          className="flex flex-col gap-2"
          onSubmit={(e) => {
            e.preventDefault()
            void save()
          }}
        >
          <input
            type="password"
            autoComplete="off"
            spellCheck={false}
            value={draft}
            disabled={disabled}
            onChange={(e) => {
              setDraft(e.target.value)
              setFlash(null)
            }}
            placeholder={configured ? t('settings.search.keepHint') : t('settings.search.placeholder')}
            aria-label={t('settings.search.apiKey')}
            data-testid="tavily-api-key-input"
            className="w-full rounded-lg border border-border bg-bg-secondary px-2.5 py-1.5 font-mono text-sm
                       text-text-primary placeholder:text-text-muted focus:border-accent focus:outline-none
                       disabled:opacity-50"
          />
          <div className="flex items-center justify-end gap-2">
            {configured ? (
              <button
                type="button"
                onClick={() => void clear()}
                disabled={disabled}
                data-testid="tavily-api-key-clear"
                className="rounded-lg border border-border px-3.5 py-1.5 text-sm text-text-muted
                           transition-colors hover:bg-bg-tertiary hover:text-text-primary
                           disabled:cursor-not-allowed disabled:opacity-40"
              >
                {t('settings.search.clear')}
              </button>
            ) : null}
            <button
              type="submit"
              disabled={disabled || draft.trim() === ''}
              data-testid="tavily-api-key-save"
              className="rounded-lg bg-accent px-3.5 py-1.5 text-sm font-medium text-white
                         transition-colors hover:bg-accent/85 disabled:cursor-not-allowed disabled:opacity-40"
            >
              {busy === 'save' ? t('settings.search.saving') : t('settings.search.save')}
            </button>
          </div>
        </form>

        <a
          href="https://app.tavily.com/home"
          target="_blank"
          rel="noreferrer"
          className="w-fit text-xs text-accent hover:underline"
        >
          {t('settings.search.getKey')}
        </a>

        {flash ? (
          <p role="status" data-testid="search-settings-flash" className="text-xs text-emerald-500">
            {flash === 'saved' ? t('settings.search.saved') : t('settings.search.cleared')}
          </p>
        ) : null}
        {error ? (
          <p role="alert" className="text-xs text-red-500">
            {error}
          </p>
        ) : null}
      </SettingsSection>
    </div>
  )
}
