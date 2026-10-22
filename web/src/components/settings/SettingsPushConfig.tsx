import { useEffect, useState, type ReactNode } from 'react'
import { Loader2 } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { useI18n } from '@/providers/i18n'
import {
  getPushSettings,
  savePushSettings,
  type PushAPNsConfig,
  type PushFCMConfig,
  type PushSettings,
  type PushWebPushConfig,
} from '@/lib/api'
import { SettingsSection } from './SettingsSection'

const DEFAULT_FCM: PushFCMConfig = {
  enable: false,
  project_id: '',
  credentials_file: '',
  mode: 'data',
}
const DEFAULT_WEBPUSH: PushWebPushConfig = {
  enable: false,
  vapid_public: '',
  vapid_private: '',
  subject: '',
  endpoint_allowlist: [],
}
const DEFAULT_APNS: PushAPNsConfig = {
  enable: false,
  key_file: '',
  key_id: '',
  team_id: '',
  topic: '',
  env: 'production',
}

function Field({
  label,
  hint,
  children,
}: {
  label: string
  hint?: string
  children: ReactNode
}) {
  return (
    <label className="flex min-w-0 flex-col gap-1">
      <span className="text-xs font-medium text-text-muted">{label}</span>
      {children}
      {hint ? <span className="text-[11px] text-text-muted">{hint}</span> : null}
    </label>
  )
}

function ProviderHeader({
  title,
  checked,
  onChange,
  disabled,
}: {
  title: string
  checked: boolean
  onChange: (checked: boolean) => void
  disabled?: boolean
}) {
  return (
    <div className="flex items-center justify-between gap-3">
      <span className="text-sm font-medium text-text-primary">{title}</span>
      <Switch checked={checked} onCheckedChange={onChange} disabled={disabled} />
    </div>
  )
}

const inputClass =
  'h-9 w-full min-w-0 rounded-lg border border-border bg-bg-secondary px-2.5 text-sm text-text-primary placeholder:text-text-muted focus:border-accent focus:outline-none disabled:opacity-50'

/**
 * Server-side push configuration (settings → notifications).
 *
 * Persisted in the DB (push_settings) by the server; saving hot-swaps the push
 * layer without a restart. The VAPID private key is never returned by GET:
 * leaving the field empty keeps the current key.
 */
export function SettingsPushConfig() {
  const { t } = useI18n()
  const [settings, setSettings] = useState<PushSettings | null>(null)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [message, setMessage] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let alive = true
    getPushSettings()
      .then((s) => {
        if (alive) setSettings(s)
      })
      .catch((e) => {
        if (alive) setError(e instanceof Error ? e.message : String(e))
      })
      .finally(() => {
        if (alive) setLoading(false)
      })
    return () => {
      alive = false
    }
  }, [])

  const fcm = settings?.fcm ?? DEFAULT_FCM
  const webpush = settings?.webpush ?? DEFAULT_WEBPUSH
  const apns = settings?.apns ?? DEFAULT_APNS
  const feishu = settings?.feishu ?? { enable: false }

  const patchFCM = (p: Partial<PushFCMConfig>) =>
    setSettings((s) => (s ? { ...s, fcm: { ...(s.fcm ?? DEFAULT_FCM), ...p } } : s))
  const patchWebPush = (p: Partial<PushWebPushConfig>) =>
    setSettings((s) =>
      s ? { ...s, webpush: { ...(s.webpush ?? DEFAULT_WEBPUSH), ...p } } : s,
    )
  const patchAPNs = (p: Partial<PushAPNsConfig>) =>
    setSettings((s) => (s ? { ...s, apns: { ...(s.apns ?? DEFAULT_APNS), ...p } } : s))

  const save = async () => {
    if (!settings) return
    setSaving(true)
    setError(null)
    setMessage(null)
    try {
      await savePushSettings(settings)
      setSettings(await getPushSettings())
      setMessage(t('settings.notifications.saved'))
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setSaving(false)
    }
  }

  return (
    <SettingsSection
      title={t('settings.notifications.pushConfig')}
      description={t('settings.notifications.pushConfigHint')}
    >
      {loading ? (
        <div className="flex items-center gap-2 text-sm text-text-muted">
          <Loader2 className="size-4 animate-spin" /> …
        </div>
      ) : !settings ? (
        <p className="text-sm text-red-400">{error || t('settings.notifications.loadFailed')}</p>
      ) : (
        <div className="flex flex-col gap-4">
          <div className="flex items-center justify-between gap-3">
            <div className="min-w-0">
              <p className="text-sm text-text-primary">
                {t('settings.notifications.enablePush')}
              </p>
              <p className="mt-0.5 text-xs text-text-muted">
                {t('settings.notifications.enablePushHint')}
              </p>
            </div>
            <Switch
              checked={settings.enable}
              onCheckedChange={(v) => setSettings({ ...settings, enable: v })}
            />
          </div>

          <div className="flex items-center justify-between gap-3">
            <div className="min-w-0">
              <p className="text-sm text-text-primary">
                {t('settings.notifications.includePreview')}
              </p>
            </div>
            <Switch
              checked={settings.include_preview}
              onCheckedChange={(v) => setSettings({ ...settings, include_preview: v })}
            />
          </div>

          <div className="flex flex-col gap-2.5 rounded-lg border border-border bg-bg-tertiary/40 p-3">
            <ProviderHeader
              title={t('settings.notifications.fcm')}
              checked={fcm.enable}
              onChange={(v) => patchFCM({ enable: v })}
            />
            <div className="grid gap-2 sm:grid-cols-2">
              <Field label={t('settings.notifications.projectId')}>
                <Input
                  value={fcm.project_id}
                  onChange={(e) => patchFCM({ project_id: e.target.value })}
                  placeholder="xbot-android-push"
                />
              </Field>
              <Field label={t('settings.notifications.credentialsFile')}>
                <Input
                  value={fcm.credentials_file}
                  onChange={(e) => patchFCM({ credentials_file: e.target.value })}
                  placeholder="~/.xbot/fcm-service-account.json"
                />
              </Field>
              <Field label={t('settings.notifications.mode')}>
                <select
                  value={fcm.mode}
                  onChange={(e) => patchFCM({ mode: e.target.value })}
                  className={inputClass}
                >
                  <option value="data">{t('settings.notifications.modeData')}</option>
                  <option value="notification">
                    {t('settings.notifications.modeNotification')}
                  </option>
                </select>
              </Field>
            </div>
          </div>

          <div className="flex flex-col gap-2.5 rounded-lg border border-border bg-bg-tertiary/40 p-3">
            <ProviderHeader
              title={t('settings.notifications.webpush')}
              checked={webpush.enable}
              onChange={(v) => patchWebPush({ enable: v })}
            />
            <div className="grid gap-2 sm:grid-cols-2">
              <Field label={t('settings.notifications.vapidPublic')}>
                <Input
                  value={webpush.vapid_public}
                  onChange={(e) => patchWebPush({ vapid_public: e.target.value })}
                />
              </Field>
              <Field
                label={t('settings.notifications.vapidPrivate')}
                hint={
                  settings.has_vapid_private
                    ? t('settings.notifications.vapidPrivateSet')
                    : t('settings.notifications.vapidPrivateHint')
                }
              >
                <Input
                  type="password"
                  value={webpush.vapid_private ?? ''}
                  placeholder={
                    settings.has_vapid_private
                      ? t('settings.notifications.vapidPrivateSet')
                      : ''
                  }
                  onChange={(e) => patchWebPush({ vapid_private: e.target.value })}
                />
              </Field>
              <Field label={t('settings.notifications.subject')}>
                <Input
                  value={webpush.subject}
                  onChange={(e) => patchWebPush({ subject: e.target.value })}
                  placeholder="mailto:you@example.com"
                />
              </Field>
            </div>
            <Field
              label={t('settings.notifications.allowlist')}
              hint={t('settings.notifications.allowlistHint')}
            >
              <textarea
                rows={3}
                value={webpush.endpoint_allowlist.join('\n')}
                onChange={(e) =>
                  patchWebPush({
                    endpoint_allowlist: e.target.value
                      .split('\n')
                      .map((v) => v.trim())
                      .filter(Boolean),
                  })
                }
                className="w-full resize-y rounded-lg border border-border bg-bg-secondary px-2.5 py-2 font-mono text-xs text-text-primary focus:border-accent focus:outline-none"
                placeholder={'fcm.googleapis.com\npush.services.mozilla.com'}
              />
            </Field>
          </div>

          <div className="flex flex-col gap-2.5 rounded-lg border border-border bg-bg-tertiary/30 p-3 opacity-70">
            <ProviderHeader
              title={t('settings.notifications.apns')}
              checked={apns.enable}
              onChange={(v) => patchAPNs({ enable: v })}
              disabled
            />
            <ProviderHeader
              title={t('settings.notifications.feishu')}
              checked={feishu.enable}
              onChange={(v) =>
                setSettings((s) => (s ? { ...s, feishu: { enable: v } } : s))
              }
              disabled
            />
            <p className="text-[11px] text-text-muted">
              {t('settings.notifications.notImplemented')}
            </p>
          </div>

          <div className="flex flex-wrap items-center gap-3">
            <Button
              variant="outline"
              size="sm"
              onClick={save}
              disabled={saving}
              className="w-fit gap-2 border-border bg-bg-tertiary hover:bg-bg-hover"
            >
              {saving ? <Loader2 className="size-4 animate-spin" /> : null}
              {saving
                ? t('settings.notifications.saving')
                : t('settings.notifications.save')}
            </Button>
            {message ? <span className="text-xs text-green-400">{message}</span> : null}
            {error ? <span className="text-xs text-red-400">{error}</span> : null}
          </div>
        </div>
      )}
    </SettingsSection>
  )
}
