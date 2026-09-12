/**
 * SettingsChannels — 设置 → 渠道：统一管理消息渠道。
 *
 * 数据来自 `get_channel_config` RPC：每个渠道一条
 *   { enabled, ...values, _schema: "<json []SettingDefinition>", _builtin: "true"|"false" }
 * 保存走 `set_channel_config`（服务端写 config.json 并按需热启停渠道）。
 *
 * 「内置渠道」（web/feishu/qq/napcat）与「用户注册的插件渠道」共用同一套渲染：
 * 插件渠道由 ChannelProvider.ConfigSchema() 提供 `_schema`，内置渠道由
 * channel.BuiltinChannelSchema 提供，两者形状一致。
 *
 * 飞书额外提供一键绑定：`feishu_bind_start` 返回飞书官方的智能体应用授权链接
 * （用户在飞书里确认后，应用即获得智能体权限/事件/回调清单，含
 * cardkit:card:write —— 流式进度卡片所必需）；`feishu_bind_status` 轮询结果，
 * 成功后服务端自动写回凭据。
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

import { postAPI } from '@/lib/api'
import { useI18n } from '@/providers/i18n'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'

import { SettingsSection } from './SettingsSection'

type ChannelConfig = Record<string, string>

interface ChannelField {
  key: string
  label: string
  description?: string
  type?: string
  default_value?: string
}

interface FeishuBindStatus {
  state: 'idle' | 'waiting' | 'done' | 'error'
  url?: string
  app_id?: string
  error?: string
  expires_in?: number
}

/** Bound the link's validity display (server reports the same value). */
const FEISHU_BIND_POLL_MS = 2500

async function rpc<T>(method: string, params: Record<string, unknown> = {}): Promise<T> {
  return postAPI<T>('/api/rpc', { method, params })
}

/** parseSchema decodes the `_schema` meta key into field descriptors. */
function parseSchema(raw?: string): ChannelField[] {
  if (!raw) return []
  try {
    const parsed = JSON.parse(raw)
    if (!Array.isArray(parsed)) return []
    return parsed.filter((f): f is ChannelField => Boolean(f) && typeof f.key === 'string')
  } catch {
    return []
  }
}

/** isMetaKey hides the transport-only meta keys from the form. */
function isMetaKey(key: string): boolean {
  return key.startsWith('_')
}

export function SettingsChannels() {
  const { t } = useI18n()
  const [channels, setChannels] = useState<Record<string, ChannelConfig> | null>(null)
  const [drafts, setDrafts] = useState<Record<string, ChannelConfig>>({})
  const [error, setError] = useState<string | null>(null)
  const [busyChannel, setBusyChannel] = useState<string | null>(null)
  const [savedChannel, setSavedChannel] = useState<string | null>(null)

  const [bind, setBind] = useState<FeishuBindStatus | null>(null)
  const [binding, setBinding] = useState(false)
  const pollRef = useRef<number | null>(null)

  const load = useCallback(async () => {
    try {
      const data = await rpc<Record<string, ChannelConfig>>('get_channel_config')
      setChannels(data ?? {})
      setDrafts(data ?? {})
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  // 轮询绑定状态：链接是单次使用的，用户确认后服务端会写回凭据。
  useEffect(() => {
    if (bind?.state !== 'waiting') {
      if (pollRef.current !== null) {
        window.clearInterval(pollRef.current)
        pollRef.current = null
      }
      return
    }
    pollRef.current = window.setInterval(() => {
      void (async () => {
        try {
          const status = await rpc<FeishuBindStatus>('feishu_bind_status')
          setBind(status)
          if (status.state === 'done') {
            setBinding(false)
            await load()
          } else if (status.state === 'error') {
            setBinding(false)
          }
        } catch (err) {
          setBinding(false)
          setError(err instanceof Error ? err.message : String(err))
        }
      })()
    }, FEISHU_BIND_POLL_MS)
    return () => {
      if (pollRef.current !== null) {
        window.clearInterval(pollRef.current)
        pollRef.current = null
      }
    }
  }, [bind?.state, load])

  const names = useMemo(() => {
    if (!channels) return []
    return Object.keys(channels).sort((a, b) => {
      const ab = channels[a]?._builtin === 'true'
      const bb = channels[b]?._builtin === 'true'
      if (ab !== bb) return ab ? -1 : 1
      return a.localeCompare(b)
    })
  }, [channels])

  const setField = (channel: string, key: string, value: string) => {
    setDrafts((prev) => ({ ...prev, [channel]: { ...(prev[channel] ?? {}), [key]: value } }))
  }

  const isDirty = (channel: string): boolean => {
    const original = channels?.[channel]
    const draft = drafts[channel]
    if (!original || !draft) return false
    return Object.keys(draft).some((k) => !isMetaKey(k) && draft[k] !== original[k])
  }

  const save = async (channel: string) => {
    const draft = drafts[channel]
    if (!draft) return
    const values: ChannelConfig = {}
    for (const [k, v] of Object.entries(draft)) {
      if (!isMetaKey(k)) values[k] = v
    }
    setBusyChannel(channel)
    setSavedChannel(null)
    try {
      await rpc('set_channel_config', { channel, values })
      setSavedChannel(channel)
      await load()
      window.setTimeout(() => setSavedChannel((cur) => (cur === channel ? null : cur)), 2000)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusyChannel(null)
    }
  }

  const startFeishuBind = async () => {
    setBinding(true)
    setError(null)
    setBind(null)
    try {
      const res = await rpc<{ url: string; expires_in: number; app_id?: string }>('feishu_bind_start', {
        app_id: drafts['feishu']?.app_id ?? '',
      })
      setBind({ state: 'waiting', url: res.url, expires_in: res.expires_in, app_id: res.app_id })
    } catch (err) {
      setBinding(false)
      setError(err instanceof Error ? err.message : String(err))
    }
  }

  const copyLink = async (url: string) => {
    try {
      await navigator.clipboard.writeText(url)
    } catch {
      // Clipboard unavailable (insecure origin) — the link stays selectable.
    }
  }

  return (
    <div className="flex flex-col gap-4 p-5">
      <SettingsSection title={t('settings.channels.title')} description={t('settings.channels.description')}>
        {error ? (
          <p className="text-xs text-red-500" role="alert">
            {error}
          </p>
        ) : null}
        {channels === null ? <p className="text-xs text-text-muted">{t('settings.channels.loading')}</p> : null}
      </SettingsSection>

      {names.map((name) => {
        const entry = channels?.[name] ?? {}
        const draft = drafts[name] ?? {}
        const schema = parseSchema(entry._schema)
        const builtin = entry._builtin === 'true'
        const enabled = draft.enabled === 'true'
        const fields = schema.filter((f) => f.key !== 'enabled' && !isMetaKey(f.key))
        const dirty = isDirty(name)

        return (
          <SettingsSection
            key={name}
            title={name + (builtin ? ` · ${t('settings.channels.builtin')}` : ` · ${t('settings.channels.plugin')}`)}
            description={builtin ? undefined : t('settings.channels.pluginHint')}
          >
            <div className="flex items-center justify-between gap-3">
              <Label htmlFor={`channel-enabled-${name}`} className="text-sm">
                {t('settings.channels.enabled')}
              </Label>
              <Switch
                id={`channel-enabled-${name}`}
                checked={enabled}
                onCheckedChange={(v) => setField(name, 'enabled', v ? 'true' : 'false')}
              />
            </div>

            {fields.map((f) => (
              <div key={f.key} className="flex flex-col gap-1">
                <Label htmlFor={`channel-${name}-${f.key}`} className="text-sm">
                  {f.label || f.key}
                </Label>
                <Input
                  id={`channel-${name}-${f.key}`}
                  type={f.type === 'password' ? 'password' : 'text'}
                  value={draft[f.key] ?? ''}
                  placeholder={f.default_value ?? ''}
                  onChange={(e) => setField(name, f.key, e.target.value)}
                />
                {f.description ? <p className="text-xs text-text-muted">{f.description}</p> : null}
              </div>
            ))}

            {name === 'feishu' ? (
              <div className="flex flex-col gap-2 rounded-lg border border-border bg-bg-tertiary/40 p-3">
                <p className="text-xs text-text-muted">{t('settings.channels.feishuBindHint')}</p>
                <div className="flex flex-wrap items-center gap-2">
                  <Button type="button" size="sm" disabled={binding} onClick={() => void startFeishuBind()}>
                    {binding ? t('settings.channels.feishuBinding') : t('settings.channels.feishuBind')}
                  </Button>
                  {bind?.state === 'done' ? (
                    <Badge variant="secondary">{t('settings.channels.feishuBound')}</Badge>
                  ) : null}
                  {bind?.state === 'error' ? (
                    <span className="text-xs text-red-500">{bind.error}</span>
                  ) : null}
                </div>
                {bind?.url ? (
                  <div className="flex flex-col gap-1">
                    <code
                      data-testid="feishu-bind-url"
                      className="max-h-24 min-w-0 overflow-auto break-all rounded bg-bg-primary p-2 font-mono text-[11px] text-text-secondary"
                    >
                      {bind.url}
                    </code>
                    <div className="flex items-center gap-2">
                      <Button type="button" size="sm" variant="secondary" onClick={() => void copyLink(bind.url!)}>
                        {t('settings.channels.copyLink')}
                      </Button>
                      <span className="text-xs text-text-muted">
                        {t('settings.channels.linkExpiry', { seconds: bind.expires_in ?? 600 })}
                      </span>
                    </div>
                  </div>
                ) : null}
              </div>
            ) : null}

            <div className="flex items-center gap-2">
              <Button type="button" size="sm" disabled={!dirty || busyChannel === name} onClick={() => void save(name)}>
                {busyChannel === name ? t('settings.channels.saving') : t('settings.channels.save')}
              </Button>
              {savedChannel === name ? (
                <span className="text-xs text-emerald-500">{t('settings.channels.saved')}</span>
              ) : null}
            </div>
          </SettingsSection>
        )
      })}
    </div>
  )
}
