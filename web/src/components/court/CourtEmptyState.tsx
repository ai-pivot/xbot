/**
 * 通信视图的**整页空态**（2026-10-23 P2 降噪）。
 *
 * 用户反馈：空态下渲染的是完整三栏骨架（左组织树空壳 + 中间一张小卡 + 右栏空插画），
 * 1280×800 里 87% 是空白，第一印象「页面坏了」，而顶栏还并排两行说明文案。
 *
 * 现在：空态收成**单栏**——两块卡片撑满内容区（左：这是什么/怎么开始；右：以后会
 * 出现什么），不渲染组织树、右详情栏、回放条。示例行明确标「示例」，不冒充真实数据。
 */
import type { ReactNode } from 'react'
import { StructuredLink } from '@/components/StructuredLink'
import type { CourtConn } from './connection'

interface Props {
  conn: CourtConn
  /** 在线但无数据时的原因（connection.emptyReasonFor）。 */
  emptyReason?: string | null
  onRefresh: () => void
  onSendTest?: () => void | Promise<void>
  sendingTest?: boolean
  /** 是否有会话（决定文案：没会话 vs 有会话没事件）。 */
  hasSessions?: boolean
}

/** 示意行：明写「示例」，不与真实事件混淆。 */
const SAMPLE_ROWS: { kind: string; color: string; text: string; meta: string }[] = [
  { kind: '汇报', color: 'var(--lane-idle, #8b93a1)', text: '工部：B300 集群 6 个 worker 全部注册', meta: 'smoke 请求 P99 1.4s，日志无 ERROR · 2 分钟前' },
  { kind: '请旨', color: 'var(--lane-waiting, #e06c75)', text: '户部：磁盘水位 88%，是否清理 7 天前日志', meta: '等你答复，红色圆点会亮起 · 8 分钟前' },
  { kind: '批红', color: 'var(--lane-done, #98c379)', text: '首辅：准，先灰度过半再全量', meta: '同一次对话会自动闭环成线程 · 11 分钟前' },
  { kind: '事件', color: 'var(--lane-busy, #d19a66)', text: '都察院：抽验 3 份证据，sha256 全部对得上', meta: '汇报里的证据路径可点击 · 1 小时前' },
  { kind: '指令', color: 'var(--app-accent, #4f9cf9)', text: '首辅 → 工部：把 glm52 权重同步到 b300-2', meta: '跨部门指令会画成一条弧线 · 2 小时前' },
  { kind: '回答', color: 'var(--lane-done, #98c379)', text: '工部：已同步完成，md5 一致', meta: '答复会收回原来的那一次对话里 · 2 小时前' },
]

/** 三步上手（左卡底部）。 */
const STEPS: { t: string; d: ReactNode }[] = [
  { t: '① 有部门在跑', d: '首辅启动工部/户部等子代理，它们各自带着会话出现在左栏组织树。' },
  { t: '② 有结构化消息', d: '子代理用 report / ask_user / decision 发消息，不写普通文本也能被看到。' },
  { t: '③ 这里自动变成流', d: '一条事件一行，点开看详情；同一「对话编号」自动聚成线程。' },
]

export function CourtEmptyState({
  conn, emptyReason, onRefresh, onSendTest, sendingTest, hasSessions,
}: Props) {
  const offline = conn === 'offline'
  return (
    <div
      data-testid="court-empty-single"
      className="grid min-h-0 flex-1 grid-cols-1 gap-3 overflow-y-auto bg-bg-primary p-4 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.05fr)]"
    >
      {/* 左卡：这是什么 + 怎么开始 */}
      <section className="flex min-h-0 flex-col justify-center gap-4 rounded-lg border border-border bg-bg-secondary/70 px-5 py-4">
        <div className="flex items-center gap-2.5">
          <span aria-hidden className="flex size-9 items-center justify-center rounded-lg bg-bg-tertiary text-[16px] text-app-accent">
            ◇
          </span>
          <div className="min-w-0">
            <h2 className="text-[16px] font-semibold text-text-primary">
              {offline ? '连不上后端' : hasSessions ? '当前会话还没有通信数据' : '还没有通信数据'}
            </h2>
            <p className="text-[10.5px] text-text-muted">
              {offline ? '通信视图 · 离线' : '通信视图 · 已连接'}
            </p>
          </div>
        </div>

        <p className="text-[12px] leading-6 text-text-secondary">
          {offline
            ? '面板拿不到会话树与消息：确认 xbot 服务在跑，然后点「刷新」。离线时页面上只会保留历史快照或这块空态。'
            : '这个页面把部门之间的汇报、请旨（提问）、批红按时间排成一条流，一条事件一行。'
              + (emptyReason ? '' : '产生第一条结构化消息后，这里会自动变成活动流。')}
        </p>

        <div className="flex flex-wrap items-center gap-2">
          {offline ? (
            <button
              type="button"
              onClick={onRefresh}
              className="rounded-md bg-app-accent px-3 py-1.5 text-[11px] font-medium text-app-accent-foreground hover:bg-app-accent-hover"
            >
              刷新
            </button>
          ) : null}
          {!offline && onSendTest ? (
            <button
              type="button"
              data-testid="feed-send-test"
              disabled={sendingTest}
              onClick={() => void onSendTest()}
              className="rounded-md bg-app-accent px-3 py-1.5 text-[11px] font-medium text-app-accent-foreground hover:bg-app-accent-hover disabled:opacity-50"
            >
              {sendingTest ? '发送中…' : '发送一条测试消息'}
            </button>
          ) : null}
          <StructuredLink
            className="rounded-md border border-border bg-bg-primary px-3 py-1.5 text-[11px] text-text-secondary hover:bg-bg-tertiary hover:text-text-primary"
            title="打开结构化消息页：结构化消息 + 收件箱"
          >
            打开结构化消息 →
          </StructuredLink>
          {!offline ? (
            <button
              type="button"
              onClick={onRefresh}
              className="rounded-md px-2 py-1.5 text-[11px] text-text-muted hover:text-text-primary"
            >
              刷新
            </button>
          ) : null}
        </div>

        <div className="space-y-2 border-t border-border/70 pt-3">
          <p className="text-[10px] font-semibold uppercase tracking-wide text-text-muted">它会怎么开始出现在这里</p>
          {STEPS.map((s) => (
            <div key={s.t} className="flex gap-2">
              <span className="w-[86px] shrink-0 text-[11px] font-medium text-text-secondary">{s.t}</span>
              <span className="min-w-0 flex-1 text-[11px] leading-5 text-text-secondary">{s.d}</span>
            </div>
          ))}
        </div>

        <p className="text-[10px] leading-4 text-text-muted">
          组织树只显示<strong className="mx-1 font-medium text-text-secondary">当前会话的直属子代理</strong>；
          历史会话、已销毁的孤儿会话与一次性探索代理收在「显示历史」里，默认不占版面。
        </p>
      </section>

      {/* 右卡：以后会看到什么（示例） */}
      <section className="flex min-h-0 flex-col rounded-lg border border-border bg-bg-secondary/70 px-4 py-3">
        <div className="flex items-center gap-2">
          <span className="text-[11px] font-semibold text-text-primary">以后会出现在这里的事件</span>
          <span className="rounded bg-lane-busy/15 px-1.5 py-0.5 text-[9px] font-semibold text-lane-busy">示例</span>
          <span className="ml-auto text-[9.5px] text-text-muted">一条事件一行 · 点开看详情</span>
        </div>
        <ul className="mt-1.5 divide-y divide-border/60">
          {SAMPLE_ROWS.map((r) => (
            <li key={r.text} className="flex items-start gap-2 py-2">
              <span className="mt-1 size-1.5 shrink-0 rounded-full" style={{ background: r.color }} />
              <span className="mt-0.5 shrink-0 rounded bg-bg-tertiary px-1 text-[9px] text-text-secondary">{r.kind}</span>
              <span className="min-w-0 flex-1">
                <span className="block truncate text-[11.5px] text-text-primary">{r.text}</span>
                <span className="block truncate text-[9.5px] text-text-muted">{r.meta}</span>
              </span>
            </li>
          ))}
        </ul>
        <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 rounded bg-bg-primary/60 px-2 py-1.5 text-[9.5px] text-text-muted">
          <span className="font-medium text-text-secondary">圆点 = 状态</span>
          {[
            { c: 'var(--lane-idle, #8b93a1)', n: '空闲' },
            { c: 'var(--lane-busy, #d19a66)', n: '忙碌（正在跑一个 turn）' },
            { c: 'var(--lane-waiting, #e06c75)', n: '等待你答复' },
            { c: 'var(--lane-done, #98c379)', n: '已完成' },
          ].map((l) => (
            <span key={l.n} className="flex items-center gap-1">
              <span className="size-1.5 rounded-full" style={{ background: l.c }} />
              {l.n}
            </span>
          ))}
        </div>

        {/* 左栏（组织树）示意：让人一眼看懂「只有直属子代理 + 历史折叠」。 */}
        <div className="mt-auto border-t border-border/70 pt-2">
          <div className="flex items-center gap-2">
            <span className="text-[10px] font-semibold text-text-secondary">左栏「组织树」会长这样</span>
            <span className="rounded bg-lane-busy/15 px-1.5 py-0.5 text-[9px] font-semibold text-lane-busy">示例</span>
          </div>
          <ul className="mt-1 space-y-0.5">
            {[
              { name: '当前会话', n: '当前', dot: 'var(--app-accent, #4f9cf9)' },
              { name: 'court  · 工部 / 户部 / 翰林院 … 7 个', n: '7', dot: 'var(--lane-idle, #8b93a1)' },
              { name: 'proj.xbot  · lead / dev / qa', n: '3', dot: 'var(--lane-busy, #d19a66)' },
              { name: '▸ 显示历史（历史会话 · 孤儿 · 一次性探索）', n: '', dot: '' },
            ].map((r) => (
              <li key={r.name} className="flex items-center gap-2 rounded bg-bg-primary/60 px-2 py-1">
                <span className="size-1.5 shrink-0 rounded-full" style={{ background: r.dot || 'transparent' }} />
                <span className="min-w-0 flex-1 truncate text-[10.5px] text-text-secondary">{r.name}</span>
                <span className="shrink-0 font-mono text-[9px] text-text-muted">{r.n}</span>
              </li>
            ))}
          </ul>
          <p className="mt-1.5 text-[9.5px] leading-4 text-text-muted">
            上面是格式示例，不是真实数据；真实事件来自各部门的结构化消息（report / ask_user / decision…）。
          </p>
        </div>
      </section>
    </div>
  )
}
