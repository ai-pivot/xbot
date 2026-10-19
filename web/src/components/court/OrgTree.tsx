/** 左侧组织树：成员状态灯、当前任务、线程进度；点击过滤主视图。 */
import type { Lane } from './types'
import { STATUS_COLORS } from './types'

interface Props {
  lanes: Lane[]
  laneFilter: string | null
  onLaneFilter: (id: string | null) => void
  counts: Record<string, number>
}

export function OrgTree({ lanes, laneFilter, onLaneFilter, counts }: Props) {
  const owner = lanes.find((l) => l.type === 'user')
  const agents = lanes.filter((l) => l.type !== 'user')
  const node = (lane: Lane, depth: number) => {
    const active = laneFilter === lane.id
    const status = STATUS_COLORS[lane.status] ?? STATUS_COLORS.idle
    return (
      <button
        key={lane.id}
        type="button"
        data-lane={lane.id}
        onClick={() => onLaneFilter(active ? null : lane.id)}
        className="group relative w-full rounded-lg border px-2.5 py-2 text-left transition-colors"
        style={{
          marginLeft: depth * 14,
          borderColor: active ? 'rgba(56,189,248,0.55)' : 'rgba(148,163,184,0.12)',
          background: active ? 'rgba(56,189,248,0.08)' : 'rgba(15,23,42,0.35)',
        }}
      >
        <div className="flex items-center gap-2">
          <span
            className="size-2 shrink-0 rounded-full"
            style={{ background: status, boxShadow: `0 0 7px ${status}` }}
          />
          <span className="truncate text-[12px] font-semibold" style={{ color: '#e2e8f0' }}>{lane.name}</span>
          <span className="ml-auto font-mono text-[9px]" style={{ color: 'rgba(148,163,184,0.65)' }}>
            {counts[lane.id] ?? 0}
          </span>
        </div>
        <div className="mt-1 truncate text-[10px]" style={{ color: 'rgba(148,163,184,0.72)' }}>
          {lane.task || '暂无任务'}
        </div>
        <div className="mt-1.5 flex items-center gap-2">
          <div className="h-1 flex-1 overflow-hidden rounded-full" style={{ background: 'rgba(148,163,184,0.16)' }}>
            <div className="h-full rounded-full transition-[width] duration-500"
              style={{ width: `${Math.round(lane.progress * 100)}%`, background: status }} />
          </div>
          <span className="font-mono text-[9px]" style={{ color: 'rgba(148,163,184,0.6)' }}>
            {Math.round(lane.progress * 100)}%
          </span>
        </div>
      </button>
    )
  }

  return (
    <aside
      className="hidden w-[228px] shrink-0 flex-col border-r md:flex"
      style={{ borderColor: 'rgba(148,163,184,0.12)', background: 'rgba(9,14,24,0.72)' }}
    >
      <div className="flex items-center justify-between px-3 pb-1 pt-3">
        <span className="text-[10px] font-semibold uppercase tracking-[0.16em]"
          style={{ color: 'rgba(148,163,184,0.7)' }}>组织树</span>
        {laneFilter ? (
          <button type="button" onClick={() => onLaneFilter(null)}
            className="rounded px-1.5 py-0.5 text-[9px]"
            style={{ color: '#38bdf8', background: 'rgba(56,189,248,0.1)' }}>清除</button>
        ) : null}
      </div>
      <div className="min-h-0 flex-1 space-y-1.5 overflow-y-auto px-2.5 pb-3">
        {owner ? (
          <>
            {node(owner, 0)}
            <div className="ml-[13px] border-l pl-2" style={{ borderColor: 'rgba(148,163,184,0.16)' }}>
              <div className="space-y-1.5">
                {agents.map((a) => node(a, 0))}
              </div>
            </div>
          </>
        ) : (
          <div className="px-2 py-6 text-center text-[11px]" style={{ color: 'rgba(148,163,184,0.6)' }}>
            暂无成员
          </div>
        )}
      </div>
      <div className="border-t px-3 py-2 text-[9px] leading-4" style={{ borderColor: 'rgba(148,163,184,0.12)', color: 'rgba(148,163,184,0.55)' }}>
        绿=空闲 黄=忙碌<br />红=等待回答 灰=已离开
      </div>
    </aside>
  )
}
