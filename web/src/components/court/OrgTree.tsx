/**
 * 左侧组织树（2026-10-22 重新设计：紧凑行）。
 *
 * 旧版每张卡片含「名字 + 数字 + 一句最近事件 + 进度条」占满整列（约 92px/行），
 * 三张卡片就把整列塞满，10 个部门根本放不下，而且信息密度极低。
 *
 * 现在每行 44px：
 *   [状态点] 部门名            未读数  [sparkline 12px]
 * hover 用原生 tooltip 给最近一条事件 + 任务 + 进度（需要细节的人才看）。
 * 状态色四类可区分：空闲青灰 / 忙碌琥珀 / 等待处理红 / 已完成绿；
 * 「已离开」不再出现在图例里（窗口内几乎无意义，且旧版与空闲同灰）。
 *
 * 2026-10-23 P1：数据源从「全部 Web 会话 + 孤儿子代理」收敛成「当前会话的直属
 * 子代理」（见 ./sources.ts buildOrgTree）。左栏因此多了三件东西：
 *   ① 搜索框（部门多了才找得到）；
 *   ② 按 role 分组、组头可折叠；
 *   ③ 「显示历史」开关 —— 历史会话/孤儿/一次性 explore 默认折叠。
 */
import { useMemo, useState } from 'react'
import type { OrgTreeGroup } from './sources'
import type { Lane } from './types'
import { laneStatusColor, STATUS_LEGEND, STATUS_COLORS } from './types'
import { StructuredLink } from '@/components/StructuredLink'

export interface OrgTreeProps {
  /** 直属条目（分组模式下 = 各组成员之和；平铺模式 = 全部）。 */
  lanes: Lane[]
  /** 按 role 分组；缺省 = 平铺（演示数据 / 定不出当前会话时）。 */
  groups?: OrgTreeGroup[]
  /** 历史：历史会话、孤儿子代理、一次性 explore、超限条目。默认折叠。 */
  history?: Lane[]
  /** 因超出上限被折叠的直属条目数。 */
  overflow?: number
  /** 当前会话（显示成第一行，不可点选筛选）。 */
  currentLane?: Lane | null
  laneFilter: string | null
  onLaneFilter: (id: string | null) => void
  /** 每个泳道的消息总数（右侧数字）。 */
  counts: Record<string, number>
  /** 未读数（等待处理/未答提问），显示成红色小徽标。 */
  unread?: Record<string, number>
  /** 每泳道最近 6 个时间桶的消息数（sparkline）。 */
  series?: Record<string, number[]>
  /** 每泳道最近一条事件的展示文本（tooltip）。 */
  latest?: Record<string, string>
  onRefresh?: () => void
  hasSessions?: boolean
}

const ROW_H = 36

/**
 * 12px 宽的迷你趋势线（最近 6 个时间桶）。
 *
 * 2026-10-22 用户反馈：旧版是 6 根 1px 细柱，在 1500px 截图里几乎是空白。
 * 现在改成 2px 描边的折线，颜色 = 该部门状态色；窗口内没有任何事件时
 * 画一条 1px 灰色基线（不再留一块空白，基线本身也说明「这里有个图，只是没数据」）。
 */
const SPARK_W = 12
const SPARK_H = 18

function Sparkline({ values, color }: { values: number[]; color: string }) {
  const list = values.length > 0 ? values : [0]
  const max = Math.max(1, ...list)
  const hasData = list.some((v) => v > 0)
  if (!hasData) {
    return (
      <svg
        data-testid="sparkline"
        data-empty="true"
        width={SPARK_W}
        height={SPARK_H}
        viewBox={`0 0 ${SPARK_W} ${SPARK_H}`}
        aria-hidden
        className="shrink-0 text-text-muted"
      >
        <line
          x1={0}
          y1={SPARK_H - 1.5}
          x2={SPARK_W}
          y2={SPARK_H - 1.5}
          stroke="currentColor"
          strokeWidth={1}
          opacity={0.45}
        />
      </svg>
    )
  }
  const n = Math.max(2, list.length)
  const points = list.map((v, i) => {
    const x = 1 + (i * (SPARK_W - 2)) / (n - 1)
    const y = SPARK_H - 1 - (v / max) * (SPARK_H - 2)
    return `${x.toFixed(2)},${y.toFixed(2)}`
  }).join(' ')
  return (
    <svg
      data-testid="sparkline"
      data-empty="false"
      width={SPARK_W}
      height={SPARK_H}
      viewBox={`0 0 ${SPARK_W} ${SPARK_H}`}
      aria-hidden
      className="shrink-0"
    >
      <polyline
        points={points}
        fill="none"
        stroke={color}
        strokeWidth={2}
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  )
}

export function OrgTree({
  lanes, groups, history = [], overflow = 0, currentLane = null,
  laneFilter, onLaneFilter, counts, unread = {}, series = {}, latest = {},
  onRefresh, hasSessions,
}: OrgTreeProps) {
  const [query, setQuery] = useState('')
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({})
  const [showHistory, setShowHistory] = useState(false)

  const byID = useMemo(() => {
    const m = new Map<string, Lane>()
    if (currentLane) m.set(currentLane.id, currentLane)
    for (const l of lanes) m.set(l.id, l)
    for (const l of history) if (!m.has(l.id)) m.set(l.id, l)
    return m
  }, [lanes, history, currentLane])

  const q = query.trim().toLowerCase()
  const hit = (lane: Lane) =>
    !q || lane.name.toLowerCase().includes(q) || lane.id.toLowerCase().includes(q)

  const visibleLanes = q ? lanes.filter(hit) : lanes
  const visibleHistory = q ? history.filter(hit) : history
  const visibleGroups = groups
    ?.map((g) => ({
      ...g,
      laneIDs: g.laneIDs.filter((id) => {
        const lane = byID.get(id)
        return lane ? hit(lane) : false
      }),
    }))
    .filter((g) => g.laneIDs.length > 0)
  // 搜索时强制展开（否则命中的行藏在折叠组里 = 搜不到）。
  const isCollapsed = (key: string) => !q && collapsed[key] === true
  const toggleGroup = (key: string) => setCollapsed((c) => ({ ...c, [key]: !c[key] }))

  const row = (lane: Lane) => {
    const active = laneFilter === lane.id
    const color = laneStatusColor(lane.status)
    const depth = lane.depth ?? 0
    const unreadN = unread[lane.id] ?? 0
    const tip = [
      `${lane.name}（${lane.status === 'left' ? '已离开' : lane.status === 'busy' ? '忙碌' : lane.status === 'blocked' ? '等待处理' : '空闲'}）`,
      lane.task ? `任务：${lane.task}` : '任务：（无）',
      `进度：${Math.round(lane.progress * 100)}%`,
      latest[lane.id] ? `最近：${latest[lane.id]}` : '最近：暂无事件',
    ].join('\n')
    return (
      <button
        key={lane.id}
        type="button"
        data-lane={lane.id}
        data-status={lane.status}
        title={tip}
        onClick={() => onLaneFilter(active ? null : lane.id)}
        style={{ height: ROW_H, paddingLeft: 8 + depth * 10 }}
        className={[
          'flex w-full items-center gap-2 rounded-md border pr-2 text-left transition-colors',
          active
            ? 'border-app-accent/60 bg-app-accent/10'
            : 'border-transparent hover:border-border hover:bg-bg-tertiary/70',
        ].join(' ')}
      >
        <span className="size-2 shrink-0 rounded-full" style={{ background: color }} />
        <span className="min-w-0 flex-1 truncate text-[12px] font-medium text-text-primary">{lane.name}</span>
        {unreadN > 0 ? (
          <span className="shrink-0 rounded-full bg-lane-waiting/15 px-1.5 text-[9px] font-semibold leading-4 text-lane-waiting">
            {unreadN > 99 ? '99+' : unreadN}
          </span>
        ) : null}
        <span className="shrink-0 font-mono text-[9px] text-text-muted">{counts[lane.id] ?? 0}</span>
        <Sparkline values={series[lane.id] ?? [0, 0, 0, 0, 0, 0]} color={color} />
      </button>
    )
  }

  const nothingVisible = visibleLanes.length === 0
    && (visibleGroups?.length ?? 0) === 0
    && visibleHistory.length === 0
  const totalKnown = lanes.length + history.length + (currentLane ? 1 : 0)

  return (
    <aside className="hidden w-[264px] shrink-0 flex-col border-r border-border bg-bg-secondary md:flex">
      <div className="flex h-[42px] shrink-0 items-center gap-2 border-b border-border/70 px-3">
        <span className="text-[11px] font-semibold text-text-primary">组织树</span>
        <span data-testid="org-count" className="text-[10px] text-text-muted">
          {groups ? `${lanes.length} 个直属` : `${lanes.length} 个部门/会话`}
        </span>
        {laneFilter ? (
          <button
            type="button"
            onClick={() => onLaneFilter(null)}
            className="ml-auto rounded bg-app-accent/10 px-1.5 py-0.5 text-[9px] text-app-accent"
          >
            清除筛选
          </button>
        ) : null}
      </div>

      {/* 搜索框：只在真的有条目时出现（空栏还给搜索框 = 多一处噪音）。 */}
      {totalKnown > 0 ? (
        <div className="shrink-0 px-1.5 pb-1 pt-1.5">
          <input
            type="search"
            value={query}
            data-testid="org-search"
            onChange={(e) => setQuery(e.target.value)}
            placeholder="搜索部门 / 角色"
            aria-label="搜索部门或角色"
            className="w-full rounded-md border border-border bg-bg-primary px-2 py-1 text-[11px] text-text-primary outline-none placeholder:text-text-muted focus:border-app-accent/60"
          />
        </div>
      ) : null}

      <div data-testid="org-tree-list" className="min-h-0 flex-1 overflow-y-auto px-1.5 pb-1.5">
        {currentLane ? (
          <div
            data-testid="org-current"
            title="当前会话：它派出的子代理才会出现在下面"
            className="mb-1 flex items-center gap-2 rounded-md bg-bg-tertiary/60 px-2 py-1"
            style={{ minHeight: ROW_H - 6 }}
          >
            <span className="size-2 shrink-0 rounded-full" style={{ background: laneStatusColor(currentLane.status) }} />
            <span className="min-w-0 flex-1 truncate text-[12px] font-semibold text-text-primary">{currentLane.name}</span>
            <span className="shrink-0 rounded bg-app-accent/10 px-1 text-[9px] text-app-accent">当前</span>
          </div>
        ) : null}

        {visibleGroups && visibleGroups.length > 0 ? (
          <div className="space-y-0.5">
            {visibleGroups.map((g) => {
              const shut = isCollapsed(g.key)
              // 单成员组不渲染组头（「一个组头 + 一行」= 纯噪音，可折叠也没有意义）。
              if (g.laneIDs.length === 1) {
                const lane = byID.get(g.laneIDs[0])
                return (
                  <div key={g.key} data-testid="org-group" data-group={g.key} data-single="true">
                    {lane ? row(lane) : null}
                  </div>
                )
              }
              return (
                <div key={g.key} data-testid="org-group" data-group={g.key}>
                  <button
                    type="button"
                    aria-expanded={!shut}
                    onClick={() => toggleGroup(g.key)}
                    className="flex w-full items-center gap-1.5 rounded-md px-1 py-1 text-left hover:bg-bg-tertiary/70"
                  >
                    <span className="w-3 shrink-0 text-[9px] text-text-muted">{shut ? '▸' : '▾'}</span>
                    <span className="min-w-0 flex-1 truncate text-[10.5px] font-medium text-text-secondary">{g.label}</span>
                    <span className="shrink-0 font-mono text-[9px] text-text-muted">{g.laneIDs.length}</span>
                  </button>
                  {shut ? null : (
                    <div className="space-y-0.5">
                      {g.laneIDs.map((id) => {
                        const lane = byID.get(id)
                        return lane ? row(lane) : null
                      })}
                    </div>
                  )}
                </div>
              )
            })}
          </div>
        ) : (
          <div className="space-y-0.5">{visibleLanes.map((lane) => row(lane))}</div>
        )}

        {/* 历史（历史会话 / 孤儿 / 一次性 explore / 超限条目）：默认折叠。 */}
        {history.length > 0 ? (
          <div className="mt-1 border-t border-border/60 pt-1">
            <button
              type="button"
              data-testid="org-history-toggle"
              aria-expanded={showHistory}
              onClick={() => setShowHistory((v) => !v)}
              className="flex w-full items-center gap-1.5 rounded-md px-1 py-1 text-left text-[10px] text-text-muted hover:bg-bg-tertiary/70 hover:text-text-secondary"
            >
              <span className="w-3 shrink-0 text-[9px]">{showHistory ? '▾' : '▸'}</span>
              <span className="min-w-0 flex-1 truncate">
                {showHistory ? '收起历史' : '显示历史'}（{visibleHistory.length}）
              </span>
              {overflow > 0 && !showHistory ? (
                <span className="shrink-0 rounded bg-bg-tertiary px-1 text-[9px]" title={`直属条目超过 ${lanes.length} 个上限，多出的 ${overflow} 个折叠在这里`}>
                  超限 {overflow}
                </span>
              ) : null}
            </button>
            {showHistory ? (
              <div data-testid="org-history" className="space-y-0.5">
                {visibleHistory.map((lane) => row(lane))}
              </div>
            ) : null}
          </div>
        ) : null}

        {nothingVisible && !currentLane ? (
          <div data-testid="org-tree-empty" className="space-y-2 px-2 py-5 text-[11px] leading-5 text-text-secondary">
            <p className="font-medium text-text-primary">
              {q
                ? '没有匹配的部门'
                : hasSessions
                  ? '当前会话没有子代理在跑'
                  : '还没有子代理在跑'}
            </p>
            <p>
              {q
                ? '换个关键词，或清空搜索框。'
                : '让首辅启动一个部门（或在本会话派一个子代理），它会带着自己的会话出现在这里。'}
            </p>
            <div className="flex flex-wrap gap-1.5">
              <StructuredLink
                className="rounded-md bg-app-accent/15 px-2 py-1 text-[10px] font-medium text-app-accent hover:bg-app-accent/25"
                title="打开结构化消息页：结构化消息 + 收件箱"
              >
                打开结构化消息 →
              </StructuredLink>
              {onRefresh ? (
                <button
                  type="button"
                  onClick={onRefresh}
                  className="rounded-md bg-bg-tertiary px-2 py-1 text-[10px] text-text-secondary hover:text-text-primary"
                >
                  刷新
                </button>
              ) : null}
            </div>
          </div>
        ) : null}

        {nothingVisible && currentLane ? (
          <p data-testid="org-tree-empty" className="px-2 py-3 text-[10.5px] leading-5 text-text-muted">
            {q ? '没有匹配的部门。' : '当前会话还没有直属子代理。'}
          </p>
        ) : null}
      </div>
      <div data-testid="org-tree-legend" className="flex shrink-0 flex-wrap gap-x-2.5 gap-y-1 border-t border-border px-2.5 py-2 text-[9px] text-text-muted">
        {STATUS_LEGEND.map(({ key, label }) => (
          <span key={key} className="flex items-center gap-1">
            <span className="size-1.5 rounded-full" style={{ background: STATUS_COLORS[key] }} />
            {label}
          </span>
        ))}
      </div>
    </aside>
  )
}
