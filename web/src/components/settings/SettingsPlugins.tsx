/**
 * Settings → 插件 —— 按插件声明的配置 schema（contributes.configuration）
 * 自动渲染配置表单（VSCode 风格）。支持顶部搜索过滤配置项、按 section 分组。
 *
 * 数据源：`plugin_config` RPC（所有带配置声明的插件的 schema + 当前值）。
 * 修改：`plugin_config_set` RPC 持久化，后端广播 web_plugin_config_changed
 * 触发插件热重载（Go/stdio 走 context 订阅、script 读 env、前端走 onConfigChange）。
 */
import { useCallback, useEffect, useMemo, useState } from 'react'
import { Loader2, Search } from 'lucide-react'

import { postAPI } from '@/lib/api'
import { Switch } from '@/components/ui/switch'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { SettingsSection } from './SettingsSection'

/** 后端 plugin_config 返回的单个插件的配置属性 schema。 */
interface PluginConfigProp {
  type: 'boolean' | 'string' | 'number' | 'select' | 'multiselect'
  label?: string
  description?: string
  default?: unknown
  options?: Array<{ label: string; value: string }>
  section?: string
  secret?: boolean
  placeholder?: string
  required?: boolean
  minimum?: number
  maximum?: number
}

/** plugin_config RPC 单插件响应。 */
interface PluginConfigView {
  id: string
  name: string
  title: string
  runtime: string
  enabled: boolean
  properties: Record<string, PluginConfigProp>
  values: Record<string, unknown>
}

export function SettingsPlugins() {
  const [plugins, setPlugins] = useState<PluginConfigView[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [query, setQuery] = useState('')

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const res = await postAPI<{ plugins?: PluginConfigView[] }>('/api/rpc', {
        method: 'plugin_config',
        params: {},
      })
      setPlugins(res.plugins ?? [])
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  // 搜索过滤：按属性 key / label / description 匹配；无查询时显示全部。
  const q = query.trim().toLowerCase()
  const filtered = useMemo(() => {
    if (!q) return plugins
    return plugins
      .map((p) => {
        const props = Object.fromEntries(
          Object.entries(p.properties).filter(
            ([key, prop]) =>
              key.toLowerCase().includes(q) ||
              (prop.label ?? '').toLowerCase().includes(q) ||
              (prop.description ?? '').toLowerCase().includes(q),
          ),
        )
        return { ...p, properties: props }
      })
      .filter((p) => Object.keys(p.properties).length > 0)
  }, [plugins, q])

  return (
    <div className="flex flex-col gap-4">
      <div className="px-5 pt-4">
        <div className="relative">
          <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="搜索插件配置项…"
            className="pl-9"
            autoFocus
          />
        </div>
      </div>

      {loading ? (
        <div className="flex items-center gap-2 px-5 py-8 text-sm text-muted-foreground">
          <Loader2 className="size-4 animate-spin" />
          加载插件配置…
        </div>
      ) : null}

      {error ? (
        <div className="px-5 py-6 text-sm text-destructive">
          加载插件配置失败：{error}
        </div>
      ) : null}

      {!loading && !error && filtered.length === 0 ? (
        <div className="px-5 py-8 text-sm text-muted-foreground">
          {q ? '没有匹配的插件配置项。' : '没有插件声明配置。'}
        </div>
      ) : null}

      {!loading &&
        filtered.map((p) => (
          <PluginConfigSection key={p.id} plugin={p} onSaved={load} />
        ))}
    </div>
  )
}

/** 按 section 分组属性；无 section 的属性归入空分组（渲染在插件标题下）。 */
function groupBySection(properties: Record<string, PluginConfigProp>) {
  const groups = new Map<string, Array<[string, PluginConfigProp]>>()
  for (const [key, prop] of Object.entries(properties)) {
    const section = prop.section ?? ''
    if (!groups.has(section)) groups.set(section, [])
    groups.get(section)!.push([key, prop])
  }
  return [...groups.entries()]
}

/** 单个插件的配置区块。 */
function PluginConfigSection({
  plugin,
  onSaved,
}: {
  plugin: PluginConfigView
  onSaved: () => void
}) {
  const [values, setValues] = useState<Record<string, unknown>>(plugin.values)
  const [saving, setSaving] = useState<string | null>(null)

  // 同步外部值（热重载后 / 切换插件时）。
  useEffect(() => {
    setValues(plugin.values)
  }, [plugin.values])

  const setValue = useCallback(
    async (key: string, value: unknown) => {
      // 乐观更新本地值。
      setValues((v) => ({ ...v, [key]: value }))
      setSaving(key)
      try {
        await postAPI('/api/rpc', {
          method: 'plugin_config_set',
          params: { id: plugin.id, key, value },
        })
        onSaved()
      } catch (e) {
        // 失败回滚。
        setValues(plugin.values)
        console.error(`[plugin-config] set ${plugin.id}.${key} 失败`, e)
      } finally {
        setSaving(null)
      }
    },
    [plugin.id, plugin.values, onSaved],
  )

  const groups = groupBySection(plugin.properties)

  return (
    <SettingsSection
      title={plugin.name}
      description={
        plugin.title && plugin.title !== plugin.name
          ? plugin.title
          : `插件配置（${plugin.runtime}）`
      }
    >
      {groups.map(([section, props]) => (
        <div key={section} className="flex flex-col gap-3">
          {section ? (
            <h4 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
              {section}
            </h4>
          ) : null}
          {props.map(([key, prop]) => (
            <ConfigField
              key={key}
              propKey={key}
              prop={prop}
              value={values[key]}
              saving={saving === key}
              onChange={setValue}
            />
          ))}
        </div>
      ))}
    </SettingsSection>
  )
}

/** 单个配置字段的控件（按 schema.type 渲染）。 */
function ConfigField({
  propKey,
  prop,
  value,
  saving,
  onChange,
}: {
  propKey: string
  prop: PluginConfigProp
  value: unknown
  saving: boolean
  onChange: (key: string, value: unknown) => Promise<void>
}) {
  const label = prop.label || propKey

  const renderControl = (): React.ReactNode => {
    switch (prop.type) {
      case 'boolean':
        return (
          <Switch
            checked={Boolean(value)}
            disabled={saving}
            onCheckedChange={(v) => void onChange(propKey, v)}
            aria-label={label}
          />
        )
      case 'select':
        return (
          <Select
            value={String(value ?? '')}
            disabled={saving}
            onValueChange={(v) => void onChange(propKey, v)}
          >
            <SelectTrigger className="w-full">
              <SelectValue placeholder="选择…" />
            </SelectTrigger>
            <SelectContent>
              {(prop.options ?? []).map((o) => (
                <SelectItem key={o.value} value={o.value}>
                  {o.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )
      case 'multiselect':
        return renderMultiselect(propKey, prop, value, saving, onChange)
      case 'number':
        return (
          <Input
            type="number"
            value={value == null ? '' : String(value)}
            disabled={saving}
            min={prop.minimum}
            max={prop.maximum}
            placeholder={prop.placeholder}
            onBlur={(e) => {
              const n = Number(e.target.value)
              if (!Number.isNaN(n)) void onChange(propKey, n)
            }}
            aria-label={label}
          />
        )
      default:
        return (
          <Input
            type={prop.secret ? 'password' : 'text'}
            value={value == null ? '' : String(value)}
            disabled={saving}
            placeholder={prop.placeholder}
            onBlur={(e) => void onChange(propKey, e.target.value)}
            aria-label={label}
          />
        )
    }
  }

  return (
    <div className="flex flex-col gap-1" data-plugin-config-key={propKey}>
      <div className="flex items-center justify-between gap-3">
        <div className="flex min-w-0 flex-col gap-0.5">
          <span className="text-sm text-foreground">{label}</span>
          {prop.description ? (
            <span className="text-xs text-muted-foreground">{prop.description}</span>
          ) : null}
        </div>
        {prop.type === 'boolean' ? renderControl() : null}
      </div>
      {prop.type !== 'boolean' ? (
        <div className="max-w-sm">{renderControl()}</div>
      ) : null}
    </div>
  )
}

function renderMultiselect(
  propKey: string,
  prop: PluginConfigProp,
  value: unknown,
  saving: boolean,
  onChange: (key: string, value: unknown) => Promise<void>,
): React.ReactNode {
  const selected = Array.isArray(value) ? (value as string[]) : []
  return (
    <div className="flex flex-wrap gap-2">
      {(prop.options ?? []).map((o) => (
        <button
          key={o.value}
          type="button"
          disabled={saving}
          onClick={() => {
            const next = selected.includes(o.value)
              ? selected.filter((v) => v !== o.value)
              : [...selected, o.value]
            void onChange(propKey, next)
          }}
          className={`rounded-md border px-2.5 py-1 text-xs transition-colors ${
            selected.includes(o.value)
              ? 'border-accent bg-accent/15 text-foreground'
              : 'border-border text-muted-foreground hover:text-foreground'
          }`}
        >
          {o.label}
        </button>
      ))}
    </div>
  )
}
