/**
 * SettingsStorage — 设置 → 存储：配置文件存储后端（本地 static / 云 OSS）。
 *
 * schema 由服务端提供（`channel.StorageSchema()` 是唯一来源，随
 * `get_storage_config` 的 `_schema` 一起下发）：provider 选择 + 各后端的凭据字段
 * （按 `depends_on_key=provider` 条件显示）。保存走 `set_storage_config` ——
 * 服务端写 config.json 并**热切换** provider + 重注册多模态解析器（无需重启）。
 *
 * 安全契约：secret 字段读取时已被服务端打码（`AKID****`）；写回掩码值不会覆盖
 * 真实凭据（服务端跳过含 `****` 的值）。`_active` 是**正在运行**的 provider（与
 * config.json 可能短暂不同，例如 apply 失败时）。
 */
import { useCallback, useEffect, useMemo, useState } from 'react'

import { postAPI } from '@/lib/api'
import { useI18n } from '@/providers/i18n'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'

import { SettingsSection } from './SettingsSection'

type StorageConfig = Record<string, string>

interface StorageField {
  key: string
  label: string
  description?: string
  type?: string
  default_value?: string
  depends_on_key?: string
  depends_on_values?: string
  options?: Array<{ label: string; value: string; description?: string }>
}

async function rpc<T>(method: string, params: Record<string, unknown> = {}): Promise<T> {
  return postAPI<T>('/api/rpc', { method, params })
}

/** parseSchema decodes `_schema` into field descriptors (same shape as SettingsChannels). */
function parseSchema(raw?: string): StorageField[] {
  if (!raw) return []
  try {
    const parsed = JSON.parse(raw)
    if (!Array.isArray(parsed)) return []
    return parsed.filter((f): f is StorageField => Boolean(f) && typeof f.key === 'string')
  } catch {
    return []
  }
}

function isMetaKey(key: string): boolean {
  return key.startsWith('_')
}

export function SettingsStorage() {
  const { t } = useI18n()
  const [config, setConfig] = useState<StorageConfig | null>(null)
  const [draft, setDraft] = useState<StorageConfig>({})
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [saved, setSaved] = useState(false)

  const load = useCallback(async () => {
    setError(null)
    try {
      const res = await rpc<StorageConfig>('get_storage_config')
      setConfig(res)
      setDraft(res)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const fields = useMemo(
    () => parseSchema(config?._schema).filter((f) => !isMetaKey(f.key)),
    [config],
  )
  const valueOf = useCallback((k: string) => draft[k] ?? '', [draft])
  const isVisible = useCallback(
    (f: StorageField) => {
      if (!f.depends_on_key) return true
      const want = (f.depends_on_values ?? '')
        .split(',')
        .map((s) => s.trim())
        .filter(Boolean)
      return want.length === 0 ? true : want.includes(valueOf(f.depends_on_key))
    },
    [valueOf],
  )
  const dirty = useMemo(
    () => Boolean(config) && fields.some((f) => valueOf(f.key) !== (config?.[f.key] ?? '')),
    [config, fields, valueOf],
  )
  const liveProvider = config?._active ?? 'local'

  const set = (k: string, v: string) => {
    setSaved(false)
    setDraft((d) => ({ ...d, [k]: v }))
  }

  const save = async () => {
    if (!config) return
    setBusy(true)
    setError(null)
    try {
      const values: Record<string, string> = {}
      for (const f of fields) values[f.key] = valueOf(f.key)
      const res = await rpc<StorageConfig>('set_storage_config', { values })
      setConfig(res)
      setDraft(res)
      setSaved(true)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  const provider = fields.find((f) => f.key === 'provider')
  const rest = fields.filter((f) => f.key !== 'provider' && isVisible(f))

  return (
    <div className="flex flex-col gap-3" data-testid="settings-storage">
      <SettingsSection title={t('settings.storage.title')} description={t('settings.storage.hint')}>
        <div className="flex items-center gap-2 text-xs text-text-muted">
          <span>{t('settings.storage.liveProvider')}</span>
          <span className="rounded bg-bg-tertiary px-1.5 py-0.5 font-medium text-text-primary" data-testid="storage-active">
            {liveProvider}
          </span>
        </div>

        {provider ? (
          <div className="flex flex-col gap-1" key={provider.key}>
            <Label htmlFor="storage-provider" className="text-sm">
              {provider.label || provider.key}
            </Label>
            <select
              id="storage-provider"
              data-testid="storage-provider"
              className="h-9 rounded-md border border-border bg-bg-primary px-2 text-sm text-text-primary"
              value={valueOf(provider.key)}
              onChange={(e) => set(provider.key, e.target.value)}
            >
              {(provider.options ?? []).map((o) => (
                <option key={o.value} value={o.value}>
                  {o.label || o.value}
                </option>
              ))}
            </select>
            {provider.description ? <p className="text-xs text-text-muted">{provider.description}</p> : null}
          </div>
        ) : null}

        {rest.map((f) => (
          <div className="flex flex-col gap-1" key={f.key}>
            {f.type === 'toggle' ? (
              <div className="flex items-center justify-between gap-3">
                <Label htmlFor={`storage-${f.key}`} className="text-sm">
                  {f.label || f.key}
                </Label>
                <Switch
                  id={`storage-${f.key}`}
                  checked={valueOf(f.key) === 'true'}
                  onCheckedChange={(v) => set(f.key, v ? 'true' : 'false')}
                />
              </div>
            ) : (
              <>
                <Label htmlFor={`storage-${f.key}`} className="text-sm">
                  {f.label || f.key}
                </Label>
                <Input
                  id={`storage-${f.key}`}
                  data-testid={`storage-${f.key}`}
                  type={f.type === 'password' ? 'password' : 'text'}
                  value={valueOf(f.key)}
                  placeholder={f.default_value ?? ''}
                  onChange={(e) => set(f.key, e.target.value)}
                />
              </>
            )}
            {f.description ? <p className="text-xs text-text-muted">{f.description}</p> : null}
          </div>
        ))}

        <div className="flex items-center gap-2">
          <Button
            type="button"
            size="sm"
            data-testid="storage-save"
            disabled={busy || !dirty}
            onClick={() => void save()}
          >
            {busy ? t('settings.storage.saving') : t('settings.storage.save')}
          </Button>
          {saved && !dirty ? (
            <span className="text-xs text-text-muted" data-testid="storage-saved">
              {t('settings.storage.saved')}
            </span>
          ) : null}
        </div>

        {error ? (
          <p className="text-xs text-destructive" data-testid="storage-error">
            {error}
          </p>
        ) : null}
      </SettingsSection>
    </div>
  )
}
