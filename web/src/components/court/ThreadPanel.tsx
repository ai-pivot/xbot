/**
 * 右侧详情 + 线程视图（correlation_id 折叠）。
 *
 * 顶部是选中消息的 payload/元信息；下面是同 correlation_id 的完整线程
 * （提问 → 回答 → 决策 → 动作），未闭环线程标红置顶。
 */
import { useMemo, useState } from 'react'
import type { GroupMessage } from './types'
import { KIND_COLORS, KIND_LABELS } from './types'

interface Props {
  messages: GroupMessage[]
  selected: GroupMessage | null
  onSelect: (m: GroupMessage) => void
  onClose: () => void
}

function threadKey(m: GroupMessage): string {
  return m.correlation_id || `single:${m.id}`
}

export function ThreadPanel({ messages, selected, onSelect, onClose }: Props) {
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({})
  const threads = useMemo(() => {
    const map = new Map<string, GroupMessage[]>()
    for (const m of messages) {
      const k = threadKey(m)
      const arr = map.get(k) ?? []
      arr.push(m)
      map.set(k, arr)
    }
    const out = [...map.entries()]
      .filter(([key]) => !key.startsWith('single:'))
      .map(([key, list]) => {
        list.sort((a, b) => new Date(a.created_at).getTime() - new Date(b.created_at).getTime())
        const last = list[list.length - 1]
        const open = last.kind === 'ask_user' &&
          !list.some((m) => m.kind === 'answer' && m.correlation_id === last.correlation_id)
        return { key, list, open, title: list[0]?.payload?.topic ?? list[0]?.content ?? key }
      })
    // 无 correlation_id 的未定向消息聚合为一条，避免右侧被单条消息刷屏。
    const singles = [...map.entries()]
      .filter(([key]) => key.startsWith('single:'))
      .flatMap(([, list]) => list)
      .sort((a, b) => new Date(a.created_at).getTime() - new Date(b.created_at).getTime())
    if (singles.length > 0) {
      out.push({ key: 'singles', list: singles, open: false, title: '未定向消息' })
    }
    out.sort((a, b) => Number(b.open) - Number(a.open))
    return out
  }, [messages])

  const selectedThread = selected ? threads.find((t) => t.key === threadKey(selected)) : null

  return (
    <aside
      className="hidden w-[330px] shrink-0 flex-col border-l lg:flex"
      style={{ borderColor: 'rgba(148,163,184,0.12)', background: 'rgba(9,14,24,0.78)' }}
    >
      <div className="flex items-center gap-2 border-b px-3 py-2" style={{ borderColor: 'rgba(148,163,184,0.12)' }}>
        <span className="text-[10px] font-semibold uppercase tracking-[0.16em]" style={{ color: 'rgba(148,163,184,0.7)' }}>
          {selected ? '消息详情' : '线程视图'}
        </span>
        {selected ? (
          <button type="button" onClick={onClose} className="ml-auto rounded px-1.5 py-0.5 text-[10px]"
            style={{ color: 'rgba(148,163,184,0.8)', background: 'rgba(148,163,184,0.1)' }}>关闭</button>
        ) : null}
      </div>

      {selected ? (
        <div className="border-b px-3 py-2.5" style={{ borderColor: 'rgba(148,163,184,0.1)' }}>
          <div className="flex items-center gap-2">
            <span className="rounded px-1.5 py-0.5 text-[10px] font-semibold"
              style={{ background: `${KIND_COLORS[selected.kind] ?? '#94a3b8'}22`, color: KIND_COLORS[selected.kind] ?? '#94a3b8' }}>
              {KIND_LABELS[selected.kind] ?? selected.kind}
            </span>
            <span className="text-[12px] font-semibold" style={{ color: '#e2e8f0' }}>{selected.sender_name}</span>
            <span className="ml-auto font-mono text-[10px]" style={{ color: 'rgba(148,163,184,0.65)' }}>
              {new Date(selected.created_at).toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })}
            </span>
          </div>
          <div className="mt-2 whitespace-pre-wrap text-[11px] leading-5" style={{ color: 'rgba(226,232,240,0.92)' }}>
            {selected.content}
          </div>
          {selected.to && selected.to.length > 0 ? (
            <div className="mt-2 text-[10px]" style={{ color: 'rgba(148,163,184,0.7)' }}>
              接收：<span className="font-mono">{selected.to.join(', ')}</span>
            </div>
          ) : null}
          {selected.correlation_id ? (
            <div className="mt-1 text-[10px]" style={{ color: 'rgba(148,163,184,0.7)' }}>
              线程：<span className="font-mono" style={{ color: '#a78bfa' }}>{selected.correlation_id}</span>
            </div>
          ) : null}
          {selected.payload ? (
            <pre className="mt-2 max-h-32 overflow-auto rounded-md p-2 text-[9.5px] leading-4"
              style={{ background: 'rgba(2,6,23,0.7)', color: 'rgba(148,163,184,0.9)' }}>
              {JSON.stringify(selected.payload, null, 2)}
            </pre>
          ) : null}
          {selectedThread ? (
            <div className="mt-2 flex items-center gap-2 text-[10px]" style={{ color: 'rgba(148,163,184,0.75)' }}>
              线程共 {selectedThread.list.length} 条 · {selectedThread.open ? '未闭环' : '已闭环'}
            </div>
          ) : null}
        </div>
      ) : null}

      <div className="min-h-0 flex-1 overflow-y-auto px-2 py-2">
        {threads.length === 0 ? (
          <div className="px-2 py-8 text-center text-[11px]" style={{ color: 'rgba(148,163,184,0.6)' }}>
            暂无线程
          </div>
        ) : threads.map((t) => {
          const isCollapsed = collapsed[t.key] ?? (!selectedThread || selectedThread.key !== t.key)
          return (
            <div key={t.key} className="mb-1.5 overflow-hidden rounded-lg border"
              style={{ borderColor: t.open ? 'rgba(239,68,68,0.35)' : 'rgba(148,163,184,0.12)' }}>
              <button type="button"
                onClick={() => setCollapsed((c) => ({ ...c, [t.key]: !isCollapsed }))}
                className="flex w-full items-center gap-2 px-2.5 py-1.5 text-left"
                style={{ background: t.open ? 'rgba(239,68,68,0.07)' : 'rgba(15,23,42,0.35)' }}>
                <span className="size-1.5 shrink-0 rounded-full"
                  style={{ background: t.open ? '#ef4444' : '#22c55e' }} />
                <span className="truncate text-[11px] font-medium" style={{ color: '#cbd5e1' }}>
                  {String(t.title).slice(0, 34)}
                </span>
                <span className="ml-auto shrink-0 font-mono text-[9px]" style={{ color: 'rgba(148,163,184,0.6)' }}>
                  {t.list.length} 条
                </span>
              </button>
              {!isCollapsed ? (
                <div className="space-y-0.5 px-2 py-1.5">
                  {t.list.map((m) => {
                    const color = KIND_COLORS[m.kind] ?? '#94a3b8'
                    const active = selected?.id === m.id
                    return (
                      <button key={m.id} type="button" onClick={() => onSelect(m)}
                        data-msg-id={m.id}
                        className="flex w-full items-start gap-2 rounded px-1.5 py-1 text-left"
                        style={{ background: active ? 'rgba(56,189,248,0.1)' : 'transparent' }}>
                        <span className="mt-1 size-1.5 shrink-0 rounded-full" style={{ background: color }} />
                        <span className="min-w-0 flex-1">
                          <span className="flex items-center gap-1.5">
                            <span className="text-[10px] font-semibold" style={{ color }}>{m.sender_name}</span>
                            <span className="font-mono text-[8.5px]" style={{ color: 'rgba(148,163,184,0.55)' }}>
                              {new Date(m.created_at).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })}
                            </span>
                            <span className="ml-auto text-[8.5px]" style={{ color: 'rgba(148,163,184,0.55)' }}>{m.kind}</span>
                          </span>
                          <span className="mt-0.5 block truncate text-[10px]" style={{ color: 'rgba(203,213,225,0.85)' }}>
                            {m.content}
                          </span>
                        </span>
                      </button>
                    )
                  })}
                </div>
              ) : null}
            </div>
          )
        })}
      </div>
    </aside>
  )
}
