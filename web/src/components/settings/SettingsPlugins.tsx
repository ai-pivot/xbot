/**
 * Settings → 插件 —— 按插件声明的配置 schema（contributes.configuration）
 * 自动渲染配置表单（VSCode 风格）。支持顶部搜索过滤配置项、按 section 分组。
 *
 * 数据源：`plugin_config` RPC（所有带配置声明的插件的 schema + 当前值）。
 * 修改：`plugin_config_set` RPC 持久化，后端广播 web_plugin_config_changed
 * 触发插件热重载（Go/stdio 走 context 订阅、script 读 env、前端走 onConfigChange）。
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { ImagePlus, Loader2, Search, X } from 'lucide-react'

import { postAPI } from '@/lib/api'
import { useWSConnection } from '@/hooks/useWSConnection'
import { Switch } from '@/components/ui/switch'
import { Input } from '@/components/ui/input'
import { Slider } from '@/components/ui/slider'
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
  type: 'boolean' | 'string' | 'number' | 'select' | 'multiselect' | 'image'
  label?: string
  description?: string
  default?: unknown
  options?: Array<{ label: string; value: string; css?: string }>
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

  // 跨端配置同步：web_plugin_config_changed（后端 SSE 广播）→ 轻量合并对应
  // 插件的 values，不重拉整列表（不闪 loading、不打断拖动中的滑条）。本端
  // plugin_config_set 的回声同值幂等。注意：SSE 事件带全量 merged values，
  // 与本地乐观更新值相同（后端 recompute 顺序：defaults → user overrides）。
  const ws = useWSConnection()
  useEffect(() => {
    const off = ws.onMessage((msg) => {
      if (msg.type !== 'web_plugin_config_changed') return
      try {
        const evt = JSON.parse(msg.content ?? '{}') as {
          plugin_id?: string
          value?: Record<string, unknown>
        }
        if (!evt.plugin_id) return
        setPlugins((prev) =>
          prev.map((p) =>
            p.id === evt.plugin_id
              ? { ...p, values: { ...p.values, ...evt.value } }
              : p,
          ),
        )
      } catch {
        /* 广播载荷解析失败忽略 */
      }
    })
    return off
  }, [ws])

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
    <div className="flex flex-col gap-2.5 p-4">
      <div className="relative">
        <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-text-muted" />
        <Input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="搜索插件配置项…"
          className="rounded-lg border-border bg-bg-secondary pl-9 focus-visible:border-accent/40 focus-visible:ring-accent/25"
          autoFocus
        />
      </div>

      {loading ? (
        <div className="flex items-center gap-2 py-8 text-sm text-text-muted">
          <Loader2 className="size-4 animate-spin" />
          加载插件配置…
        </div>
      ) : null}

      {error ? (
        <div className="py-6 text-sm text-[var(--status-error,#ef4444)]">
          加载插件配置失败：{error}
        </div>
      ) : null}

      {!loading && !error && filtered.length === 0 ? (
        <div className="py-8 text-sm text-text-muted">
          {q ? '没有匹配的插件配置项。' : '没有插件声明配置。'}
        </div>
      ) : null}

      {!loading &&
        filtered.map((p) => (
          <PluginConfigSection key={p.id} plugin={p} />
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

/** 图片选择 + 上传（type: 'image'）——React 组件（需要 hooks）。
 *
 * 选项网格 = manifest 声明预设（prop.options，css 渐变）+ 已上传图片
 * （plugin files API，真实图片背景）。上传成功后刷新列表并选中 —— 上传的
 * 图片立即作为选项出现，切换预设后也能切回。
 */
interface UploadedImage {
  filename: string
  url: string
  contentType: string
}

function ImageSelectControl({
  propKey, prop, value, saving, onChange, pluginId,
}: {
  propKey: string
  prop: PluginConfigProp
  value: unknown
  saving: boolean
  onChange: (key: string, value: unknown) => Promise<void>
  pluginId: string
}) {
  const current = String(value ?? '')
  const uploadRef = useRef<HTMLInputElement>(null)
  const [uploading, setUploading] = useState(false)
  const [uploadErr, setUploadErr] = useState<string | null>(null)
  const [images, setImages] = useState<UploadedImage[]>([])

  const refreshImages = useCallback(async () => {
    try {
      const res = await fetch(`/api/plugin-files/${encodeURIComponent(pluginId)}`)
      if (!res.ok) return
      const json = (await res.json()) as {
        ok: boolean
        data?: Array<{ filename: string; url: string; contentType: string }>
      }
      if (!json.ok || !Array.isArray(json.data)) return
      setImages(json.data.filter((f) => f.contentType.startsWith('image/')))
    } catch {
      /* 列表拉取失败不阻塞选项渲染（预设仍可用） */
    }
  }, [pluginId])

  useEffect(() => {
    void refreshImages()
  }, [refreshImages])

  const onUpload = async (file: File | undefined) => {
    if (!file) return
    setUploading(true)
    setUploadErr(null)
    try {
      const form = new FormData()
      form.append('plugin_id', pluginId)
      form.append('file', file)
      const res = await fetch('/api/plugin-files/upload', { method: 'POST', body: form })
      if (!res.ok) throw new Error(`upload failed: ${res.statusText}`)
      const json = (await res.json()) as { ok: boolean; data?: { url: string }; error?: string }
      if (!json.ok || !json.data) throw new Error(json.error || 'upload failed')
      await refreshImages()
      await onChange(propKey, json.data.url)
    } catch (e) {
      setUploadErr(e instanceof Error ? e.message : String(e))
    } finally {
      setUploading(false)
      if (uploadRef.current) uploadRef.current.value = ''
    }
  }

  const onDelete = async (img: UploadedImage) => {
    // 正在使用的图片禁删 —— 当前值指向它，删除后壁纸 404。
    if (saving || uploading || current === img.url) return
    try {
      const res = await fetch(img.url, { method: 'DELETE' })
      if (!res.ok) throw new Error(`delete failed: ${res.statusText}`)
      await refreshImages()
    } catch (e) {
      setUploadErr(e instanceof Error ? e.message : String(e))
    }
  }

  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex flex-wrap gap-2">
        {(prop.options ?? []).map((o) => (
          <button
            key={o.value}
            type="button"
            disabled={saving}
            onClick={() => void onChange(propKey, o.value)}
            className={`relative h-14 w-24 overflow-hidden rounded-lg border-2 text-[10px] transition-transform hover:scale-[1.02] ${current === o.value ? 'border-accent' : 'border-border'}`}
            style={{ background: o.css || 'var(--bg-tertiary)' }}
            aria-label={o.label}
          >
            <span className="absolute inset-x-0 bottom-0 truncate bg-black/45 px-1 py-0.5 text-[9px] text-white">{o.label}</span>
          </button>
        ))}
        {images.map((img) => (
          <div
            key={img.url}
            className={`group/img relative h-14 w-24 overflow-hidden rounded-lg border-2 transition-transform hover:scale-[1.02] ${current === img.url ? 'border-accent' : 'border-border'}`}
          >
            <button
              type="button"
              disabled={saving}
              onClick={() => void onChange(propKey, img.url)}
              className="h-full w-full"
              style={{
                backgroundImage: `url(${img.url})`,
                backgroundSize: 'cover',
                backgroundPosition: 'center',
              }}
              aria-label={img.filename}
              title={img.filename}
            />
            {current !== img.url && (
              <button
                type="button"
                disabled={saving || uploading}
                onClick={() => void onDelete(img)}
                className="absolute right-0.5 top-0.5 rounded bg-black/60 p-0.5 text-white opacity-70 hover:bg-red-500 hover:opacity-100"
                aria-label={`删除 ${img.filename}`}
                title="删除"
              >
                <X className="size-3" />
              </button>
            )}
            <span className="absolute inset-x-0 bottom-0 truncate bg-black/45 px-1 py-0.5 text-[9px] text-white">{img.filename}</span>
          </div>
        ))}
        <button
          type="button"
          disabled={saving || uploading}
          onClick={() => uploadRef.current?.click()}
          className="flex h-14 w-24 flex-col items-center justify-center gap-0.5 rounded-lg border border-dashed border-border bg-bg-secondary text-[10px] text-text-muted transition-colors hover:bg-bg-tertiary hover:text-text-primary disabled:opacity-50"
        >
          <ImagePlus className="size-4" />
          {uploading ? '…' : '上传'}
        </button>
        <input ref={uploadRef} type="file" accept="image/*" className="hidden" onChange={(e) => void onUpload(e.target.files?.[0])} />
      </div>
      {uploadErr && <div className="text-[10px] text-red-400">{uploadErr}</div>}
    </div>
  )
}

// ImageSelectControl: inline in ConfigField via JSX (no standalone function needed for propKeyOf).

/** 数字配置控件（type: 'number'）。
 *
 * 有 min+max 范围 → 滑条 + 数字输入组合（拖动即时预览，松手/防抖提交）；
 * 无范围 → 纯输入框。
 *
 * 本地 draft 受控 —— 修复受控 input 无 onChange 被 React 重置的问题
 * （手机端数字改不了的根因：value 只挂 props、onBlur 才 commit，输入
 * 中任何重渲染都会把 DOM 值弹回 props value）。onBlur / Enter /
 * 滑条 onValueCommit 提交；滑条拖动走 300ms 防抖（拖动过程不刷 RPC）。
 */
function NumberControl({
  propKey, prop, value, saving, onChange,
}: {
  propKey: string
  prop: PluginConfigProp
  value: unknown
  saving: boolean
  onChange: (key: string, value: unknown) => Promise<void>
}) {
  const label = prop.label || propKey
  const [draft, setDraft] = useState<string>(value == null ? '' : String(value))
  const commitTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  // 最近一次提交的值（回声检测）：plugin_config_set 成功后后端广播
  // web_plugin_config_changed 回来更新 value prop —— 若与本地刚提交值相同
  // 则跳过 draft 同步，不打断拖动中的滑条（拖动中 draft 领先于已提交值）。
  const lastCommitted = useRef<string | null>(null)

  // 外部 value 变化（其他入口/热重载回写）同步 draft —— 本端提交的回声
  // （value === lastCommitted）跳过：拖动继续中，draft 已领先该值。
  useEffect(() => {
    const next = value == null ? '' : String(value)
    if (next === lastCommitted.current) return
    setDraft(next)
  }, [value])

  useEffect(() => {
    return () => {
      if (commitTimer.current) clearTimeout(commitTimer.current)
    }
  }, [])

  const commit = useCallback((raw: string) => {
    if (raw === '') return
    const n = Number(raw)
    if (Number.isNaN(n)) return
    // clamp 到声明范围（超出范围的输入收敛到边界而非拒绝——温和）。
    const clamped = Math.min(Math.max(n, typeof prop.minimum === 'number' ? prop.minimum : n), typeof prop.maximum === 'number' ? prop.maximum : n)
    setDraft(String(clamped))
    lastCommitted.current = String(clamped)
    if (clamped !== value) void onChange(propKey, clamped)
  }, [onChange, propKey, prop.maximum, prop.minimum, value])

  const hasRange =
    typeof prop.minimum === 'number' &&
    typeof prop.maximum === 'number' &&
    prop.maximum > prop.minimum
  const min = hasRange ? (prop.minimum as number) : 0
  const max = hasRange ? (prop.maximum as number) : 0

  const draftNum = Number(draft)
  const sliderValue = hasRange
    ? Math.min(max, Math.max(min, Number.isFinite(draftNum) ? draftNum : min))
    : min

  // 滑条拖动：本地 draft 即时更新 + 防抖提交（300ms 拖动过程不刷 RPC）。
  const onSlide = useCallback((vals: number[]) => {
    const v = String(vals[0])
    setDraft(v)
    if (commitTimer.current) clearTimeout(commitTimer.current)
    commitTimer.current = setTimeout(() => commit(v), 300)
  }, [commit])

  // 滑条松手：清防抖，立即提交。
  const onSlideCommit = useCallback((vals: number[]) => {
    if (commitTimer.current) {
      clearTimeout(commitTimer.current)
      commitTimer.current = null
    }
    const v = String(vals[0])
    setDraft(v)
    commit(v)
  }, [commit])

  const inputEl = (
    <Input
      type="number"
      inputMode="decimal"
      value={draft}
      disabled={saving}
      min={prop.minimum}
      max={prop.maximum}
      step={hasRange ? (Number.isInteger(min) && Number.isInteger(max) ? 1 : max - min <= 2 ? 0.01 : max - min <= 20 ? 0.1 : 1) : undefined}
      placeholder={prop.placeholder}
      onChange={(e) => setDraft(e.target.value)}
      onBlur={() => commit(draft)}
      onKeyDown={(e) => {
        if (e.key === 'Enter' && !e.nativeEvent.isComposing) commit(draft)
      }}
      aria-label={label}
    />
  )

  if (!hasRange) {
    return inputEl
  }

  return (
    <div className="flex items-center gap-3">
      <span className="shrink-0 text-xs tabular-nums text-text-muted">{min}</span>
      <Slider
        className="min-w-0 flex-1"
        value={[sliderValue]}
        min={min}
        max={max}
        step={Number.isInteger(min) && Number.isInteger(max) ? 1 : max - min <= 2 ? 0.01 : max - min <= 20 ? 0.1 : 1}
        disabled={saving}
        onValueChange={onSlide}
        onValueCommit={onSlideCommit}
        aria-label={label}
      />
      <span className="shrink-0 text-xs tabular-nums text-text-muted">{max}</span>
      <div className="w-20 shrink-0">{inputEl}</div>
    </div>
  )
}

/** 单个插件的配置区块。 */
function PluginConfigSection({
  plugin,
}: {
  plugin: PluginConfigView
}) {
  const [values, setValues] = useState<Record<string, unknown>>(plugin.values)
  const [saving, setSaving] = useState<string | null>(null)

  // 同步外部值（热重载后 / 切换插件时）。
  useEffect(() => {
    setValues(plugin.values)
  }, [plugin.values])

  const setValue = useCallback(
    async (key: string, value: unknown) => {
      // 乐观更新本地值 —— 服务器持久化成功后不重拉整个插件配置列表
      //（全量 load 会闪 loading 并打断拖动中的滑条；值已广播回来由
      // web_plugin_config_changed SSE 监听轻量合并进 plugins state）。
      setValues((v) => ({ ...v, [key]: value }))
      setSaving(key)
      try {
        await postAPI('/api/rpc', {
          method: 'plugin_config_set',
          params: { id: plugin.id, key, value },
        })
      } catch (e) {
        // 失败回滚。
        setValues(plugin.values)
        console.error(`[plugin-config] set ${plugin.id}.${key} 失败`, e)
      } finally {
        setSaving(null)
      }
    },
    [plugin.id, plugin.values],
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
        <div key={section} className="flex flex-col gap-2.5">
          {section ? (
            <h4 className="text-[10px] font-semibold uppercase tracking-wider text-text-muted">
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
              pluginId={plugin.id}
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
  pluginId,
}: {
  propKey: string
  prop: PluginConfigProp
  value: unknown
  saving: boolean
  onChange: (key: string, value: unknown) => Promise<void>
  pluginId: string
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
            <SelectTrigger className="w-full rounded-lg border-border bg-bg-secondary focus:border-accent/40 focus:ring-accent/25">
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
      case 'image':
        return <ImageSelectControl propKey={propKey} prop={prop} value={value} saving={saving} onChange={onChange} pluginId={pluginId} />
      case 'number':
        return <NumberControl propKey={propKey} prop={prop} value={value} saving={saving} onChange={onChange} />
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
      <div className="flex items-center justify-between gap-2.5">
        <div className="flex min-w-0 flex-col gap-0.5">
          <span className="text-sm text-text-primary">{label}</span>
          {prop.description ? (
            <span className="text-xs text-text-muted">{prop.description}</span>
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
          className={`rounded-lg border px-2.5 py-1 text-xs transition-colors ${
            selected.includes(o.value)
              ? 'border-accent/40 bg-accent/14 text-accent'
              : 'border-border bg-bg-secondary text-text-muted hover:bg-bg-tertiary hover:text-text-primary'
          }`}
        >
          {o.label}
        </button>
      ))}
    </div>
  )
}
