/**
 * llm-console.tsx — LLM 控制台的原子组件与模态集合。
 * 被 SettingsLLM（控制台主体）使用。文案直接中文（面板独立于 i18n 命名空间，
 * 与 genui 面板同策略；如需国际化后续统一迁移）。
 *
 * 事实约束（与后端逐一对齐，勿凭记忆改）：
 * - 协议仅两种：openai / anthropic。后端 createClient（agent/llm_factory.go）只有
 *   `case "anthropic"` + `default`（OpenAI）——任何其他 provider 字符串都走 OpenAI
 *   客户端，旧 UI 的 openai_responses/deepseek 等值是误导。
 * - OpenAI 的两种 API：api_type = "chat_completions"（默认）| "responses"
 *   （llm/openai.go；订阅级 sub.api_type + per-model 覆盖）。anthropic 无此概念。
 * - 订阅启用/禁用：set_subscription_enabled RPC（v40）——禁用后模型从 picker 消失、
 *   凭据保留，重新启用无损。
 * - PerModelConfig：max_output_tokens / max_context（0 = 跟随默认，系统默认 1M）/
 *   api_type（"" = 跟随订阅默认）/ enabled（写走 set_model_enabled）。
 */
import { useEffect, useState, type ReactNode } from 'react'

// ── 常量 ──────────────────────────────────────────────────────────────────

export const LLM_PROVIDERS = [
  { k: 'openai', label: 'OpenAI', color: '#10a37f', url: 'https://api.openai.com/v1' },
  { k: 'anthropic', label: 'Anthropic', color: '#d97757', url: 'https://api.anthropic.com' },
] as const

export const API_TYPES = [
  { k: 'chat_completions', label: 'Chat Completions', desc: 'POST /v1/chat/completions' },
  { k: 'responses', label: 'Responses', desc: 'POST /v1/responses' },
] as const

/** max_context 预设（完整 token 数）。0 = 跟随默认（系统默认 1M）。 */
export const CTX_PRESETS = [0, 32000, 64000, 128000, 200000, 1000000, 2000000]
/** max_output_tokens 预设。0 = 跟随订阅默认。 */
export const OUT_PRESETS = [0, 8192, 16384, 32768, 65536]

export function fmtTokens(n: number): string {
  if (!n || n <= 0) return '默认 (1M)'
  if (n >= 1000000) return n % 1000000 === 0 ? n / 1000000 + 'M' : (n / 1000000).toFixed(1) + 'M'
  if (n >= 1000) return n % 1000 === 0 ? n / 1000 + 'K' : Math.round(n / 1000) + 'K'
  return String(n)
}

export function providerMeta(provider: string): { label: string; color: string } {
  if (provider === 'anthropic') return { label: 'Anthropic', color: '#d97757' }
  // openai 以及一切历史非标准值（openai_responses/deepseek/…）都由 OpenAI 客户端服务
  return { label: provider === 'openai' ? 'OpenAI' : provider || 'OpenAI', color: '#10a37f' }
}

// ── 图标（内联 SVG，lucide 风格 path） ────────────────────────────────────

const P: Record<string, string[]> = {
  search: ['M21 21l-4.35-4.35', 'M11 19a8 8 0 100-16 8 8 0 000 16z'],
  plus: ['M5 12h14', 'M12 5v14'],
  x: ['M18 6L6 18', 'M6 6l12 12'],
  check: ['M20 6L9 17l-5-5'],
  left: ['M15 18l-6-6 6-6'],
  pencil: ['M17 3a2.85 2.83 0 114 4L7.5 20.5 2 22l1.5-5.5L17 3z'],
  trash: ['M3 6h18', 'M19 6v14a2 2 0 01-2 2H7a2 2 0 01-2-2V6', 'M8 6V4a2 2 0 012-2h4a2 2 0 012 2v2'],
  star: ['M11.53 2.3a.53.53 0 01.95 0l2.31 4.68a2.12 2.12 0 001.59 1.16l5.17.75a.53.53 0 01.3.91l-3.74 3.64a2.12 2.12 0 00-.61 1.88l.88 5.14a.53.53 0 01-.77.56l-4.62-2.43a2.12 2.12 0 00-1.97 0l-4.62 2.43a.53.53 0 01-.77-.56l.88-5.14a2.12 2.12 0 00-.61-1.88L2.16 9.8a.53.53 0 01.3-.91l5.16-.75a2.12 2.12 0 001.6-1.16l2.31-4.68z'],
  lock: ['M7 11V7a5 5 0 0110 0v4', 'M3 13a2 2 0 012-2h14a2 2 0 012 2v6a2 2 0 01-2 2H5a2 2 0 01-2-2v-6z'],
  refresh: ['M3 12a9 9 0 019-9 9.75 9.75 0 016.74 2.74L21 8', 'M21 3v5h-5', 'M21 12a9 9 0 01-9 9 9.75 9.75 0 01-6.74-2.74L3 16', 'M8 16H3v5'],
  more: ['M12 5.5h.01', 'M12 12h.01', 'M12 18.5h.01'],
  eye: ['M2 12s4-8 10-8 10 8 10 8-4 8-10 8-10-8-10-8z', 'M12 15a3 3 0 100-6 3 3 0 000 6z'],
  eyeoff: ['M10.7 5.1a10.7 10.7 0 0111.2 6.6 1 1 0 010 .7 10.7 10.7 0 01-1.4 2.5', 'M14.1 14.2a3 3 0 01-4.3-4.3', 'M17.5 17.5A10.7 10.7 0 012 12s4-8 10-8a10.7 10.7 0 014.5 1', 'M2 2l20 20'],
  crown: ['M11.56 3.27a.5.5 0 01.88 0l2.95 5.6a1 1 0 001.52.3l4.28-3.67a.5.5 0 01.8.52l-2.84 10.25a1 1 0 01-.96.73H5.81a1 1 0 01-.96-.73L2.02 6.02a.5.5 0 01.8-.52l4.27 3.67a1 1 0 001.52-.3l2.95-5.6z'],
  scale: ['M16 16l3-8 3 8c-.87.65-1.92 1-3 1s-2.13-.35-3-1z', 'M2 16l3-8 3 8c-.87.65-1.92 1-3 1s-2.13-.35-3-1z', 'M7 21h10', 'M12 3v18', 'M3 7h2c2 0 5-1 7-2 2 1 5 2 7 2h2'],
  gauge: ['M12 14l4-4', 'M3.34 19a10 10 0 1117.32 0'],
  spark: ['M9.94 15.5A2 2 0 008.5 14.06l-6.14-1.58a.5.5 0 010-.96L8.5 9.94A2 2 0 009.94 8.5l1.58-6.14a.5.5 0 01.96 0L14.06 8.5A2 2 0 0015.5 9.94l6.14 1.58a.5.5 0 010 .96L15.5 14.06a2 2 0 00-1.44 1.44l-1.58 6.14a.5.5 0 01-.96 0l-1.58-6.14z'],
  alert: ['M21.73 18l-8-14a2 2 0 00-3.48 0l-8 14A2 2 0 004 21h16a2 2 0 001.73-3', 'M12 9v4', 'M12 17h.01'],
  globe: ['M12 22a10 10 0 100-20 10 10 0 000 20z', 'M2 12h20', 'M12 2a15.3 15.3 0 010 20 15.3 15.3 0 010-20z'],
}

export function LlmIcon(props: { n: string; s?: number; c?: string; w?: number }) {
  const s = props.s || 15
  const c = props.c || 'var(--text-muted)'
  return (
    <svg width={s} height={s} viewBox="0 0 24 24" fill="none" stroke={c} strokeWidth={props.w || 2}
      strokeLinecap="round" strokeLinejoin="round" className="shrink-0">
      {(P[props.n] || []).map(function(d, i) { return <path key={i} d={d} /> })}
    </svg>
  )
}

// ── 原子组件 ──────────────────────────────────────────────────────────────

export function StatusPill(props: { status: string }) {
  const map: Record<string, [string, string, string]> = {
    normal: ['运行中', 'var(--status-success, #22c55e)', 'rgba(34,197,94,0.12)'],
    offline: ['未同步', '#f59e0b', 'rgba(245,158,11,0.12)'],
    disabled: ['已停用', 'var(--text-muted)', 'var(--bg-tertiary)'],
  }
  const info = map[props.status] || map.disabled
  return (
    <span className="inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-[11px] font-medium"
      style={{ color: info[1], background: info[2] }}>
      <span className="size-1.5 rounded-full" style={{ background: info[1] }} />
      {info[0]}
    </span>
  )
}

export function LlmSwitch(props: { on: boolean; disabled?: boolean; onClick: () => void }) {
  return (
    <button role="switch" aria-checked={props.on} disabled={props.disabled}
      onClick={function(e) { e.stopPropagation(); props.onClick() }}
      className="relative shrink-0 rounded-full transition-colors duration-200 disabled:opacity-40"
      style={{ background: props.on ? 'var(--accent)' : 'var(--border)', width: 40, height: 22 }}>
      <span className="absolute rounded-full bg-white shadow transition-transform duration-200"
        style={{ top: 2, left: 2, width: 18, height: 18, transform: props.on ? 'translateX(18px)' : 'translateX(0px)' }} />
    </button>
  )
}

export function ProvBadge(props: { provider: string; size?: number }) {
  const meta = providerMeta(props.provider)
  const s = props.size || 30
  return (
    <div className="flex shrink-0 items-center justify-center rounded-lg font-bold text-white"
      style={{ width: s, height: s, fontSize: s * 0.44, background: 'linear-gradient(135deg,' + meta.color + ',' + meta.color + 'bb)' }}>
      {meta.label[0]}
    </div>
  )
}

export function PresetChip(props: { active: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button type="button" onClick={props.onClick}
      className="rounded-full px-2.5 py-1 text-[11px] font-mono transition-colors"
      style={props.active
        ? { background: 'color-mix(in srgb, var(--accent) 14%, transparent)', color: 'var(--accent)', border: '1px solid var(--accent)' }
        : { background: 'var(--bg-tertiary)', color: 'var(--text-secondary)', border: '1px solid var(--border)' }}>
      {props.children}
    </button>
  )
}

export function LlmField(props: { label: string; hint?: string; children: ReactNode }) {
  return (
    <div>
      <div className="mb-1.5 text-xs font-medium" style={{ color: 'var(--text-secondary)' }}>{props.label}</div>
      {props.children}
      {props.hint ? <div className="mt-1 text-[11px]" style={{ color: 'var(--text-muted)' }}>{props.hint}</div> : null}
    </div>
  )
}

export const llmInputStyle: React.CSSProperties = {
  background: 'var(--bg-tertiary)', border: '1px solid var(--border)', color: 'var(--text-primary)',
}

// ── ModalShell：backdrop + 居中卡片 ───────────────────────────────────────
// SettingsDialog 的 sheet 覆盖视口，fixed 锚定到 sheet ≈ 视口，弹层不会跑出设置面板。

export function ModalShell(props: { open: boolean; onClose: () => void; maxWidth?: string; children: ReactNode }) {
  const open = props.open
  return (
    <div role="dialog" aria-modal="true" onClick={props.onClose}
      className="fixed inset-0 z-50 flex items-center justify-center p-4"
      style={{ background: 'rgba(0,0,0,0.55)', backdropFilter: 'blur(2px)', opacity: open ? 1 : 0, pointerEvents: open ? 'auto' : 'none', transition: 'opacity 200ms ease' }}>
      <div onClick={function(e) { e.stopPropagation() }}
        className="flex w-full flex-col overflow-hidden rounded-2xl border"
        style={{
          maxWidth: props.maxWidth || '26rem', background: 'var(--bg-primary)',
          borderColor: 'var(--border)', boxShadow: '0 20px 60px rgba(0,0,0,0.4)',
          transform: open ? 'scale(1)' : 'scale(0.96)', transition: 'transform 200ms cubic-bezier(0.2,0.8,0.3,1)',
          maxHeight: 'calc(100vh - 32px)',
        }}>
        {props.children}
      </div>
    </div>
  )
}

export function ModalHeader(props: { title: string; sub?: string; onClose: () => void }) {
  return (
    <div className="flex shrink-0 items-center gap-2 border-b p-4" style={{ borderColor: 'var(--border)' }}>
      <div className="min-w-0 flex-1">
        <div className="truncate text-sm font-semibold" style={{ color: 'var(--text-primary)' }}>{props.title}</div>
        {props.sub ? <div className="truncate text-[11px]" style={{ color: 'var(--text-muted)' }}>{props.sub}</div> : null}
      </div>
      <button onClick={props.onClose} aria-label="关闭" className="rounded-lg p-1.5 hover:bg-[var(--bg-tertiary)]">
        <LlmIcon n="x" s={15} />
      </button>
    </div>
  )
}

// ── 编辑模型配置 ──────────────────────────────────────────────────────────
// max_context：预设（含 1M/2M）+ 自定义输入；0 = 跟随默认（系统默认 1M）。

export function EditModelModal(props: {
  subName: string
  provider: string
  model: string
  pmc?: { max_context?: number; max_output_tokens?: number; api_type?: string }
  saving?: boolean
  onSave: (cfg: { max_context: number; max_output_tokens: number; api_type: string }) => void
  onClose: () => void
}) {
  const cs = useState({ ctx: props.pmc?.max_context || 0, out: props.pmc?.max_output_tokens || 0, api: props.pmc?.api_type || '' })
  const cfg = cs[0], setCfg = cs[1]
  const isOpenAI = props.provider !== 'anthropic'
  const set = function(patch: { ctx?: number; out?: number; api?: string }) { setCfg(Object.assign({}, cfg, patch)) }
  return (
    <ModalShell open onClose={props.onClose} maxWidth="28rem">
      <ModalHeader title={props.model} sub={props.subName + ' · 上下文 / 输出上限（0 = 跟随默认）'} onClose={props.onClose} />
      <div className="min-h-0 flex-1 space-y-4 overflow-y-auto p-4">
        <LlmField label="Max Context（tokens）" hint="点选预设或直接输入任意值；0 表示跟随系统默认（1M）">
          <div className="flex flex-wrap gap-1.5">
            {CTX_PRESETS.map(function(cv) {
              return <PresetChip key={cv} active={cfg.ctx === cv} onClick={function() { set({ ctx: cv }) }}>{cv === 0 ? '默认 (1M)' : fmtTokens(cv)}</PresetChip>
            })}
          </div>
          <input type="number" min={0} value={cfg.ctx || ''} placeholder="默认 (1M)"
            onChange={function(e) { set({ ctx: Math.max(0, Number(e.target.value) || 0) }) }}
            className="mt-2 w-full rounded-xl px-3 py-2.5 text-sm outline-none" style={llmInputStyle} />
        </LlmField>
        <LlmField label="Max Output（tokens）" hint="0 = 跟随订阅默认">
          <div className="flex flex-wrap gap-1.5">
            {OUT_PRESETS.map(function(ov) {
              return <PresetChip key={ov} active={cfg.out === ov} onClick={function() { set({ out: ov }) }}>{ov === 0 ? '默认' : fmtTokens(ov)}</PresetChip>
            })}
          </div>
          <input type="number" min={0} value={cfg.out || ''} placeholder="默认"
            onChange={function(e) { set({ out: Math.max(0, Number(e.target.value) || 0) }) }}
            className="mt-2 w-full rounded-xl px-3 py-2.5 text-sm outline-none" style={llmInputStyle} />
        </LlmField>
        {isOpenAI ? (
          <LlmField label="API 类型" hint="Per-model 覆盖；留空跟随订阅默认">
            <div className="flex gap-2">
              {API_TYPES.map(function(at) {
                const cur = (cfg.api || 'chat_completions') === at.k
                return (
                  <button key={at.k} type="button" onClick={function() { set({ api: at.k }) }}
                    className="flex-1 rounded-xl border px-3 py-2 text-left transition-colors"
                    style={{ borderColor: cur ? 'var(--accent)' : 'var(--border)', background: cur ? 'color-mix(in srgb, var(--accent) 8%, transparent)' : 'transparent' }}>
                    <div className="text-[12px] font-medium" style={{ color: cur ? 'var(--accent)' : 'var(--text-primary)' }}>{at.label}</div>
                    <div className="font-mono text-[10px]" style={{ color: 'var(--text-muted)' }}>{at.desc}</div>
                  </button>
                )
              })}
            </div>
          </LlmField>
        ) : null}
      </div>
      <div className="shrink-0 border-t p-4" style={{ borderColor: 'var(--border)' }}>
        <button onClick={function() { props.onSave({ max_context: cfg.ctx, max_output_tokens: cfg.out, api_type: isOpenAI ? cfg.api : '' }) }}
          disabled={props.saving}
          className="w-full rounded-xl py-2.5 text-sm font-semibold text-white transition-opacity disabled:opacity-40" style={{ background: 'var(--accent)' }}>
          {props.saving ? '保存中…' : '保存'}
        </button>
      </div>
    </ModalShell>
  )
}

// ── 添加模型 ──────────────────────────────────────────────────────────────

export function AddModelModal(props: { subName: string; saving?: boolean; onAdd: (name: string) => void; onClose: () => void }) {
  const ns = useState('')
  const name = ns[0], setName = ns[1]
  return (
    <ModalShell open onClose={props.onClose}>
      <ModalHeader title="添加模型" sub={props.subName} onClose={props.onClose} />
      <div className="p-4">
        <input value={name} onChange={function(e) { setName(e.target.value) }} autoFocus
          placeholder="输入模型名，如 gpt-5.2-mini"
          onKeyDown={function(e) { if (e.key === 'Enter' && name.trim() && !props.saving) props.onAdd(name.trim()) }}
          className="w-full rounded-xl px-3 py-2.5 font-mono text-xs outline-none" style={llmInputStyle} />
        <div className="mt-3 flex flex-wrap gap-1.5">
          {['o4-mini', 'gpt-5.2-turbo'].map(function(x) { return <PresetChip key={x} active={false} onClick={function() { setName(x) }}>{x}</PresetChip> })}
        </div>
        <button onClick={function() { if (name.trim()) props.onAdd(name.trim()) }} disabled={!name.trim() || props.saving}
          className="mt-4 w-full rounded-xl py-2.5 text-sm font-semibold text-white disabled:opacity-40" style={{ background: 'var(--accent)' }}>
          {props.saving ? '添加中…' : '注册模型'}
        </button>
        <div className="mt-2 text-center text-[11px]" style={{ color: 'var(--text-muted)' }}>注册后可在模型列表手动启用；「刷新模型列表」会自动校正状态</div>
      </div>
    </ModalShell>
  )
}

// ── 删除确认 ──────────────────────────────────────────────────────────────

export function DeleteConfirmModal(props: { subName: string; modelCount: number; saving?: boolean; onConfirm: () => void; onClose: () => void }) {
  return (
    <ModalShell open onClose={props.onClose}>
      <div className="flex items-start gap-3 p-5">
        <span className="flex size-9 shrink-0 items-center justify-center rounded-xl" style={{ background: 'var(--status-error, #ef4444)', color: '#fff' }}>
          <LlmIcon n="trash" s={16} c="#fff" />
        </span>
        <div>
          <div className="text-[15px] font-semibold" style={{ color: 'var(--text-primary)' }}>删除订阅「{props.subName}」？</div>
          <div className="mt-1.5 text-[13px] leading-relaxed" style={{ color: 'var(--text-secondary)' }}>
            该订阅下 <b style={{ color: 'var(--text-primary)' }}>{props.modelCount} 个模型</b> 将从模型选择器中移除。进行中的会话不受影响，此操作不可撤销。
          </div>
        </div>
      </div>
      <div className="flex gap-2 border-t p-4" style={{ borderColor: 'var(--border)' }}>
        <button onClick={props.onClose} className="flex-1 rounded-xl border py-2.5 text-[13px] font-medium" style={{ borderColor: 'var(--border)', color: 'var(--text-primary)' }}>取消</button>
        <button onClick={props.onConfirm} disabled={props.saving}
          className="flex-1 rounded-xl py-2.5 text-[13px] font-medium text-white transition-opacity disabled:opacity-40" style={{ background: 'var(--status-error, #ef4444)' }}>
          {props.saving ? '删除中…' : '删除'}
        </button>
      </div>
    </ModalShell>
  )
}

// ── Tier 选择器（带搜索） ─────────────────────────────────────────────────

export function TierPickerModal(props: {
  tierLabel: string
  entries: Array<{ sub_id: string; sub_name: string; model: string; status: string }>
  value: string
  onPick: (subID: string, model: string) => void
  onClose: () => void
}) {
  const qs = useState('')
  const q = qs[0], setQ = qs[1]
  const list = props.entries.filter(function(e) {
    if (e.status === 'disabled') return false
    if (!q) return true
    return (e.model + ' ' + e.sub_name).toLowerCase().indexOf(q.toLowerCase()) >= 0
  })
  return (
    <ModalShell open onClose={props.onClose} maxWidth="30rem">
      <ModalHeader title={'选择' + props.tierLabel + '模型'} sub="点击即切换；未配置的分层回落系统默认" onClose={props.onClose} />
      <div className="shrink-0 p-3 pb-0">
        <div className="flex items-center gap-2 rounded-xl border px-3" style={{ borderColor: 'var(--border)', background: 'var(--bg-tertiary)' }}>
          <LlmIcon n="search" s={14} />
          <input value={q} onChange={function(e) { setQ(e.target.value) }} autoFocus placeholder="搜索模型或订阅…"
            className="w-full bg-transparent py-2.5 text-sm outline-none" style={{ color: 'var(--text-primary)' }} />
        </div>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto p-2" style={{ maxHeight: '340px' }}>
        {list.map(function(e) {
          const cur = props.value === e.sub_id + '|' + e.model
          return (
            <button key={e.sub_id + '\x00' + e.model} onClick={function() { props.onPick(e.sub_id, e.model) }}
              className="flex w-full items-center gap-3 rounded-xl px-3 py-2.5 text-left transition-colors hover:bg-[var(--bg-tertiary)]">
              <span className="flex size-7 shrink-0 items-center justify-center rounded-lg text-[11px] font-bold text-white"
                style={{ background: 'var(--text-muted)' }}>{(e.sub_name || '?')[0]}</span>
              <div className="min-w-0 flex-1">
                <div className="truncate font-mono text-[13px]" style={{ color: 'var(--text-primary)' }}>{e.model}</div>
                <div className="text-[11px]" style={{ color: 'var(--text-muted)' }}>{e.sub_name}</div>
              </div>
              {cur ? <span className="flex size-5 items-center justify-center rounded-full text-white" style={{ background: 'var(--accent)' }}><LlmIcon n="check" s={12} c="#fff" w={3} /></span> : null}
            </button>
          )
        })}
        {list.length === 0 ? <div className="p-6 text-center text-xs" style={{ color: 'var(--text-muted)' }}>无匹配的可用模型</div> : null}
      </div>
    </ModalShell>
  )
}

// ── 订阅表单（添加/编辑） ─────────────────────────────────────────────────
// 协议仅 OpenAI / Anthropic；OpenAI 提供 API 类型选择（chat_completions / responses）。

export function SubFormModal(props: {
  initial?: { id: string; name: string; provider: string; base_url: string; api_key: string; model: string; api_type: string } | null
  saving?: boolean
  onSave: (data: { name: string; provider: string; base_url: string; api_key: string; model: string; api_type: string }) => void
  onClose: () => void
}) {
  const editing = props.initial || null
  const fs = useState({
    name: editing ? editing.name : '',
    provider: editing ? editing.provider : 'openai',
    base_url: editing ? editing.base_url : 'https://api.openai.com/v1',
    api_key: editing ? editing.api_key : '',
    model: editing ? editing.model : '',
    api_type: editing ? editing.api_type : 'chat_completions',
  })
  const form = fs[0], setForm = fs[1]
  const set = function(patch: Record<string, string>) { setForm(Object.assign({}, form, patch)) }
  const pickProvider = function(k: string) {
    const pv = LLM_PROVIDERS.find(function(p) { return p.k === k })
    setForm(Object.assign({}, form, { provider: k, base_url: pv ? pv.url : form.base_url }))
  }
  const valid = form.name.trim() !== '' && form.base_url.trim() !== '' && form.model.trim() !== '' &&
    (editing !== null || form.api_key.trim() !== '')
  return (
    <ModalShell open onClose={props.onClose} maxWidth="30rem">
      <ModalHeader title={editing ? '编辑订阅' : '添加订阅'} sub="保存后可点「刷新模型列表」拉取该端点的模型" onClose={props.onClose} />
      <div className="min-h-0 flex-1 space-y-4 overflow-y-auto p-4">
        <LlmField label="协议">
          <div className="grid grid-cols-2 gap-2">
            {LLM_PROVIDERS.map(function(pv) {
              const cur = form.provider === pv.k
              return (
                <button key={pv.k} type="button" onClick={function() { pickProvider(pv.k) }}
                  className="flex items-center gap-2 rounded-xl border py-2.5 transition-all"
                  style={{ borderColor: cur ? pv.color : 'var(--border)', background: cur ? 'color-mix(in srgb, ' + pv.color + ' 8%, transparent)' : 'transparent' }}>
                  <span className="ml-3 size-4 rounded" style={{ background: pv.color }} />
                  <span className="text-[12px] font-medium" style={{ color: cur ? 'var(--text-primary)' : 'var(--text-secondary)' }}>{pv.label}</span>
                </button>
              )
            })}
          </div>
        </LlmField>
        {form.provider === 'openai' ? (
          <LlmField label="API 类型" hint="chat_completions 走 /v1/chat/completions；responses 走 /v1/responses">
            <div className="flex gap-2">
              {API_TYPES.map(function(at) {
                const cur = (form.api_type || 'chat_completions') === at.k
                return (
                  <button key={at.k} type="button" onClick={function() { set({ api_type: at.k }) }}
                    className="flex-1 rounded-xl border px-3 py-2 text-left transition-colors"
                    style={{ borderColor: cur ? 'var(--accent)' : 'var(--border)', background: cur ? 'color-mix(in srgb, var(--accent) 8%, transparent)' : 'transparent' }}>
                    <div className="text-[12px] font-medium" style={{ color: cur ? 'var(--accent)' : 'var(--text-primary)' }}>{at.label}</div>
                    <div className="font-mono text-[10px]" style={{ color: 'var(--text-muted)' }}>{at.desc}</div>
                  </button>
                )
              })}
            </div>
          </LlmField>
        ) : null}
        <LlmField label="名称">
          <input value={form.name} onChange={function(e) { set({ name: e.target.value }) }} placeholder="例如：OpenAI 官方"
            className="w-full rounded-xl px-3 py-2.5 text-sm outline-none" style={llmInputStyle} />
        </LlmField>
        <LlmField label="Base URL" hint="选择协议时已自动填充，可修改">
          <input value={form.base_url} onChange={function(e) { set({ base_url: e.target.value }) }} placeholder="https://…"
            className="w-full rounded-xl px-3 py-2.5 font-mono text-xs outline-none" style={llmInputStyle} />
        </LlmField>
        <LlmField label="API Key" hint={editing ? '已掩码；留空保持现有 Key 不变' : '以 sk- 开头，仅存储于服务端'}>
          <input type="password" value={form.api_key} onChange={function(e) { set({ api_key: e.target.value }) }} placeholder={editing ? 'sk-****（保持不变）' : 'sk-…'}
            className="w-full rounded-xl px-3 py-2.5 font-mono text-xs outline-none" style={llmInputStyle} />
        </LlmField>
        <LlmField label="默认模型" hint="订阅的首选模型，如 gpt-5.2 / claude-opus-4-6">
          <input value={form.model} onChange={function(e) { set({ model: e.target.value }) }} placeholder="model-name"
            className="w-full rounded-xl px-3 py-2.5 font-mono text-xs outline-none" style={llmInputStyle} />
        </LlmField>
      </div>
      <div className="shrink-0 border-t p-4" style={{ borderColor: 'var(--border)' }}>
        <button onClick={function() {
          props.onSave({
            name: form.name.trim(), provider: form.provider, base_url: form.base_url.trim(),
            api_key: form.api_key.trim(), model: form.model.trim(),
            api_type: form.provider === 'openai' ? (form.api_type || 'chat_completions') : '',
          })
        }} disabled={!valid || props.saving}
          className="w-full rounded-xl py-2.5 text-sm font-semibold text-white transition-opacity disabled:opacity-40" style={{ background: 'var(--accent)' }}>
          {props.saving ? '保存中…' : editing ? '保存' : '添加订阅'}
        </button>
      </div>
    </ModalShell>
  )
}

// ── 底部 ActionSheet（移动端菜单） ────────────────────────────────────────

export function ActionSheet(props: { title: string; onClose: () => void; items: Array<{ icon: string; label: string; danger?: boolean; onClick: () => void }> }) {
  const es = useState(false)
  const shown = es[0], setShown = es[1]
  useEffect(function() {
    const frame = requestAnimationFrame(function() { setShown(true) })
    return function() { cancelAnimationFrame(frame) }
  }, [])
  return (
    <div role="dialog" aria-modal="true" onClick={props.onClose}
      className="fixed inset-0 z-50 flex items-end p-3"
      style={{ background: 'rgba(0,0,0,0.55)', opacity: shown ? 1 : 0, transition: 'opacity 200ms ease' }}>
      <div onClick={function(e) { e.stopPropagation() }}
        className="w-full overflow-hidden rounded-2xl border transition-transform duration-250"
        style={{ background: 'var(--bg-primary)', borderColor: 'var(--border)', boxShadow: '0 20px 60px rgba(0,0,0,0.4)', transform: shown ? 'translateY(0)' : 'translateY(40px)' }}>
        <div className="border-b px-4 py-3 text-center text-xs" style={{ borderColor: 'var(--border)', color: 'var(--text-muted)' }}>{props.title}</div>
        {props.items.map(function(it, i) {
          const c = it.danger ? 'var(--status-error, #ef4444)' : 'var(--text-primary)'
          return (
            <button key={i} onClick={function() { it.onClick() }}
              className="flex w-full items-center gap-3 px-4 py-3.5 text-[14px] transition-colors active:bg-[var(--bg-tertiary)]"
              style={{ color: c, borderTop: i ? '1px solid var(--border)' : 'none' }}>
              <LlmIcon n={it.icon} s={16} c={c} />{it.label}
            </button>
          )
        })}
        <div className="p-3" style={{ borderTop: '1px solid var(--border)' }}>
          <button onClick={props.onClose} className="w-full rounded-xl py-2.5 text-[13px] font-semibold" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)' }}>取消</button>
        </div>
      </div>
    </div>
  )
}
