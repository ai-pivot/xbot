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
import { Button } from '@/components/ui/button'
import { Loader2, RefreshCw, Activity } from 'lucide-react'
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

function Metric({ label, value, sub }: { label: string; value: string; sub?: string }) {
  return (
    <div className="rounded-md border border-border bg-card px-2.5 py-2">
      <div className="text-[10px] uppercase tracking-wide text-muted-foreground">{label}</div>
      <div className="font-mono text-sm font-semibold tabular-nums">{value}</div>
      {sub ? <div className="font-mono text-[10px] text-muted-foreground">{sub}</div> : null}
    </div>
  )
}

export function SessionStatsPanel() {
  const runtime = usePluginRuntime()
  const activeSession = useSessionStore().activeSession
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
      <div className="flex items-center justify-between border-b border-border px-3 py-2">
        <span className="inline-flex items-center gap-1.5 text-sm font-medium">
          <Activity className="size-3.5 text-muted-foreground" />
          统计
        </span>
        <Button
          size="sm"
          variant="ghost"
          className="size-6 p-0"
          title="刷新"
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
          <div className="py-8 text-center text-sm text-muted-foreground">暂无活跃会话</div>
        ) : error ? (
          <div className="py-4 text-center text-sm text-destructive">{error}</div>
        ) : !stats ? (
          loading ? (
            <div className="flex items-center justify-center py-8">
              <Loader2 className="size-4 animate-spin text-muted-foreground" />
            </div>
          ) : (
            <div className="py-8 text-center text-sm text-muted-foreground">暂无数据</div>
          )
        ) : (
          <>
            {/* ── 用量指标 ── */}
            <div className="grid grid-cols-2 gap-1.5">
              <Metric label="输入" value={fmtTokens(stats.input_tokens)} />
              <Metric label="输出" value={fmtTokens(stats.output_tokens)} />
              <Metric
                label="缓存命中"
                value={fmtTokens(stats.cached_tokens)}
                sub={`命中率 ${cacheRate}`}
              />
              <Metric label="LLM 时长" value={fmtMs(stats.llm_total_ms)} sub={`${stats.iteration_count} 次迭代 / ${stats.turn_count} turns`} />
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
              <Metric label="上下文水位" value={fmtTokens(stats.last_prompt_tokens)} sub={`completion ${fmtTokens(stats.last_completion_tokens)}`} />
              <Metric label="当前模型" value={stats.current_model || '—'} sub={stats.session_created_at ? `since ${stats.session_created_at.slice(5, 16)}` : undefined} />
            </div>

            {/* ── per-model 分组（多模型时才显示） ── */}
            {stats.by_model && stats.by_model.filter((m) => m.model).length > 1 && (
              <div>
                <div className="mb-1 text-[10px] uppercase tracking-wide text-muted-foreground">按模型</div>
                <div className="space-y-1">
                  {stats.by_model.map((m) => (
                    <div
                      key={m.model}
                      className="flex items-center justify-between rounded-md border border-border px-2 py-1.5 font-mono text-[11px] tabular-nums"
                    >
                      <span className="max-w-[45%] truncate" title={m.model}>{m.model}</span>
                      <span className="text-muted-foreground">
                        in {fmtTokens(m.input_tokens)} · out {fmtTokens(m.output_tokens)} · cache{' '}
                        {fmtTokens(m.cached_tokens)} · {m.iterations} iters
                      </span>
                    </div>
                  ))}
                </div>
              </div>
            )}

            {/* ── 最近迭代明细 ── */}
            {stats.recent_iterations && stats.recent_iterations.length > 0 && (
              <div>
                <div className="mb-1 text-[10px] uppercase tracking-wide text-muted-foreground">
                  最近迭代（{stats.recent_iterations.length}）
                </div>
                <div className="overflow-x-auto rounded-md border border-border">
                  <table className="w-full font-mono text-[10px] tabular-nums">
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
