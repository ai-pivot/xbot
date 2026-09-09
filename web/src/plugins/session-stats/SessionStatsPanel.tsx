/**
 * xbot.session-stats 的视图组件——当前会话用量统计面板。
 *
 * 数据：get_session_usage_stats 核心 RPC（iteration_history v59 聚合）。
 * - 指标：input / output / cached tokens（prompt cache 命中）、命中率
 * - 性能：Avg TTFT / Avg TPOT / Avg tok/s、LLM 总时长
 * - 规模：迭代数 / turn 数、当前上下文水位
 * - 明细：最近迭代表（每行 = 一次 LLM 调用的用量与性能）+ per-model 分组
 *
 * 刷新：会话切换（activeSession 变化）、turn.ended 事件（activate 订阅 →
 * 模块级信号）、手动刷新按钮。requestRef 递增防竞态（skill-manager 同模式）。
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import { useSessionStore } from '@/hooks/useSessionStore'
import { usePluginRuntime } from '@/plugin-runtime'
import type { TenantUsageStats } from '@/plugin-api'
import { useI18n } from '@/providers/i18n'
import { Button } from '@/components/ui/button'
import { Loader2, RefreshCw } from 'lucide-react'
import { subscribeStatsRefresh } from './sessionStats'
/** token 数缩写：12,345 → 12.3k；1,234,567 → 1.23M。 */
function fmtTokens(n: number): string {
  if (!n) return '0'
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(2)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`
  return String(n)
}

/** 毫秒缩写：850 → 850ms；12,300 → 12.3s；75,000 → 1m05s。 */
function fmtMs(ms: number): string {
  if (!ms) return '—'
  if (ms < 1_000) return `${Math.round(ms)}ms`
  if (ms < 60_000) return `${(ms / 1_000).toFixed(1)}s`
  const m = Math.floor(ms / 60_000)
  const s = Math.round((ms % 60_000) / 1_000)
  return `${m}m${String(s).padStart(2, '0')}s`
}

function fmtPct(numerator: number, denominator: number): string {
  if (!denominator) return '—'
  return `${((numerator / denominator) * 100).toFixed(1)}%`
}

function Metric({ label, value, sub, title }: { label: string; value: string; sub?: string; title?: string }) {
  return (
    // min-h 统一卡片高度（2 行/3 行卡片视觉对齐）；truncate + title 防长模型名
    // 换行撑高（旧版 deepseek-v4.1-flash-expires-on-0910 会换 2-3 行导致同行卡片不齐）。
    <div className="flex min-h-[56px] min-w-0 flex-col justify-between rounded-lg bg-bg-secondary/50 px-3 py-2">
      <div className="truncate text-[10px] uppercase tracking-wide text-muted-foreground">{label}</div>
      <div className="truncate font-mono text-sm font-semibold tabular-nums" title={title ?? value}>
        {value}
      </div>
      {sub ? <div className="truncate font-mono text-[10px] text-muted-foreground">{sub}</div> : null}
    </div>
  )
}

export function SessionStatsPanel() {
  const runtime = usePluginRuntime()
  const activeSession = useSessionStore().activeSession
  const { t } = useI18n()
  const [stats, setStats] = useState<TenantUsageStats | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const requestRef = useRef(0)

  const load = useCallback(async () => {
    if (!activeSession) {
      setStats(null)
      setLoading(false)
      return
    }
    const id = ++requestRef.current
    setLoading(true)
    setError('')
    try {
      const res = await runtime.rpc.call('get_session_usage_stats', {
        channel: activeSession.channel,
        chat_id: activeSession.chatID,
        limit: 30,
      })
      if (id !== requestRef.current) return
      setStats(res)
    } catch (e) {
      if (id !== requestRef.current) return
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      if (id === requestRef.current) setLoading(false)
    }
  }, [runtime, activeSession])

  // 会话切换 / 手动刷新时加载。
  useEffect(() => {
    void load()
  }, [load])

  // turn.ended 自动刷新（activate 订阅 → 模块级信号）。load 存 ref 防 stale closure。
  const loadRef = useRef(load)
  loadRef.current = load
  useEffect(() => subscribeStatsRefresh(() => void loadRef.current()), [])

  const cacheRate = stats ? fmtPct(stats.cached_tokens, stats.input_tokens) : '—'

  return (
    <div className="flex h-full flex-col">
      {/* 内部工具栏：不再重复 PanelChrome header 的标题（"统计"）——只留刷新操作 */}
      <div className="flex items-center justify-end border-b border-border/40 px-1.5 py-1">
        <Button
          size="sm"
          variant="ghost"
          className="size-6 p-0"
          title={t('plugins.sessionStats.refresh')}
          onClick={() => void load()}
        >
          {loading ? (
            <Loader2 className="size-3.5 animate-spin" />
          ) : (
            <RefreshCw className="size-3.5" />
          )}
        </Button>
      </div>

      <div className="flex-1 space-y-3 overflow-y-auto p-3">
        {!activeSession && !loading ? (
          <div className="py-8 text-center text-sm text-muted-foreground">{t('plugins.sessionStats.noActiveSession')}</div>
        ) : error ? (
          <div className="py-4 text-center text-sm text-destructive">{error}</div>
        ) : !stats ? (
          loading ? (
            <div className="flex items-center justify-center py-8">
              <Loader2 className="size-4 animate-spin text-muted-foreground" />
            </div>
          ) : (
            <div className="py-8 text-center text-sm text-muted-foreground">{t('plugins.sessionStats.noData')}</div>
          )
        ) : (
          <>
            {/* ── 用量指标 ── */}
            <div className="grid grid-cols-2 gap-1.5">
              <Metric label={t('plugins.sessionStats.input')} value={fmtTokens(stats.input_tokens)} />
              <Metric label={t('plugins.sessionStats.output')} value={fmtTokens(stats.output_tokens)} />
              <Metric
                label={t('plugins.sessionStats.cacheHit')}
                value={fmtTokens(stats.cached_tokens)}
                sub={t('plugins.sessionStats.cacheRate', { rate: cacheRate })}
              />
              <Metric label={t('plugins.sessionStats.llmDuration')} value={fmtMs(stats.llm_total_ms)} sub={t('plugins.sessionStats.iterationsTurns', { count: stats.iteration_count, turns: stats.turn_count })} />
            </div>

            {/* ── 性能指标 ── */}
            <div className="grid grid-cols-3 gap-1.5">
              <Metric label="Avg TTFT" value={fmtMs(stats.avg_ttft_ms)} />
              <Metric label="Avg TPOT" value={stats.avg_tpot_ms ? `${stats.avg_tpot_ms.toFixed(0)}ms` : '—'} />
              <Metric
                label="Avg tok/s"
                value={stats.avg_tokens_per_sec ? stats.avg_tokens_per_sec.toFixed(0) : '—'}
              />
            </div>

            {/* ── 上下文水位 + 模型 ── */}
            <div className="grid grid-cols-2 gap-1.5">
              <Metric label={t('plugins.sessionStats.contextLevel')} value={fmtTokens(stats.last_prompt_tokens)} sub={`completion ${fmtTokens(stats.last_completion_tokens)}`} />
              <Metric label={t('plugins.sessionStats.currentModel')} value={stats.current_model || '—'} sub={stats.session_created_at ? `since ${stats.session_created_at.slice(5, 16)}` : undefined} />
            </div>

            {/* ── per-model 分组（多模型时才显示） ── */}
            {stats.by_model && stats.by_model.filter((m) => m.model).length > 1 && (
              <div>
                <div className="mb-1 text-[10px] uppercase tracking-wide text-muted-foreground">{t('plugins.sessionStats.byModel')}</div>
                <div className="space-y-1">
                  {stats.by_model.map((m) => (
                    // 两行布局（模型名 / 指标）——旧版单行塞 4 个指标，329px 窄栏下
                    // 右侧数字被压到换行或溢出（用户："拥挤"）。
                    <div key={m.model} className="rounded-lg bg-bg-secondary/40 px-2.5 py-1.5">
                      <div className="truncate font-mono text-[11px] font-medium" title={m.model}>
                        {m.model}
                      </div>
                      <div className="mt-0.5 flex flex-wrap gap-x-2.5 font-mono text-[10px] tabular-nums text-muted-foreground">
                        <span>in {fmtTokens(m.input_tokens)}</span>
                        <span>out {fmtTokens(m.output_tokens)}</span>
                        <span>cache {fmtTokens(m.cached_tokens)}</span>
                        <span>{m.iterations} iters</span>
                      </div>
                    </div>
                  ))}
                </div>
              </div>
            )}

            {/* ── 最近迭代明细 ── */}
            {stats.recent_iterations && stats.recent_iterations.length > 0 && (
              <div>
                <div className="mb-1 text-[10px] uppercase tracking-wide text-muted-foreground">
                  {t('plugins.sessionStats.recentIterations', { count: stats.recent_iterations.length })}
                </div>
                <div className="overflow-x-auto rounded-md border border-border">
                  <table className="w-full whitespace-nowrap font-mono text-[10px] tabular-nums">
                    <thead>
                      <tr className="border-b border-border text-muted-foreground">
                        <th className="px-1.5 py-1 text-left font-medium">T.I</th>
                        <th className="px-1.5 py-1 text-right font-medium">In</th>
                        <th className="px-1.5 py-1 text-right font-medium">Cache</th>
                        <th className="px-1.5 py-1 text-right font-medium">Out</th>
                        <th className="px-1.5 py-1 text-right font-medium">TTFT</th>
                        <th className="px-1.5 py-1 text-right font-medium">TPOT</th>
                        <th className="px-1.5 py-1 text-right font-medium">tok/s</th>
                      </tr>
                    </thead>
                    <tbody>
                      {stats.recent_iterations.map((it, i) => (
                        <tr key={`${it.turn_id}-${it.iteration}-${i}`} className="border-b border-border/50 last:border-0">
                          <td className="px-1.5 py-1 text-left text-muted-foreground">
                            {it.turn_id}.{it.iteration}
                          </td>
                          <td className="px-1.5 py-1 text-right">{fmtTokens(it.input_tokens)}</td>
                          <td className="px-1.5 py-1 text-right text-emerald-600 dark:text-emerald-400">
                            {it.cached_tokens ? fmtPct(it.cached_tokens, it.input_tokens) : '—'}
                          </td>
                          <td className="px-1.5 py-1 text-right">{fmtTokens(it.output_tokens)}</td>
                          <td className="px-1.5 py-1 text-right">{fmtMs(it.ttft_ms)}</td>
                          <td className="px-1.5 py-1 text-right">
                            {it.tpot_ms ? `${it.tpot_ms}ms` : '—'}
                          </td>
                          <td className="px-1.5 py-1 text-right">{it.tokens_per_sec || '—'}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </div>
            )}
          </>
        )}
      </div>
    </div>
  )
}
