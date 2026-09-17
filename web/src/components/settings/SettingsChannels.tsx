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
  // feishu_app_guide：要创建的应用长什么样（权限/事件/回调预设，与一键创建同源）。
  const [guide, setGuide] = useState<{
    scopes: string[]; events: string[]; callbacks: string[]; create_app_url: string; needs_public_url: boolean
  } | null>(null)
  const [showPreset, setShowPreset] = useState(false)
  const [binding, setBinding] = useState(false)
  // The authorization window is opened BY THE USER GESTURE (browsers only allow
  // a popup requested inside a click) and navigated to the link once the RPC
  // returns — see startFeishuBind. `popupBlocked` records the fallback case so
  // the panel points at the manual "Open link" / copy actions instead of
  // pretending the window opened.
  const [popupBlocked, setPopupBlocked] = useState(false)
  // Waiting link past its validity window: the single-use code is dead, so stop
  // polling and let the user regenerate. Without this the panel keeps polling a
  // dead link and shows no way forward.
  const [expired, setExpired] = useState(false)
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
    if (bind?.state !== 'waiting' || expired) {
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
          if (status.state === 'idle') {
            // The server no longer owns this attempt (restart, or a newer
            // attempt superseded it) — the link is dead. Reset the panel
            // instead of leaving a disabled button behind (user report:
            // "一直停在 Requesting link…").
            setBind(null)
            setError(t('settings.channels.feishuLinkStale'))
            return
          }
          setBind(status)
        } catch (err) {
          setBind(null)
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
  }, [bind?.state, expired, t])

  // Local expiry guard: the single-use code lives `expires_in` seconds while the
  // server-side attempt may live longer. Without this the panel keeps polling a
  // dead link and offers no way forward.
  useEffect(() => {
    if (bind?.state !== 'waiting') return
    const seconds = bind.expires_in ?? 600
    const timer = window.setTimeout(() => setExpired(true), Math.max(1, seconds) * 1000)
    return () => window.clearTimeout(timer)
  }, [bind?.state, bind?.expires_in])

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

  useEffect(() => {
    void (async () => {
      try {
        const g = await rpc<{
          scopes?: string[]; events?: string[]; callbacks?: string[]; create_app_url?: string; needs_public_url?: boolean
        }>('feishu_app_guide')
        // 引导是增强项：形状不完整（旧服务端/测试 mock）时宁可不渲染，也不能崩面板。
        if (g && Array.isArray(g.scopes) && Array.isArray(g.events) && Array.isArray(g.callbacks)) {
          setGuide({
            scopes: g.scopes,
            events: g.events,
            callbacks: g.callbacks,
            create_app_url: g.create_app_url ?? 'https://open.feishu.cn/app',
            needs_public_url: g.needs_public_url ?? false,
          })
        }
      } catch {
        // 引导信息拿不到不影响绑定本身（按钮仍可用）。
      }
    })()
  }, [])

  const startFeishuBind = async () => {
    setBinding(true)
    setError(null)
    setBind(null)
    setExpired(false)
    setPopupBlocked(false)
    // 打开授权窗口必须发生在**用户手势内**（浏览器只允许 click 内发起的
    // window.open），而链接要等 RPC 返回才有。所以先生成一个空白窗口，拿到 URL
    // 后再导航过去 —— 用户看到的就是「点按钮 → 飞书创建应用页自动打开」。
    const popup = window.open('about:blank', '_blank')
    try {
      const res = await rpc<{ url: string; expires_in: number; app_id?: string }>('feishu_bind_start', {
        app_id: drafts['feishu']?.app_id ?? '',
      })
      setBind({ state: 'waiting', url: res.url, expires_in: res.expires_in, app_id: res.app_id })
      if (popup && !popup.closed) {
        try {
          popup.opener = null // 不要把本面板交给第三方 origin
        } catch {
          // 已经跨域，无法再触碰 opener —— 无妨。
        }
        popup.location.replace(res.url)
      } else {
        // 被拦截（或用户秒关）：如实告知，让用户走「打开链接 / 复制链接」。
        setPopupBlocked(true)
      }
    } catch (err) {
      if (popup && !popup.closed) popup.close()
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      // 绝不能让按钮停在 in-flight 文案上：旧实现只在 done/error 时清这个标志，
      // 于是 waiting 期间按钮一直禁用、文案一直「正在获取链接…」（用户报告
      // 「一直 Requesting link…」）。
      setBinding(false)
    }
  }

  const openBindLink = (url: string) => {
    window.open(url, '_blank', 'noopener,noreferrer')
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
              <div className="flex flex-col gap-2 rounded-lg border border-border bg-bg-tertiary/40 p-3" data-testid="feishu-guide">
                <p className="text-sm font-medium text-text-primary">{t('settings.channels.feishuGuideTitle')}</p>
                <ol className="ml-4 list-decimal text-xs text-text-muted" data-testid="feishu-guide-steps">
                  <li>{t('settings.channels.feishuGuideStep1')}</li>
                  <li>{t('settings.channels.feishuGuideStep2')}</li>
                  <li>{t('settings.channels.feishuGuideStep3')}</li>
                </ol>
                <p className="text-xs text-text-muted">{t('settings.channels.feishuBindHint')}</p>
                {guide ? (
                  <div className="flex flex-col gap-1.5">
                    <button
                      type="button"
                      data-testid="feishu-guide-preset-toggle"
                      className="w-fit text-xs text-accent underline-offset-2 hover:underline"
                      onClick={() => setShowPreset((v) => !v)}
                    >
                      {t('settings.channels.feishuGuidePreset', {
                        scopes: guide.scopes.length,
                        events: guide.events.length,
                        callbacks: guide.callbacks.length,
                      })}
                    </button>
                    {showPreset ? (
                      <div
                        data-testid="feishu-guide-preset"
                        className="flex max-h-48 flex-col gap-2 overflow-auto rounded bg-bg-primary p-2 text-[11px] text-text-secondary"
                      >
                        {(
                          [
                            ['feishu-guide-scopes', t('settings.channels.feishuGuideScopes'), guide.scopes],
                            ['feishu-guide-events', t('settings.channels.feishuGuideEvents'), guide.events],
                            ['feishu-guide-callbacks', t('settings.channels.feishuGuideCallbacks'), guide.callbacks],
                          ] as const
                        ).map(([tid, label, items]) => (
                          <div key={tid} className="flex flex-col gap-1">
                            <div className="flex items-center gap-2">
                              <span className="font-medium text-text-primary">
                                {t('settings.channels.feishuGuideItemCount', { label, count: items.length })}
                              </span>
                              <button
                                type="button"
                                data-testid={`${tid}-copy`}
                                className="text-accent underline-offset-2 hover:underline"
                                onClick={() => void copyLink(items.join('\n'))}
                              >
                                {t('settings.channels.feishuGuideCopyAll')}
                              </button>
                            </div>
                            <code data-testid={tid} className="break-all font-mono">
                              {items.join(', ')}
                            </code>
                          </div>
                        ))}
                      </div>
                    ) : null}
                    <p className="text-xs text-text-muted" data-testid="feishu-guide-no-public-url">
                      {guide.needs_public_url
                        ? t('settings.channels.feishuGuideNeedsPublicUrl')
                        : t('settings.channels.feishuGuideNoPublicUrl')}
                    </p>
                    <a
                      data-testid="feishu-guide-manual"
                      href={guide.create_app_url}
                      target="_blank"
                      rel="noreferrer"
                      className="w-fit text-xs text-accent underline-offset-2 hover:underline"
                    >
                      {t('settings.channels.feishuGuideManual')}
                    </a>
                  </div>
                ) : null}
                <div className="flex flex-wrap items-center gap-2">
                  <Button
                    type="button"
                    size="sm"
                    disabled={binding}
                    data-testid="feishu-bind"
                    onClick={() => void startFeishuBind()}
                  >
                    {binding
                      ? t('settings.channels.feishuBinding')
                      : bind?.state === 'waiting'
                        ? t('settings.channels.feishuRebind')
                        : t('settings.channels.feishuBind')}
                  </Button>
                  {bind?.state === 'waiting' && bind.url && !expired ? (
                    <Button
                      type="button"
                      size="sm"
                      variant="secondary"
                      data-testid="feishu-open-link"
                      onClick={() => openBindLink(bind.url!)}
                    >
                      {t('settings.channels.feishuOpenLink')}
                    </Button>
                  ) : null}
                  {bind?.state === 'done' ? (
                    <Badge variant="secondary">{t('settings.channels.feishuBound')}</Badge>
                  ) : null}
                  {bind?.state === 'error' ? (
                    <span className="text-xs text-red-500">{bind.error}</span>
                  ) : null}
                </div>
                {bind?.state === 'waiting' && !expired ? (
                  <p className="text-xs text-text-muted" data-testid="feishu-bind-waiting">
                    {t('settings.channels.feishuWaitingConfirm')}
                  </p>
                ) : null}
                {popupBlocked ? (
                  <p className="text-xs text-amber-500" data-testid="feishu-popup-blocked">
                    {t('settings.channels.feishuPopupBlocked')}
                  </p>
                ) : null}
                {expired && bind?.state === 'waiting' ? (
                  <p className="text-xs text-amber-500" data-testid="feishu-link-expired">
                    {t('settings.channels.feishuLinkExpired')}
                  </p>
                ) : null}
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
