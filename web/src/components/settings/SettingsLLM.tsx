/**
 * SettingsLLM — LLM 控制台（重写版）。
 *
 * 替代旧的 480px sheet 内联表单实现。信息架构：卡片概览 → 详情抽屉 →
 * 模态编辑（与设计稿一致）。数据层复用 useLLMSettings（由 SettingsDialog
 * 创建并注入），组件自身只做展示与交互编排。
 *
 * 事实对齐（agent/llm_factory.go + llm/openai.go）：
 * - 协议仅 openai / anthropic 两种（createClient 只有 case "anthropic" + default）
 * - OpenAI 的 api_type：chat_completions（默认）| responses
 * - 订阅启用/禁用：set_subscription_enabled（v40），禁用=模型从 picker 消失、凭据保留
 * - 模型 max_context：预设含 1M/2M + 自定义输入；0 = 跟随默认（系统默认 1M）
 */
import { useMemo, useState } from 'react'
import { toast } from 'sonner'
import { useWSConnection } from '@/hooks/useWSConnection'
import type { useLLMSettings } from '@/hooks/useLLMSettings'
import type { Subscription } from '@/types/shared'
import {
  ActionSheet,
  AddModelModal,
  DeleteConfirmModal,
  EditModelModal,
  LlmField,
  LlmIcon,
  LlmSwitch,
  ProvBadge,
  StatusPill,
  SubFormModal,
  TierPickerModal,
  fmtTokens,
  providerMeta,
} from './llm-console'

type Settings = ReturnType<typeof useLLMSettings>
type LlmSub = Subscription

const THINK_OPTS: Array<[string, string]> = [
  ['auto', '自动'],
  ['think', '思考'],
  ['think-max', '深度思考'],
  ['disabled', '关闭'],
]

const TIER_META: Record<string, { label: string; icon: string; color: string }> = {
  vanguard: { label: '先锋', icon: 'crown', color: '#a78bfa' },
  balance: { label: '均衡', icon: 'scale', color: '#3aa6dd' },
  swift: { label: '疾速', icon: 'gauge', color: '#34d399' },
}

function exportSubscriptions(conn: ReturnType<typeof useWSConnection>) {
  conn.rpc('export_subscriptions', { ids: [] })
    .then((resp: unknown) => {
      const r = resp as { subscriptions?: Array<Record<string, unknown>> }
      const subs = r?.subscriptions ?? []
      if (subs.length === 0) {
        toast.info('没有可导出的订阅')
        return
      }
      const json = JSON.stringify(resp, null, 2)
      const blob = new Blob([json], { type: 'application/json' })
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = 'xbot-llm-subscriptions.json'
      a.click()
      URL.revokeObjectURL(url)
      toast.success('已导出 ' + subs.length + ' 个订阅')
    })
    .catch((e: unknown) => toast.error('导出失败：' + (e instanceof Error ? e.message : String(e))))
}

export function SettingsLLM({ settings }: { settings: Settings }) {
  const conn = useWSConnection()
  const { data, loading, saving, refreshing } = settings
  const {
    addSubscription, updateSubscription, removeSubscription, setDefaultSubscription,
    setSubscriptionEnabled, updatePerModelConfig, setModelEnabled, removeModel,
    upsertModel, refreshModels, setThinkingMode, setLLMConcurrency, setTier,
  } = settings

  const [q, setQ] = useState('')
  const [detailId, setDetailId] = useState<string | null>(null)
  const [formOpen, setFormOpen] = useState(false)
  const [editSub, setEditSub] = useState<LlmSub | null>(null)
  const [addModelFor, setAddModelFor] = useState<string | null>(null)
  const [editModel, setEditModel] = useState<{ sid: string; model: string } | null>(null)
  const [confirmDel, setConfirmDel] = useState<LlmSub | null>(null)
  const [tierPick, setTierPick] = useState<string | null>(null)
  const [menuSub, setMenuSub] = useState<LlmSub | null>(null)
  const [menuModel, setMenuModel] = useState<{ sid: string; model: string; status: string } | null>(null)
  const [thinking, setThinking] = useState<string | null>(null)
  const [conc, setConc] = useState<number | null>(null)
  const [importing, setImporting] = useState(false)

  const thinkingVal = thinking ?? (data.thinkingMode === 'enabled' ? 'think' : data.thinkingMode || 'auto')
  const concVal = conc ?? data.llmConcurrency ?? 0

  const subs = useMemo(() => data.subscriptions, [data.subscriptions])
  const totalModels = data.modelEntries.length
  const liveModels = data.modelEntries.filter((e) => e.status === 'normal').length
  const filtered = subs.filter((s) => {
    if (!q) return true
    const ql = q.toLowerCase()
    if (s.name.toLowerCase().includes(ql) || s.provider.toLowerCase().includes(ql)) return true
    return data.modelEntries.some((e) => e.sub_id === s.id && e.model.toLowerCase().includes(ql))
  })
  const detailSub = subs.find((s) => s.id === detailId) || null
  const detailModels = detailSub ? data.modelEntries.filter((e) => e.sub_id === detailSub.id) : []

  const fail = (e: unknown) => toast.error(e instanceof Error ? e.message : String(e))

  const commitThinking = (mode: string) => {
    setThinking(mode)
    const dbMode = mode === 'think' ? 'enabled' : mode
    void setThinkingMode(dbMode).then((ok) => toast[ok ? 'success' : 'error'](ok ? '已保存' : '保存失败'))
  }
  const commitConc = (n: number) => {
    setConc(n)
    void setLLMConcurrency(n).then((ok) => toast[ok ? 'success' : 'error'](ok ? '已保存' : '保存失败'))
  }
  const toggleSub = (s: LlmSub) => {
    void setSubscriptionEnabled(s.id, !s.enabled).then((ok) => {
      if (!ok) return fail('操作失败')
      toast[!s.enabled ? 'success' : 'warning'](!s.enabled ? '已启用 ' + s.name : '已停用 ' + s.name)
    })
  }
  const makeDefault = (s: LlmSub) => {
    void setDefaultSubscription(s.id).then((ok) => toast[ok ? 'success' : 'error'](ok ? '默认订阅 → ' + s.name : '设置失败'))
  }
  const confirmDelete = (s: LlmSub) => {
    setConfirmDel(null)
    if (detailId === s.id) setDetailId(null)
    void removeSubscription(s.id).then((ok) => toast[ok ? 'success' : 'error'](ok ? '已删除 ' + s.name : '删除失败'))
  }
  const toggleModel = (sid: string, model: string, curEnabled: boolean) => {
    void setModelEnabled(sid, model, !curEnabled).then((ok) =>
      toast[ok ? 'success' : 'error'](ok ? (curEnabled ? '已停用 ' + model : '已启用 ' + model) : '操作失败'))
  }
  const removeModelById = (sid: string, model: string) => {
    setMenuModel(null)
    void removeModel(sid, model).then((ok) => toast[ok ? 'success' : 'error'](ok ? '已移除 ' + model : '移除失败'))
  }
  const saveModel = (sid: string, model: string, cfg: { max_context: number; max_output_tokens: number; api_type: string }) => {
    setEditModel(null)
    void updatePerModelConfig(sid, model, {
      max_context: cfg.max_context,
      max_output_tokens: cfg.max_output_tokens,
      api_type: cfg.api_type,
      enabled: true,
    }).then((ok) => toast[ok ? 'success' : 'error'](ok ? '模型配置已保存' : '保存失败'))
  }
  const addModelByName = (sid: string, name: string) => {
    setAddModelFor(null)
    void upsertModel(sid, name, 0, 0, '').then((ok) => toast[ok ? 'success' : 'error'](ok ? '已注册 ' + name : '添加失败'))
  }
  const saveSub = (d: {
    name: string; provider: string; base_url: string; api_key: string; model: string; api_type: string
  }) => {
    setFormOpen(false)
    const editing = editSub
    setEditSub(null)
    const p = editing ? updateSubscription(editing.id, d) : addSubscription(d)
    void p.then((ok) => toast[ok ? 'success' : 'error'](ok ? (editing ? '订阅已保存' : '已添加 ' + d.name) : '保存失败'))
  }
  const pickTier = (tier: string, subID: string, model: string) => {
    setTierPick(null)
    void setTier(tier as 'vanguard' | 'balance' | 'swift', subID + '|' + model).then((ok) =>
      toast[ok ? 'success' : 'error'](ok ? 'Tier ' + TIER_META[tier].label + ' → ' + model : '设置失败'))
  }
  const handleImport = (file: File) => {
    setImporting(true)
    const reader = new FileReader()
    reader.onload = () => {
      conn.rpc('import_subscriptions', { subs: JSON.parse(String(reader.result)), overwrite: false })
        .then(() => {
          toast.success('导入成功')
          void settings.reload()
        })
        .catch((e: unknown) => fail('导入失败：' + (e instanceof Error ? e.message : String(e))))
        .finally(() => setImporting(false))
    }
    reader.readAsText(file)
  }

  const currentTierRaw = (tier: string) =>
    tier === 'vanguard' ? data.tierVanguard : tier === 'balance' ? data.tierBalance : data.tierSwift || ''
  const tierCur = (tier: string): { model: string; sub: string } | null => {
    const raw = currentTierRaw(tier)
    if (!raw) return null
    const idx = raw.indexOf('|')
    if (idx < 0) {
      const owner = data.modelEntries.find((e) => e.model === raw)
      return { model: raw, sub: owner ? owner.sub_name : '' }
    }
    const sub = subs.find((s) => s.id === raw.slice(0, idx))
    return { model: raw.slice(idx + 1), sub: sub ? sub.name : '' }
  }
  const modelPmc = (sub: LlmSub | null, model: string) =>
    (sub && sub.per_model_configs?.[model]) || undefined
  const subById = (id: string) => subs.find((s) => s.id === id)

  if (loading) {
    return (
      <div className="flex h-full items-center justify-center text-sm" style={{ color: 'var(--text-muted)' }}>
        加载中…
      </div>
    )
  }

  return (
    <div className="relative flex h-full min-h-0 flex-col">
      {/* ── header ── */}
      <div className="flex shrink-0 flex-wrap items-center gap-2.5 border-b px-1 pb-3" style={{ borderColor: 'rgba(255,255,255,0.06)' }}>
        <div className="min-w-0 flex-1">
          <h2 className="text-base font-bold" style={{ color: 'var(--text-primary)' }}>LLM 控制台</h2>
          <p className="mt-0.5 text-[11px]" style={{ color: 'var(--text-muted)' }}>
            {subs.length} 个订阅 · {totalModels} 个模型 · {liveModels} 个可用
          </p>
        </div>
        <div className="flex items-center gap-1.5">
          <button onClick={function() { void refreshModels().then((ok) => toast[ok ? 'success' : 'error'](ok ? '模型列表已刷新' : '刷新失败')) }}
            disabled={refreshing}
            className="flex h-8 items-center gap-1 rounded-lg border border-white/[.08] bg-white/[.05] px-2.5 text-[12px] font-medium text-text-primary transition-colors hover:bg-white/[.1] disabled:opacity-50">
            <LlmIcon n="refresh" s={12} c={refreshing ? 'var(--accent)' : undefined} />{refreshing ? '刷新中…' : '刷新模型'}
          </button>
          <button onClick={function() { exportSubscriptions(conn) }}
            className="flex h-8 items-center rounded-lg border border-white/[.08] bg-white/[.05] px-2.5 text-[12px] font-medium text-text-primary transition-colors hover:bg-white/[.1]">导出</button>
          <label className="flex h-8 cursor-pointer items-center rounded-lg border border-white/[.08] bg-white/[.05] px-2.5 text-[12px] font-medium text-text-primary transition-colors hover:bg-white/[.1]">
            {importing ? '导入中…' : '导入'}
            <input type="file" accept="application/json" className="hidden"
              onChange={function(e) { const f = e.target.files?.[0]; if (f) handleImport(f); e.target.value = '' }} />
          </label>
          <button onClick={function() { setEditSub(null); setFormOpen(true) }}
            className="flex h-8 items-center gap-1 rounded-lg bg-[#6c8cff]/14 px-2.5 text-[12px] font-semibold text-[#6c8cff] transition-colors hover:bg-[#6c8cff]/25">
            <LlmIcon n="plus" s={13} c="currentColor" />添加订阅
          </button>
        </div>
      </div>

      {/* ── scroll area ── */}
      <div className="min-h-0 flex-1 overflow-y-auto p-4 pt-4">
        <div className="mb-2 flex items-center gap-1.5 text-[10px] font-semibold uppercase tracking-wider" style={{ color: 'var(--text-muted)' }}>
          <LlmIcon n="spark" s={14} c="var(--accent)" />模型与推理
        </div>
        <div className="rounded-xl border p-3.5" style={{ background: 'rgba(255,255,255,0.02)', borderColor: 'rgba(255,255,255,0.06)' }}>
          <LlmField label="思考模式" hint="全局用户设置 · Ctrl+M">
            <div className="relative flex rounded-xl p-1" style={{ background: 'rgba(255,255,255,0.05)' }}>
              <span className="absolute rounded-lg transition-all duration-300"
                style={{ top: 4, bottom: 4, left: 'calc(4px + ' + THINK_OPTS.map(function(o) { return o[0] }).indexOf(thinkingVal) + ' * 24.5%)', width: '24%', background: 'var(--bg-primary)', boxShadow: '0 1px 4px rgba(0,0,0,0.15)' }} />
              {THINK_OPTS.map(function(o) {
                const cur = thinkingVal === o[0]
                return (
                  <button key={o[0]} onClick={function() { commitThinking(o[0]) }}
                    className="relative z-10 flex-1 rounded-lg py-1.5 text-[11px] font-medium transition-colors"
                    style={{ color: cur ? 'var(--text-primary)' : 'var(--text-muted)' }}>{o[1]}</button>
                )
              })}
            </div>
          </LlmField>
          <div className="mt-3 flex items-center justify-between border-t pt-3" style={{ borderColor: 'rgba(255,255,255,0.06)' }}>
            <div className="text-xs font-medium" style={{ color: 'var(--text-muted)' }}>最大并发会话</div>
            <div className="flex items-center gap-2">
              <button onClick={function() { commitConc(Math.max(1, concVal - 1)) }}
                className="flex size-7 items-center justify-center rounded-lg border border-white/[.08] bg-white/[.05] text-base transition-colors hover:bg-white/[.1]" style={{ color: 'var(--text-primary)' }}>−</button>
              <span className="w-8 text-center font-mono text-sm font-bold" style={{ color: 'var(--text-primary)' }}>{concVal}</span>
              <button onClick={function() { commitConc(concVal + 1) }}
                className="flex size-7 items-center justify-center rounded-lg border border-white/[.08] bg-white/[.05] text-base transition-colors hover:bg-white/[.1]" style={{ color: 'var(--text-primary)' }}>＋</button>
            </div>
          </div>
        </div>

        <div className="mb-2 mt-4 flex items-center gap-1.5 text-[10px] font-semibold uppercase tracking-wider" style={{ color: 'var(--text-muted)' }}>
          <LlmIcon n="crown" s={14} c="var(--accent)" />模型分层
          <span className="font-normal normal-case tracking-normal" style={{ color: 'var(--text-muted)' }}>未配置回落系统默认</span>
        </div>
        <div className="grid grid-cols-1 gap-2.5">
          {Object.keys(TIER_META).map(function(k) {
            const m = TIER_META[k]
            const cur = tierCur(k)
            return (
              <button key={k} onClick={function() { setTierPick(k) }}
                className="flex items-center gap-2.5 rounded-xl border p-3 text-left transition-all hover:bg-white/[.04]"
                style={{ background: 'rgba(255,255,255,0.02)', borderColor: 'rgba(255,255,255,0.06)' }}>
                <span className="flex size-8 shrink-0 items-center justify-center rounded-lg" style={{ background: m.color + '22', color: m.color }}>
                  <LlmIcon n={m.icon} s={15} c={m.color} />
                </span>
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-1.5">
                    <span className="text-[13px] font-semibold" style={{ color: 'var(--text-primary)' }}>{m.label}</span>
                    <span className="text-[10px] tracking-wider" style={{ color: 'var(--text-muted)' }}>{k.toUpperCase()}</span>
                  </div>
                  <div className="truncate font-mono text-[11px]" style={{ color: cur ? m.color : 'var(--text-muted)' }}>
                    {cur ? cur.model + ' · ' + cur.sub : '未配置 · 回落系统默认'}
                  </div>
                </div>
                <span className="text-[11px] font-medium" style={{ color: 'var(--accent)' }}>更换 ›</span>
              </button>
            )
          })}
        </div>

        <div className="mb-2 mt-4 flex items-center gap-1.5 text-[10px] font-semibold uppercase tracking-wider" style={{ color: 'var(--text-muted)' }}>
          <LlmIcon n="globe" s={14} c="var(--accent)" />订阅
          <span className="font-normal normal-case tracking-normal" style={{ color: 'var(--text-muted)' }}>点击卡片查看详情</span>
        </div>
        <div className="mb-1.5 flex h-8 items-center gap-2 rounded-lg border px-3" style={{ borderColor: 'rgba(255,255,255,0.08)', background: 'rgba(255,255,255,0.03)' }}>
          <LlmIcon n="search" s={13} />
          <input value={q} onChange={function(e) { setQ(e.target.value) }} placeholder="搜索订阅或模型…"
            className="w-full bg-transparent text-[12px] outline-none" style={{ color: 'var(--text-primary)' }} />
        </div>
        <div className="space-y-2.5 pb-2">
          {filtered.map(function(s) {
            const meta = providerMeta(s.provider)
            const models = data.modelEntries.filter(function(e) { return e.sub_id === s.id })
            const avail = models.filter(function(e) { return e.status !== 'disabled' })
            const chips = avail.slice(0, 3)
            return (
              <div key={s.id} onClick={function() { setDetailId(s.id) }} role="button" tabIndex={0}
                className="cursor-pointer overflow-hidden rounded-xl border transition-all hover:bg-white/[.04]"
                style={{ background: 'rgba(255,255,255,0.02)', borderColor: 'rgba(255,255,255,0.06)' }}
                onMouseEnter={function(e) { e.currentTarget.style.borderColor = meta.color + '66' }}
                onMouseLeave={function(e) { e.currentTarget.style.borderColor = 'rgba(255,255,255,0.06)' }}>
                <div className="h-0.5 w-full" style={{ background: 'linear-gradient(90deg,' + meta.color + ',' + meta.color + '44)' }} />
                <div className="flex items-center gap-2.5 p-3">
                  <ProvBadge provider={s.provider} />
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-1.5">
                      <span className="truncate text-[13px] font-semibold" style={{ color: 'var(--text-primary)' }}>{s.name}</span>
                      {s.is_system ? <LlmIcon n="lock" s={11} /> : null}
                      {s.active ? <span className="rounded px-1 py-0.5 text-[9px] font-medium" style={{ background: 'color-mix(in srgb, var(--accent) 14%, transparent)', color: 'var(--accent)' }}>默认</span> : null}
                    </div>
                    <div className="truncate font-mono text-[10px]" style={{ color: 'var(--text-muted)' }}>
                      {s.base_url.replace(/^https?:\/\//, '')}{s.provider === 'openai' && s.api_type === 'responses' ? ' · responses' : ''}
                    </div>
                  </div>
                  <LlmSwitch on={s.enabled} onClick={function() { toggleSub(s) }} />
                  <button onClick={function(e) { e.stopPropagation(); setMenuSub(s) }} aria-label="更多操作"
                    className="flex size-7 shrink-0 items-center justify-center rounded-lg hover:bg-[var(--bg-tertiary)]" style={{ color: 'var(--text-secondary)' }}>
                    <LlmIcon n="more" s={15} w={2.5} />
                  </button>
                </div>
                <div className="flex flex-wrap items-center gap-1.5 px-3 pb-3">
                  <StatusPill status={s.enabled ? 'normal' : 'disabled'} />
                  <span className="text-[10px]" style={{ color: 'var(--text-muted)' }}>{avail.length}/{models.length} 可用</span>
                  {chips.map(function(e) {
                    return <span key={e.model} className="rounded-md px-1.5 py-0.5 font-mono text-[9px]" style={{ background: 'rgba(255,255,255,0.05)', color: 'var(--text-muted)' }}>{e.model}</span>
                  })}
                  {avail.length > 3 ? <span className="text-[9px]" style={{ color: 'var(--text-muted)' }}>+{avail.length - 3}</span> : null}
                </div>
              </div>
            )
          })}
          {filtered.length === 0 ? (
            <div className="rounded-xl border border-dashed p-6 text-center text-xs" style={{ borderColor: 'rgba(255,255,255,0.08)', color: 'var(--text-muted)' }}>
              {q ? '无匹配订阅' : '暂无订阅 · 点击右上角「添加订阅」'}
            </div>
          ) : null}
        </div>
      </div>

      {/* ── detail drawer（面板内覆盖，主从布局） ── */}
      <div onClick={function() { setDetailId(null) }} className="absolute inset-0 z-30 transition-opacity duration-200"
        style={{ background: 'rgba(0,0,0,0.5)', opacity: detailId ? 1 : 0, pointerEvents: detailId ? 'auto' : 'none' }} />
      <aside className="absolute inset-y-0 right-0 z-40 flex w-full flex-col transition-transform duration-300"
        style={{ background: 'var(--bg-primary)', borderLeft: '1px solid rgba(255,255,255,0.06)', transform: detailId ? 'translateX(0)' : 'translateX(105%)' }}>
        {detailSub ? (
          <div className="flex h-full flex-col">
            <div className="flex items-center gap-1 border-b px-2 py-2" style={{ borderColor: 'rgba(255,255,255,0.06)', background: 'rgba(255,255,255,0.02)' }}>
              <button onClick={function() { setDetailId(null) }} className="flex size-8 items-center justify-center rounded-lg hover:bg-white/[.05]" style={{ color: 'var(--text-muted)' }}>
                <LlmIcon n="left" s={16} />
              </button>
              <span className="text-[12px] font-semibold" style={{ color: 'var(--text-primary)' }}>订阅详情</span>
            </div>
            <div className="flex items-center gap-2.5 border-b p-4" style={{ borderColor: 'rgba(255,255,255,0.06)' }}>
              <ProvBadge provider={detailSub.provider} size={36} />
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-1.5 text-sm font-semibold" style={{ color: 'var(--text-primary)' }}>
                  {detailSub.name}{detailSub.is_system ? <LlmIcon n="lock" s={12} /> : null}
                </div>
                <div className="mt-0.5 truncate font-mono text-[10px]" style={{ color: 'var(--text-muted)' }}>
                  {detailSub.base_url}{detailSub.provider === 'openai' && detailSub.api_type === 'responses' ? ' · responses' : ''}
                </div>
              </div>
              <LlmSwitch on={detailSub.enabled} onClick={function() { toggleSub(detailSub) }} />
            </div>
            <div className="min-h-0 flex-1 overflow-y-auto p-4">
              <div className="mb-2 flex items-center justify-between">
                <span className="text-[10px] font-semibold uppercase tracking-wider" style={{ color: 'var(--text-muted)' }}>凭据</span>
                {!detailSub.is_system ? (
                  <button onClick={function() { setEditSub(detailSub); setFormOpen(true) }}
                    className="flex items-center gap-1 text-[11px] font-medium" style={{ color: 'var(--accent)' }}>
                    <LlmIcon n="pencil" s={11} /> 编辑
                  </button>
                ) : null}
              </div>
              <div className="mb-4 space-y-2 rounded-xl border p-3" style={{ borderColor: 'rgba(255,255,255,0.06)', background: 'rgba(255,255,255,0.02)' }}>
                <div className="flex items-center justify-between text-[12px]">
                  <span style={{ color: 'var(--text-secondary)' }}>API Key</span>
                  <span className="font-mono text-[11px]" style={{ color: 'var(--text-primary)' }}>{detailSub.api_key || '—'}</span>
                </div>
                <div className="flex items-center justify-between text-[12px]">
                  <span style={{ color: 'var(--text-secondary)' }}>协议</span>
                  <span style={{ color: 'var(--text-primary)' }}>{providerMeta(detailSub.provider).label}{detailSub.provider === 'openai' ? ' · ' + (detailSub.api_type === 'responses' ? 'Responses' : 'Chat Completions') : ''}</span>
                </div>
                <div className="flex items-center justify-between text-[12px]">
                  <span style={{ color: 'var(--text-secondary)' }}>默认模型</span>
                  <span className="font-mono text-[11px]" style={{ color: 'var(--text-primary)' }}>{detailSub.model || '—'}</span>
                </div>
              </div>
              <div className="mb-2 flex items-center justify-between">
                <span className="text-[10px] font-semibold uppercase tracking-wider" style={{ color: 'var(--text-muted)' }}>模型 · {detailModels.length}</span>
                {!detailSub.is_system ? (
                  <button onClick={function() { setAddModelFor(detailSub.id) }} className="flex items-center gap-1 text-[11px] font-medium" style={{ color: 'var(--accent)' }}>
                    <LlmIcon n="plus" s={11} /> 添加模型
                  </button>
                ) : null}
              </div>
              <div className="space-y-0.5">
                {detailModels.map(function(e) {
                  const pmc = modelPmc(detailSub, e.model)
                  return (
                    <div key={e.model} className="group flex items-center gap-2.5 rounded-xl px-2.5 py-2 transition-colors hover:bg-white/[.04]">
                      <StatusPill status={e.status} />
                      <span className="min-w-0 flex-1 truncate font-mono text-[12px]" style={{ color: e.status === 'disabled' ? 'var(--text-muted)' : 'var(--text-primary)' }}>{e.model}</span>
                      {pmc?.max_context ? <span className="rounded px-1.5 py-0.5 font-mono text-[9px]" style={{ background: 'rgba(255,255,255,0.05)', color: 'var(--text-muted)' }}>{fmtTokens(pmc.max_context)}</span> : null}
                      {pmc?.max_output_tokens ? <span className="rounded px-1.5 py-0.5 font-mono text-[9px]" style={{ background: 'rgba(255,255,255,0.05)', color: 'var(--text-muted)' }}>out {fmtTokens(pmc.max_output_tokens)}</span> : null}
                      {e.status === 'disabled'
                        ? <button onClick={function() { toggleModel(detailSub.id, e.model, false) }} className="rounded-lg px-2 py-1 text-[10px] font-medium hover:bg-white/[.05]" style={{ color: 'var(--accent)' }}>启用</button>
                        : <button onClick={function() { toggleModel(detailSub.id, e.model, true) }} className="rounded-lg px-2 py-1 text-[10px] font-medium hover:bg-white/[.05]" style={{ color: 'var(--text-muted)' }}>停用</button>}
                      <button onClick={function() { setMenuModel({ sid: detailSub.id, model: e.model, status: e.status }) }}
                        aria-label="模型操作" className="flex size-6 items-center justify-center rounded-lg hover:bg-white/[.05]" style={{ color: 'var(--text-muted)' }}>
                        <LlmIcon n="more" s={13} w={2.5} />
                      </button>
                    </div>
                  )
                })}
                {detailModels.length === 0 ? (
                  <div className="rounded-xl border border-dashed p-5 text-center text-[11px]" style={{ borderColor: 'rgba(255,255,255,0.08)', color: 'var(--text-muted)' }}>
                    暂无模型 · 点「刷新模型列表」拉取，或手动添加
                  </div>
                ) : null}
              </div>
              {!detailSub.is_system ? (
                <div className="mt-5 flex gap-2.5">
                  <button onClick={function() { makeDefault(detailSub) }} disabled={detailSub.active}
                    className="flex flex-1 items-center justify-center gap-1.5 rounded-xl border border-white/[.08] bg-white/[.05] py-2.5 text-[12px] font-medium transition-colors hover:bg-white/[.1] disabled:opacity-40"
                    style={{ color: detailSub.active ? 'var(--text-muted)' : 'var(--text-primary)' }}>
                    <LlmIcon n="star" s={12} />{detailSub.active ? '当前默认' : '设为默认'}
                  </button>
                  <button onClick={function() { setConfirmDel(detailSub) }}
                    className="flex flex-1 items-center justify-center gap-1.5 rounded-xl border border-white/[.08] py-2.5 text-[12px] font-medium transition-colors hover:bg-white/[.05]"
                    style={{ color: 'var(--status-error, #ef4444)' }}>
                    <LlmIcon n="trash" s={12} /> 删除订阅
                  </button>
                </div>
              ) : null}
            </div>
          </div>
        ) : null}
      </aside>

      {/* ── modals ── */}
      {formOpen ? (
        <SubFormModal
          initial={editSub ? {
            id: editSub.id, name: editSub.name, provider: editSub.provider,
            base_url: editSub.base_url, api_key: editSub.api_key,
            model: editSub.model, api_type: editSub.api_type || '',
          } : null}
          saving={saving}
          onSave={saveSub}
          onClose={function() { setFormOpen(false); setEditSub(null) }}
        />
      ) : null}
      {addModelFor && detailSub ? (
        <AddModelModal subName={detailSub.name} saving={saving}
          onAdd={function(name) { addModelByName(addModelFor, name) }}
          onClose={function() { setAddModelFor(null) }} />
      ) : null}
      {confirmDel ? (
        <DeleteConfirmModal subName={confirmDel.name}
          modelCount={data.modelEntries.filter(function(e) { return e.sub_id === confirmDel.id }).length}
          saving={saving} onConfirm={function() { confirmDelete(confirmDel) }} onClose={function() { setConfirmDel(null) }} />
      ) : null}
      {tierPick ? (
        <TierPickerModal tierLabel={TIER_META[tierPick].label} entries={data.modelEntries}
          value={currentTierRaw(tierPick)} onClose={function() { setTierPick(null) }}
          onPick={function(subID, model) { pickTier(tierPick, subID, model) }} />
      ) : null}
      {editModel ? (
        <EditModelModal
          subName={(subById(editModel.sid) || { name: '' }).name}
          provider={(subById(editModel.sid) || { provider: 'openai' }).provider}
          model={editModel.model}
          pmc={modelPmc(subById(editModel.sid) || null, editModel.model)}
          saving={saving}
          onSave={function(cfg) { saveModel(editModel.sid, editModel.model, cfg) }}
          onClose={function() { setEditModel(null) }} />
      ) : null}
      {menuSub ? (
        <ActionSheet title={menuSub.name} onClose={function() { setMenuSub(null) }}
          items={[
            { icon: 'pencil', label: '编辑凭据', onClick: function() { setEditSub(menuSub); setFormOpen(true); setMenuSub(null) } },
            { icon: 'star', label: '设为默认', onClick: function() { makeDefault(menuSub); setMenuSub(null) } },
            { icon: 'power', label: menuSub.enabled ? '停用订阅' : '启用订阅', onClick: function() { toggleSub(menuSub); setMenuSub(null) } },
            { icon: 'trash', label: '删除订阅', danger: true, onClick: function() { setConfirmDel(menuSub); setMenuSub(null) } },
          ]} />
      ) : null}
      {menuModel ? (
        <ActionSheet title={menuModel.model} onClose={function() { setMenuModel(null) }}
          items={[
            { icon: 'pencil', label: '编辑配置', onClick: function() { setEditModel({ sid: menuModel.sid, model: menuModel.model }); setMenuModel(null) } },
            menuModel.status === 'disabled'
              ? { icon: 'check', label: '启用模型', onClick: function() { toggleModel(menuModel.sid, menuModel.model, false); setMenuModel(null) } }
              : { icon: 'power', label: '停用模型', onClick: function() { toggleModel(menuModel.sid, menuModel.model, true); setMenuModel(null) } },
            { icon: 'trash', label: '移除模型', danger: true, onClick: function() { removeModelById(menuModel.sid, menuModel.model) } },
          ]} />
      ) : null}
    </div>
  )
}
