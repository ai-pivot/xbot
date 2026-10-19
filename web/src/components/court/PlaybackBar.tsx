/** 时间回放条：滑块 + 1x/10x/100x + 播放/暂停 + 回到最新。 */
interface Props {
  min: number
  max: number
  cursor: number
  playing: boolean
  speed: number
  onSeek: (t: number) => void
  onToggle: () => void
  onSpeed: (s: number) => void
  onLive: () => void
}

function fmt(t: number): string {
  return new Date(t).toLocaleString('zh-CN', {
    month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit',
  })
}

export function PlaybackBar({ min, max, cursor, playing, speed, onSeek, onToggle, onSpeed, onLive }: Props) {
  const pct = max > min ? ((cursor - min) / (max - min)) * 100 : 100
  const live = cursor >= max - 1
  return (
    <div className="flex items-center gap-3 border-t px-4 py-2"
      style={{ borderColor: 'rgba(148,163,184,0.12)', background: 'rgba(9,14,24,0.85)' }}>
      <button type="button" onClick={onToggle}
        className="flex size-7 items-center justify-center rounded-full text-[11px] font-bold"
        style={{ background: playing ? 'rgba(239,68,68,0.18)' : 'rgba(56,189,248,0.18)', color: playing ? '#ef4444' : '#38bdf8' }}
        aria-label={playing ? '暂停回放' : '开始回放'}>
        {playing ? '❚❚' : '▶'}
      </button>
      <span className="w-[112px] shrink-0 font-mono text-[10px]" style={{ color: 'rgba(148,163,184,0.8)' }}>
        {fmt(cursor)}
      </span>
      <div className="relative min-w-0 flex-1">
        <input
          type="range"
          min={min}
          max={max}
          value={cursor}
          onChange={(e) => onSeek(Number(e.target.value))}
          className="h-1 w-full cursor-pointer appearance-none rounded-full"
          style={{
            background: `linear-gradient(to right, #38bdf8 0%, #38bdf8 ${pct}%, rgba(148,163,184,0.22) ${pct}%, rgba(148,163,184,0.22) 100%)`,
          }}
          aria-label="回放时间"
        />
      </div>
      <span className="shrink-0 font-mono text-[10px]" style={{ color: 'rgba(148,163,184,0.55)' }}>{fmt(max)}</span>
      <div className="flex shrink-0 items-center gap-1">
        {[1, 10, 100].map((s) => (
          <button key={s} type="button" onClick={() => onSpeed(s)}
            className="rounded px-1.5 py-0.5 font-mono text-[10px]"
            style={{
              background: speed === s ? 'rgba(56,189,248,0.18)' : 'rgba(148,163,184,0.08)',
              color: speed === s ? '#38bdf8' : 'rgba(148,163,184,0.75)',
            }}>
            {s}x
          </button>
        ))}
      </div>
      <button type="button" onClick={onLive}
        className="shrink-0 rounded px-2 py-1 text-[10px]"
        style={{
          background: live ? 'rgba(34,197,94,0.12)' : 'rgba(245,165,36,0.14)',
          color: live ? '#22c55e' : '#f5a524',
        }}>
        {live ? '● 实时' : '回到最新'}
      </button>
    </div>
  )
}
