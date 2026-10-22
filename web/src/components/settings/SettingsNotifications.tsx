import { useEffect, useState } from 'react'
import { Bell, BellOff, Loader2 } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { useI18n } from '@/providers/i18n'
import {
  getPushConfig,
  registerWebPush,
  unregisterWebPush,
  type WebPushSubscription,
} from '@/lib/api'
import { SettingsSection } from './SettingsSection'

/** VAPID public keys are base64url; PushManager wants raw bytes. */
function urlBase64ToUint8Array(base64String: string): Uint8Array<ArrayBuffer> {
  const padding = '='.repeat((4 - (base64String.length % 4)) % 4)
  const base64 = (base64String + padding).replace(/-/g, '+').replace(/_/g, '/')
  const raw = atob(base64)
  const out = new Uint8Array(new ArrayBuffer(raw.length))
  for (let i = 0; i < raw.length; i += 1) out[i] = raw.charCodeAt(i)
  return out
}

async function readyRegistration(): Promise<ServiceWorkerRegistration> {
  let reg = await navigator.serviceWorker.getRegistration()
  if (!reg) reg = await navigator.serviceWorker.register('/sw2.js', { scope: '/' })
  return navigator.serviceWorker.ready
}

/**
 * Browser notifications (WebPush). The server pushes the same semantic events
 * as FCM; the service worker (public/push-sw.js) renders them while the page is
 * closed. Requires HTTPS and a subscribed PushManager.
 */
export function SettingsNotifications() {
  const { t } = useI18n()
  const [supported, setSupported] = useState(true)
  const [enabled, setEnabled] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    const ok =
      typeof window !== 'undefined' &&
      'serviceWorker' in navigator &&
      'PushManager' in window &&
      'Notification' in window
    setSupported(ok)
    if (!ok) return
    navigator.serviceWorker
      .getRegistration()
      .then(async (reg) => {
        if (!reg) return
        const sub = await reg.pushManager.getSubscription()
        setEnabled(Boolean(sub))
      })
      .catch(() => {})
  }, [])

  const enable = async () => {
    setBusy(true)
    setError(null)
    try {
      const cfg = await getPushConfig()
      if (!cfg.webpush?.enabled || !cfg.webpush.public_key) {
        throw new Error(t('settings.notifications.disabled'))
      }
      if (Notification.permission !== 'granted') {
        const permission = await Notification.requestPermission()
        if (permission !== 'granted') {
          throw new Error(t('settings.notifications.permissionDenied'))
        }
      }
      const reg = await readyRegistration()
      const sub = await reg.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: urlBase64ToUint8Array(cfg.webpush.public_key),
      })
      const json = sub.toJSON()
      const payload: WebPushSubscription = {
        endpoint: json.endpoint || sub.endpoint,
        keys: json.keys,
      }
      await registerWebPush(payload, navigator.userAgent)
      setEnabled(true)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  const disable = async () => {
    setBusy(true)
    setError(null)
    try {
      const reg = await navigator.serviceWorker.getRegistration()
      const sub = reg ? await reg.pushManager.getSubscription() : null
      if (sub) {
        await unregisterWebPush(sub.endpoint)
        await sub.unsubscribe()
      }
      setEnabled(false)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="flex flex-col gap-2.5 p-4">
      <SettingsSection
        title={t('settings.notifications.title')}
        description={t('settings.notifications.description')}
      >
        <div className="flex flex-col gap-2.5">
          <p className="text-sm text-text-secondary">
            {!supported
              ? t('settings.notifications.unsupported')
              : enabled
                ? t('settings.notifications.enabled')
                : t('settings.notifications.notEnabled')}
          </p>
          {supported ? (
            <Button
              variant="outline"
              size="sm"
              onClick={enabled ? disable : enable}
              disabled={busy}
              className="w-fit gap-2 border-border bg-bg-tertiary hover:bg-bg-hover"
            >
              {busy ? (
                <Loader2 className="size-4 animate-spin" />
              ) : enabled ? (
                <BellOff className="size-4" />
              ) : (
                <Bell className="size-4" />
              )}
              {enabled
                ? t('settings.notifications.disable')
                : t('settings.notifications.enable')}
            </Button>
          ) : null}
          {error ? <p className="text-sm text-red-400">{error}</p> : null}
        </div>
      </SettingsSection>
    </div>
  )
}
