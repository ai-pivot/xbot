/**
 * xbot.session-stats — 内置前端插件：当前会话用量统计面板。
 *
 * 数据源：get_session_usage_stats 核心 RPC（iteration_history v59 聚合 ——
 * per-iteration input/cached tokens + model，按 tenant 聚合出 input/output/
 * cache 命中、TTFT/TPOT 均值、上下文水位与最近迭代明细）。
 *
 * 形态：内置插件（静态 import + activateBuiltin + builtin: 视图），
 * 同 xbot.skill-manager 范式。RPC 经 runtime.rpc.call 无点号直传 /api/rpc。
 *
 * 自动刷新：activate 订阅 turn.ended 事件（宿主 SSE 桥 emitPluginEvent 派发），
 * turn 结束（usage 已落库）后广播模块级刷新信号，面板订阅
 * （iteration-stats __configListeners 同模式）→ 重拉聚合数据。
 */
import type { PluginContext, PluginManifest, Disposable } from '@/plugin-api'
import i18n from '@/i18n'
import { setPluginI18n } from './i18n'

// ── 模块级刷新信号（activate 的 event handler → 面板组件）──────────────────
// 事件 handler 在 React 树外，经模块级 Set 广播；面板挂载时订阅、卸载时退订。
const refreshListeners = new Set<() => void>()

function notifyStatsRefresh(): void {
  refreshListeners.forEach((f) => f())
}

/** 面板订阅 turn.ended 驱动的刷新信号。 */
export function subscribeStatsRefresh(cb: () => void): () => void {
  refreshListeners.add(cb)
  return () => {
    refreshListeners.delete(cb)
  }
}

export const manifest = {
  id: 'xbot.session-stats',
  // 内置插件随主 bundle 打包（拿不到清单 web.i18n）⇒ 名称/描述/视图标题走宿主 i18n。
  name: i18n.t('plugins.sessionStats.manifest.name', { defaultValue: '会话统计' }),
  version: '0.2.0',
  description: i18n.t('plugins.sessionStats.manifest.description', {
    defaultValue: '当前会话用量统计：token / cache 命中 / TTFT / TPOT / 迭代明细 / 多粒度趋势',
  }),
  permissions: ['rpc', 'ui', 'events'] as const,
  // 本插件自己的文案表（随清单分发，**不写进宿主 web/src/i18n/*.ts**）。
  // 趋势卡片的新文案走这里；历史卡片沿用宿主 plugins.sessionStats.* key（那条线由
  // i18n-builtins 独占，本插件不碰）。
  i18n: {
    'zh-CN': {
      'trend.title': 'token 趋势',
      'trend.series.cached': '缓存命中',
      'trend.series.uncached': '未命中输入',
      'trend.series.output': '输出',
      'trend.chartAria': 'token 用量趋势图',
      'trend.uncovered': '未覆盖',
      'trend.bucketUncovered': '该区间不在明细覆盖范围内',
      'trend.bucketTotal': '本桶合计',
      'trend.cacheRate': '缓存命中率',
      'trend.cacheRateAxis': '缓存命中率',
      'trend.calls': '调用次数',
      'trend.singlePoint': '该时间窗内只有 1 个时间桶有数据',
      'trend.empty': '该时间窗内没有调用记录',
      'trend.emptyHintSample': '明细里有 {{n}} 条记录，但都不在这个时间窗内 —— 试试更粗的粒度',
      'trend.emptyHint': '本会话还没有落库的调用明细',
      'trend.granularity.minute': '分钟',
      'trend.granularity.hour': '小时',
      'trend.granularity.day': '天',
      'trend.granularityLabel': '时间粒度',
      'trend.window.minute': '最近 {{n}} 分钟',
      'trend.window.hour': '最近 {{n}} 小时',
      'trend.window.day': '最近 {{n}} 天',
      'trend.summaryTotal': '窗口总量',
      'trend.callsCount': '{{n}} 次调用',
      'trend.peakBucket': '峰值区间',
      'trend.activeBuckets': '活跃区间',
      'trend.bucketsCount': '/ {{n}} 个时间桶',
      'trend.coverage': '覆盖',
      'trend.fullCoverage': '全量聚合',
      'trend.bucketSource': '服务端全量分桶 · {{n}} 个非空时间桶',
      'trend.sampleSize': '明细 {{n}} 行',
      'trend.uncoveredNote': '{{n}} 个更早的时间桶不在明细内（未按 0 渲染）',
      'trend.unparsableNote': '{{n}} 行时间戳无法解析，已忽略',
    },
    en: {
      'trend.title': 'Token trend',
      'trend.series.cached': 'Cache hit',
      'trend.series.uncached': 'Uncached input',
      'trend.series.output': 'Output',
      'trend.chartAria': 'Token usage trend chart',
      'trend.uncovered': 'not covered',
      'trend.bucketUncovered': 'This interval is outside the detail coverage',
      'trend.bucketTotal': 'Bucket total',
      'trend.cacheRate': 'Cache hit rate',
      'trend.cacheRateAxis': 'Cache hit rate',
      'trend.calls': 'Calls',
      'trend.singlePoint': 'Only one time bucket has data in this window',
      'trend.empty': 'No calls in this time window',
      'trend.emptyHintSample': 'The sample has {{n}} rows, but none fall inside this window — try a coarser granularity',
      'trend.emptyHint': 'No call detail recorded for this session yet',
      'trend.granularity.minute': 'Minute',
      'trend.granularity.hour': 'Hour',
      'trend.granularity.day': 'Day',
      'trend.granularityLabel': 'Time granularity',
      'trend.window.minute': 'Last {{n}} minutes',
      'trend.window.hour': 'Last {{n}} hours',
      'trend.window.day': 'Last {{n}} days',
      'trend.summaryTotal': 'Window total',
      'trend.callsCount': '{{n}} calls',
      'trend.peakBucket': 'Peak interval',
      'trend.activeBuckets': 'Active intervals',
      'trend.bucketsCount': '/ {{n}} buckets',
      'trend.coverage': 'coverage',
      'trend.fullCoverage': 'full history',
      'trend.bucketSource': 'Server-side full aggregation · {{n}} non-empty buckets',
      'trend.sampleSize': '{{n}} detail rows',
      'trend.uncoveredNote': '{{n}} earlier buckets are outside the sample (not rendered as zero)',
      'trend.unparsableNote': '{{n}} rows have an unparsable timestamp and were ignored',
    },
    ja: {
      'trend.title': 'token トレンド',
      'trend.series.cached': 'キャッシュヒット',
      'trend.series.uncached': '未ヒット入力',
      'trend.series.output': '出力',
      'trend.chartAria': 'token 使用量トレンド図',
      'trend.uncovered': '未カバー',
      'trend.bucketUncovered': 'この区間は明細のカバー範囲外です',
      'trend.bucketTotal': 'バケット合計',
      'trend.cacheRate': 'キャッシュヒット率',
      'trend.cacheRateAxis': 'キャッシュヒット率',
      'trend.calls': '呼び出し回数',
      'trend.singlePoint': 'この時間窓でデータがあるバケットは 1 つだけです',
      'trend.empty': 'この時間窓に呼び出し記録がありません',
      'trend.emptyHintSample': '明細は {{n}} 行ありますが、この時間窓にはありません —— より粗い粒度をお試しください',
      'trend.emptyHint': 'このセッションにはまだ明細がありません',
      'trend.granularity.minute': '分',
      'trend.granularity.hour': '時',
      'trend.granularity.day': '日',
      'trend.granularityLabel': '時間粒度',
      'trend.window.minute': '直近 {{n}} 分',
      'trend.window.hour': '直近 {{n}} 時間',
      'trend.window.day': '直近 {{n}} 日',
      'trend.summaryTotal': '窓の合計',
      'trend.callsCount': '{{n}} 回',
      'trend.peakBucket': 'ピーク区間',
      'trend.activeBuckets': 'アクティブ区間',
      'trend.bucketsCount': '/ {{n}} バケット',
      'trend.coverage': 'カバー',
      'trend.fullCoverage': '全履歴集計',
      'trend.bucketSource': 'サーバー側で全量集計 · 空でないバケット {{n}}',
      'trend.sampleSize': '明細 {{n}} 行',
      'trend.uncoveredNote': '{{n}} 個の古いバケットは明細外です（0 としては描画しません）',
      'trend.unparsableNote': '{{n}} 行のタイムスタンプを解析できず無視しました',
    },
  },
  contributes: [
    {
      kind: 'view',
      id: 'xbot.session-stats.panel',
      container: 'right_sidebar',
      // 复用既有宿主 key（宿主 i18n 的 plugins.sessionStats.title = 统计 / Stats / 統計）。
      title: i18n.t('plugins.sessionStats.title', { defaultValue: '统计' }),
      icon: 'chart',
      entry: 'builtin:xbot.session-stats.panel',
    },
  ],
} satisfies PluginManifest

export function activate(ctx: PluginContext<typeof manifest.permissions>): Disposable | void {
  // 插件自带文案解析器（表来自清单 web.i18n）——趋势卡片用它取文案。
  setPluginI18n(ctx.i18n)
  // turn 结束后 usage 已写入 iteration_history，广播刷新信号让面板重拉聚合。
  // progress.iteration 不刷 —— 迭代中途 usage 行尚未落库，刷了也是旧数据。
  const off = ctx.events?.on('turn.ended', () => {
    notifyStatsRefresh()
  })
  return () => {
    off?.()
    setPluginI18n(undefined)
  }
}
